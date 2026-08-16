package main

import (
	"fmt"
	"io"

	"takoda99/internal/sim"
)

// report.go は計測結果の出力。パラメータ調整の材料になる粒度で出す。

// printer は端末への書き出し。書き込みエラーは握り潰す
// （出力先は stdout で、途中で書けなくなっても計測結果は変わらない）。
type printer struct{ w io.Writer }

func (p printer) f(format string, a ...any) { _, _ = fmt.Fprintf(p.w, format, a...) }

func seconds(ms int64) float64 { return float64(ms) / 1000 }

// reportRun は1試合ぶんの詳細を出す。
func reportRun(w io.Writer, r runResult, index int) {
	p := printer{w}
	p.f("\n=== match %d (profile=%s seed=%d stores=%d) ===\n", index, r.Profile, r.seed, r.Stores)

	if r.Stalled {
		p.f("膠着       : %d tick / %.1f 秒で決着せず（生存 %d 店）\n", r.Ticks, seconds(r.ElapsedMs), r.AliveAtEnd)
	} else {
		p.f("決着       : %d tick / %.1f 秒（tickIntervalMs=%d）\n", r.Ticks, seconds(r.ElapsedMs), r.TickMs)
	}
	p.f("最終フェーズ : %s\n", r.FinalPhase)
	p.f("最終heatLevel: %d\n", r.HeatLevel)
	// 難度カーブ（plan-h32）。上端に届かないと最上位の語彙が使われず、
	// 段差が大きいと難度が「突然」上がる。
	p.f("難度カーブ   : 到達 heat %d / 上端 %d / 最大段差 +%d\n",
		r.MaxHeatLevel, r.HeatMaxLevel, r.MaxHeatStep)
	if r.Winner != "" {
		p.f("優勝       : %s (msPerKey=%d missRate=%.3f)\n", r.Winner, r.WinnerMsPerKey, r.WinnerMissRate)
	}
	p.f("脱落       : 足切り %d 店（本戦の脱落経路はこれ1本）\n", r.Culls)
	p.f("客の捌き   : 提供 %d\n", r.Served)
	if len(r.CullStages) > 0 {
		p.f("足切りカーブ:")
		for _, cs := range r.CullStages {
			p.f(" %.0fs→%d", seconds(cs.ElapsedMs), cs.Alive)
		}
		p.f("\n")
	}
	if r.Rejected > 0 {
		p.f("⚠ 弾かれた提供報告: %d（ダミー店の行列が session とズレている）\n", r.Rejected)
	}

	if len(r.PhaseChanges) > 0 {
		p.f("\nフェーズ推移:\n")
		for _, e := range r.PhaseChanges {
			p.f("  →%-5s : tick %4d (%6.1fs) alive=%d\n", e.Phase, e.Tick, seconds(e.ElapsedMs), e.Alive)
		}
	}

	p.f("\n生存数の推移（10%%刻み）:\n")
	for _, s := range aliveDeciles(r) {
		p.f("  tick %5d (%6.1fs)  alive %d\n", s.Tick, seconds(s.ElapsedMs), s.Alive)
	}
}

// aliveDeciles は生存数が店舗数の 100%,90%,…,10% を初めて割った時点を拾う。
func aliveDeciles(r runResult) []sim.AlivePoint {
	var out []sim.AlivePoint
	seen := make(map[int]bool, 11)
	for k := 0; k <= 10; k++ {
		target := r.Stores * (10 - k) / 10
		if target < 1 {
			target = 1
		}
		for _, s := range r.AliveCurve {
			if s.Alive > target {
				continue
			}
			if !seen[s.Tick] {
				seen[s.Tick] = true
				out = append(out, s)
			}
			break
		}
	}
	return out
}

// reportSummary は複数試行の統計サマリを出す。
func reportSummary(w io.Writer, results []runResult, targetMinSec, targetMaxSec float64) {
	if len(results) == 0 {
		return
	}
	p := printer{w}

	var (
		finished  int
		stalled   int
		inTarget  int
		heatSum   int
		heatMax   int
		heatPeak  int
		heatStep  int
		cullSum   int
		servedSum int
		rejectSum int
		secMin    = -1.0
		secMax    = 0.0
		secSum    = 0.0
		tickMin   = -1
		tickMax   = 0
		tickSum   = 0
	)

	for _, r := range results {
		heatSum += r.HeatLevel
		if r.HeatLevel > heatMax {
			heatMax = r.HeatLevel
		}
		if r.MaxHeatLevel > heatPeak {
			heatPeak = r.MaxHeatLevel
		}
		if r.MaxHeatStep > heatStep {
			heatStep = r.MaxHeatStep
		}
		cullSum += r.Culls
		servedSum += r.Served
		rejectSum += r.Rejected

		if r.Stalled {
			stalled++
			continue
		}
		finished++

		sec := seconds(r.ElapsedMs)
		secSum += sec
		if secMin < 0 || sec < secMin {
			secMin = sec
		}
		if sec > secMax {
			secMax = sec
		}
		tickSum += r.Ticks
		if tickMin < 0 || r.Ticks < tickMin {
			tickMin = r.Ticks
		}
		if r.Ticks > tickMax {
			tickMax = r.Ticks
		}
		if sec >= targetMinSec && sec <= targetMaxSec {
			inTarget++
		}
	}

	n := len(results)
	p.f("\n=== %d runs / profile=%s / stores=%d ===\n", n, results[0].Profile, results[0].Stores)
	if finished > 0 {
		f := float64(finished)
		p.f("決着時間     : 平均 %.1fs / 最短 %.1fs / 最長 %.1fs\n", secSum/f, secMin, secMax)
		p.f("決着 tick    : 平均 %.0f / 最短 %d / 最長 %d\n", float64(tickSum)/f, tickMin, tickMax)
	} else {
		p.f("決着時間     : （決着した試行なし）\n")
	}
	p.f("最終heatLevel: 平均 %.1f / 最大 %d\n", float64(heatSum)/float64(n), heatMax)
	// 難度カーブの健全性（plan-h32）。上端(heat.maxLevel)に届かないと最上位の語彙が
	// 死に、段差が大きいと「突然殴られる」体験になる。
	p.f("難度カーブ   : 到達 heat %d / 上端 %d / 最大段差 +%d\n",
		heatPeak, results[0].HeatMaxLevel, heatStep)
	p.f("脱落         : 足切り 平均 %.1f 店\n", float64(cullSum)/float64(n))
	p.f("客の捌き     : 提供 平均 %.0f\n", float64(servedSum)/float64(n))
	p.f("膠着(max-ticks到達): %d / %d\n", stalled, n)

	// ── バランス観測（plan-h26 §2）──
	// 数値を決めるために見る指標。判定はしない（合否は人間が決める）。
	bs := make([]sim.Balance, 0, len(results))
	for _, r := range results {
		bs = append(bs, sim.Analyze(r.Result))
	}
	m := sim.MeanBalance(bs)
	p.f("スコア分布   : 上位1/4 平均 %.0f / 下位1/4 平均 %.0f / 分離度 %.0f\n",
		m.TopAvg, m.BottomAvg, m.Separation)
	p.f("負スコア     : 合計 %d 店（%d試合ぶん。ほぼ0が目標）\n", m.NegativeScores, n)
	p.f("早期切り事故 : 合計 %d 店（実力上位1/4なのに最初の2段階で脱落）\n", m.EarlyCutStrong)
	p.f("実力相関     : %.2f（+1に近いほど「強い店ほど上位」。低いと運ゲー）\n", m.RankAbilityCorr)
	if rejectSum > 0 {
		p.f("⚠ 弾かれた提供報告 合計: %d\n", rejectSum)
	}

	mark := "❌"
	if inTarget == n {
		mark = "✅"
	}
	p.f("\n目安%.0f〜%.0f秒に収まった: %d/%d %s\n", targetMinSec, targetMaxSec, inTarget, n, mark)
}
