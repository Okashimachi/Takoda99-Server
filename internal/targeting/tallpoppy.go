package targeting

import "textro99/internal/game"

// TallPoppyStrategy は作戦6(出る杭)。生存者(自分以外)で ComboValue が最大の相手を狙う。
// 同値はランダム。母集団が空なら不発。大技を溜めている相手を潰す（溜めプレイへの牽制）。
type TallPoppyStrategy struct{}

func (TallPoppyStrategy) Id() int { return 6 }

func (TallPoppyStrategy) SelectTargets(ctx game.TargetingContext) []game.PlayerId {
	_, tied := maxBy(ctx, func(p game.PlayerView) int { return p.ComboValue })
	return pickTied(ctx, tied)
}

var _ game.TargetingStrategy = TallPoppyStrategy{}
