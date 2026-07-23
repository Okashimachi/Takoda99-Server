package targeting

import "textro99/internal/game"

// PileOnStrategy は作戦8(巻き添え)。現在 予告を受けている人数（IncomingWarnings）が最多の相手を狙う。
// 同値はランダム。誰も狙われていない（最大0）なら4(ランダム)へフォールバック。
// 集中砲火に相乗りしてKOのお零れを狙う。
type PileOnStrategy struct{}

func (PileOnStrategy) Id() int { return 8 }

func (PileOnStrategy) SelectTargets(ctx game.TargetingContext) []game.PlayerId {
	max, tied := maxBy(ctx, func(p game.PlayerView) int { return p.IncomingWarnings })
	if max <= 0 {
		return PickRandomOther(ctx)
	}
	return pickTied(ctx, tied)
}

var _ game.TargetingStrategy = PileOnStrategy{}
