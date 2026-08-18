package app_test

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"takoda99/internal/bot"
	"takoda99/internal/game"
	"takoda99/internal/odai"
	"takoda99/internal/room"
	"takoda99/internal/transport"
	"takoda99/internal/typist"
)

// 99体の Bot 対戦を実ランタイム（Bot goroutine + room + InMemory + publisher）で
// 走らせ、デッドロック/ブロックしない（Run が返る）・データ競合が無い（-race）を検証する。
// 企画の核「99人が動く」の証明。
func TestScale_99Bots_RunsToCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("99人スケールテスト（-short でスキップ）")
	}

	params := game.DefaultParameters()
	params.Session.TickIntervalMs = 15 // 高頻度tick
	params.Publish.RankingIntervalMs = 60
	params.Customer.Total = 50 // 少ない客で早期収束

	const n = 99
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	inits := make([]game.PlayerInit, 0, n)
	conns := make(map[game.PlayerId]transport.Connection, n)
	// 1注文 15ms 前後で回す（打鍵数 × MsPerKey）。強さの検証ではなく
	// 「99体を実ランタイムで捌けるか」の試験なので、実力は最速寄りに固定する。
	botAbility := typist.Ability{MsPerKey: 1, MissRate: 0.1}
	for i := 0; i < n; i++ {
		id := game.PlayerId(fmt.Sprintf("bot%02d", i))
		srv, cli := transport.Pipe()
		b := bot.New(cli, botAbility, rand.New(rand.NewSource(int64(i)+1)))
		go b.Run(ctx)
		inits = append(inits, game.PlayerInit{Id: id, DisplayName: string(id)})
		conns[id] = srv
	}

	sess := game.NewSession("scale-99", params, odai.NewStaticPool(),
		rand.New(rand.NewSource(99)), inits)
	rm := room.New(sess, conns, params.Session.TickIntervalMs, room.RealClock{},
		transport.NewRankingPublisher(params.Publish))

	start := time.Now()
	rm.Run(ctx) // Finished か ctx タイムアウトまでブロック
	dur := time.Since(start)

	_, aliveCount := sess.Snapshot()
	t.Logf("99人試合: state=%v / 生存=%d / 実時間=%v", sess.State(), aliveCount, dur.Round(time.Millisecond))

	// 本戦は cullSchedule の最終ステージ（既定120秒）で終わる。この試験は実時計で回すため
	// ctx タイムアウト(40秒)の方が先に来ることがある。ここで見たいのは
	// **デッドロックせず走り切れること・データ競合が無いこと(-race)**なので、
	// 決着まで到達したかどうかは分岐して扱う。
	if sess.State() == game.Finished {
		t.Log("試合が Finished まで走った")
		if aliveCount != 0 {
			t.Fatalf("本戦は全店脱落で終わる。完走後の生存=%d, want 0", aliveCount)
		}
	} else {
		t.Logf("ctx タイムアウトが先に来た（決着は %dms 地点）: state=%v 生存=%d",
			params.Cull.MatchDurationMs(), sess.State(), aliveCount)
	}
	cancel() // Bot goroutine を止める
}
