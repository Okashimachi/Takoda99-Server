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
	return b
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
	for _, b := range bs {
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
