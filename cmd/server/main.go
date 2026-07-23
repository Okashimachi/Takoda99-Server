// Command server は textro99 ゲームサーバーの合成ルート。全部品を配線し WebSocket で待ち受ける。
//
//	go run ./cmd/server --mode match   # マッチングプール起動（人数下限＋カウントダウン＋Bot補完）
//	go run ./cmd/server --mode solo    # 接続クライアント＋Botで即試合（ローカル確認用）
//
// 数値は GameParameters 経由（config）。作戦/お題/Bot は差し替え可能な部品を注入する。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"

	"textro99/internal/app"
	"textro99/internal/bot"
	"textro99/internal/config"
	"textro99/internal/game"
	"textro99/internal/matchmaking"
	"textro99/internal/proto"
	"textro99/internal/transport"
)

func main() {
	mode := flag.String("mode", "match", "solo | match")
	addr := flag.String("addr", ":8080", "listen address")
	bots := flag.Int("bots", 3, "solo=補完Bot数 / match=Bot補完してこの人数まで埋める")
	flag.Parse()

	ctx := context.Background()

	params, err := config.DefaultLoader{}.Load(ctx)
	if err != nil {
		log.Printf("config: デフォルト値で起動: %v", err)
	}
	deps := app.DefaultDeps()
	deps.Params = params // config から取得した値で上書き（失敗時はデフォルトのまま）

	var ids atomic.Int64
	nextID := func() game.PlayerId { return game.PlayerId(idString(ids.Add(1))) }

	switch *mode {
	case "solo":
		http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			conn, err := transport.Accept(w, r)
			if err != nil {
				return
			}
			id := nextID()
			welcome(conn, id)
			players := []matchmaking.Player{{Id: id, Conn: conn}}
			for i := 0; i < *bots; i++ {
				players = append(players, app.NewBotPlayer(ctx, nextID(), bot.DefaultConfig()))
			}
			log.Printf("solo: 試合開始 human=%s bots=%d", id, *bots)
			go app.RunMatch(ctx, deps, players)
		})

	default: // match
		mm := matchmaking.New(matchmaking.Config{
			Params:  params.Matching,
			Start:   func(players []matchmaking.Player) { log.Printf("match: 試合開始 players=%d", len(players)); go app.RunMatch(ctx, deps, players) },
			NewBot:  func() matchmaking.Player { return app.NewBotPlayer(ctx, nextID(), bot.DefaultConfig()) },
			MinFill: *bots,
		})
		go mm.Run(ctx)
		http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			conn, err := transport.Accept(w, r)
			if err != nil {
				return
			}
			id := nextID()
			welcome(conn, id)
			log.Printf("match: 参加 %s", id)
			mm.Join(matchmaking.Player{Id: id, Conn: conn})
		})
	}

	log.Printf("textro99 server: mode=%s addr=%s", *mode, *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func welcome(conn transport.Connection, id game.PlayerId) {
	data, err := json.Marshal(proto.Welcome{PlayerId: id})
	if err != nil {
		return
	}
	_ = conn.Send(proto.Envelope{Type: proto.TypeWelcome, Payload: data})
}

func idString(n int64) string {
	return "p-" + strconv.FormatInt(n, 10)
}
