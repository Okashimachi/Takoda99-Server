package sim

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"takoda99/internal/game"
	"takoda99/internal/odai"
)

// お題ツマミ（plan-h35 §2.1 の odai.levelSpread / levelOffset）の実測。
//
// 「難度が上がる＝全部が一律に難しくなる」しか無い状態を、当日にビルドせず崩せるようにした値。
// ここでは **実際に配られる語の level 分布と打鍵数**が設定でどう動くかを観測する。

// levelRecorder は本物の辞書の手前に挿さり、要求された level と
// 返ってきた語の打鍵数を記録する WordSource。
type levelRecorder struct {
	inner  game.WordSource
	levels []int
	keys   []int
}

func (r *levelRecorder) Next(effectiveLevel int, rng *rand.Rand) game.Word {
	w := r.inner.Next(effectiveLevel, rng)
	r.levels = append(r.levels, effectiveLevel)
	r.keys = append(r.keys, w.KeystrokeCount)
	return w
}

func (r *levelRecorder) stats() (mean float64, meanKeys float64, min, max int) {
	if len(r.levels) == 0 {
		return 0, 0, 0, 0
	}
	min, max = r.levels[0], r.levels[0]
	sum, sumKeys := 0, 0
	for i, l := range r.levels {
		sum += l
		sumKeys += r.keys[i]
		if l < min {
			min = l
		}
		if l > max {
			max = l
		}
	}
	return float64(sum) / float64(len(r.levels)), float64(sumKeys) / float64(len(r.keys)), min, max
}

func (r *levelRecorder) histogram() string {
	counts := map[int]int{}
	for _, l := range r.levels {
		counts[l]++
	}
	keys := make([]int, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%d:%d", k, counts[k])
	}
	return b.String()
}

// odaiConfig は99店・本番同等の設定で、お題供給に記録ラッパを挿した Config を返す。
func odaiConfig(params game.GameParameters, seed int64) (Config, *levelRecorder) {
	rec := &levelRecorder{inner: odai.NewStaticPool()}
	c := newConfig(99, ProfileNormal, seed)
	c.Params = params
	c.Words = rec
	return c, rec
}

// 🔴 **既定（levelSpread=0 / levelOffset=0）で、要求 level が heatLevel と完全一致すること。**
//
// 記録した level の集合が、その試合で観測された heatLevel の集合と一致していれば、
// お題は heat 以外のものに影響されていない＝現行と同じ挙動。
// wordLevel() に下駄やばらつきが混ざる変異で落ちる。
func TestOdai_DefaultsFollowHeatLevelExactly(t *testing.T) {
	cfg, rec := odaiConfig(game.DefaultParameters(), 1)
	r := Simulate(cfg)
	if len(rec.levels) == 0 {
		t.Fatal("語が1つも要求されていない")
	}

	// HeatCurve は「heat が変化した時点」だけを記録するので、開始直後の初期値（0）は載らない。
	// 客の大半は序盤に来店するため、この 0 が要求 level の最頻値になる。
	heatSeen := map[int]bool{0: true}
	for _, p := range r.HeatCurve {
		heatSeen[p.HeatLevel] = true
	}
	for _, l := range rec.levels {
		if !heatSeen[l] {
			t.Fatalf("要求 level=%d が試合中の heatLevel のどれとも一致しない（heat=%v）", l, heatSeen)
		}
	}
	_, _, min, max := rec.stats()
	if max != r.MaxHeatLevel {
		t.Fatalf("要求 level の最大=%d が heatLevel の最大=%d と違う", max, r.MaxHeatLevel)
	}
	if min < 0 {
		t.Fatalf("要求 level の最小=%d が負", min)
	}
}

// levelSpread を上げると、同じ試合の中で level の幅が実際に広がること。
//
// spread を無視する変異（wordLevel が heatLevel を返すだけ）で落ちる。
func TestOdai_SpreadWidensLevelRange(t *testing.T) {
	base := game.DefaultParameters()

	_, recA := func() (Result, *levelRecorder) {
		cfg, rec := odaiConfig(base, 2)
		return Simulate(cfg), rec
	}()

	spread := base
	spread.Odai.LevelSpread = 3
	_, recB := func() (Result, *levelRecorder) {
		cfg, rec := odaiConfig(spread, 2)
		return Simulate(cfg), rec
	}()

	_, _, minA, maxA := recA.stats()
	_, _, minB, maxB := recB.stats()
	if maxB <= maxA {
		t.Fatalf("levelSpread=3 で上端が広がっていない: max %d → %d", maxA, maxB)
	}
	if minB >= minA && minA > 0 {
		t.Fatalf("levelSpread=3 で下端が広がっていない: min %d → %d", minA, minB)
	}
}

// levelOffset が平均難度を平行移動させること（当日いちばん使うツマミ）。
func TestOdai_OffsetShiftsMeanLevel(t *testing.T) {
	base := game.DefaultParameters()

	meanFor := func(off int) float64 {
		p := base
		p.Odai.LevelOffset = off
		cfg, rec := odaiConfig(p, 3)
		Simulate(cfg)
		m, _, _, _ := rec.stats()
		return m
	}

	down, mid, up := meanFor(-2), meanFor(0), meanFor(2)
	if !(down < mid && mid < up) {
		t.Fatalf("levelOffset が平均難度を動かしていない: -2→%.2f / 0→%.2f / +2→%.2f", down, mid, up)
	}
}

// 実測レポート（失敗させない）。判断は人間がする。
//
//	go test -v ./internal/sim/ -run ReportOdaiLevel
func TestOdai_ReportOdaiLevelDistribution(t *testing.T) {
	if testing.Short() {
		t.Skip("-short では回さない")
	}
	cases := []struct{ spread, offset int }{
		{0, 0}, {0, -2}, {0, -1}, {0, 1}, {0, 2},
		{1, 0}, {2, 0}, {3, 0}, {2, -1},
	}
	t.Logf("%6s %6s | %6s | %7s | %7s | %s", "spread", "offset", "語数", "平均lvl", "平均打鍵", "分布(level:件数)")
	for _, c := range cases {
		p := game.DefaultParameters()
		p.Odai.LevelSpread = c.spread
		p.Odai.LevelOffset = c.offset
		cfg, rec := odaiConfig(p, 1)
		Simulate(cfg)
		mean, meanKeys, min, max := rec.stats()
		t.Logf("%6d %6d | %6d | %7.2f | %7.1f | [%d..%d] %s",
			c.spread, c.offset, len(rec.levels), mean, meanKeys, min, max, rec.histogram())
	}
}
