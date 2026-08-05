package odai

import (
	"math/rand"
	"testing"

	"takoda99/internal/game"
)

func rng(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

func TestStaticPool_Next(t *testing.T) {
	p := NewStaticPool()
	w := p.Next(2, rng(1))
	if w.Text == "" || w.KeystrokeCount <= 0 {
		t.Fatalf("got %+v, want 有効な語", w)
	}
}

func TestStaticPool_Next_FallsBackForHighLevel(t *testing.T) {
	p := NewStaticPool()
	w := p.Next(20, rng(1))
	if w.Text == "" || w.KeystrokeCount <= 0 {
		t.Fatalf("got %+v, want フォールバックで有効な語", w)
	}
}

func TestConfigurablePool_UsesDBEntries(t *testing.T) {
	entries := []WordEntry{
		{Text: "てすと", Reading: "てすと", KeystrokeCount: 6, Level: 0, Category: "test"},
		{Text: "さんぷる", Reading: "さんぷる", KeystrokeCount: 7, Level: 1, Category: "test"},
	}
	p := NewConfigurablePool(entries)
	w := p.Next(0, rng(1))
	if w.Text != "てすと" {
		t.Fatalf("DB語彙から出題されるべき: got %q", w.Text)
	}
}

func TestConfigurablePool_LevelFallback(t *testing.T) {
	entries := []WordEntry{
		{Text: "れべるぜろ", Reading: "れべるぜろ", KeystrokeCount: 8, Level: 0, Category: "test"},
	}
	p := NewConfigurablePool(entries)
	w := p.Next(3, rng(1))
	if w.Text != "れべるぜろ" {
		t.Fatalf("上位レベルから下位へフォールバックすべき: got %q", w.Text)
	}
}

func TestConfigurablePool_EmptyFallsBackToStatic(t *testing.T) {
	p := NewConfigurablePool(nil)
	w := p.Next(0, rng(1))
	if w.Text == "" || w.KeystrokeCount <= 0 {
		t.Fatalf("空entries のときはフォールバック語彙: got %+v", w)
	}
}

func TestConfigurablePool_ImplementsWordSource(t *testing.T) {
	var _ game.WordSource = (*ConfigurablePool)(nil)
}

func TestBuildFallbackEntries(t *testing.T) {
	entries := BuildFallbackEntries()
	if len(entries) < 100 {
		t.Fatalf("フォールバックentries は100語以上: got %d", len(entries))
	}
	for _, e := range entries {
		if e.Text == "" || e.Reading == "" || e.KeystrokeCount <= 0 {
			t.Fatalf("不正なentry: %+v", e)
		}
	}
}

func TestKeystrokes_Exported(t *testing.T) {
	got := Keystrokes("たこやき")
	if got <= 0 {
		t.Fatalf("Keystrokes should return positive: got %d", got)
	}
}

// MaxLevel は「これ以上 heatLevel を上げてもお題が変わらない」段階を返す。
// 決着保証の検証（internal/sim）が難度の頭打ちを検出するのに使う。
func TestStaticPool_MaxLevel(t *testing.T) {
	p := NewStaticPool()
	got := p.MaxLevel()
	if got <= 0 {
		t.Fatalf("MaxLevel が非正: %d", got)
	}
	// 上端に語彙があり、その上を要求しても同じ段階へ降りること。
	if len(p.wordsByLevel[got]) == 0 {
		t.Fatalf("MaxLevel=%d に語彙が無い", got)
	}
	if len(p.wordsByLevel[got+1]) != 0 {
		t.Fatalf("MaxLevel=%d より上に語彙がある", got)
	}
}

func TestConfigurablePool_MaxLevel(t *testing.T) {
	// DB 由来の語彙があればその最大段階。
	p := NewConfigurablePool([]WordEntry{
		{Text: "たこ", Reading: "たこ", KeystrokeCount: 4, Level: 0},
		{Text: "たこやき", Reading: "たこやき", KeystrokeCount: 8, Level: 7},
	})
	if got := p.MaxLevel(); got != 7 {
		t.Fatalf("MaxLevel = %d, want 7", got)
	}

	// 空ならフォールバック(StaticPool)の段階に委譲する。
	empty := NewConfigurablePool(nil)
	if got, want := empty.MaxLevel(), NewStaticPool().MaxLevel(); got != want {
		t.Fatalf("空のとき MaxLevel = %d, want %d（フォールバック委譲）", got, want)
	}
}
