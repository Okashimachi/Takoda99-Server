package main

import (
	"fmt"
	"io"
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
	p.f("\n=== match %d (profile=%s seed=%d stores=%d) ===\n", index, r.profile, r.seed, r.stores)

	if r.stalled {
		p.f("膠着       : %d tick / %.1f 秒で決着せず（生存 %d 店）\n", r.ticks, seconds(r.elapsedMs), r.aliveAtEnd)
	} else {
		p.f("決着       : %d tick / %.1f 秒（tickIntervalMs=%d）\n", r.ticks, seconds(r.elapsedMs), r.tickMs)
	}
	p.f("最終フェーズ : %s\n", r.finalPhase)
	p.f("最終heatLevel: %d\n", r.finalHeat)
	if r.winner != "" {
		p.f("優勝       : %s (msPerKey=%d missRate=%.3f)\n", r.winner, r.winnerMsPerKey, r.winnerMissRate)
	}
	// 自滅が0だと「決着が下位淘汰100%頼み」＝我慢ゲージが効いていない兆候なので必ず出す。
	p.f("脱落内訳   : 自滅 %d / 淘汰 %d\n", r.selfCollapses, r.culls)
	p.f("客の捌き   : 提供 %d / 離脱 %d\n", r.servedTotal, r.leftTotal)
	if r.rejected > 0 {
		p.f("⚠ 弾かれた提供報告: %d（ダミー店の行列が session とズレている）\n", r.rejected)
	}

	if len(r.phaseEvents) > 0 {
		p.f("\nフェーズ推移:\n")
		for _, e := range r.phaseEvents {
			p.f("  →%-5s : tick %4d (%6.1fs) alive=%d\n", e.phase, e.tick, seconds(e.elapsedMs), e.alive)
		}
	}

	p.f("\n生存数の推移（10%%刻み）:\n")
	for _, s := range aliveDeciles(r) {
		p.f("  tick %5d (%6.1fs)  alive %d\n", s.tick, seconds(s.elapsedMs), s.alive)
	}
}

// aliveDeciles は生存数が店舗数の 100%,90%,…,10% を初めて割った時点を拾う。
func aliveDeciles(r runResult) []aliveSample {
	var out []aliveSample
	seen := make(map[int]bool, 11)
	for k := 0; k <= 10; k++ {
		target := r.stores * (10 - k) / 10
		if target < 1 {
			target = 1
		}
		for _, s := range r.aliveTimeline {
			if s.alive > target {
				continue
			}
			if !seen[s.tick] {
				seen[s.tick] = true
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
		selfSum   int
		cullSum   int
		servedSum int
		leftSum   int
		rejectSum int
		secMin    = -1.0
		secMax    = 0.0
		secSum    = 0.0
		tickMin   = -1
		tickMax   = 0
		tickSum   = 0
	)

	for _, r := range results {
		heatSum += r.finalHeat
		if r.finalHeat > heatMax {
			heatMax = r.finalHeat
		}
		selfSum += r.selfCollapses
		cullSum += r.culls
		servedSum += r.servedTotal
		leftSum += r.leftTotal
		rejectSum += r.rejected

		if r.stalled {
			stalled++
			continue
		}
		finished++

		sec := seconds(r.elapsedMs)
		secSum += sec
		if secMin < 0 || sec < secMin {
			secMin = sec
		}
		if sec > secMax {
			secMax = sec
		}
		tickSum += r.ticks
		if tickMin < 0 || r.ticks < tickMin {
			tickMin = r.ticks
		}
		if r.ticks > tickMax {
			tickMax = r.ticks
		}
		if sec >= targetMinSec && sec <= targetMaxSec {
			inTarget++
		}
	}

	n := len(results)
	p.f("\n=== %d runs / profile=%s / stores=%d ===\n", n, results[0].profile, results[0].stores)
	if finished > 0 {
		f := float64(finished)
		p.f("決着時間     : 平均 %.1fs / 最短 %.1fs / 最長 %.1fs\n", secSum/f, secMin, secMax)
		p.f("決着 tick    : 平均 %.0f / 最短 %d / 最長 %d\n", float64(tickSum)/f, tickMin, tickMax)
	} else {
		p.f("決着時間     : （決着した試行なし）\n")
	}
	p.f("最終heatLevel: 平均 %.1f / 最大 %d\n", float64(heatSum)/float64(n), heatMax)
	p.f("脱落内訳     : 自滅 平均 %.1f / 淘汰 平均 %.1f\n",
		float64(selfSum)/float64(n), float64(cullSum)/float64(n))
	p.f("客の捌き     : 提供 平均 %.0f / 離脱 平均 %.0f\n",
		float64(servedSum)/float64(n), float64(leftSum)/float64(n))
	p.f("膠着(max-ticks到達): %d / %d\n", stalled, n)
	if rejectSum > 0 {
		p.f("⚠ 弾かれた提供報告 合計: %d\n", rejectSum)
	}

	mark := "❌"
	if inTarget == n {
		mark = "✅"
	}
	p.f("\n目安%.0f〜%.0f秒に収まった: %d/%d %s\n", targetMinSec, targetMaxSec, inTarget, n, mark)
}
