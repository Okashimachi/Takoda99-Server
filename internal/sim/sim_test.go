package sim

import (
	"fmt"
	"math/rand"
	"testing"

	"takoda99/internal/game"
	"takoda99/internal/proto"
)

// maxTicks は膠着とみなす上限。150ms × 20000 = 50分ぶん。
const maxTicks = 20000

func newConfig(stores int, p Profile, seed int64) Config {
	return Config{
		Params:   game.DefaultParameters(),
		Stores:   stores,
		Profile:  p,
		Rng:      rand.New(rand.NewSource(seed)),
		MaxTicks: maxTicks,
	}
}

// ── 決着保証（Plan-14 / #34）─────────────────────────────────
//
// 制限時間を廃止したので、試合が終わる保証は下位淘汰(storm)だけになった。
// storm が止まると試合は永遠に終わらない＝当日「次に進めない」という事故になる。
// ここが落ちたらマージ不可。

// 全プロファイル × 複数シードで有限ティック内に生存店=1 へ到達すること。
//
// ProfileUniform が本命。全員同実力だと評価がほぼ同値になり、パーセンタイル正規化でも
// 差がつきにくい。そこで storm が確実に削れるかが決着保証の核心。
func TestDecisiveness_AllProfiles(t *testing.T) {
	for _, p := range AllProfiles() {
		t.Run(string(p), func(t *testing.T) {
			for seed := int64(1); seed <= 5; seed++ {
				r := Simulate(newConfig(99, p, seed))
				if r.Stalled {
					t.Fatalf("seed=%d で決着せず（%d tick で生存%d店・heatLevel=%d）",
						seed, maxTicks, r.AliveAtEnd, r.HeatLevel)
				}
				if r.Winner == "" {
					t.Fatalf("seed=%d 優勝店が確定していない", seed)
				}
				if r.AliveAtEnd != 1 {
					t.Fatalf("seed=%d 決着したのに生存が %d 店", seed, r.AliveAtEnd)
				}
			}
		})
	}
}

// 生存数が少ない領域でも1店ずつ確実に減ること。
//
// 「下位 ThresholdPct%」は生存3店 × 10% = 0.3店 のように0へ丸まりうる。
// 0になると誰も減らず、火力も上がらないまま永久に続く（膠着の穴1）。
// session.cullCandidates の最低1店保証がこれを塞いでいる。
func TestDecisiveness_EndgameAlwaysShrinks(t *testing.T) {
	for _, n := range []int{2, 3, 5, 10} {
		t.Run(fmt.Sprintf("alive=%d", n), func(t *testing.T) {
			// 差がつかない条件で回す（実力差に頼らず storm だけで畳めるか）。
			r := Simulate(newConfig(n, ProfileUniform, 1))
			if r.Stalled {
				t.Fatalf("%d店で決着せず（下位%%が0に丸まっている可能性）", n)
			}
			if r.AliveAtEnd != 1 {
				t.Fatalf("%d店から始めて生存が %d 店で止まった", n, r.AliveAtEnd)
			}
		})
	}
}

// thresholdPct=0 でも storm が必ず1店は削ること（cullCandidates の最低1店保証）。
//
// 「下位N%が0店に丸まる」穴は2段で塞がれているが、**効く範囲が違う**ので注意:
//
//   - `int(len*pct + 0.999999)` の切り上げ … pct > 0 の全域を守る。0.001% でも1店になる
//   - `if cullCount < 1 { cullCount = 1 }` … **pct == 0 のときだけ**効く。切り上げても0のため
//
// つまり小さい pct を渡すテストでは後者の退行を検出できない（切り上げが先に成立してしまう）。
// ここを 0 で突かないと最低1保証は空振りする。
//
// 運営UIから `storm.thresholdPct=0` は保存できてしまう（`Validate()` は 0..1 しか見ない）。
// その設定でも試合が終わることが、制限時間を廃止した今の唯一の決着保証。
func TestDecisiveness_ZeroThresholdStillCulls(t *testing.T) {
	cfg := newConfig(20, ProfileUniform, 1)
	cfg.Params.Storm.ThresholdPct = 0
	// 離脱ペナルティを 0 にして自滅の経路を完全に閉じる。これで storm が唯一の決着手段になり、
	// 「storm が削らなければ必ず膠着する」状態を作れる。
	// （InitialLife を大きくするだけでは、長い試行の間に離脱が積もって自滅が起きうる）
	cfg.Params.Credit.LeaveLoss = game.LeaveLoss{}

	r := Simulate(cfg)
	if r.Stalled {
		t.Fatalf("thresholdPct=0 で決着せず（淘汰人数が0に丸まっている）。"+
			"生存%d店・自滅%d・淘汰%d", r.AliveAtEnd, r.SelfCollapses, r.Culls)
	}
	if r.Culls != cfg.Stores-1 {
		t.Fatalf("storm が削った店数が %d（%d店を期待）。1店ずつ削れていない", r.Culls, cfg.Stores-1)
	}
	if r.SelfCollapses != 0 {
		t.Fatalf("離脱ペナルティ0なのに自滅が %d 件ある（テストの前提が崩れている）", r.SelfCollapses)
	}
}

// 極端に小さい pct でも切り上げで1店以上になること。
func TestDecisiveness_TinyThresholdRoundsUp(t *testing.T) {
	cfg := newConfig(20, ProfileUniform, 1)
	cfg.Params.Storm.ThresholdPct = 0.001 // 20店 × 0.1% = 0.02店
	cfg.Params.Credit.LeaveLoss = game.LeaveLoss{}

	r := Simulate(cfg)
	if r.Stalled {
		t.Fatalf("thresholdPct=0.001 で決着せず。生存%d店・淘汰%d", r.AliveAtEnd, r.Culls)
	}
	if r.Culls != cfg.Stores-1 {
		t.Fatalf("storm が削った店数が %d（%d店を期待）", r.Culls, cfg.Stores-1)
	}
}

// 難度が上端に張り付いたまま長時間決着しないことを検出する（膠着の穴2）。
//
// heatLevel が上がってもお題辞書に段階が無ければ、Next は下の段階へ降りるので
// **火力を上げてもお題は変わらない**。上手い者同士が延々と捌き続ける状態になりうる。
func TestDecisiveness_HeatNotSaturatedTooLong(t *testing.T) {
	// 3000 tick × 150ms = 7.5分。ここまで頭打ちのまま続くなら火力設計に穴がある。
	const limit = 3000

	r := Simulate(newConfig(99, ProfileUniform, 1))
	if r.Stalled {
		t.Fatal("決着せず")
	}
	if r.WordMaxLevel <= 0 {
		t.Fatal("お題辞書の最大段階が取れていない（TicksAtMaxHeat が常に0になり検出が効かない）")
	}
	// 計測器そのものが死んでいないことを確認する。既定値では heatLevel が辞書の上端
	// (level 4) を早々に超えるので、頭打ちの期間は必ず存在する。ここが 0 なら
	// TicksAtMaxHeat は何も見ておらず、下の上限チェックは永久に通ってしまう。
	if r.TicksAtMaxHeat <= 0 {
		t.Fatalf("頭打ち期間が0 tick（最大heatLevel=%d・辞書上端=%d なのに計測されていない）",
			r.MaxHeatLevel, r.WordMaxLevel)
	}
	if r.TicksAtMaxHeat > r.Ticks {
		t.Fatalf("頭打ち期間 %d tick が総 tick 数 %d を超えている", r.TicksAtMaxHeat, r.Ticks)
	}
	if r.TicksAtMaxHeat > limit {
		t.Errorf("難度が上端(level=%d)に張り付いたまま %d tick 経過している。"+
			"お題の段階を増やすか storm を強めること（決着時 heatLevel=%d / 最大 %d）",
			r.WordMaxLevel, r.TicksAtMaxHeat, r.HeatLevel, r.MaxHeatLevel)
	}
}

// 決着時間の実測（レポート専用・失敗させない）。数値の判断は人間がする。
//
//	go test -v ./internal/sim/ -run ReportTiming
func TestDecisiveness_ReportTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("-short では回さない")
	}
	const runs = 10

	t.Logf("%-8s | %-26s | %-22s | %s", "profile", "決着(秒) 平均/最短/最長", "脱落 自滅/淘汰", "heat 決着/最大/上端tick")
	for _, p := range AllProfiles() {
		var times []float64
		var self, cull, atMax, heat, maxHeat int
		for seed := int64(1); seed <= runs; seed++ {
			r := Simulate(newConfig(99, p, seed))
			if r.Stalled {
				t.Fatalf("%s seed=%d 決着せず", p, seed)
			}
			times = append(times, float64(r.ElapsedMs)/1000)
			self += r.SelfCollapses
			cull += r.Culls
			atMax += r.TicksAtMaxHeat
			heat += r.HeatLevel
			maxHeat += r.MaxHeatLevel
		}
		mean, lo, hi := stats(times)
		t.Logf("%-8s | %6.1f / %6.1f / %6.1f      | %8.1f / %8.1f   | %d / %d / %d",
			p, mean, lo, hi,
			float64(self)/runs, float64(cull)/runs,
			heat/runs, maxHeat/runs, atMax/runs)
		if mean < 60 || mean > 300 {
			t.Logf("  ⚠ 目安120〜180秒から大きく外れている（#74）。"+
				"調整候補: storm.intervalTicks / storm.thresholdPct / heat.perAliveDrop  [平均 %.1fs]", mean)
		}
	}
}

func stats(v []float64) (mean, lo, hi float64) {
	lo, hi = v[0], v[0]
	sum := 0.0
	for _, x := range v {
		sum += x
		if x < lo {
			lo = x
		}
		if x > hi {
			hi = x
		}
	}
	return sum / float64(len(v)), lo, hi
}

// ── シミュレータ自体の健全性 ───────────────────────────────

// ダミー店の行列が session の storeQueues とズレていないこと。
// ズレると OrderServed が弾かれ、結果が実態と食い違ったまま気付けない。
func TestSimulate_NoRejectedOrders(t *testing.T) {
	r := Simulate(newConfig(30, ProfileWide, 5))
	if r.Rejected != 0 {
		t.Fatalf("提供報告が %d 件弾かれた（ダミー店の行列が session とズレている）", r.Rejected)
	}
	if r.Served == 0 {
		t.Fatal("1件も提供できていない（打鍵モデルが動いていない）")
	}
}

// 同一シードで結果が再現すること。再現しないとパラメータ調整の前後比較ができない。
func TestSimulate_IsDeterministic(t *testing.T) {
	a := Simulate(newConfig(20, ProfileNormal, 9))
	b := Simulate(newConfig(20, ProfileNormal, 9))

	if a.Ticks != b.Ticks || a.ElapsedMs != b.ElapsedMs || a.Winner != b.Winner ||
		a.Served != b.Served || a.SelfCollapses != b.SelfCollapses || a.Culls != b.Culls {
		t.Fatalf("同一シードで結果が一致しない:\n a=%+v\n b=%+v", a, b)
	}
}

// 生存数の推移が単調減少で、最後は1店になること。
func TestSimulate_AliveCurveIsMonotonic(t *testing.T) {
	r := Simulate(newConfig(30, ProfileNormal, 4))
	if len(r.AliveCurve) < 2 {
		t.Fatalf("生存数の推移が記録されていない: %+v", r.AliveCurve)
	}
	prev := r.Stores + 1
	for _, pt := range r.AliveCurve {
		if pt.Alive > prev {
			t.Fatalf("生存数が増えている: %d → %d (tick %d)", prev, pt.Alive, pt.Tick)
		}
		prev = pt.Alive
	}
	if last := r.AliveCurve[len(r.AliveCurve)-1]; last.Alive != 1 {
		t.Fatalf("最後が生存1店で終わっていない: %+v", last)
	}
}

func TestParseProfile(t *testing.T) {
	for _, s := range []string{"uniform", "normal", "bipolar", "wide"} {
		if _, err := ParseProfile(s); err != nil {
			t.Errorf("ParseProfile(%q) が失敗: %v", s, err)
		}
	}
	if _, err := ParseProfile("fast"); err == nil {
		t.Error("未知の profile がエラーにならない")
	}
	if len(AllProfiles()) != 4 {
		t.Errorf("AllProfiles が4種でない: %v", AllProfiles())
	}
}

// 我慢切れで帰られた客を取りこぼすと、その店は以後1人も捌けなくなる。
func TestDummyStore_LeaveClearsCurrentOrder(t *testing.T) {
	d := &dummyStore{id: "s-1", msPerKey: 100, missRate: 0, alive: true}
	rng := rand.New(rand.NewSource(1))

	d.arrive(proto.CustomerView{CustomerId: "c-1", Words: []string{"たこ"}})
	d.arrive(proto.CustomerView{CustomerId: "c-2", Words: []string{"たこ"}})

	if _, done := d.step(10, rng); done {
		t.Fatal("10ms で打ち終わってしまった")
	}
	if d.current == nil || d.current.customerId != "c-1" {
		t.Fatalf("先頭客に取り掛かっていない: %+v", d.current)
	}

	d.leave("c-1")
	if d.current != nil {
		t.Fatal("帰った客の打鍵状態が残っている")
	}

	served := false
	for i := 0; i < 100 && !served; i++ {
		o, done := d.step(100, rng)
		if done {
			served = true
			if o.CustomerId != "c-2" {
				t.Fatalf("提供した客が違う: %s", o.CustomerId)
			}
		}
	}
	if !served {
		t.Fatal("離脱の後に次の客を捌けていない")
	}
	if len(d.queue) != 0 {
		t.Fatalf("行列が残っている: %+v", d.queue)
	}
}

// 待機中の客が帰った場合は、打鍵中の客の進行を巻き戻さないこと。
func TestDummyStore_LeaveOfWaitingCustomerKeepsProgress(t *testing.T) {
	d := &dummyStore{id: "s-1", msPerKey: 100, missRate: 0, alive: true}
	rng := rand.New(rand.NewSource(1))

	d.arrive(proto.CustomerView{CustomerId: "c-1", Words: []string{"たこやき"}})
	d.arrive(proto.CustomerView{CustomerId: "c-2", Words: []string{"たこ"}})
	d.step(100, rng)
	before := d.current.remainingMs

	d.leave("c-2")
	if d.current == nil || d.current.customerId != "c-1" {
		t.Fatal("打鍵中の客が巻き添えで消えた")
	}
	if d.current.remainingMs != before {
		t.Fatalf("打鍵の進行が巻き戻った: %d → %d", before, d.current.remainingMs)
	}
	if len(d.queue) != 1 || d.queue[0].customerId != "c-1" {
		t.Fatalf("行列の同期が壊れた: %+v", d.queue)
	}
}

// 打鍵数はローマ字換算であること。ルーン数で数えると speed 評価が
// SpeedCap に張り付いて全店同点になり、バランス検証にならない。
func TestCountKeystrokes_UsesRomajiLength(t *testing.T) {
	if got := countKeystrokes([]string{"たこやき"}); got != 8 {
		t.Fatalf("countKeystrokes(たこやき) = %d, want 8", got)
	}
	if got := countKeystrokes([]string{"たこ", "たこ"}); got != 8 {
		t.Fatalf("countKeystrokes(たこ×2) = %d, want 8", got)
	}
}
