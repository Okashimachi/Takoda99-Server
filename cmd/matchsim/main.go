// Command matchsim は1試合をヘッドレスに tick 駆動して決着時間を測る、バランス調整用の
// シミュレータ（Plan-13 / issue #12）。
//
// room / transport / bot は通さず game.Session を直接叩く。room は実時計(ticker)で回るので
// 「1試合を数秒で」が成立しないため。Bot の代わりに、打鍵速度とミス率の2値で実力を表した
// ダミー店（dummy.go）を sim 内に持ち、ApplyOrderServed を直接呼ぶ。
//
// これは **バランス調整** の道具であって性能検証ではない。99接続の WebSocket を捌けるか
// （実時間・実配信）は別物で、Plan-18 の負荷テストが担う。
package main

import (
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"time"

	"takoda99/internal/game"
)

func main() {
	stores := flag.Int("stores", 99, "店舗数")
	prof := flag.String("profile", "normal", "実力分布 uniform|normal|bipolar|wide")
	runs := flag.Int("runs", 1, "試行回数。複数なら統計サマリを出す")
	seed := flag.Int64("seed", time.Now().UnixNano(), "乱数シード（再現性）")
	maxTicks := flag.Int("max-ticks", 20000, "膠着とみなす上限tick")
	quiet := flag.Bool("quiet", false, "1試合ごとの詳細を出さない")
	targetMinSec := flag.Float64("target-min-sec", 120, "決着時間の目安（下限・秒）")
	targetMaxSec := flag.Float64("target-max-sec", 180, "決着時間の目安（上限・秒）")
	flag.Parse()

	if err := run(os.Stdout, *stores, *prof, *runs, *seed, *maxTicks, *quiet, *targetMinSec, *targetMaxSec); err != nil {
		fmt.Fprintln(os.Stderr, "matchsim:", err)
		os.Exit(1)
	}
}

func run(w io.Writer, stores int, prof string, runs int, seed int64, maxTicks int, quiet bool,
	targetMinSec, targetMaxSec float64) error {

	p, err := parseProfile(prof)
	if err != nil {
		return err
	}
	if stores < 2 {
		return fmt.Errorf("--stores は2以上である必要 (got %d)", stores)
	}
	if runs < 1 {
		return fmt.Errorf("--runs は1以上である必要 (got %d)", runs)
	}
	if maxTicks < 1 {
		return fmt.Errorf("--max-ticks は1以上である必要 (got %d)", maxTicks)
	}

	// 調整値は GameParameters が正典。sim 側で数値を作らない。
	params := game.DefaultParameters()
	if err := params.Validate(); err != nil {
		return fmt.Errorf("DefaultParameters が不正: %w", err)
	}

	results := make([]runResult, 0, runs)
	for i := 0; i < runs; i++ {
		s := seed + int64(i)
		r := simulate(params, stores, p, rand.New(rand.NewSource(s)), maxTicks)
		r.seed = s
		results = append(results, r)
		if !quiet {
			reportRun(w, r, i+1)
		}
	}

	if runs > 1 || quiet {
		reportSummary(w, results, targetMinSec, targetMaxSec)
	}

	// 膠着は「決着が保証されていない」ことなので、気付かず素通りさせない。
	for _, r := range results {
		if r.stalled {
			return fmt.Errorf("膠着: max-ticks(%d) に到達した試行がある。決着が保証されていない", maxTicks)
		}
	}
	return nil
}
