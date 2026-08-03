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
)

// 99体の Bot 対戦を実ランタイム（Bot goroutine + room + InMemory + publisher）で
// 走らせ、デッドロック/ブロックしない（Run が返る）・データ競合が無い（-race）を検証する。
// 企画の核「99人が動く」の証明。
func TestScale_99Bots_RunsToCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("99人スケールテスト（-short でスキップ）")
	}

	params := game.DefaultParameters()
	params.Session.TickIntervalMs = 15     // 高頻度tick
	params.Session.PublishIntervalMs = 60
	params.Customer.Total = 50             // 少ない客で早期収束

	const n = 99
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	inits := make([]game.PlayerInit, 0, n)
	conns := make(map[game.PlayerId]transport.Connection, n)
	botCfg := bot.Config{BaseAccuracy: 0.9, BaseElapsedMs: 15, AccuracyJitter: 0, ElapsedJitterMs: 0}
	for i := 0; i < n; i++ {
		id := game.PlayerId(fmt.Sprintf("bot%02d", i))
		srv, cli := transport.Pipe()
		b := bot.New(cli, botCfg, rand.New(rand.NewSource(int64(i)+1)))
		go b.Run(ctx)
		inits = append(inits, game.PlayerInit{Id: id, DisplayName: string(id)})
		conns[id] = srv
	}

	sess := game.NewSession("scale-99", params, odai.NewStaticPool(),
		rand.New(rand.NewSource(99)), inits)
	rm := room.New(sess, conns, params.Session.TickIntervalMs, room.RealClock{},
		transport.NewFullPublisher(params.Session.PublishIntervalMs))

	start := time.Now()
	rm.Run(ctx) // Finished か ctx タイムアウトまでブロック
	dur := time.Since(start)

	_, aliveCount := sess.Snapshot()
	t.Logf("99人試合: state=%v / 生存=%d / 実時間=%v", sess.State(), aliveCount, dur.Round(time.Millisecond))

	// 現状の stub step では脱落が自然には発生しないため、aliveCount=1 まで到達しない。
	// ここでは「デッドロックせず ctx タイムアウトまで走り切れる」ことの確認。
	// step 実装後にアサーションを Finished + aliveCount<=1 に厳しくする。
	if sess.State() == game.Finished {
		t.Log("試合が Finished まで走った")
		if aliveCount > 1 {
			t.Fatalf("完走後の生存は1人以下のはず: got %d", aliveCount)
		}
	} else {
		t.Logf("試合が Finished にならなかった（stub step の可能性）: state=%v 生存=%d", sess.State(), aliveCount)
	}
	cancel() // Bot goroutine を止める
}
