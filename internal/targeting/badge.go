package targeting

import "textro99/internal/game"

// BadgeHunterStrategy は作戦3(バッジ狙い)。自分以外の生存者で badgeCount が最大の相手を狙う。
// 同数はランダムでタイブレーク。母集団が空なら不発。
//
// ※ 後輩向けの実装リファレンス②：「最大値を持つ相手を選び、同値はランダム」型。
//    作戦2(とどめ=スタック比率最大) / 作戦6(出る杭=コンボ最大) も同じ骨格で書ける。
type BadgeHunterStrategy struct{}

func (BadgeHunterStrategy) Id() int { return 3 }

func (BadgeHunterStrategy) SelectTargets(ctx game.TargetingContext) []game.PlayerId {
	others := ctx.Others()
	if len(others) == 0 {
		return nil
	}
	best := others[0].BadgeCount
	for _, p := range others[1:] {
		if p.BadgeCount > best {
			best = p.BadgeCount
		}
	}
	var tied []game.PlayerId
	for _, p := range others {
		if p.BadgeCount == best {
			tied = append(tied, p.PlayerId)
		}
	}
	return single(tied[ctx.Rng.Intn(len(tied))])
}

var _ game.TargetingStrategy = BadgeHunterStrategy{}
