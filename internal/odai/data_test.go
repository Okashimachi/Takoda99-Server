package odai

import "testing"

// keystrokes が正準ローマ字打鍵数を返す（issue #11 の最短系）。
func TestKeystrokes(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"ねこ", 4},          // neko
		{"さくら", 6},         // sakura
		{"し", 2},           // si
		{"ん", 1},           // n
		{"がっこう", 6},        // gakkou（促音=子音重ね）
		{"きゃべつ", 7},        // kyabetu（拗音）
		{"でんしゃ", 6},        // densya
		{"パソコン", 7},        // pasokon（カタカナ→ひらがな正規化）
		{"コンピュータ", 9},      // konpyu-ta（長音符 ー=+1）
		{"れべる8", 7},        // reberu + 8（数字=1）
	}
	for _, c := range cases {
		if got := keystrokes(c.in); got != c.want {
			t.Errorf("keystrokes(%q)=%d, want %d", c.in, got, c.want)
		}
	}
}

// 段階0〜10 がすべて 最低5語 で埋まり、各語の打鍵数が正である。
func TestPlaceholderWords_AllLevelsCovered(t *testing.T) {
	words := placeholderWords()
	for lvl := 0; lvl <= 10; lvl++ {
		ws, ok := words[lvl]
		if !ok || len(ws) < 5 {
			t.Fatalf("段階 %d は最低5語必要: len=%d ok=%v", lvl, len(ws), ok)
		}
		for _, w := range ws {
			if w.Text == "" || w.KeystrokeCount <= 0 {
				t.Fatalf("段階 %d に不正な語: %+v", lvl, w)
			}
		}
	}
}

// トラップは3個以上、各打鍵数が正。
func TestPlaceholderTraps(t *testing.T) {
	traps := placeholderTraps()
	if len(traps) < 3 {
		t.Fatalf("トラップは3個以上必要: %d", len(traps))
	}
	for _, w := range traps {
		if w.Text == "" || w.KeystrokeCount <= 0 {
			t.Fatalf("不正なトラップ: %+v", w)
		}
	}
}
