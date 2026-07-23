package targeting

import "textro99/internal/game"

// SplitAttackStrategy は作戦0(全体割り)。対象は自分以外の生存者「全員」。
// 威力の均等分配（floor(power/N)）は MatchSession の責務で、ここは対象集合を返すだけ。
// ※ 唯一「複数件を返してよい」作戦（1〜9は0/1件）。
type SplitAttackStrategy struct{}

func (SplitAttackStrategy) Id() int { return 0 }

func (SplitAttackStrategy) SelectTargets(ctx game.TargetingContext) []game.PlayerId {
	others := ctx.Others()
	ids := make([]game.PlayerId, 0, len(others))
	for _, p := range others {
		ids = append(ids, p.PlayerId)
	}
	return ids // 他に誰もいなければ空＝不発
}

var _ game.TargetingStrategy = SplitAttackStrategy{}
