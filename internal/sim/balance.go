package sim

import (
	"math"
	"sort"

	"takoda99/internal/game"
)

// balance.go は plan-h26（バランス検証）の観測。
//
// Simulate が返す「1試合の生データ」から、**数値を決めるために見たい指標**を計算する。
// ここに判定は書かない（合否は人間が決める）。sim_test の回帰固定と cmd/matchsim の
// レポートが同じ計算を共有するための場所。

// Balance は1試合ぶんのバランス観測。
type Balance struct {
	// ── 速さ型 vs 正確型（plan-h26 §2.1・最重要）──
	// ProfileDuel のときだけ意味を持つ。
	FastCount, PreciseCount int
	// FastWins は上位半分に入った速さ型の数。拮抗していれば FastCount の約半分になる。
	FastWins, PreciseWins int
	// FastAvgRank / PreciseAvgRank は平均最終順位（小さいほど強い）。
	// **この2つが近いほど「どちらを取るかの判断が発生する」状態**。
	FastAvgRank, PreciseAvgRank float64
	// Winner1Class は優勝者のクラス。
	Winner1Class string
	// Top10Fast は決勝ライン（上位10位）に入った速さ型の数。
	//
	// 🔴 **平均順位だけでは拮抗を判定できない。** 2つの型は分散が違う
	// （ミス減点が速さ型の分散を抑えるため、正確型のほうが上振れしやすい）。
	// 平均順位が同じでも上位は片方が独占しうるので、こちらも併せて見る。
	Top10Fast int

	// ── スコア分布（P3）──
	// TopAvg / BottomAvg は最終スコアの上位1/4・下位1/4の平均。
	TopAvg, BottomAvg float64
	// Separation は上位と下位の差。小さいと団子＝足切りがタイブレーク頼みになる。
	Separation float64
	// NegativeScores は最終スコアが負だった店の数。ほぼ 0 であることが目標（h21 §1.1）。
	NegativeScores int

	// ── 足切りの妥当性（P1 / P4）──
	// EarlyCutStrong は「実力上位1/4なのに最初の2ステージで切られた店」の数＝事故。
	EarlyCutStrong int
	// RankAbilityCorr は実力（実効ms・小さいほど強い）と最終順位の順位相関（Spearman）。
	// **+1 に近いほど「強い店ほど上位」**。低いと運ゲー。
	RankAbilityCorr float64

	// FastWinnerRatio は MeanBalance でのみ埋まる「速さ型が優勝した試合の割合」。
	// 0.5 付近が狙い。0 か 1 に張り付いていたら重みが偏っている。
	FastWinnerRatio float64

	// ── 本番の卓での人間の位置（ProfileMatch・plan-h33 §2）──
	// **これが h33 の実用的な目的**。「人間が真ん中あたりに来る」を数字で確認する。
	//
	// HumanCount は人間として置いた店の数（延べ）。
	HumanCount int
	// HumanAvgRank は人間の平均最終順位（99人中 40〜60 が目標）。
	HumanAvgRank float64
	// HumanTop1 は1位を取った人間の延べ数。HumanWinRatio は MeanBalance でのみ埋まる
	// 「その試合で人間の誰かが1位だった割合」。
	HumanTop1     int
	HumanWinRatio float64
	// HumanCullStages[k] は「k+1 段目の足切りで落ちた人間の延べ数」。
	// どの足切りで落ちるかの分布で、平均順位だけでは見えない偏りが出る。
	HumanCullStages []int
	// BotTierCounts は tier ラベル別の Bot 数（延べ）。h31 の重みどおりかの確認用。
	BotTierCounts map[string]int
}

// Analyze は1試合の結果からバランス観測を計算する。
func Analyze(r Result) Balance {
	var b Balance
	if len(r.Results) == 0 {
		return b
	}

	rankOf := make(map[game.PlayerId]int, len(r.Results))
	scoreOf := make(map[game.PlayerId]int, len(r.Results))
	for _, res := range r.Results {
		rankOf[res.StoreId] = res.FinalRank
		scoreOf[res.StoreId] = res.Score
		if res.Score < 0 {
			b.NegativeScores++
		}
	}

	n := len(r.Results)
	half := n / 2
	quarter := max(1, n/4)

	// ── 速さ型 vs 正確型 ──
	var fastRankSum, preciseRankSum float64
	for _, a := range r.Abilities {
		rk, ok := rankOf[a.Id]
		if !ok {
			continue
		}
		switch a.Class {
		case ClassFast:
			b.FastCount++
			fastRankSum += float64(rk)
			if rk <= half {
				b.FastWins++
			}
			if rk <= 10 {
				b.Top10Fast++
			}
		case ClassPrecise:
			b.PreciseCount++
			preciseRankSum += float64(rk)
			if rk <= half {
				b.PreciseWins++
			}
		}
		if rk == 1 {
			b.Winner1Class = a.Class
		}
	}
	if b.FastCount > 0 {
		b.FastAvgRank = fastRankSum / float64(b.FastCount)
	}
	if b.PreciseCount > 0 {
		b.PreciseAvgRank = preciseRankSum / float64(b.PreciseCount)
	}

	// ── スコア分布 ──
	// 順位順（1位が先頭）に並べてから上下1/4を取る。
	byRank := append([]game.StoreResult(nil), r.Results...)
	sort.Slice(byRank, func(i, j int) bool { return byRank[i].FinalRank < byRank[j].FinalRank })
	avg := func(rows []game.StoreResult) float64 {
		if len(rows) == 0 {
			return 0
		}
		sum := 0
		for _, x := range rows {
			sum += x.Score
		}
		return float64(sum) / float64(len(rows))
	}
	b.TopAvg = avg(byRank[:quarter])
	b.BottomAvg = avg(byRank[len(byRank)-quarter:])
	b.Separation = b.TopAvg - b.BottomAvg

	// ── 足切りの妥当性 ──
	// 実力順（実効msの昇順＝強い順）に並べ、上位1/4を「実力上位」とみなす。
	ab := append([]Ability(nil), r.Abilities...)
	sort.Slice(ab, func(i, j int) bool {
		if ab[i].EffectiveMsPerKey != ab[j].EffectiveMsPerKey {
			return ab[i].EffectiveMsPerKey < ab[j].EffectiveMsPerKey
		}
		return ab[i].Id < ab[j].Id
	})
	// 最初の2ステージで切られた店 = finalRank が「2段階目の目標生存数」より大きい。
	// CullStages から実際の生存数を取る（config を変えても追随する）。
	earlyCutBelow := 0
	if len(r.CullStages) >= 2 {
		earlyCutBelow = r.CullStages[1].Alive
	}
	for i, a := range ab {
		if i >= quarter {
			break
		}
		if rk, ok := rankOf[a.Id]; ok && earlyCutBelow > 0 && rk > earlyCutBelow {
			b.EarlyCutStrong++
		}
	}

	b.RankAbilityCorr = spearman(ab, rankOf)
	analyzeHumans(&b, r, rankOf)
	return b
}

// analyzeHumans は ProfileMatch の観測（人間の順位・脱落ステージ・Bot の tier 配分）を埋める。
// 人間が居ないプロファイルでは何もしない。
func analyzeHumans(b *Balance, r Result, rankOf map[game.PlayerId]int) {
	b.HumanCullStages = make([]int, len(r.CullStages))
	var rankSum float64
	for _, a := range r.Abilities {
		if a.Tier != "" {
			if b.BotTierCounts == nil {
				b.BotTierCounts = make(map[string]int, 3)
			}
			b.BotTierCounts[a.Tier]++
		}
		if !a.Human {
			continue
		}
		rk, ok := rankOf[a.Id]
		if !ok {
			continue
		}
		b.HumanCount++
		rankSum += float64(rk)
		if rk == 1 {
			b.HumanTop1++
		}
		if s := cullStageOf(rk, r.CullStages); s > 0 {
			b.HumanCullStages[s-1]++
		}
	}
	if b.HumanCount > 0 {
		b.HumanAvgRank = rankSum / float64(b.HumanCount)
	}
}

// cullStageOf は最終順位 rank の店が**何段目の足切りで落ちたか**（1始まり）を返す。
//
// ステージ k を通過できたのは上位 CullStages[k-1].Alive 店なので、
// `rank > Alive` になる最初の段がその店の脱落した段。最終ステージ（targetAliveCount=0）は
// 全店が同時に落ちるので、生き残った店は全員そこに入る。
func cullStageOf(rank int, stages []CullStageResult) int {
	for i, s := range stages {
		if rank > s.Alive {
			return i + 1
		}
	}
	return len(stages)
}

// spearman は「実力の順位」と「試合の最終順位」の順位相関。
//
// 実力は EffectiveMsPerKey の昇順（速い＝強い＝1位）。同値は出現順で割り振る
// （duel のように同値が大量に並ぶケースでも破綻させないため、厳密な平均順位は取らない）。
// +1 に近いほど「強い店ほど上位」。0 付近なら運ゲー。
func spearman(abilityAsc []Ability, rankOf map[game.PlayerId]int) float64 {
	n := 0
	var sumD2 float64
	for i, a := range abilityAsc {
		rk, ok := rankOf[a.Id]
		if !ok {
			continue
		}
		n++
		d := float64(i+1) - float64(rk)
		sumD2 += d * d
	}
	if n < 2 {
		return 0
	}
	fn := float64(n)
	return 1 - (6*sumD2)/(fn*(fn*fn-1))
}

// MeanBalance は複数試合の観測を平均する（1試合だとお題のランダム性でブレるため）。
func MeanBalance(bs []Balance) Balance {
	var m Balance
	if len(bs) == 0 {
		return m
	}
	fn := float64(len(bs))
	fastWinner := 0
	humanWinner := 0
	humanRankSum := 0.0
	for _, b := range bs {
		m.HumanCount += b.HumanCount
		m.HumanTop1 += b.HumanTop1
		humanRankSum += b.HumanAvgRank
		if b.HumanTop1 > 0 {
			humanWinner++
		}
		for i, c := range b.HumanCullStages {
			for len(m.HumanCullStages) <= i {
				m.HumanCullStages = append(m.HumanCullStages, 0)
			}
			m.HumanCullStages[i] += c
		}
		for k, v := range b.BotTierCounts {
			if m.BotTierCounts == nil {
				m.BotTierCounts = make(map[string]int, 3)
			}
			m.BotTierCounts[k] += v
		}
		m.FastCount += b.FastCount
		m.PreciseCount += b.PreciseCount
		m.FastWins += b.FastWins
		m.PreciseWins += b.PreciseWins
		m.Top10Fast += b.Top10Fast
		m.NegativeScores += b.NegativeScores
		m.EarlyCutStrong += b.EarlyCutStrong
		m.FastAvgRank += b.FastAvgRank
		m.PreciseAvgRank += b.PreciseAvgRank
		m.TopAvg += b.TopAvg
		m.BottomAvg += b.BottomAvg
		m.Separation += b.Separation
		m.RankAbilityCorr += b.RankAbilityCorr
		if b.Winner1Class == ClassFast {
			fastWinner++
		}
	}
	m.FastAvgRank /= fn
	m.PreciseAvgRank /= fn
	m.TopAvg /= fn
	m.BottomAvg /= fn
	m.Separation /= fn
	m.RankAbilityCorr /= fn
	// 平均では Winner1Class に意味が無いので、速さ型が優勝した回数を割合として持たせる。
	m.Winner1Class = ""
	m.FastWinnerRatio = float64(fastWinner) / fn
	// 人間の平均順位は「試合ごとの平均」の平均（人数が同じなので全体平均と一致する）。
	m.HumanAvgRank = humanRankSum / fn
	m.HumanWinRatio = float64(humanWinner) / fn
	return m
}

// Balanced は速さ型と正確型の平均順位の差が tol 以内か（＝拮抗しているか）。
func (b Balance) Balanced(tol float64) bool {
	if b.FastCount == 0 || b.PreciseCount == 0 {
		return false
	}
	return math.Abs(b.FastAvgRank-b.PreciseAvgRank) <= tol
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
