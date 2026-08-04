// Command server は takoda99 ゲームサーバーの合成ルート。全部品を配線し WebSocket で待ち受ける。
//
//	go run ./cmd/server --mode match   # マッチングプール起動（人数下限＋カウントダウン＋Bot補完）
//	go run ./cmd/server --mode solo    # 接続クライアント＋Botで即試合（ローカル確認用）
package main

import (
	"context"
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

	switch *mode {
	case "solo":
		http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			conn, err := transport.Accept(w, r, wsAccept)
			if err != nil {
				return
			}
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
			conn, err := transport.Accept(w, r, wsAccept)
			if err != nil {
				return
			}
			id := nextID()
			name := awaitJoinName(conn, joinTimeout)
			log.Printf("match: 参加 %s name=%q", id, name)
			mm.Join(matchmaking.Player{Id: id, Conn: conn, Name: name})
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
