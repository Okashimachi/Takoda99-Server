// Package targeting は【層3・部品】作戦0〜9のターゲット解決を実装する。
//
// interface と入力型（TargetingContext/PlayerView）はコア game が所有する（game/ports.go, DIP）。
// ここは game.TargetingStrategy を1作戦1ファイルで実装するだけ。依存は game/stdlib のみ
// （.golangci.yml の depguard で機械強制）。ターゲティングは「誰を撃つか」だけで威力に触れない。
package targeting

import "textro99/internal/game"

// Registry は id→作戦の対応表。合成ルートが組み立て、session が引く想定。
type Registry struct {
	byId map[int]game.TargetingStrategy
}

// NewRegistry は空の Registry を作る。
func NewRegistry() *Registry {
	return &Registry{byId: make(map[int]game.TargetingStrategy)}
}

// Register は作戦を登録する（同じidは後勝ち）。
func (r *Registry) Register(s game.TargetingStrategy) {
	r.byId[s.Id()] = s
}

// Resolve は id の作戦で対象を解決する。未登録idは空（不発）。
func (r *Registry) Resolve(id int, ctx game.TargetingContext) []game.PlayerId {
	s, ok := r.byId[id]
	if !ok {
		return nil
	}
	return s.SelectTargets(ctx)
}

// PickRandomOther は自分以外の生存者から一様乱択で1名返す（作戦4の本体、かつ
// 作戦1/5/8/9の「該当なし→ランダム」フォールバックの共有ヘルパ）。母集団が空なら空。
func PickRandomOther(ctx game.TargetingContext) []game.PlayerId {
	o := ctx.Others()
	if len(o) == 0 {
		return nil
	}
	return []game.PlayerId{o[ctx.Rng.Intn(len(o))].PlayerId}
}

// single は1名を集合として返す小ヘルパ（各作戦の可読性用）。
func single(id game.PlayerId) []game.PlayerId { return []game.PlayerId{id} }

// aliveContains は id が現在の生存者に含まれるかを返す（カウンター/リベンジで、
// 予告主・直近着弾者が既に脱落していないかの確認に使う）。
func aliveContains(ctx game.TargetingContext, id game.PlayerId) bool {
	for _, p := range ctx.Alive {
		if p.PlayerId == id {
			return true
		}
	}
	return false
}

// maxBy は Others のうち score が最大の相手を集め、同値はランダムで1名返す。
// score<=0 の扱いは呼び出し側に委ねる（作戦8の「最大0なら該当なし」等）。
func maxBy(ctx game.TargetingContext, score func(game.PlayerView) int) (best int, tied []game.PlayerId) {
	others := ctx.Others()
	if len(others) == 0 {
		return 0, nil
	}
	best = score(others[0])
	for _, p := range others[1:] {
		if s := score(p); s > best {
			best = s
		}
	}
	for _, p := range others {
		if score(p) == best {
			tied = append(tied, p.PlayerId)
		}
	}
	return best, tied
}

// pickTied はタイ集合からランダムに1名返す（空なら空）。
func pickTied(ctx game.TargetingContext, tied []game.PlayerId) []game.PlayerId {
	if len(tied) == 0 {
		return nil
	}
	return single(tied[ctx.Rng.Intn(len(tied))])
}
