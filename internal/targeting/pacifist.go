package targeting

import "textro99/internal/game"

// PacifistHunterStrategy は作戦9(平和主義)。誰からも予告を受けていない（IncomingWarnings==0）
// 相手からランダムに1名を狙う。該当なしなら4(ランダム)へフォールバック。
// 安全にファームしている相手を狩る（逃げ切り戦略への牽制）。
type PacifistHunterStrategy struct{}

func (PacifistHunterStrategy) Id() int { return 9 }

func (PacifistHunterStrategy) SelectTargets(ctx game.TargetingContext) []game.PlayerId {
	var peaceful []game.PlayerId
	for _, p := range ctx.Others() {
		if p.IncomingWarnings == 0 {
			peaceful = append(peaceful, p.PlayerId)
		}
	}
	if len(peaceful) == 0 {
		return PickRandomOther(ctx)
	}
	return single(peaceful[ctx.Rng.Intn(len(peaceful))])
}

var _ game.TargetingStrategy = PacifistHunterStrategy{}
