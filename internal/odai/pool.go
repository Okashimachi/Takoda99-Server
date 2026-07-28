// Package odai は【層3・部品】難易度（火力）段階ごとのお題単語を供給する。
//
// interface(WordSource) と Word 型はコア game が所有する（game/ports.go, DIP）。
// ここは game.WordSource を実装するだけ。依存は game/stdlib のみ（depguard で機械強制）。
// お題ID発行・我慢ゲージ・難易度算出は game.Session が Word を包んで行う（odai は状態を持たない）。
package odai

import (
	"math/rand"

	"textro99/internal/game"
)

// StaticPool は段階別の固定リストから供給するプレースホルダ実装。
// 単語データ（data.go）は後輩が用意する。テーマ未確定のため当面はダミー文言でよい。
type StaticPool struct {
	wordsByLevel map[int][]game.Word // level(0..maxLevel) -> 候補語
	traps        []game.Word         // トラップ候補
}

// NewStaticPool はプレースホルダ辞書（data.go）を積んだ StaticPool を作る。
func NewStaticPool() *StaticPool {
	return &StaticPool{
		wordsByLevel: placeholderWords(),
		traps:        placeholderTraps(),
	}
}

// Next は指定段階の候補から乱択で1語返す。指定段階が空なら、それ以下で最も近い段階へ
// フォールバックする（プレースホルダ辞書が疎でも動くように）。
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

// NextTrap はトラップ候補から乱択で1語返す。空なら保険の1語。
func (p *StaticPool) NextTrap(rng *rand.Rand) game.Word {
	if len(p.traps) == 0 {
		return fallbackTrap
	}
	return p.traps[rng.Intn(len(p.traps))]
}

var _ game.WordSource = (*StaticPool)(nil)
