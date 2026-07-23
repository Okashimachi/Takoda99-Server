package targeting

import "textro99/internal/game"

// FinisherStrategy は作戦2(とどめ)。生存者(自分以外)で スタック比率 count/limit が最大の相手を狙う。
// 同値はランダムでタイブレーク。母集団が空なら不発。
// 比率はゼロ除算回避のため a.count*b.limit と b.count*a.limit のクロス乗算で比較する。
type FinisherStrategy struct{}

func (FinisherStrategy) Id() int { return 2 }

func (FinisherStrategy) SelectTargets(ctx game.TargetingContext) []game.PlayerId {
	others := ctx.Others()
	if len(others) == 0 {
		return nil
	}
	best := others[0]
	for _, p := range others[1:] {
		if p.DakenStackCount*best.DakenStackLimit > best.DakenStackCount*p.DakenStackLimit {
			best = p
		}
	}
	var tied []game.PlayerId
	for _, p := range others {
		if p.DakenStackCount*best.DakenStackLimit == best.DakenStackCount*p.DakenStackLimit {
			tied = append(tied, p.PlayerId)
		}
	}
	return pickTied(ctx, tied)
}

var _ game.TargetingStrategy = FinisherStrategy{}
