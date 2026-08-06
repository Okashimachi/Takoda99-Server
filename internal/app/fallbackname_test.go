package app

import (
	"testing"

	"takoda99/internal/matchmaking"
)

// フォールバック名・Bot名も表示名の上限に収まること。
//
// 入力名だけ6文字にしても、名前を送らなかった人と Bot が長いままだと結局崩れる。
// しかも崩れるのは「名前を入れなかった人」と Bot だけなので、
// 手元のテストでは気付かず**本番の会場で初めて出る**種類の不具合になる。
func TestFallbackName_FitsDisplayLimit(t *testing.T) {
	for seat := 1; seat <= 99; seat++ {
		for _, isBot := range []bool{false, true} {
			got := fallbackName(isBot, seat)
			if got == "" {
				t.Fatalf("fallbackName(isBot=%v, seat=%d) が空", isBot, seat)
			}
			if n := len([]rune(got)); n > matchmaking.MaxDisplayNameLen {
				t.Fatalf("fallbackName(isBot=%v, seat=%d) = %q は %d ルーン（上限 %d）",
					isBot, seat, got, n, matchmaking.MaxDisplayNameLen)
			}
		}
	}
}

// 人間と Bot が見分けられること。同じ名前だと盤面で区別できない。
func TestFallbackName_DistinguishesBot(t *testing.T) {
	if a, b := fallbackName(false, 1), fallbackName(true, 1); a == b {
		t.Fatalf("人間と Bot が同じ名前になる: %q", a)
	}
	if got, want := fallbackName(false, 12), "ゲスト12"; got != want {
		t.Fatalf("fallbackName(false, 12) = %q, want %q", got, want)
	}
	if got, want := fallbackName(true, 12), "CPU12"; got != want {
		t.Fatalf("fallbackName(true, 12) = %q, want %q", got, want)
	}
}

// 同じ試合の中で名前が衝突しないこと（席番号で採番しているため）。
func TestFallbackName_UniqueWithinMatch(t *testing.T) {
	seen := map[string]bool{}
	for seat := 1; seat <= 99; seat++ {
		for _, isBot := range []bool{false, true} {
			n := fallbackName(isBot, seat)
			if seen[n] {
				t.Fatalf("名前が衝突: %q", n)
			}
			seen[n] = true
		}
	}
}
