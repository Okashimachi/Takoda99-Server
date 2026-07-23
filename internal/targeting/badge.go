package targeting

import "textro99/internal/game"

// BadgeHunterStrategy は作戦3(バッジ狙い)。自分以外の生存者で badgeCount が最大の相手を狙う。
// 同数はランダムでタイブレーク。母集団が空なら不発。「最大値＋同値ランダム」型（maxBy 共有）。
type BadgeHunterStrategy struct{}

func (BadgeHunterStrategy) Id() int { return 3 }

func (BadgeHunterStrategy) SelectTargets(ctx game.TargetingContext) []game.PlayerId {
	_, tied := maxBy(ctx, func(p game.PlayerView) int { return p.BadgeCount })
	return pickTied(ctx, tied)
}

var _ game.TargetingStrategy = BadgeHunterStrategy{}
