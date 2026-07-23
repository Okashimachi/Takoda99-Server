package targeting

import (
	"sort"

	"textro99/internal/game"
)

// NeighborStrategy は作戦7(隣狙い)。生存者を PlayerId 昇順に並べ、自分の「次」の相手を狙う
// （末尾なら先頭へラップ）。表示順はIDソートで代替して実装を単純化する（仕様どおり）。
// 生存2人未満なら不発（隣＝ラップ対象がいないため。7だけランダムにフォールバックしない）。
type NeighborStrategy struct{}

func (NeighborStrategy) Id() int { return 7 }

func (NeighborStrategy) SelectTargets(ctx game.TargetingContext) []game.PlayerId {
	if len(ctx.Alive) < 2 {
		return nil
	}
	ids := make([]game.PlayerId, len(ctx.Alive))
	for i, p := range ctx.Alive {
		ids[i] = p.PlayerId
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, id := range ids {
		if id == ctx.SelfId {
			return single(ids[(i+1)%len(ids)])
		}
	}
	return nil // 自分が生存者に居ない（通常起きない）
}

var _ game.TargetingStrategy = NeighborStrategy{}
