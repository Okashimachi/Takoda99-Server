package targeting

import "textro99/internal/game"

// RevengeStrategy は作戦5(リベンジ)。直近で自分に着弾させた相手が まだ生存していれば狙う。
// 履歴なし/既に脱落なら4(ランダム)へフォールバック。
type RevengeStrategy struct{}

func (RevengeStrategy) Id() int { return 5 }

func (RevengeStrategy) SelectTargets(ctx game.TargetingContext) []game.PlayerId {
	if id := ctx.LastImpactorId; id != nil && *id != ctx.SelfId && aliveContains(ctx, *id) {
		return single(*id)
	}
	return PickRandomOther(ctx)
}

var _ game.TargetingStrategy = RevengeStrategy{}
