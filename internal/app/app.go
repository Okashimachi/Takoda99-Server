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

	"takoda99/internal/admin"
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
	// Hub は観測ファンアウト（plan-h01）。プロセス共有・全試合で同一実体。
	// main が baseDeps に載せ、loadDeps() のコピーでも同じポインタを指す（plan-h00 §3.1）。
	// nil 安全（未設定なら room は観測配信をしない）。
	Hub *admin.Hub

	// SaveBotOrders は Bot の注文記録も order_attempt に保存するか（plan-h03 §2）。
	//
	// **既定 false（人間のみ）。** 学習の入力は人間の分布なので、Bot を混ぜると
	// h04 が「Bot を真似た Bot」を作ってしまう。Bot の A/B 検証をしたい時だけ ON にする。
	//
	// ⚠ **GameParameters には置かない。** これはゲームの調整値ではなく永続化の運用スイッチで、
	// DATABASE_URL などと同じ層の設定（環境変数 SAVE_BOT_ORDERS=1）。
	// GameParameters に足すと config-front のミラーも増えるが、当日いじる値ではない。
	SaveBotOrders bool
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
var bootTime = time.Now().Unix()

func nextMatchID() string { return fmt.Sprintf("m-%d-%d", bootTime, matchSeq.Add(1)) }

// RunMatch は players で1試合を構築して最後まで駆動する（ctx キャンセルでも抜ける）。
func RunMatch(ctx context.Context, d Deps, players []matchmaking.Player) {
	inits := make([]game.PlayerInit, 0, len(players))
	conns := make(map[game.PlayerId]transport.Connection, len(players))
	botIds := make(map[game.PlayerId]bool)
	for i, p := range players {
		name := p.Name
		if name == "" {
			name = matchmaking.FallbackDisplayName(p.IsBot, i+1)
		}
		inits = append(inits, game.PlayerInit{Id: p.Id, DisplayName: name})
		conns[p.Id] = p.Conn
		if p.IsBot {
			botIds[p.Id] = true
		}
	}
	matchId := nextMatchID()
	sess := game.NewSession(matchId, d.Params, d.Words, newRng(), inits)
	pub := transport.NewRankingPublisher(d.Params.Publish)
	rm := room.New(sess, conns, d.Params.Session.TickIntervalMs, d.Clock, pub)
	rm.SetAdminHub(d.Hub) // nil 安全（plan-h00 §3.2）
	rm.SetBotIds(botIds)  // 観測で Bot/人間を出し分けるため（plan-h25 §1.2）
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
			StoreId:         string(r.StoreId),
			DisplayName:     r.DisplayName,
			FinalRank:       r.FinalRank,
			Elimination:     r.Elimination,
			Score:           r.Score,
			TakoyakiCount:   r.TakoyakiCount,
			ServedCount:     r.Stats.ServedCount,
			AvgAccuracy:     r.Stats.AvgAccuracy,
			AvgElapsedMs:    r.Stats.AvgElapsedMs,
			IsBot:           isBot,
			SurvivedMs:      r.SurvivedMs,
			LeftCount:       r.Stats.LeftCount,
			TotalKeystrokes: r.Stats.TotalKeystrokes,
			TotalMisses:     r.Stats.TotalMisses,
			FastestMs:       r.Stats.FastestMs,
			SlowestMs:       r.Stats.SlowestMs,
			NormalServed:    r.Stats.Normal.Served,
			NormalLeft:      r.Stats.Normal.Left,
			BonusServed:     r.Stats.Bonus.Served,
			BonusLeft:       r.Stats.Bonus.Left,
			ClaimerServed:   r.Stats.Claimer.Served,
			ClaimerLeft:     r.Stats.Claimer.Left,
			BuzzServed:      r.Stats.Buzz.Served,
			BuzzLeft:        r.Stats.Buzz.Left,
		})
	}

	// 注文単位の記録（plan-h03）。game は Bot を区別しないので、ここで is_bot を埋める。
	// 既定は人間のみ保存（学習の入力を人間の分布に保つ）。
	attempts := make([]store.OrderAttempt, 0, len(sess.Attempts()))
	for _, a := range sess.Attempts() {
		isBot := botIds[a.StoreId]
		if isBot && !d.SaveBotOrders {
			continue
		}
		attempts = append(attempts, store.OrderAttempt{
			StoreId:    string(a.StoreId),
			CustomerId: string(a.CustomerId),
			Attribute:  string(a.Attribute),
			HeatLevel:  a.HeatLevel,
			OrderCount: a.OrderCount,
			Keystrokes: a.Keystrokes,
			ElapsedMs:  a.ElapsedMs,
			MissCount:  a.MissCount,
			IsBot:      isBot,
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
		Attempts:   attempts,
	}

	if err := d.Store.SaveMatch(ctx, mr); err != nil {
		log.Printf("result: 保存失敗（試合は正常終了済み）: %v", err)
	}
}

// NewBotPlayer は Bot 枠を1つ作る。
func NewBotPlayer(ctx context.Context, id game.PlayerId, cfg bot.Config) matchmaking.Player {
	srv, cli := transport.Pipe()
	b := bot.New(cli, cfg, newRng())
	go b.Run(ctx)
	// Name は空にして RunMatch の fallbackName に任せる。
	// ここで採番すると接続IDベースになり、試合数が増えるほど桁が伸びて6文字を超える。
	return matchmaking.Player{Id: id, Conn: srv, IsBot: true}
}
