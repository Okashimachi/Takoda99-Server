package app_test

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"textro99/internal/app"
	"textro99/internal/bot"
	"textro99/internal/game"
	"textro99/internal/odai"
	"textro99/internal/room"
	"textro99/internal/transport"
)

// #50b: 99体の Bot 対戦を実ランタイム（Bot goroutine ＋ room ＋ InMemory ＋ publisher）で
// 走らせ、①最後まで完走し順位が確定する ②デッドロック/ブロックしない（Run が返る）
// ③データ競合が無い（-race）を検証する。企画の核「99人が動く」の証明。
//
// 収束を現実時間で速くするため、検証用に威力/猶予/tick を攻めた値にする（挙動の正しさは
// #50a で検証済み。ここは"スケールで詰まらないか"の確認）。
func TestScale_99Bots_RunsToCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("99人スケールテスト（-short でスキップ）")
	}

	params := game.DefaultParameters()
	params.Stack.Limit = 6              // 早く脱落させる
	params.Attack.PowerToDakenRate = 0.5
	params.Attack.WarningGraceMs = 120  // 予告がすぐ着弾
	params.Session.TickIntervalMs = 15  // 高頻度tick
	params.Session.PublishIntervalMs = 60

	const n = 99
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	inits := make([]game.PlayerInit, 0, n)
	conns := make(map[game.PlayerId]transport.Connection, n)
	botCfg := bot.Config{ClearIntervalMs: 15, MissRate: 0.1, AttackEvery: 2}
	for i := 0; i < n; i++ {
		id := game.PlayerId(fmt.Sprintf("bot%02d", i))
		srv, cli := transport.Pipe()
		b := bot.New(cli, botCfg, rand.New(rand.NewSource(int64(i)+1)))
		go b.Run(ctx)
		inits = append(inits, game.PlayerInit{Id: id, DisplayName: string(id)})
		conns[id] = srv
	}

	sess := game.NewSession("scale-99", params, app.DefaultStrategies(), odai.NewStaticPool(),
		rand.New(rand.NewSource(99)), inits)
	rm := room.New(sess, conns, params.Session.TickIntervalMs, room.RealClock{},
		transport.NewFullPublisher(params.Session.PublishIntervalMs))

	start := time.Now()
	rm.Run(ctx) // Finished か ctx タイムアウトまでブロック
	dur := time.Since(start)

	_, aliveCount := sess.Snapshot()
	t.Logf("99人試合: state=%v / 生存=%d / 実時間=%v", sess.State(), aliveCount, dur.Round(time.Millisecond))

	if sess.State() != game.Finished {
		t.Fatalf("40秒で完走しなかった（収束せず or ブロック）: state=%v 生存=%d", sess.State(), aliveCount)
	}
	if aliveCount != 1 {
		t.Fatalf("最終生存は1人のはず: got %d", aliveCount)
	}
	cancel() // Bot goroutine を止める
}
