package typist

import (
	"math"
	"math/rand"
	"testing"
)

// 🔴 ミス数は**打鍵ごとに判定**する（plan-h31 §2.2）。
//
// 旧 Bot は `miss ∈ {0,1}` 固定で、1注文で引かれる点が weightMiss ぶんで頭打ちだった。
// `miss = 0 or 1` に戻す変異を入れると、下の「打鍵数に比例して増える」で落ちる。
func TestServe_MissCountScalesWithKeystrokes(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	a := Ability{MsPerKey: 100, MissRate: 0.1}

	const runs = 3000
	for _, keys := range []int{10, 100} {
		total := 0
		for i := 0; i < runs; i++ {
			total += Serve(a, keys, 0, rng).MissCount
		}
		got := float64(total) / runs
		want := float64(keys) * a.MissRate
		if math.Abs(got-want) > want*0.1 {
			t.Fatalf("keys=%d の平均ミス数=%.2f, want %.2f 付近（打鍵ごとの判定になっていない）",
				keys, got, want)
		}
		t.Logf("keys=%3d → 平均ミス %.2f（期待 %.2f）", keys, got, want)
	}
}

// 所要時間は (打鍵数＋ミス数) × MsPerKey。ミスの打ち直しぶんも乗る。
func TestServe_ElapsedIncludesRetypes(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	a := Ability{MsPerKey: 10, MissRate: 0}
	out := Serve(a, 20, 0, rng)
	if out.MissCount != 0 {
		t.Fatalf("missRate=0 なのに miss=%d", out.MissCount)
	}
	if out.ElapsedMs != 200 {
		t.Fatalf("elapsed=%d, want 200 (20打鍵×10ms)", out.ElapsedMs)
	}

	// ミスが出れば必ず伸びる。
	b := Ability{MsPerKey: 10, MissRate: 1}
	out = Serve(b, 20, 0, rng)
	if out.MissCount != 20 || out.ElapsedMs != 400 {
		t.Fatalf("missRate=1: miss=%d elapsed=%d, want 20 / 400", out.MissCount, out.ElapsedMs)
	}
}

// 🔴 難度追従（plan-h31 §2.3）。heatLevel が上がると伸び、tier ごとに伸び方が違う。
func TestServe_HeatPenaltyDiffersByTier(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	strong := Ability{MsPerKey: 100, HeatPenalty: 0.01}
	weak := Ability{MsPerKey: 100, HeatPenalty: 0.04}

	sBase := Serve(strong, 10, 0, rng).ElapsedMs
	sHot := Serve(strong, 10, 17, rng).ElapsedMs
	wBase := Serve(weak, 10, 0, rng).ElapsedMs
	wHot := Serve(weak, 10, 17, rng).ElapsedMs

	if sHot <= sBase || wHot <= wBase {
		t.Fatalf("heat で遅くなるべき: strong %d→%d / weak %d→%d", sBase, sHot, wBase, wHot)
	}
	sRatio := float64(sHot) / float64(sBase)
	wRatio := float64(wHot) / float64(wBase)
	if wRatio <= sRatio {
		t.Fatalf("弱い tier のほうが難度に弱いべき: strong×%.2f / weak×%.2f", sRatio, wRatio)
	}
	t.Logf("heat 17 での伸び: strong ×%.2f / weak ×%.2f", sRatio, wRatio)
}

// 🔴 速度とミス率が**同じ個体係数で動く**＝正の相関（plan-h31 §2.1）。
//
// 独立に振る変異（MissRate を factor で掛けない）を入れると相関が 0 付近に落ちてここで落ちる。
func TestIndividual_SpeedAndMissRateCorrelate(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	base := Ability{MsPerKey: 200, MissRate: 0.05}

	const n = 500
	xs := make([]float64, 0, n)
	ys := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		a := Individual(base, IndividualFactor(0.2, rng))
		xs = append(xs, a.MsPerKey)
		ys = append(ys, a.MissRate)
	}
	r := pearson(xs, ys)
	if r < 0.99 {
		t.Fatalf("速度とミス率の相関 r=%.4f, want ≈1（同じ係数で動いていない）", r)
	}
	t.Logf("個体差の相関 r=%.4f（遅い個体ほどミスが多い）", r)
}

// 個体係数は spread の範囲に収まり、spread=0（未設定扱いの前段）では 1 のまま。
func TestIndividualFactor_Range(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	if f := IndividualFactor(0, rng); f != 1 {
		t.Fatalf("spread=0 では f=1 であるべき: %v", f)
	}
	for i := 0; i < 1000; i++ {
		f := IndividualFactor(0.2, rng)
		if f < 0.8-1e-9 || f > 1.2+1e-9 {
			t.Fatalf("f=%v が ±20%% を外れた", f)
		}
	}
	// 1 以上の spread は f<=0（所要時間が破綻）を生むのでクランプする。
	for i := 0; i < 1000; i++ {
		if f := IndividualFactor(5, rng); f <= 0 {
			t.Fatalf("f=%v（0 以下は所要時間が壊れる）", f)
		}
	}
}

// Ability は値型で、Individual は元の Ability を書き換えない（個体の取り違えを防ぐ）。
func TestIndividual_DoesNotMutateBase(t *testing.T) {
	base := Ability{MsPerKey: 200, MissRate: 0.05, HeatPenalty: 0.02, JitterMs: 500}
	got := Individual(base, 1.5)
	if base.MsPerKey != 200 || base.MissRate != 0.05 {
		t.Fatalf("base が書き換わった: %+v", base)
	}
	if got.MsPerKey != 300 || math.Abs(got.MissRate-0.075) > 1e-9 {
		t.Fatalf("個体係数が両方に掛かっていない: %+v", got)
	}
	if got.HeatPenalty != base.HeatPenalty || got.JitterMs != base.JitterMs {
		t.Fatalf("HeatPenalty / JitterMs は個体で振らない: %+v", got)
	}
}

// 🔴 乱数の消費数は keystrokes 回で固定（＋Jitter があれば1回）。
//
// sim のダミー店はこの関数へ移す前と**同じ結果**でなければならない（h31 は sim の挙動を
// 変えない）。MissRate=0 でループを飛ばす最適化を入れると、この不変が崩れて
// sim のシード固定の再現性がズレる。
func TestServe_ConsumesFixedRandomDraws(t *testing.T) {
	a := Ability{MsPerKey: 100, MissRate: 0}

	rngA := rand.New(rand.NewSource(9))
	Serve(a, 25, 0, rngA)
	got := rngA.Float64()

	rngB := rand.New(rand.NewSource(9))
	for i := 0; i < 25; i++ {
		rngB.Float64()
	}
	want := rngB.Float64()

	if got != want {
		t.Fatalf("Serve が消費した乱数の数が 25 ではない（sim の再現性が壊れる）")
	}
}

// Jitter は毎回振り直す「揺らぎ」で、個体差とは別物（同じ Ability でも結果が散る）。
func TestServe_JitterVariesPerOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(6))
	a := Ability{MsPerKey: 100, JitterMs: 300}
	seen := map[int]bool{}
	for i := 0; i < 50; i++ {
		seen[Serve(a, 10, 0, rng).ElapsedMs] = true
	}
	if len(seen) < 5 {
		t.Fatalf("Jitter が効いていない（%d 種類しか出ない）", len(seen))
	}
}

// 所要時間は必ず 1ms 以上（sanity クランプの下限に依存しないため）。
func TestServe_NeverZero(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	a := Ability{MsPerKey: 1, JitterMs: 10_000}
	for i := 0; i < 100; i++ {
		if got := Serve(a, 1, 0, rng).ElapsedMs; got < 1 {
			t.Fatalf("elapsed=%d（0 以下）", got)
		}
	}
	// 打鍵数 0 や負も 1 打鍵として扱う（語が空の異常データで 0ms にしない）。
	if got := Serve(Ability{MsPerKey: 50}, 0, 0, rng).ElapsedMs; got != 50 {
		t.Fatalf("keystrokes=0 の扱い: elapsed=%d, want 50", got)
	}
}

// EffectiveMsPerKey は速度とミス率を1本にまとめた実力指標（小さいほど強い）。
func TestEffectiveMsPerKey(t *testing.T) {
	fast := Ability{MsPerKey: 150, MissRate: 0.02}
	slow := Ability{MsPerKey: 280, MissRate: 0.10}
	if fast.EffectiveMsPerKey() >= slow.EffectiveMsPerKey() {
		t.Fatal("速く正確な個体のほうが実効時間は小さいべき")
	}
}

func pearson(xs, ys []float64) float64 {
	n := float64(len(xs))
	var sx, sy float64
	for i := range xs {
		sx += xs[i]
		sy += ys[i]
	}
	mx, my := sx/n, sy/n
	var num, dx, dy float64
	for i := range xs {
		a, b := xs[i]-mx, ys[i]-my
		num += a * b
		dx += a * a
		dy += b * b
	}
	if dx == 0 || dy == 0 {
		return 0
	}
	return num / math.Sqrt(dx*dy)
}
