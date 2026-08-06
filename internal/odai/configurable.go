package odai

import (
	"errors"
	"math/rand"

	"takoda99/internal/game"
)

// WordEntry は DB/API で受け渡す単語データ。
type WordEntry struct {
	ID             int    `json:"id,omitempty"`
	Text           string `json:"text"`
	Reading        string `json:"reading"`
	KeystrokeCount int    `json:"keystrokeCount"`
	Level          int    `json:"level"`
	Category       string `json:"category"`
}

var ErrNotFound = errors.New("odai: word not found")

// WordPatch は words の部分更新。nil のフィールドは変更しない。
type WordPatch struct {
	Text           *string `json:"text,omitempty"`
	Reading        *string `json:"reading,omitempty"`
	KeystrokeCount *int    `json:"keystrokeCount,omitempty"`
	Level          *int    `json:"level,omitempty"`
	Category       *string `json:"category,omitempty"`
}

// ConfigurablePool は DB から取得した語彙で WordSource を満たす。
// DB取得失敗時はフォールバック(StaticPool)に委譲する。
type ConfigurablePool struct {
	wordsByLevel map[int][]game.Word
	fallback     *StaticPool
}

// NewConfigurablePool は DB の語彙リストから構築する。
// entries が空ならフォールバック(StaticPool)のみで動く。
func NewConfigurablePool(entries []WordEntry) *ConfigurablePool {
	fb := NewStaticPool()
	if len(entries) == 0 {
		return &ConfigurablePool{
			wordsByLevel: fb.wordsByLevel,
			fallback:     fb,
		}
	}
	byLevel := make(map[int][]game.Word)
	for _, e := range entries {
		w := game.Word{Text: e.Text, KeystrokeCount: e.KeystrokeCount}
		byLevel[e.Level] = append(byLevel[e.Level], w)
	}
	return &ConfigurablePool{
		wordsByLevel: byLevel,
		fallback:     fb,
	}
}

// MaxLevel は語彙が存在する最大の難易度段階を返す（意味は StaticPool.MaxLevel と同じ）。
// DB 由来の語彙が空ならフォールバック側の段階を返す。
func (p *ConfigurablePool) MaxLevel() int {
	max := 0
	for lvl, words := range p.wordsByLevel {
		if len(words) > 0 && lvl > max {
			max = lvl
		}
	}
	if max == 0 && len(p.wordsByLevel) == 0 {
		return p.fallback.MaxLevel()
	}
	return max
}

func (p *ConfigurablePool) Next(effectiveLevel int, rng *rand.Rand) game.Word {
	list := p.wordsByLevel[effectiveLevel]
	for l := effectiveLevel - 1; l >= 0 && len(list) == 0; l-- {
		list = p.wordsByLevel[l]
	}
	if len(list) == 0 {
		return p.fallback.Next(effectiveLevel, rng)
	}
	return list[rng.Intn(len(list))]
}

// Keystrokes は reading のローマ字打鍵数を算出する（DB保存時にサーバーが呼ぶ）。
func Keystrokes(reading string) int {
	return keystrokes(reading)
}

// BuildFallbackEntries は data.go の rawWords を WordEntry として返す（DB seed 用）。
func BuildFallbackEntries() []WordEntry {
	var entries []WordEntry
	for lvl, words := range rawWords {
		for _, text := range words {
			entries = append(entries, WordEntry{
				Text:           text,
				Reading:        text,
				KeystrokeCount: keystrokes(text),
				Level:          lvl,
				Category:       "general",
			})
		}
	}
	return entries
}

var _ game.WordSource = (*ConfigurablePool)(nil)
