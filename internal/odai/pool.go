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

// MaxLevel は語彙が存在する最大の難易度段階を返す。
//
// Next はこの段階より上を要求されても下へ降りて探すため、`heatLevel > MaxLevel()` の
// 領域では**火力を上げてもお題が変わらない**（＝難度が頭打ち）。決着保証の検証で
// 「上端に張り付いたまま試合が終わらない」状態を検出するのに要る。
func (p *StaticPool) MaxLevel() int {
	max := 0
	for lvl, words := range p.wordsByLevel {
		if len(words) > 0 && lvl > max {
			max = lvl
		}
	}
	return max
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
