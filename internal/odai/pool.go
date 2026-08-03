// Package odai は【層3・部品】難易度段階ごとのお題語を供給する。
//
// interface(WordSource) と Word 型はコア game が所有する（game/ports.go, DIP）。
// ここは game.WordSource を実装するだけ。依存は game/stdlib のみ（depguard で機械強制）。
package odai

import (
	"math/rand"

	"takoda99/internal/game"
)

// StaticPool は段階別の固定リストから供給するプレースホルダ実装。
type StaticPool struct {
	wordsByLevel map[int][]game.Word
}

// NewStaticPool はプレースホルダ辞書（data.go）を積んだ StaticPool を作る。
func NewStaticPool() *StaticPool {
	return &StaticPool{
		wordsByLevel: placeholderWords(),
	}
}

// Next は指定段階の候補から乱択で1語返す。
func (p *StaticPool) Next(effectiveLevel int, rng *rand.Rand) game.Word {
	list := p.wordsByLevel[effectiveLevel]
	for l := effectiveLevel - 1; l >= 0 && len(list) == 0; l-- {
		list = p.wordsByLevel[l]
	}
	if len(list) == 0 {
		return fallbackWord
	}
	return list[rng.Intn(len(list))]
}

var _ game.WordSource = (*StaticPool)(nil)
