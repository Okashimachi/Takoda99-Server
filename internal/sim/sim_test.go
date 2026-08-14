package sim

import (
	"math/rand"
	"testing"

	"takoda99/internal/game"
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

// ── 決着保証（Plan-14 / #34 → plan-h22 §5 で前提を更新）──────────
//
// ~~制限時間を廃止したので、試合が終わる保証は下位淘汰(storm)だけになった。~~
// ~~storm が止まると試合は永遠に終わらない＝当日「次に進めない」という事故になる。~~
//
// **訂正（plan-h22）**: 決着は cullSchedule の最終ステージ（120秒）が**時刻で**保証する。
// 「storm が0店に丸まって膠着する」という予選の穴（下位%指定に由来）は、
// 目標生存数への変更で構造的に消えた。旧テスト
// （AllProfiles / EndgameAlwaysShrinks / ZeroThresholdStillCulls / TinyThresholdRoundsUp /
//   AliveCurveIsMonotonic / HeatNotSaturatedTooLong）は役目を終えたので削除した。
//
// HeatNotSaturatedTooLong は「難度が上端に張り付いたまま決着しない」（膠着の穴2）の検出器
// だったが、120秒で必ず終わる以上、頭打ちが膠着を生むことはもう無い。
// 加えて**試合が生存10店で終わるようになり heatLevel が上端(17)に届かなくなった**ため
// （0 + int(0.1×(99−10)) + Late 8 = 16）、検出器そのものが空振りする。
// 難度カーブと辞書の使い切りはバランスの話なので h26 と ReportTiming が引き取る。
//
// 代わりに検証するのは以下。ここが落ちたらマージ不可。
//   - 脱落カーブが targetAliveCount どおりか
//   - 20秒より前に誰も落ちないか（企画 C4）
//   - finalRank が重複なく 1..N を埋めるか
//   - 同じシードで再現するか（タイブレークの storeId 段が効いているか）

// 決着時間の実測（レポート専用・失敗させない）。数値の判断は人間がする。
//
//	go test -v ./internal/sim/ -run ReportTiming
func TestDecisiveness_ReportTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("-short では回さない")
	}
	const runs = 10

	// 本戦の決着時間は cullSchedule の最終 atMs で確定するので、ここで見るのは
	// 「設定どおりか」の確認と、提供数・heat の分布（h26 のバランス調整の入口）。
	wantMs := game.DefaultParameters().Cull.MatchDurationMs()
	t.Logf("%-8s | %-26s | %-14s | %s", "profile", "決着(秒) 平均/最短/最長", "淘汰/提供", "heat 決着/最大/上端tick")
	for _, p := range AllProfiles() {
		var times []float64
		var cull, served, atMax, heat, maxHeat int
		for seed := int64(1); seed <= runs; seed++ {
			r := Simulate(newConfig(99, p, seed))
			if r.Stalled {
				t.Fatalf("%s seed=%d 決着せず", p, seed)
			}
			times = append(times, float64(r.ElapsedMs)/1000)
			cull += r.Culls
			served += r.Served
			atMax += r.TicksAtMaxHeat
			heat += r.HeatLevel
			maxHeat += r.MaxHeatLevel
		}
		mean, lo, hi := stats(times)
		t.Logf("%-8s | %6.1f / %6.1f / %6.1f      | %5.0f / %6.0f | %d / %d / %d",
			p, mean, lo, hi,
			float64(cull)/runs, float64(served)/runs,
			heat/runs, maxHeat/runs, atMax/runs)
		if mean*1000 != float64(wantMs) {
			t.Logf("  ⚠ 決着が cullSchedule の最終 atMs (%dms) と一致していない [平均 %.1fs]", wantMs, mean)
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
		a.Served != b.Served || a.Culls != b.Culls {
		t.Fatalf("同一シードで結果が一致しない:\n a=%+v\n b=%+v", a, b)
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

// ── 本戦の決着保証（plan-h22 §5）───────────────────────────

// 脱落カーブが cullSchedule の targetAliveCount どおりに出ること。
//
// 実力分布（profile）を変えても脱落人数は変わらないのが目標生存数方式の要点。
// %指定だと生存数に依存して結果が揺れるが、ここは実力に関係なく一致するはず。
func TestCull_AliveCurveMatchesSchedule(t *testing.T) {
	stages := game.DefaultParameters().Cull.Stages
	for _, p := range AllProfiles() {
		t.Run(string(p), func(t *testing.T) {
			for seed := int64(1); seed <= 3; seed++ {
				r := Simulate(newConfig(99, p, seed))
				if r.Stalled {
					t.Fatalf("seed=%d 決着せず", seed)
				}
				if len(r.CullStages) != len(stages) {
					t.Fatalf("seed=%d 実行されたステージ=%d, want %d", seed, len(r.CullStages), len(stages))
				}
				for i, got := range r.CullStages {
					if got.Alive != stages[i].TargetAliveCount {
						t.Fatalf("seed=%d ステージ%d 後の生存=%d, want %d",
							seed, i+1, got.Alive, stages[i].TargetAliveCount)
					}
				}
			}
		})
	}
}

// 120秒（cullSchedule の最終 atMs）で必ず終わり、生存0になること。
func TestCull_FinishesAtScheduleEnd(t *testing.T) {
	wantMs := int64(game.DefaultParameters().Cull.MatchDurationMs())
	for _, p := range AllProfiles() {
		for seed := int64(1); seed <= 3; seed++ {
			r := Simulate(newConfig(99, p, seed))
			if r.Stalled {
				t.Fatalf("%s seed=%d 決着せず", p, seed)
			}
			if r.ElapsedMs != wantMs {
				t.Fatalf("%s seed=%d 決着=%dms, want %dms（cullSchedule の最終 atMs）",
					p, seed, r.ElapsedMs, wantMs)
			}
			if r.AliveAtEnd != 0 {
				t.Fatalf("%s seed=%d 終了時の生存=%d, want 0（全店同時脱落）", p, seed, r.AliveAtEnd)
			}
			if r.Winner == "" {
				t.Fatalf("%s seed=%d 優勝店が確定していない", p, seed)
			}
		}
	}
}

// 第1ステージ（20秒）より前に誰も脱落しないこと（企画 C4）。
//
// 「どれだけ弱くても20秒は遊べる」は本戦が明示的に約束している体験。
// ここが崩れると、開始直後に切られたプレイヤーが何もできずに終わる。
func TestCull_NobodyEliminatedBeforeFirstStage(t *testing.T) {
	firstAt := int64(game.DefaultParameters().Cull.Stages[0].AtMs)
	for _, p := range AllProfiles() {
		r := Simulate(newConfig(99, p, 1))
		for _, pt := range r.AliveCurve {
			if pt.Alive < 99 && pt.ElapsedMs < firstAt {
				t.Fatalf("%s: %dms 時点で生存が %d に減っている（第1ステージは %dms）",
					p, pt.ElapsedMs, pt.Alive, firstAt)
			}
		}
		if len(r.CullStages) > 0 && r.CullStages[0].ElapsedMs < firstAt {
			t.Fatalf("%s: 第1ステージが %dms に実行された（want >= %dms）",
				p, r.CullStages[0].ElapsedMs, firstAt)
		}
	}
}

// finalRank が 1..99 で重複なく埋まること。
//
// 同一ステージで数十店が同時に落ちるので、ここが崩れると順位表に穴や重複が出る。
func TestCull_FinalRanksAreUniqueAndComplete(t *testing.T) {
	sess := simulateForRanks(t, 99, ProfileWide, 7)
	seen := map[int]game.PlayerId{}
	for _, res := range sess {
		if res.FinalRank < 1 || res.FinalRank > 99 {
			t.Fatalf("%s の finalRank=%d が範囲外", res.StoreId, res.FinalRank)
		}
		if prev, dup := seen[res.FinalRank]; dup {
			t.Fatalf("finalRank=%d が重複: %s と %s", res.FinalRank, prev, res.StoreId)
		}
		seen[res.FinalRank] = res.StoreId
	}
	if len(seen) != 99 {
		t.Fatalf("finalRank の種類=%d, want 99", len(seen))
	}
}

// 同一シードで最終順位まで完全に再現すること（タイブレークの storeId 段が効いているか）。
//
// 20秒地点では未提供の店が大量に同点で並ぶ。storeId 段が無いと map 反復順で
// 並びが揺れ、シードを固定しても結果が変わってバランス調整が信用できなくなる。
func TestCull_RanksAreDeterministic(t *testing.T) {
	a := simulateForRanks(t, 99, ProfileUniform, 3)
	b := simulateForRanks(t, 99, ProfileUniform, 3)
	if len(a) != len(b) {
		t.Fatalf("結果の店数が違う: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].StoreId != b[i].StoreId || a[i].FinalRank != b[i].FinalRank {
			t.Fatalf("同一シードで順位が一致しない: %s=%d vs %s=%d",
				a[i].StoreId, a[i].FinalRank, b[i].StoreId, b[i].FinalRank)
		}
	}
}

// simulateForRanks は1試合を回して最終結果を返す（順位検証用）。
func simulateForRanks(t *testing.T, stores int, p Profile, seed int64) []game.StoreResult {
	t.Helper()
	r := Simulate(newConfig(stores, p, seed))
	if r.Stalled {
		t.Fatalf("seed=%d 決着せず", seed)
	}
	return r.Results
}

// ── バランスの回帰固定（plan-h26 §2）─────────────────────────
//
// 数値そのものは h26 で調整し続けるので、ここで固定するのは
// **「壊れていないこと」の下限**だけ。狭く固定すると調整のたびに落ちて邪魔になる。

// 負のスコアがほぼ発生しないこと（h21 §1.1 でクランプを外した副作用の確認）。
//
// 0 でクランプしない判断は「下位が 0 に密集して足切りが恣意的になる」のを避けるためで、
// 実際に負が多発するなら重みが厳しすぎる。
func TestBalance_NegativeScoresAreRare(t *testing.T) {
	total, negative := 0, 0
	for _, p := range AllProfiles() {
		for seed := int64(1); seed <= 3; seed++ {
			r := Simulate(newConfig(99, p, seed))
			b := Analyze(r)
			total += len(r.Results)
			negative += b.NegativeScores
		}
	}
	if total == 0 {
		t.Fatal("試合が回っていない")
	}
	// 1% を超えたら重みが厳しすぎる。
	if rate := float64(negative) / float64(total); rate > 0.01 {
		t.Fatalf("負スコアの発生率 %.1f%%（%d/%d）。score.weightMiss が厳しすぎる", rate*100, negative, total)
	}
}

// スコア分布が上位と下位に分離していること（P3）。
//
// 団子だと足切りが「誰を切るか」をタイブレーク頼みで決めることになり、
// 「速く正確に打った人が上に行く」という本戦の原則が成立しない。
func TestBalance_ScoreDistributionSeparates(t *testing.T) {
	for _, p := range []Profile{ProfileNormal, ProfileWide, ProfileBipolar} {
		t.Run(string(p), func(t *testing.T) {
			b := Analyze(Simulate(newConfig(99, p, 3)))
			if b.Separation <= 0 {
				t.Fatalf("上位1/4と下位1/4が分離していない: 上位 %.0f / 下位 %.0f", b.TopAvg, b.BottomAvg)
			}
			// 上位が下位の2倍未満なら団子とみなす。
			if b.TopAvg < b.BottomAvg*2 {
				t.Fatalf("スコアが団子になっている: 上位 %.0f / 下位 %.0f（分離度 %.0f）",
					b.TopAvg, b.BottomAvg, b.Separation)
			}
		})
	}
}

// 実力と最終順位が強く相関すること（＝運ゲーになっていない）。
func TestBalance_StrongPlayersRankHigher(t *testing.T) {
	for _, p := range []Profile{ProfileNormal, ProfileWide} {
		t.Run(string(p), func(t *testing.T) {
			var sum float64
			const runs = 3
			for seed := int64(1); seed <= runs; seed++ {
				sum += Analyze(Simulate(newConfig(99, p, seed))).RankAbilityCorr
			}
			if corr := sum / runs; corr < 0.6 {
				t.Fatalf("実力と順位の相関 %.2f が低すぎる（運ゲーになっている）", corr)
			}
		})
	}
}

// 実力上位が最初の2ステージで切られる事故が稀であること（P1 / P4）。
//
// 「どれだけ弱くても20秒は遊べる」の裏返しで、**強いのに20秒で落ちる**のは納得感を壊す。
func TestBalance_StrongPlayersSurviveEarlyStages(t *testing.T) {
	strong, accidents := 0, 0
	const runs = 5
	for seed := int64(1); seed <= runs; seed++ {
		r := Simulate(newConfig(99, ProfileNormal, seed))
		strong += len(r.Results) / 4
		accidents += Analyze(r).EarlyCutStrong
	}
	// 実力上位1/4のうち 5% を超えて早期に落ちるなら、足切りが実力を見ていない。
	if rate := float64(accidents) / float64(strong); rate > 0.05 {
		t.Fatalf("早期切り事故率 %.1f%%（%d/%d）が高すぎる", rate*100, accidents, strong)
	}
}

// 火力が辞書の上端に到達すること（plan-h26 §1.2）。
//
// 本戦は生存10店で終わるので、heat.phaseLate が低いと最上位レベルの語彙が一度も使われず、
// heat.maxLevel が「絶対に効かないツマミ」になる。
func TestBalance_HeatReachesDictionaryTop(t *testing.T) {
	r := Simulate(newConfig(99, ProfileNormal, 1))
	if r.WordMaxLevel <= 0 {
		t.Fatal("お題辞書の最大段階が取れていない")
	}
	if r.MaxHeatLevel < r.WordMaxLevel {
		t.Fatalf("火力が辞書上端に届いていない: 最大 heatLevel=%d / 辞書上端=%d。"+
			"heat.phaseLate を上げるか heat.maxLevel を下げること（plan-h26 §1.2）",
			r.MaxHeatLevel, r.WordMaxLevel)
	}
}
