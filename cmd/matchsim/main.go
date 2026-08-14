// Command matchsim は1試合をヘッドレスに tick 駆動して決着時間を測る、バランス調整用の CLI
// （Plan-13 / issue #12）。
//
// シミュレータ本体は `internal/sim`。ここはフラグを解釈してレポートを出すだけの薄い層で、
// 同じ `sim.Simulate` を Plan-14（#34）の決着保証テストが CI から呼んでいる。
package main

import (
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"takoda99/internal/game"
	"takoda99/internal/sim"
)

func main() {
	stores := flag.Int("stores", 99, "店舗数")
	prof := flag.String("profile", "normal", "実力分布 uniform|normal|bipolar|wide|duel")
	runs := flag.Int("runs", 1, "試行回数。複数なら統計サマリを出す")
	seed := flag.Int64("seed", time.Now().UnixNano(), "乱数シード（再現性）")
	maxTicks := flag.Int("max-ticks", 20000, "膠着とみなす上限tick")
	quiet := flag.Bool("quiet", false, "1試合ごとの詳細を出さない")
	targetMinSec := flag.Float64("target-min-sec", 120, "決着時間の目安（下限・秒）")
	targetMaxSec := flag.Float64("target-max-sec", 180, "決着時間の目安（上限・秒）")
	sweepMiss := flag.String("sweep-miss", "", "score.weightMiss をカンマ区切りで振る（例 0,10,20,30,40,60）。profile=duel で走る")
	flag.Parse()

	if *sweepMiss != "" {
		weights, err := parseInts(*sweepMiss)
		if err != nil {
			fmt.Fprintln(os.Stderr, "matchsim:", err)
			os.Exit(1)
		}
		if err := runSweep(os.Stdout, weights, *stores, *runs, *seed, *maxTicks); err != nil {
			fmt.Fprintln(os.Stderr, "matchsim:", err)
			os.Exit(1)
		}
		return
	}

	if err := run(os.Stdout, *stores, *prof, *runs, *seed, *maxTicks, *quiet, *targetMinSec, *targetMaxSec); err != nil {
		fmt.Fprintln(os.Stderr, "matchsim:", err)
		os.Exit(1)
	}
}

// parseInts は "0,10,20" を []int にする。
func parseInts(csv string) ([]int, error) {
	parts := strings.Split(csv, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("--sweep-miss の %q が数値でない", p)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--sweep-miss が空")
	}
	return out, nil
}

// runResult はレポート用に Result へシード（再現に要る）を添えたもの。
type runResult struct {
	sim.Result
	seed int64
}

func run(w io.Writer, stores int, prof string, runs int, seed int64, maxTicks int, quiet bool,
	targetMinSec, targetMaxSec float64) error {

	p, err := sim.ParseProfile(prof)
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

	// 調整値は GameParameters が正典。CLI 側で数値を作らない。
	params := game.DefaultParameters()
	if err := params.Validate(); err != nil {
		return fmt.Errorf("DefaultParameters が不正: %w", err)
	}

	results := make([]runResult, 0, runs)
	for i := 0; i < runs; i++ {
		s := seed + int64(i)
		r := runResult{
			Result: sim.Simulate(sim.Config{
				Params:   params,
				Stores:   stores,
				Profile:  p,
				Rng:      rand.New(rand.NewSource(s)),
				MaxTicks: maxTicks,
			}),
			seed: s,
		}
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
		if r.Stalled {
			return fmt.Errorf("膠着: max-ticks(%d) に到達した試行がある。決着が保証されていない", maxTicks)
		}
	}
	return nil
}
