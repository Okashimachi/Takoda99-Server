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

	"textro99/internal/proto"
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
	ctx       context.Context
	cancel    context.CancelFunc
	writeMu   sync.Mutex // coder/websocket は同時Write非対応。送信を直列化する
	closeOnce sync.Once
}

// recvBuffer は受信チャネルのバッファ。読み手（room）が一時的に詰まっても数件は吸収する。
const recvBuffer = 16

func newWSConnection(conn *websocket.Conn) *wsConnection {
	ctx, cancel := context.WithCancel(context.Background())
	c := &wsConnection{conn: conn, recv: make(chan proto.Envelope, recvBuffer), ctx: ctx, cancel: cancel}
	go c.readLoop()
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

func (c *wsConnection) Send(env proto.Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Write(ctx, websocket.MessageText, data)
}

func (c *wsConnection) Receive() <-chan proto.Envelope { return c.recv }

func (c *wsConnection) Close() error {
	var err error
	c.closeOnce.Do(func() {
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
