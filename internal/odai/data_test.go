package odai

import "testing"

// keystrokes が正準ローマ字打鍵数を返す（issue #11 の最短系）。
func TestKeystrokes(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"ねこ", 4},     // neko
		{"さくら", 6},    // sakura
		{"し", 2},      // si
		{"ん", 1},      // n
		{"がっこう", 6},   // gakkou（促音=子音重ね）
		{"きゃべつ", 7},   // kyabetu（拗音）
		{"でんしゃ", 6},   // densya
		{"パソコン", 7},   // pasokon（カタカナ→ひらがな正規化）
		{"コンピュータ", 9}, // konpyu-ta（長音符 ー=+1）
		{"れべる8", 7},   // reberu + 8（数字=1）
	}
	for _, c := range cases {
		if got := keystrokes(c.in); got != c.want {
			t.Errorf("keystrokes(%q)=%d, want %d", c.in, got, c.want)
		}
	}
}

// 段階0〜17 がすべて 最低20語 で埋まり、各語の打鍵数が正である。
//
// 上端が 17 なのは heatLevel の到達域に合わせているため（#75）。
// heatLevel は 0 + int(perAliveDrop × 脱落数) + フェーズ加算 で最大 17 まで上がり、
// 語彙が無い段階は Next が下へ降りるので「火力を上げてもお題が変わらない」状態になる。
func TestPlaceholderWords_AllLevelsCovered(t *testing.T) {
	words := placeholderWords()
	for lvl := 0; lvl <= MaxWordLevel; lvl++ {
		ws, ok := words[lvl]
		if !ok || len(ws) < 20 {
			t.Fatalf("段階 %d は最低20語必要: len=%d ok=%v", lvl, len(ws), ok)
		}
		for _, w := range ws {
			if w.Text == "" || w.KeystrokeCount <= 0 {
				t.Fatalf("段階 %d に不正な語: %+v", lvl, w)
			}
		}
	}
}

func TestPlaceholderWords_TotalCount(t *testing.T) {
	words := placeholderWords()
	total := 0
	for _, ws := range words {
		total += len(ws)
	}
	if total < 100 {
		t.Fatalf("語彙は最低100語必要: got %d", total)
	}
}

// 段階が上がるほど平均打鍵数が増えること。
//
// 難度の勾配が崩れていると「火力を上げたのに簡単になる」ことが起きる。
// 語を追加・入れ替えする時にここで気付けるようにしておく。
func TestPlaceholderWords_DifficultyIsMonotonic(t *testing.T) {
	words := placeholderWords()
	prev := 0.0
	for lvl := 0; lvl <= MaxWordLevel; lvl++ {
		sum := 0
		for _, w := range words[lvl] {
			sum += w.KeystrokeCount
		}
		avg := float64(sum) / float64(len(words[lvl]))
		if avg <= prev {
			t.Errorf("段階 %d の平均打鍵数 %.1f が段階 %d の %.1f 以下（勾配が逆転）", lvl, avg, lvl-1, prev)
		}
		t.Logf("段階 %2d: 平均 %.1f 打鍵", lvl, avg)
		prev = avg
	}
}

// StaticPool.MaxLevel が辞書の上端と一致すること。
// ここがズレると internal/sim の「難度の頭打ち」検出が誤った段階を見る。
func TestStaticPool_MaxLevelMatchesDictionary(t *testing.T) {
	if got := NewStaticPool().MaxLevel(); got != MaxWordLevel {
		t.Fatalf("MaxLevel = %d, want %d", got, MaxWordLevel)
	}
}
