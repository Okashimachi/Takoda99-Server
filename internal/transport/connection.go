// Package transport は【スパイン】通信層。クライアント接続の抽象(Connection)と、その実装
// （実WebSocket / 後続で InMemory）・状態配信(StatePublisher, #32)を持つ。
//
// game(コア) はここを import しない。room が Connection を介して session の Outbound を届ける。
// Bot も人間も同じ Connection interface に乗せ、session からは区別しない（#31/#35）。
package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"takoda99/internal/proto"
)

// writeTimeout は1メッセージ送信の上限。遅い/ストールしたクライアントで Write が無限に
// ブロックし、呼び出し元（room の単一goroutine）ごと試合が止まるのを防ぐ。
const writeTimeout = 10 * time.Second

// Connection は1クライアントとの双方向メッセージ経路。実WS / InMemory を差し替える。
type Connection interface {
	// Send は1メッセージを送る。接続が閉じていればエラー。
	Send(env proto.Envelope) error
	// Receive は受信メッセージのチャネル。接続が閉じると close される。
	Receive() <-chan proto.Envelope
	// Close は接続を閉じる（冪等）。
	Close() error
}

// wsConnection は coder/websocket による Connection 実装。
type wsConnection struct {
	conn      *websocket.Conn
	recv      chan proto.Envelope
	send      chan proto.Envelope
	ctx       context.Context
	cancel    context.CancelFunc
	drain     chan struct{} // Close が閉じる。writeLoop へ「残りを吐いて終われ」の合図
	writeDone chan struct{} // writeLoop の終了通知（Close が drain 完了を待つのに使う）
	closeOnce sync.Once
}

// recvBuffer は受信チャネルのバッファ。読み手（room）が一時的に詰まっても数件は吸収する。
const recvBuffer = 16

// sendBuffer は送信キューの深さ。一時的に遅いクライアントを吸収しつつ、これを超えて滞留したら
// 「配信に追従できていない」とみなして接続を切る（slow-consumer eviction）。
const sendBuffer = 64

func newWSConnection(conn *websocket.Conn) *wsConnection {
	ctx, cancel := context.WithCancel(context.Background())
	c := &wsConnection{
		conn:      conn,
		recv:      make(chan proto.Envelope, recvBuffer),
		send:      make(chan proto.Envelope, sendBuffer),
		ctx:       ctx,
		cancel:    cancel,
		drain:     make(chan struct{}),
		writeDone: make(chan struct{}),
	}
	go c.readLoop()
	go c.writeLoop()
	return c
}

// readLoop は WS から読み続け、Envelope に復号して recv へ流す。エラー/切断で recv を閉じる。
func (c *wsConnection) readLoop() {
	defer close(c.recv)
	for {
		_, data, err := c.conn.Read(c.ctx)
		if err != nil {
			return // 切断・エラー・ctxキャンセル
		}
		var env proto.Envelope
		if json.Unmarshal(data, &env) != nil {
			continue // 壊れたメッセージは無視
		}
		select {
		case c.recv <- env:
		case <-c.ctx.Done():
			return
		}
	}
}

// writeLoop は送信キューを実WSへ書き出す唯一の goroutine。
// coder/websocket は同時Write非対応だが、書き手をここ1本に集約することで直列化される。
func (c *wsConnection) writeLoop() {
	defer close(c.writeDone)
	for {
		select {
		case env := <-c.send:
			if !c.write(env) {
				return
			}
		case <-c.drain:
			// Close 経路。キューに残っているぶんを吐き切ってから終わる
			// （試合終了直後の MatchEnd を落とさないため）。
			for {
				select {
				case env := <-c.send:
					if !c.write(env) {
						return
					}
				default:
					return
				}
			}
		case <-c.ctx.Done():
			return
		}
	}
}

// write は1件を実WSへ書く。書き込み不能なら false を返し、切断を全体へ伝播させる。
func (c *wsConnection) write(env proto.Envelope) bool {
	data, err := json.Marshal(env)
	if err != nil {
		return true // 壊れたメッセージは捨てる（接続は生かす）
	}
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	err = c.conn.Write(ctx, websocket.MessageText, data)
	cancel()
	if err != nil {
		// 書き込み不能＝実質切断。cancel で readLoop も止め recv を閉じ、room に切断を伝える。
		// ここで Close() を呼ぶと drain 待ちで自分自身とデッドロックするため cancel のみ。
		c.cancel()
		return false
	}
	return true
}

// Send は送信キューへ積むだけで、実際の書き込みは writeLoop が行う。
//
// room は単一 goroutine から全接続へ順に Send する（broadcast）。ここで実 I/O をすると、
// 半開接続（FIN が来ないまま応答しないクライアント＝モバイル回線切断の典型）1つで
// Write が writeTimeout まで固まり、**試合全体が停止する**。それを防ぐための非同期化。
//
// キューが埋まっている＝そのクライアントは配信に追従できていないので、接続を切る。
// 以降は自然減衰（客の我慢切れ→信用減→SelfCollapse）で脱落する（#40）。
func (c *wsConnection) Send(env proto.Envelope) error {
	select {
	case <-c.ctx.Done():
		return ErrConnClosed
	default:
	}
	select {
	case c.send <- env:
		return nil
	default:
		// Close() は drain 完了を待つため、Send から呼ぶとここがブロックしてしまう。
		// 追従不能なクライアントは cancel で切り離すだけにする（readLoop 側が recv を閉じる）。
		c.cancel()
		return ErrConnClosed
	}
}

func (c *wsConnection) Receive() <-chan proto.Envelope { return c.recv }

// Close は接続を閉じる（冪等）。送信キューに残っているぶんは writeLoop に吐かせてから閉じる
// （room は MatchEnd を dispatch した直後に closeConns するので、drain しないと最終メッセージを落とす）。
func (c *wsConnection) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.drain)
		select {
		case <-c.writeDone:
		case <-time.After(writeTimeout): // 吐き切れないクライアントは待ち続けない
		}
		c.cancel()
		err = c.conn.Close(websocket.StatusNormalClosure, "")
	})
	return err
}

// AcceptOptions は WS ハンドシェイクの Origin 許可設定。
//
// coder/websocket は既定で「Origin の host が Host と一致しないと拒否」する（CSWSH 防御）。
// ブラウザは Origin を必ず送るため、別オリジンのフロント（localhost:5173 等）はこの既定だと
// 一切接続できない。許可オリジンを OriginPatterns で明示するか、AllowAll で検証を無効化する。
// 本ゲームの /ws は Cookie 認証等の ambient authority を持たないため AllowAll でも実害は小さい。
type AcceptOptions struct {
	// AllowedOriginHosts は許可する Origin の host[:port] パターン（例 "localhost:5173", "*.vercel.app"）。
	// 空かつ AllowAll=false なら同一オリジンのみ許可（coder/websocket の既定）。
	AllowedOriginHosts []string
	// AllowAll=true で任意オリジンを許可（Origin 検証を無効化）。dev/結合用。
	AllowAll bool
}

// Accept は HTTP リクエストを WebSocket に昇格し、Connection を返す（サーバー側）。
// opts で許可オリジンを制御する（未指定＝同一オリジンのみ）。
func Accept(w http.ResponseWriter, r *http.Request, opts AcceptOptions) (Connection, error) {
	wopts := &websocket.AcceptOptions{}
	if opts.AllowAll {
		wopts.InsecureSkipVerify = true
	} else if len(opts.AllowedOriginHosts) > 0 {
		wopts.OriginPatterns = opts.AllowedOriginHosts
	}
	conn, err := websocket.Accept(w, r, wopts)
	if err != nil {
		return nil, err
	}
	return newWSConnection(conn), nil
}

// Dial は WebSocket サーバーへ接続し Connection を返す（クライアント側・テスト用）。
func Dial(ctx context.Context, url string) (Connection, error) {
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	return newWSConnection(conn), nil
}
