package targeting

import "textro99/internal/game"

// RandomStrategy は作戦4(ランダム)。自分以外の生存者から一様乱択で1名を狙う。
// 他作戦の「該当なし→4(ランダム)」フォールバックの土台でもある。母集団が空なら不発。
//
// ※ 後輩向けの実装リファレンス①：一番シンプルな形。
type RandomStrategy struct{}

func (RandomStrategy) Id() int { return 4 }

func (RandomStrategy) SelectTargets(ctx game.TargetingContext) []game.PlayerId {
	return PickRandomOther(ctx)
}

var _ game.TargetingStrategy = RandomStrategy{}
