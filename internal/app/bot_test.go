package app_test

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"takoda99/internal/app"
	"takoda99/internal/game"
	"takoda99/internal/odai"
	"takoda99/internal/typist"
)

// 🔴 tier 配分が重みどおりで、**シード固定で再現する**（plan-h31 §3・§7）。
//
// 99体で 25/50/25 に近いこと。抽選を「全 Bot に同じ tier」へ戻す変異で落ちる。
func TestDrawBotSpec_TierDistribution(t *testing.T) {
	bp := game.DefaultParameters().Bot

	draw := func(seed int64) [game.BotTierCount]int {
		rng := rand.New(rand.NewSource(seed))
		var counts [game.BotTierCount]int
		for i := 0; i < 99; i++ {
			counts[app.DrawBotSpec(bp, rng).Tier]++
		}
		return counts
	}

	got := draw(1)
	t.Logf("99体の tier 配分（seed=1）: strong=%d normal=%d weak=%d", got[0], got[1], got[2])

	// 重み 25/50/25 → 期待 24.75 / 49.5 / 24.75。二項分布のばらつきを見て ±12 まで許す。
	want := [game.BotTierCount]float64{24.75, 49.5, 24.75}
	for i := range got {
		if math.Abs(float64(got[i])-want[i]) > 12 {
			t.Errorf("tier %s = %d 体（期待 %.1f 付近）。重み付き抽選になっていない",
				game.BotTierLabel(i), got[i], want[i])
		}
	}

	// シードを固定すれば同じ配分（matchsim で再現できることの担保）。
	if again := draw(1); again != got {
		t.Fatalf("同じシードで配分が変わった: %v vs %v", got, again)
	}
	if other := draw(2); other == got {
		t.Fatal("別シードでも同じ配分（乱数を使っていない疑い）")
	}
}

// 🔴 個体差が入り、**速度とミス率が正の相関**を持つこと（plan-h31 §2.1）。
//
// 同じ tier の中でも個体がばらけること、tier をまたいでも「遅い個体ほどミスが多い」こと。
func TestDrawBotSpec_IndividualSpreadAndCorrelation(t *testing.T) {
	bp := game.DefaultParameters().Bot
	rng := rand.New(rand.NewSource(11))

	const n = 990
	xs := make([]float64, 0, n)
	ys := make([]float64, 0, n)
	sameTier := map[float64]bool{}
	for i := 0; i < n; i++ {
		s := app.DrawBotSpec(bp, rng)
		xs = append(xs, s.Ability.MsPerKey)
		ys = append(ys, s.Ability.MissRate)
		if s.Tier == game.BotTierNormal {
			sameTier[s.Ability.MsPerKey] = true
		}
	}

	if len(sameTier) < 100 {
		t.Fatalf("同じ tier の個体が %d 種類しかない。個体差が入っていない", len(sameTier))
	}

	r := pearson(xs, ys)
	t.Logf("速度とミス率の相関 r=%.4f（990体・tier 混在）", r)
	if r < 0.9 {
		t.Fatalf("相関 r=%.4f が低い。速度とミス率を独立に振っている疑い", r)
	}
}

// 個体は生成時に固定される（同じ BotSpec を2回使っても基準が変わらない）。
func TestDrawBotSpec_AbilityIsFixed(t *testing.T) {
	bp := game.DefaultParameters().Bot
	spec := app.DrawBotSpec(bp, rand.New(rand.NewSource(3)))

	// 揺らぎを消して同じ語を2回打たせると、まったく同じ結果になる。
	ab := spec.Ability
	ab.JitterMs = 0
	rng := rand.New(rand.NewSource(1))
	first := typist.Serve(ab, 40, 5, rng)
	rng = rand.New(rand.NewSource(1))
	second := typist.Serve(ab, 40, 5, rng)
	if first != second {
		t.Fatalf("同じ個体・同じ条件で結果が変わった: %+v vs %+v", first, second)
	}
}

// ゼロの BotParams（＝本番DBに tiers が無い状態）でも抽選が成立する。
// EffectiveTiers の読み替えが効かないと「1打鍵0ms の Bot」が生まれる。
func TestDrawBotSpec_SurvivesZeroParams(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for i := 0; i < 100; i++ {
		s := app.DrawBotSpec(game.BotParams{}, rng)
		if s.Ability.MsPerKey <= 0 {
			t.Fatalf("msPerKey=%v（0 だと無限に速い Bot になる）", s.Ability.MsPerKey)
		}
		if s.TierName == "" {
			t.Fatal("tier 名が空（観測で人間と区別できない）")
		}
	}
}

// Bot 枠には tier が載り、matchmaking.Player 経由で観測へ運ばれる（plan-h31 §6）。
func TestNewBotPlayer_CarriesTier(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := app.NewBotPlayer(ctx, "b-1", game.DefaultParameters().Bot)
	if !p.IsBot {
		t.Fatal("IsBot が false")
	}
	switch p.Tier {
	case "strong", "normal", "weak":
	default:
		t.Fatalf("tier=%q が想定外", p.Tier)
	}
}

// ── 実測レポート（数値を決めるための観測。判定はしない）───────────────────
//
//	go test -v ./internal/app/ -run ReportBotTiers
//
// tier ごとに「heat 別の1注文あたり所要時間・ミス数」を出す。plan-h31 §4 の既定値を
// 詰めるときと、h33（sim の人間モデル）と突き合わせるときに使う。
func TestReportBotTiers(t *testing.T) {
	bp := game.DefaultParameters().Bot
	tiers := bp.EffectiveTiers()
	pool := odai.NewStaticPool()
	orderCount := game.DefaultParameters().Customer.Normal.OrderCount

	t.Logf("=== 既定 tier（個体差 ±%.0f%%）===", bp.EffectiveIndividualSpread()*100)
	for i, tr := range tiers {
		t.Logf("  %-6s weight=%2d msPerKey=%3d missRate=%.3f heatPenalty=%.3f",
			game.BotTierLabel(i), tr.Weight, tr.MsPerKey, tr.MissRate, tr.HeatPenalty)
	}

	t.Logf("=== 通常客1人（%d語）を打ち切るまで ===", orderCount)
	t.Logf("  %-5s %-6s %8s %8s %8s", "heat", "tier", "打鍵", "所要s", "ミス")
	for _, heat := range []int{0, 4, 8, 12, 17} {
		for i := range tiers {
			rng := rand.New(rand.NewSource(int64(100 + heat)))
			ab := typist.Individual(typist.Ability{
				MsPerKey:    float64(tiers[i].MsPerKey),
				MissRate:    tiers[i].MissRate,
				HeatPenalty: tiers[i].HeatPenalty,
			}, 1)
			const runs = 200
			keys, ms, miss := 0, 0, 0
			for r := 0; r < runs; r++ {
				k := 0
				for w := 0; w < orderCount; w++ {
					k += pool.Next(heat, rng).KeystrokeCount
				}
				out := typist.Serve(ab, k, heat, rng)
				keys += k
				ms += out.ElapsedMs
				miss += out.MissCount
			}
			t.Logf("  %-5d %-6s %8.1f %8.2f %8.2f", heat, game.BotTierLabel(i),
				float64(keys)/runs, float64(ms)/runs/1000, float64(miss)/runs)
		}
	}
}

// 🔴 **本 plan の目的そのものの実測: 人間は真ん中に来るか**（plan-h31 冒頭）。
//
// 「実効ms/打鍵（＝速度とミス率をまとめた実力）」で 99体の Bot と人間を並べ、
// 人間が上から何%に来るかを出す。既定 tier の中心は sim の ProfileNormal（200ms/打鍵・
// ミス率5%）と揃えてあるので、その実力の人間はちょうど中位に落ちるはず。
//
// ⚠ ここは**モデル上の順位**であって試合結果ではない（お題の運・客の分配は含まない）。
// 実試合での確認は h26 §3 と当日のダッシュボード（h34）の役目。
func TestReportHumanPercentile(t *testing.T) {
	bp := game.DefaultParameters().Bot
	tiers := bp.EffectiveTiers()

	// 参考にする人間の実力（sim の ProfileNormal の中心）と、その上下。
	humans := []struct {
		label    string
		msPerKey float64
		missRate float64
	}{
		{"速い人間 (150ms/打鍵)", 150, 0.03},
		{"標準の人間 (200ms/打鍵)", 200, 0.05},
		{"のんびり (280ms/打鍵)", 280, 0.10},
		{"初心者 (400ms/打鍵)", 400, 0.15},
	}

	rng := rand.New(rand.NewSource(31))
	bots := make([]float64, 0, 99)
	for i := 0; i < 99; i++ {
		s := app.DrawBotSpec(bp, rng)
		bots = append(bots, s.Ability.EffectiveMsPerKey())
	}

	t.Logf("=== 99体の Bot に人間を混ぜたときの順位（実効ms/打鍵の昇順・1位が最速）===")
	for _, h := range humans {
		eff := typist.Ability{MsPerKey: h.msPerKey, MissRate: h.missRate}.EffectiveMsPerKey()
		rank := 1
		for _, b := range bots {
			if b < eff {
				rank++
			}
		}
		t.Logf("  %-24s 実効 %6.1f ms/打鍵 → %2d 位 / 100（上位 %.0f%%）",
			h.label, eff, rank, float64(rank)/100*100)
	}
	for i := range tiers {
		t.Logf("  tier %-6s 実効 %6.1f ms/打鍵", game.BotTierLabel(i),
			typist.Ability{MsPerKey: float64(tiers[i].MsPerKey), MissRate: tiers[i].MissRate}.EffectiveMsPerKey())
	}
}

// 語の長さ（打鍵数）ごとの平均ミス数。**0/1 固定でないこと**の実測（plan-h31 §7）。
func TestReportBotMissByWordLength(t *testing.T) {
	tiers := game.DefaultParameters().Bot.EffectiveTiers()
	t.Logf("  %-8s %s", "打鍵数", "平均ミス数（strong / normal / weak）")
	for _, keys := range []int{5, 13, 26, 43, 87, 130} {
		row := make([]string, 0, game.BotTierCount)
		for i := range tiers {
			rng := rand.New(rand.NewSource(int64(keys)))
			ab := typist.Ability{MsPerKey: float64(tiers[i].MsPerKey), MissRate: tiers[i].MissRate}
			const runs = 2000
			miss := 0
			for r := 0; r < runs; r++ {
				miss += typist.Serve(ab, keys, 0, rng).MissCount
			}
			row = append(row, fmt.Sprintf("%.2f", float64(miss)/runs))
		}
		t.Logf("  %-8d %s", keys, fmt.Sprint(row))
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
