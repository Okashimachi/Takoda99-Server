// Package app は【合成寄り】試合の組み立て（session+room の構築、Bot枠の生成）を
// 再利用可能・テスト可能な形で提供する。cmd/server/main.go はこれと transport/matchmaking を
// 薄く配線するだけにする。
package app

import (
	"context"
	"fmt"
	"math/rand"
	"sync/atomic"
	"time"

	"takoda99/internal/bot"
	"takoda99/internal/game"
	"takoda99/internal/matchmaking"
	"takoda99/internal/odai"
	"takoda99/internal/room"
	"takoda99/internal/store"
	"takoda99/internal/transport"
)

// Deps は試合を組むための依存一式（合成ルートが用意して注入する）。
type Deps struct {
	Params game.GameParameters
	Words  game.WordSource
	Store  store.ResultStore
	Clock  room.Clock
}

// DefaultDeps は DefaultLoader 相当の内蔵デフォルトで Deps を組む（solo/検証用の手軽な既定）。
func DefaultDeps() Deps {
	return Deps{
		Params: game.DefaultParameters(),
		Words:  odai.NewStaticPool(),
		Store:  store.Noop{},
		Clock:  room.RealClock{},
	}
}

var seedSeq atomic.Int64

func newRng() *rand.Rand {
	return rand.New(rand.NewSource(time.Now().UnixNano() + seedSeq.Add(1)))
}

var matchSeq atomic.Int64

func nextMatchID() string { return fmt.Sprintf("m-%d", matchSeq.Add(1)) }

// RunMatch は players で1試合を構築して最後まで駆動する（ctx キャンセルでも抜ける）。
func RunMatch(ctx context.Context, d Deps, players []matchmaking.Player) {
	inits := make([]game.PlayerInit, 0, len(players))
	conns := make(map[game.PlayerId]transport.Connection, len(players))
	for _, p := range players {
		name := p.Name
		if name == "" {
			name = string(p.Id)
		}
		inits = append(inits, game.PlayerInit{Id: p.Id, DisplayName: name})
		conns[p.Id] = p.Conn
	}
	sess := game.NewSession(nextMatchID(), d.Params, d.Words, newRng(), inits)
	pub := transport.NewFullPublisher(d.Params.Session.PublishIntervalMs)
	rm := room.New(sess, conns, d.Params.Session.TickIntervalMs, d.Clock, pub)
	rm.Run(ctx)
}

// NewBotPlayer は Bot 枠を1つ作る。
func NewBotPlayer(ctx context.Context, id game.PlayerId, cfg bot.Config) matchmaking.Player {
	srv, cli := transport.Pipe()
	b := bot.New(cli, cfg, newRng())
	go b.Run(ctx)
	return matchmaking.Player{Id: id, Conn: srv, Name: "BOT " + string(id)}
}
