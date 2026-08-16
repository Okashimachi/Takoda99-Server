// Command server は takoda99 ゲームサーバーの合成ルート。全部品を配線し WebSocket で待ち受ける。
//
//	go run ./cmd/server --mode match   # マッチングプール起動（人数下限＋カウントダウン＋Bot補完）
//	go run ./cmd/server --mode solo    # 接続クライアント＋Botで即試合（ローカル確認用）
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"takoda99/internal/admin"
	"takoda99/internal/app"
	"takoda99/internal/bot"
	"takoda99/internal/config"
	"takoda99/internal/configapi"
	"takoda99/internal/db"
	"takoda99/internal/game"
	"takoda99/internal/matchmaking"
	"takoda99/internal/odai"
	"takoda99/internal/proto"
	"takoda99/internal/store"
	"takoda99/internal/transport"
)

func main() {
	mode := flag.String("mode", "match", "solo | match")
	addr := flag.String("addr", listenAddr(), "listen address")
	bots := flag.Int("bots", 3, "solo=補完Bot数 / match=Bot補完してこの人数まで埋める")
	configURL := flag.String("config-url", "", "GameParameters を JSON で返す HTTPエンドポイント")
	flag.Parse()

	ctx := context.Background()

	provider, wordStore, resultStore := chooseProvider(ctx, *configURL)

	_, err := provider.Load(ctx)
	if err != nil {
		log.Printf("config: 起動時取得失敗のためデフォルトで継続: %v", err)
	}
	baseDeps := app.DefaultDeps()

	// hub はプロセス共有で1個だけ作る。loadDeps() は baseDeps をコピーして返すが、Hub は
	// ポインタなので全試合が同一実体を共有する（＝/admin/ws の登録先と配信元が一致）。
	// hub を loadDeps 内で作ると試合ごとに別 hub ができてズレるので、ここで作る（plan-h00 §3.1）。
	hub := admin.NewHub()
	baseDeps.Hub = hub

	// Bot の注文記録も残すか（plan-h03 §2）。既定は人間のみ。
	// 学習の入力に Bot を混ぜると「Bot を真似た Bot」ができるので、
	// A/B 検証したい時だけ SAVE_BOT_ORDERS=1 で ON にする。
	baseDeps.SaveBotOrders = os.Getenv("SAVE_BOT_ORDERS") == "1"
	if baseDeps.SaveBotOrders {
		log.Printf("order_attempt: Bot の記録も保存する（SAVE_BOT_ORDERS=1）")
	}

	loadDeps := func() app.Deps {
		d := baseDeps
		p, err := provider.Load(ctx)
		if err != nil {
			log.Printf("config: マッチ用取得失敗のためデフォルトで継続: %v", err)
		}
		d.Params = p
		d.Store = resultStore
		if wordStore != nil {
			entries, err := wordStore.LoadAll(ctx)
			if err != nil {
				log.Printf("odai: DB取得失敗。フォールバック語彙で続行: %v", err)
			} else {
				d.Words = odai.NewConfigurablePool(entries)
			}
		}
		return d
	}

	botConfig := func() bot.Config {
		p, _ := provider.Load(ctx)
		return bot.Config{
			BaseAccuracy:    p.Bot.BaseAccuracy,
			BaseElapsedMs:   p.Bot.BaseElapsedMs,
			AccuracyJitter:  p.Bot.AccuracyJitter,
			ElapsedJitterMs: p.Bot.ElapsedJitterMs,
		}
	}

	var ids atomic.Int64
	nextID := func() game.PlayerId { return game.PlayerId(idString(ids.Add(1))) }

	wsAccept := wsAcceptOptions(parseCSV(os.Getenv("ALLOWED_ORIGINS")))

	botsExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "bots" {
			botsExplicit = true
		}
	})

	limiter := newConnLimiter(maxConcurrentConnections)

	switch *mode {
	case "solo":
		http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			if !limiter.acquire() {
				http.Error(w, "too many connections", http.StatusServiceUnavailable)
				return
			}
			conn, err := transport.Accept(w, r, wsAccept)
			if err != nil {
				limiter.release()
				return
			}
			limiter.releaseOnDisconnect(conn)
			id := nextID()
			name := awaitJoinName(conn, joinTimeout)
			players := []matchmaking.Player{{Id: id, Conn: conn, Name: name}}
			for i := 0; i < *bots; i++ {
				players = append(players, app.NewBotPlayer(ctx, nextID(), botConfig()))
			}
			log.Printf("solo: 試合開始 human=%s bots=%d", id, *bots)
			go app.RunMatch(ctx, loadDeps(), players)
		})

	default: // match
		mm := matchmaking.New(matchmaking.Config{
			GetParams: func() game.MatchingParams {
				p, _ := provider.Load(ctx)
				m := p.Matching
				if botsExplicit {
					m.MinFill = *bots
				}
				if m.MinFill == 0 {
					m.MinFill = game.DefaultParameters().Matching.MinFill
				}
				return m
			},
			Start: func(players []matchmaking.Player) {
				log.Printf("match: 試合開始 players=%d", len(players))
				go app.RunMatch(ctx, loadDeps(), players)
			},
			NewBot: func() matchmaking.Player { return app.NewBotPlayer(ctx, nextID(), botConfig()) },
		})
		go mm.Run(ctx)
		http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			if !limiter.acquire() {
				http.Error(w, "too many connections", http.StatusServiceUnavailable)
				return
			}
			conn, err := transport.Accept(w, r, wsAccept)
			if err != nil {
				limiter.release()
				return
			}
			limiter.releaseOnDisconnect(conn)
			id := nextID()
			name := awaitJoinName(conn, joinTimeout)
			log.Printf("match: 参加 %s name=%q (active=%d)", id, name, limiter.active())
			mm.Join(matchmaking.Player{Id: id, Conn: conn, Name: name})
			// 待機中に切断されたらプールから外す（幽霊の待機者が WaitingCount を
			// 水増しし、実体のない人数で試合が始まるのを防ぐ）。Join の後に張る。
			leaveOnDisconnect(conn, mm, id)
		})
	}

	var cfgStore configapi.Store
	if cs, ok := provider.(*db.ConfigStore); ok {
		cfgStore = cs
	}
	adminToken := os.Getenv("CONFIG_ADMIN_TOKEN")
	frontOrigins := parseCSV(os.Getenv("CONFIG_FRONT_ORIGIN"))
	http.Handle("/api/params", configapi.NewHandler(cfgStore, adminToken, frontOrigins))

	var wStore configapi.WordStore
	if wordStore != nil {
		wStore = wordStore
	}
	http.Handle("/api/words", configapi.NewWordsHandler(wStore, adminToken, frontOrigins))
	http.Handle("/api/words/", configapi.NewWordsHandler(wStore, adminToken, frontOrigins))

	// ── 観測ダッシュボード（plan-h01 / plan-h00 §5） ──
	// /admin       … 同梱の静的ダッシュボード（別リポ Takoda99-DashBoard のビルド成果物を embed）。
	// /admin/ws    … 読み取り専用の観測ストリーム。mm.Join しない＝店として試合に参加しない。
	//                認証は ?token=（ブラウザWSはヘッダ不可）＋定数時間比較。Origin は /ws と共用。
	http.Handle("/admin/", http.StripPrefix("/admin/", admin.StaticHandler()))
	http.HandleFunc("/admin/ws", adminWSHandler(hub, adminToken, wsAccept))

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	log.Printf("takoda99 server: mode=%s addr=%s", *mode, *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func chooseProvider(ctx context.Context, configURL string) (game.ConfigProvider, *db.WordStore, store.ResultStore) {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		pool, err := db.NewPool(ctx, dsn)
		if err != nil {
			log.Printf("config: DB接続失敗のため設定は内蔵デフォルト: %v", err)
			return config.DefaultLoader{}, nil, store.Noop{}
		}
		cs := db.NewConfigStore(pool)
		if err := cs.Migrate(ctx); err != nil {
			log.Printf("config: DBマイグレーション失敗のため設定は内蔵デフォルト: %v", err)
			return config.DefaultLoader{}, nil, store.Noop{}
		}
		ws := db.NewWordStore(pool)
		if err := ws.Migrate(ctx); err != nil {
			log.Printf("odai: words テーブルマイグレーション失敗: %v", err)
		}
		// 🔴 当日の逃げ道（plan-h30 §3.3）。h30 で外した長い旧語を DB へ戻す。
		// ビルドを作り直さずに「お題が短くなった」を巻き戻せるようにしてある。
		// 手順は docs/runbook.md「お題を h30 以前へ戻す」。
		if os.Getenv("TAKODA99_RESTORE_RETIRED_WORDS") == "1" {
			if err := ws.RestoreRetired(ctx); err != nil {
				log.Printf("odai: 旧お題の復元に失敗: %v", err)
			} else {
				log.Printf("odai: 旧お題（h30 で外した %d 語）を復元した", len(db.RetiredEntries()))
			}
		}
		rs := db.NewResultStore(pool)
		if err := rs.Migrate(ctx); err != nil {
			log.Printf("result: マイグレーション失敗: %v", err)
		}
		log.Printf("config: Postgres から取得（DATABASE_URL）")
		return cs, ws, rs
	}
	u := configURL
	if u == "" {
		u = os.Getenv("CONFIG_URL")
	}
	if u != "" {
		log.Printf("config: HTTP から取得（%s）", u)
		return config.NewRemoteLoader(u), nil, store.Noop{}
	}
	log.Printf("config: 内蔵デフォルトで起動")
	return config.DefaultLoader{}, nil, store.Noop{}
}

func parseCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func wsAcceptOptions(origins []string) transport.AcceptOptions {
	if len(origins) == 0 {
		return transport.AcceptOptions{AllowAll: true}
	}
	hosts := make([]string, 0, len(origins))
	for _, o := range origins {
		if o == "*" {
			return transport.AcceptOptions{AllowAll: true}
		}
		hosts = append(hosts, originHost(o))
	}
	return transport.AcceptOptions{AllowedOriginHosts: hosts}
}

func originHost(o string) string {
	o = strings.TrimRight(strings.TrimSpace(o), "/")
	if u, err := url.Parse(o); err == nil && u.Host != "" {
		return u.Host
	}
	return o
}

func listenAddr() string {
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ":8080"
}

// joinTimeout は MatchmakingJoin（表示名つき）を待つ上限。
// 期限内に来なければ空名で続行し、試合構築側が接続IDへフォールバックする。
const joinTimeout = 3 * time.Second

// awaitJoinName は接続の最初の MatchmakingJoin を読み、サニタイズ済み表示名を返す。
// タイムアウト・切断・別種メッセージ・不正JSON はいずれも空名を返す（フォールバック運用）。
func awaitJoinName(conn transport.Connection, timeout time.Duration) string {
	select {
	case env, ok := <-conn.Receive():
		if !ok || env.Type != proto.TypeMatchmakingJoin {
			return ""
		}
		var m proto.MatchmakingJoin
		if json.Unmarshal(env.Payload, &m) != nil {
			return ""
		}
		return matchmaking.SanitizeDisplayName(m.DisplayName)
	case <-time.After(timeout):
		return ""
	}
}

func idString(n int64) string {
	return "p-" + strconv.FormatInt(n, 10)
}

// tokenEqual は定数時間比較（タイミング攻撃対策）。configapi の同名ヘルパと同じ流儀。
// /admin/ws の ?token= 検証に使う。
func tokenEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// adminWSHandler は読み取り専用の観測ストリーム /admin/ws のハンドラを返す（plan-h01）。
//
// mm.Join しない＝店として試合に参加しない。認証は ?token=（ブラウザWSはヘッダ不可）を
// 定数時間で比較。トークン未設定は 503、誤りは 401（いずれも WS 昇格前に弾く）。
// 昇格後は hub に登録し、conn.Done() で登録解除する（受信は読まない＝観測は片方向）。
func adminWSHandler(hub *admin.Hub, token string, wsAccept transport.AcceptOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.Error(w, "admin token not configured (CONFIG_ADMIN_TOKEN 未設定)", http.StatusServiceUnavailable)
			return
		}
		if !tokenEqual(r.URL.Query().Get("token"), token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := transport.Accept(w, r, wsAccept)
		if err != nil {
			return
		}
		hub.Register(conn)
		// 観測者が切れたら hub から外す。Receive() は監視しない（room の readConn と
		// チャネルを奪い合うため）。受信メッセージは読まない＝捨てる（観測は片方向）。
		go func() {
			<-conn.Done()
			hub.Unregister(conn)
		}()
	}
}

// maxConcurrentConnections は同時 WebSocket 接続数の上限（99人＋再接続/観戦の余裕）。
// ハッカソン会場の「せーの」で一斉接続が来た時に、受け付けすぎてメモリ・goroutine が
// 破綻するのを防ぐための back-pressure（Plan-09）。
const maxConcurrentConnections = 200

// connLimiter は同時 WebSocket 接続数を制限する。
//
// 枠は「接続の生存期間ぶん」保持し、実際に切断されたら返す（＝真の同時接続数キャップ）。
// upgrade 直後に返す実装だと瞬間的なハンドシェイク数しか見ないので、居座る接続に対して
// 無防備になる。解放の検知には Connection.Done() を使う（Receive() を読むと room と
// メッセージを奪い合うため）。
type connLimiter struct {
	sem chan struct{}
}

func newConnLimiter(max int) *connLimiter {
	return &connLimiter{sem: make(chan struct{}, max)}
}

// acquire は枠を1つ取る。取れなければ false（ブロックしない）。
func (cl *connLimiter) acquire() bool {
	select {
	case cl.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// release は枠を1つ返す。
func (cl *connLimiter) release() { <-cl.sem }

// active は現在の使用枠数（ログ・監視用）。
func (cl *connLimiter) active() int { return len(cl.sem) }

// releaseOnDisconnect は接続が死んだら枠を返す。
func (cl *connLimiter) releaseOnDisconnect(conn transport.Connection) {
	go func() {
		<-conn.Done()
		cl.release()
	}()
}

// leaveOnDisconnect は接続が死んだら待機プールから外す。
//
// **必ず Join の後に呼ぶこと**。先に呼ぶと、切断が Join より早い場合に Leave が空振りし、
// その後の Join が幽霊の待機者をプールへ残してしまう（WaitingCount が水増しされ、
// 実体のない人数で試合が始まる）。Join の後なら、既に切れていても即 Leave が走って外れる。
func leaveOnDisconnect(conn transport.Connection, mm *matchmaking.Matchmaker, id game.PlayerId) {
	go func() {
		<-conn.Done()
		mm.Leave(id)
	}()
}
