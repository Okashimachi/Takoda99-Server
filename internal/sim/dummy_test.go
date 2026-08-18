package sim

import (
	"math"
	"math/rand"
	"testing"

	"takoda99/internal/game"
	"takoda99/internal/typist"
)

// dummy_test.go は plan-h33（sim の人間モデルの精緻化）の検証。
//
// 方式は「バグを注入したら落ちること」（plan-h33 §4）。各テストのコメントに
// **どの変異で落ちるか**を書いてある。そこが書けない検証は守れていないのと同じ。

func buildFor(t *testing.T, n int, p Profile, seed int64) []*dummyStore {
	t.Helper()
	return buildStores(Config{
		Params:  game.DefaultParameters(),
		Stores:  n,
		Profile: p,
		Rng:     rand.New(rand.NewSource(seed)),
	})
}

// ── ① 速度とミス率の相関（plan-h33 §0.2① が直したかった当のもの）──

// 速度とミス率は**正の相関**を持つこと。
//
// 変異で落ちること: buildStores の normal を `msPerKey` と `missRate` の
// 独立な乱数2本に戻すと、相関が 0 付近に落ちてこのテストが失敗する（旧実装の実測 +0.016）。
func TestSkill_SpeedAndMissRateCorrelate(t *testing.T) {
	for _, p := range []Profile{ProfileNormal, ProfileWide} {
		ss := buildFor(t, 3000, p, 42)
		ms := make([]float64, 0, len(ss))
		miss := make([]float64, 0, len(ss))
		for _, s := range ss {
			ms = append(ms, s.ability.MsPerKey)
			miss = append(miss, s.ability.MissRate)
		}
		got := pearson(ms, miss)
		// skill 1本から線形に導いているので理想は +1.00。
		// msPerKey の下限クランプぶんだけ 1 を割る余地があるので 0.99 で見る。
		if got < 0.99 {
			t.Errorf("profile=%s: 速度とミス率の相関が %.3f（正の相関を期待）", p, got)
		}
		t.Logf("profile=%-7s pearson(msPerKey, missRate) = %+.3f", p, got)
	}
}

// 実在しない個体（速いのに雑／遅いのに完璧）が生まれないこと。
//
// 旧実装は独立の乱数2本だったので「130ms/打鍵でミス率9%」が普通に出ていた。
// 変異で落ちること: 相関を外すと必ず反例が出る。
func TestSkill_NoImpossibleIndividuals(t *testing.T) {
	ss := buildFor(t, 3000, ProfileNormal, 7)
	for _, s := range ss {
		fast := s.ability.MsPerKey <= 150
		sloppy := s.ability.MissRate >= 0.07
		if fast && sloppy {
			t.Fatalf("実在しない個体: %.0fms/打鍵 なのにミス率 %.3f", s.ability.MsPerKey, s.ability.MissRate)
		}
		slow := s.ability.MsPerKey >= 280
		perfect := s.ability.MissRate <= 0.02
		if slow && perfect {
			t.Fatalf("実在しない個体: %.0fms/打鍵 なのにミス率 %.3f", s.ability.MsPerKey, s.ability.MissRate)
		}
	}
}

// ── ② 難度追従（plan-h33 §1.2）──

// 難度が上がると1打鍵あたりも遅くなること、かつ **skill が高いほど難度に強い**こと。
//
// 変異で落ちること: SkillCurve の HighHeatPenalty / LowHeatPenalty を同値にすると
// 「上手いほど難度に強い」が消えて後半の検査が落ちる。両方 0 にすると前半も落ちる。
func TestSkill_HeatPenaltyIsInverseToSkill(t *testing.T) {
	curve := HumanCurve()
	const topHeat = 17

	prevRatio := math.Inf(1)
	t.Logf("=== 難度追従（heat 0 → %d の実効ms/打鍵）===", topHeat)
	for _, skill := range []float64{0, 0.25, 0.5, 0.75, 1} {
		ab := curve.At(skill)
		at0 := ab.MsPerKey
		at17 := ab.MsPerKey * (1 + ab.HeatPenalty*topHeat)
		if at17 <= at0 {
			t.Errorf("skill=%.2f: heat が上がっても遅くならない（%.0f → %.0f）", skill, at0, at17)
		}
		ratio := at17 / at0
		if ratio >= prevRatio {
			t.Errorf("skill=%.2f: 劣化率 %.3f が skill の低い側 %.3f 以上。難度追従が skill に反比例していない",
				skill, ratio, prevRatio)
		}
		prevRatio = ratio
		t.Logf("  skill %.2f  penalty %.3f  %6.0f → %6.0f ms/打鍵 (×%.2f)",
			skill, ab.HeatPenalty, at0, at17, ratio)
	}
}

// 難度追従が**試合を通しても効いている**こと（実効値が heat で伸びる）。
//
// 変異で落ちること: dummyStore.begin が typist.Serve に heatLevel=0 を渡す旧実装へ戻すと、
// heat 17 の所要時間が heat 0 と同じになって落ちる。
func TestSkill_ServeSlowsDownWithHeat(t *testing.T) {
	ab := HumanCurve().At(0.5)
	const keys = 60
	sum := func(heat int) int {
		rng := rand.New(rand.NewSource(11))
		total := 0
		for i := 0; i < 300; i++ {
			total += typist.Serve(ab, keys, heat, rng).ElapsedMs
		}
		return total
	}
	at0, at17 := sum(0), sum(17)
	want := 1 + ab.HeatPenalty*17
	got := float64(at17) / float64(at0)
	if math.Abs(got-want) > 0.02 {
		t.Errorf("heat 0→17 の所要時間比が %.3f（期待 %.3f）", got, want)
	}
	t.Logf("heat 0: %d ms / heat 17: %d ms（×%.2f）", at0, at17, got)
}

// ── ③ duel は h26 の道具のまま無傷であること ──

// duel は skill 相関にも難度追従にも乗せない（plan-h33 §1.1 / 本セッションの制約）。
//
// 🔴 変異で落ちること: duel に HeatPenalty を入れる／skill 経由に変えると落ちる。
// h26 §2.2 の実測表がこの数字に乗っているので、動かすと過去の測定と比較できなくなる。
func TestDuel_IsExcludedFromSkillModel(t *testing.T) {
	ss := buildFor(t, 100, ProfileDuel, 3)
	var fast, precise int
	for _, s := range ss {
		if s.skill != noSkill {
			t.Fatalf("duel の個体に skill が付いている: %v", s.skill)
		}
		if s.ability.HeatPenalty != 0 {
			t.Fatalf("duel に難度追従が入っている: %v", s.ability.HeatPenalty)
		}
		switch s.class {
		case ClassFast:
			fast++
			if s.ability.MsPerKey != duelFastMsPerKey || s.ability.MissRate != duelFastMissRate {
				t.Fatalf("速さ型の実力が変わっている: %v", s.ability)
			}
		case ClassPrecise:
			precise++
			if s.ability.MsPerKey != duelPreciseMsPerKey || s.ability.MissRate != duelPreciseMissRate {
				t.Fatalf("正確型の実力が変わっている: %v", s.ability)
			}
		default:
			t.Fatalf("duel なのにクラスが付いていない: %q", s.class)
		}
	}
	if fast != 50 || precise != 50 {
		t.Fatalf("duel が半々でない: fast=%d precise=%d", fast, precise)
	}
}

// duel は**乱数を1つも消費しない**こと。
//
// 🔴 これが崩れると、後続の打鍵乱数が全部ずれて h26 のスイープが別物になる。
// 変異で落ちること: duel でも skill を引くようにすると落ちる。
func TestDuel_ConsumesNoRandomDraws(t *testing.T) {
	probe := rand.New(rand.NewSource(99))
	want := probe.Int63()

	rng := rand.New(rand.NewSource(99))
	buildStores(Config{Params: game.DefaultParameters(), Stores: 99, Profile: ProfileDuel, Rng: rng})
	if got := rng.Int63(); got != want {
		t.Fatalf("duel の店生成が乱数を消費している（次の値 %d != %d）", got, want)
	}
}

// uniform も乱数を消費せず、旧実装と**同じ実力値**であること（200ms/打鍵・ミス率5%）。
// skill 曲線の中央を旧実装の中心に合わせてある根拠（dummy.go の定数コメント）の固定。
func TestUniform_MatchesLegacyCenter(t *testing.T) {
	ss := buildFor(t, 10, ProfileUniform, 5)
	for _, s := range ss {
		if s.ability.MsPerKey != 200 || math.Abs(s.ability.MissRate-0.05) > 1e-9 {
			t.Fatalf("uniform の実力が 200ms/5%% でない: %v", s.ability)
		}
	}
}

// ── ④ 本番の卓（--profile match）──

// Bot の tier 配分が h31 の重み（25/50/25）どおりで、シード固定で再現すること。
//
// 変異で落ちること: drawBotAbility の重み付き抽選を一様抽選にすると 33/33/33 になって落ちる。
func TestMatch_BotTierDistributionFollowsWeights(t *testing.T) {
	const bots = 96
	counts := map[string]int{}
	ss := buildFor(t, bots+3, ProfileMatch, 31)
	humans := 0
	for _, s := range ss {
		if s.human {
			humans++
			continue
		}
		counts[s.tier]++
	}
	if humans != DefaultMatchHumans {
		t.Fatalf("人間が %d 名（既定 %d を期待）", humans, DefaultMatchHumans)
	}
	tiers := game.DefaultParameters().Bot.EffectiveTiers()
	total := 0
	for _, tr := range tiers {
		total += tr.Weight
	}
	for i, tr := range tiers {
		label := game.BotTierLabel(i)
		want := float64(bots) * float64(tr.Weight) / float64(total)
		got := float64(counts[label])
		if math.Abs(got-want) > want*0.5 {
			t.Errorf("tier %s が %.0f 体（期待 %.1f 体）", label, got, want)
		}
		t.Logf("  tier %-6s %2.0f 体（期待 %.1f）", label, got, want)
	}

	// シード固定で再現すること。
	again := buildFor(t, bots+3, ProfileMatch, 31)
	for i := range ss {
		if ss[i].tier != again[i].tier || ss[i].ability != again[i].ability {
			t.Fatalf("同じシードで再現しない: %d 番目 %v/%v vs %v/%v",
				i, ss[i].tier, ss[i].ability, again[i].tier, again[i].ability)
		}
	}
}

// 人間の平均順位が出ること。**これが h33 の実用的な目的**（plan-h33 §2）。
//
// 変異で落ちること: analyzeHumans が Human フラグを見なくなると HumanCount=0 で落ちる。
func TestMatch_ReportsHumanRank(t *testing.T) {
	const runs = 6
	bs := make([]Balance, 0, runs)
	for i := 0; i < runs; i++ {
		cfg := newConfig(99, ProfileMatch, 200+int64(i))
		bs = append(bs, Analyze(Simulate(cfg)))
	}
	m := MeanBalance(bs)
	if m.HumanCount != DefaultMatchHumans*runs {
		t.Fatalf("人間の観測数が %d（期待 %d）", m.HumanCount, DefaultMatchHumans*runs)
	}
	if m.HumanAvgRank <= 0 || m.HumanAvgRank > 99 {
		t.Fatalf("人間の平均順位が範囲外: %.1f", m.HumanAvgRank)
	}
	stageSum := 0
	for _, c := range m.HumanCullStages {
		stageSum += c
	}
	if stageSum != m.HumanCount {
		t.Errorf("脱落ステージの合計 %d が人間の数 %d と合わない", stageSum, m.HumanCount)
	}
	t.Logf("人間の平均順位 %.1f/99 ・1位 %d/%d 試合 ・脱落段 %v",
		m.HumanAvgRank, m.HumanTop1, runs, m.HumanCullStages)
}

// cullStageOf は「その順位の店が何段目で落ちたか」を返すこと。
func TestCullStageOf(t *testing.T) {
	stages := []CullStageResult{{Alive: 75}, {Alive: 55}, {Alive: 35}, {Alive: 20}, {Alive: 10}, {Alive: 0}}
	cases := []struct{ rank, want int }{
		{99, 1}, {76, 1}, {75, 2}, {56, 2}, {55, 3}, {36, 3}, {21, 4}, {11, 5}, {10, 6}, {1, 6},
	}
	for _, c := range cases {
		if got := cullStageOf(c.rank, stages); got != c.want {
			t.Errorf("cullStageOf(%d) = %d（期待 %d）", c.rank, got, c.want)
		}
	}
}

// ── ⑤ 決定性（sim の全結論の前提）──

// 同じシードなら**店の実力も試合結果も**完全に一致すること。
//
// 変異で落ちること: buildStores の乱数消費をプロファイルごとに条件分岐で飛ばすと、
// 消費数が状況で変わって再現しなくなる。
func TestBuildStores_IsDeterministic(t *testing.T) {
	for _, p := range append(AllProfiles(), ProfileDuel) {
		a := buildFor(t, 99, p, 1234)
		b := buildFor(t, 99, p, 1234)
		for i := range a {
			if a[i].ability != b[i].ability || a[i].skill != b[i].skill ||
				a[i].tier != b[i].tier || a[i].class != b[i].class || a[i].human != b[i].human {
				t.Fatalf("profile=%s の %d 番目が再現しない", p, i)
			}
		}
	}
}

func pearson(xs, ys []float64) float64 {
	n := float64(len(xs))
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx /= n
	my /= n
	var sxy, sxx, syy float64
	for i := range xs {
		dx, dy := xs[i]-mx, ys[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		return 0
	}
	return sxy / math.Sqrt(sxx*syy)
}
