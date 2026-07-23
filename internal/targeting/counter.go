package targeting

import "textro99/internal/game"

// CounterStrategy は作戦1(カウンター)。自分に予告中の相手のうち、最も新しく予告を送ってきた
// （まだ生存している）相手を狙う。予告が無ければ4(ランダム)へフォールバック。
type CounterStrategy struct{}

func (CounterStrategy) Id() int { return 1 }

func (CounterStrategy) SelectTargets(ctx game.TargetingContext) []game.PlayerId {
	// PendingAttackers は新しい順。生存している最初の1名を狙う。
	for _, att := range ctx.PendingAttackers {
		if att != ctx.SelfId && aliveContains(ctx, att) {
			return single(att)
		}
	}
	return PickRandomOther(ctx)
}

var _ game.TargetingStrategy = CounterStrategy{}
