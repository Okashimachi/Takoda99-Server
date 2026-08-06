// Package app は【合成寄り】試合の組み立て（session+room の構築、Bot枠の生成）を
// 再利用可能・テスト可能な形で提供する。cmd/server/main.go はこれと transport/matchmaking を
// 薄く配線するだけにする。
package app

import (
	"context"
	"fmt"
	"log"
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
	botIds := make(map[game.PlayerId]bool)
	for i, p := range players {
		name := p.Name
		if name == "" {
			name = fallbackName(p.IsBot, i+1)
		}
		inits = append(inits, game.PlayerInit{Id: p.Id, DisplayName: name})
		conns[p.Id] = p.Conn
		if p.IsBot {
			botIds[p.Id] = true
		}
	}
	matchId := nextMatchID()
	sess := game.NewSession(matchId, d.Params, d.Words, newRng(), inits)
	pub := transport.NewFullPublisher(d.Params.Session.PublishIntervalMs)
	rm := room.New(sess, conns, d.Params.Session.TickIntervalMs, d.Clock, pub)
	rm.Run(ctx)

	saveResults(ctx, d, sess, matchId, botIds)
}

func saveResults(ctx context.Context, d Deps, sess *game.Session, matchId string, botIds map[game.PlayerId]bool) {
	results := sess.Results()
	if len(results) == 0 {
		return
	}

	humanCount, botCount := 0, 0
	storeResults := make([]store.Result, 0, len(results))
	var winnerId string

	for _, r := range results {
		isBot := botIds[r.StoreId]
		if isBot {
			botCount++
		} else {
			humanCount++
		}
		if r.FinalRank == 1 {
			winnerId = string(r.StoreId)
		}
		storeResults = append(storeResults, store.Result{
			StoreId:      string(r.StoreId),
			DisplayName:  r.DisplayName,
			FinalRank:    r.FinalRank,
			Elimination:  r.Elimination,
			CreditLife:   r.CreditLife,
			EvalRaw:      r.EvalRaw,
			ServedCount:  r.Stats.ServedCount,
			AvgAccuracy:  r.Stats.AvgAccuracy,
			AvgElapsedMs: r.Stats.AvgElapsedMs,
			IsBot:        isBot,
		})
	}

	mr := store.MatchResult{
		MatchId:    matchId,
		DurationMs: sess.ElapsedMs(),
		HumanCount: humanCount,
		BotCount:   botCount,
		WinnerId:   winnerId,
		ConfigHash: d.Params.ConfigHash(),
		Results:    storeResults,
	}

	if err := d.Store.SaveMatch(ctx, mr); err != nil {
		log.Printf("result: 保存失敗（試合は正常終了済み）: %v", err)
	}
}

// NewBotPlayer は Bot 枠を1つ作る。
// fallbackName は名前を送らなかったプレイヤーと Bot に割り当てる表示名。
//
// **matchmaking.MaxDisplayNameLen（6文字）に必ず収まること。** クライアントは
// マッチング画面に99枠のグリッドを描いており、ここが溢れるとレイアウトが崩れる。
// しかも崩れるのは「名前を入力しなかった人」と Bot だけなので、
// **手元のテストでは気付かず本番で初めて出る**種類の不具合になる。
//
// 採番は**試合内の通し番号**（1..99）。接続IDの `p-1234` を使うと試合数が増えるほど
// 桁が伸びて6文字を超える（`p-12345` で7文字）。試合内番号なら最大 `ゲスト99` / `CPU99` の
// 5文字で頭打ちになる。
//
//	名前なしの人間 : ゲスト1 〜 ゲスト99  （最大5文字）
//	Bot            : CPU1   〜 CPU99    （最大5文字）
func fallbackName(isBot bool, seat int) string {
	if isBot {
		return fmt.Sprintf("CPU%d", seat)
	}
	return fmt.Sprintf("ゲスト%d", seat)
}

func NewBotPlayer(ctx context.Context, id game.PlayerId, cfg bot.Config) matchmaking.Player {
	srv, cli := transport.Pipe()
	b := bot.New(cli, cfg, newRng())
	go b.Run(ctx)
	// Name は空にして RunMatch の fallbackName に任せる。
	// ここで採番すると接続IDベースになり、試合数が増えるほど桁が伸びて6文字を超える。
	return matchmaking.Player{Id: id, Conn: srv, IsBot: true}
}
