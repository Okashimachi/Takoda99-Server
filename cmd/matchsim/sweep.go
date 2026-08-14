package main

import (
	"fmt"
	"io"
	"math/rand"

	"takoda99/internal/game"
	"takoda99/internal/sim"
)

// sweep.go は plan-h26 の中心作業「速さ型 vs 正確型 が拮抗する score.weightMiss を探す」道具。
//
//	go run ./cmd/matchsim --sweep-miss 0,10,20,30,40,60 --runs 10
//
// ProfileDuel（速さ型と正確型を半々）で weightMiss を振り、平均順位がどちらに傾くかを見る。
// **判定はしない。数字を並べるだけ**で、どれを採るかは人間が決める（plan-h26 §0）。

// sweepRow は weightMiss 1点ぶんの観測。
type sweepRow struct {
	weightMiss int
	b          sim.Balance
}

func runSweep(w io.Writer, weights []int, stores, runs int, seed int64, maxTicks int) error {
	p := printer{w}
	base := game.DefaultParameters()

	p.f("=== weightMiss スイープ（profile=duel / stores=%d / runs=%d / seed=%d）===\n",
		stores, runs, seed)
	p.f("速さ型 %dms・ミス率12%% ／ 正確型 %dms・ミス率1%%（実効 168ms vs 202ms）\n\n",
		150, 200)
	p.f("%-8s %-11s %-11s %-9s %-9s %-11s %-9s %s\n",
		"W_MISS", "速さ型 平均順位", "正確型 平均順位", "差", "速優勝率", "上位10の速%", "分離度", "負スコア")

	rows := make([]sweepRow, 0, len(weights))
	for _, wm := range weights {
		pp := base
		pp.Score.WeightMiss = wm
		if err := pp.Validate(); err != nil {
			return fmt.Errorf("weightMiss=%d が不正: %w", wm, err)
		}

		bs := make([]sim.Balance, 0, runs)
		for i := 0; i < runs; i++ {
			r := sim.Simulate(sim.Config{
				Params:   pp,
				Stores:   stores,
				Profile:  sim.ProfileDuel,
				Rng:      rand.New(rand.NewSource(seed + int64(i))),
				MaxTicks: maxTicks,
			})
			if r.Stalled {
				return fmt.Errorf("weightMiss=%d seed=%d で膠着", wm, seed+int64(i))
			}
			bs = append(bs, sim.Analyze(r))
		}
		m := sim.MeanBalance(bs)
		rows = append(rows, sweepRow{weightMiss: wm, b: m})

		diff := m.FastAvgRank - m.PreciseAvgRank
		top10Pct := float64(m.Top10Fast) / float64(10*runs) * 100
		p.f("%-8d %-11.1f %-11.1f %+-9.1f %-9.0f%% %-11.0f%% %-9.0f %d\n",
			wm, m.FastAvgRank, m.PreciseAvgRank, diff,
			m.FastWinnerRatio*100, top10Pct, m.Separation, m.NegativeScores)
	}

	// 差の絶対値が最小の行を「拮抗点」として示す。あくまで目安で、採用は人間が決める。
	best := rows[0]
	for _, r := range rows[1:] {
		if absF(r.b.FastAvgRank-r.b.PreciseAvgRank) < absF(best.b.FastAvgRank-best.b.PreciseAvgRank) {
			best = r
		}
	}
	p.f("\n平均順位が最も拮抗: weightMiss=%d（差 %+.1f 位）\n",
		best.weightMiss, best.b.FastAvgRank-best.b.PreciseAvgRank)
	p.f("\n🔴 **平均順位と勝率は一致しない。** 2つの型は分散が違う\n")
	p.f("   （ミス減点が速さ型の分散を抑えるので、正確型のほうが上振れしやすい）。\n")
	p.f("   平均順位が拮抗していても上位10を片方が独占しうるので、\n")
	p.f("   **「上位10の速%%」が 50%% 付近に来る重み**も併せて見ること。\n")
	p.f("   負スコアが多発する重みは採らない（h21 §1.1 の副作用）。\n")
	return nil
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
