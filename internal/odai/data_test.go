package odai

import (
	"testing"

	"takoda99/internal/game"
)

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

// MaxKeystrokesPerWord は1語の打鍵数の上限（plan-h30）。
//
// 50打鍵 ≒ 11秒。決勝ステージが20秒なので、これを超えると「1語で決勝が終わる」領域に入る。
// h30 以前は level 17 が 85打鍵（約20秒）あり、打ち切るまでスコアが1点も入らないので
// 終盤ほど手応えが無かった。**難度は1語の長さではなく orderCount（何語打つか）で上げる。**
const MaxKeystrokesPerWord = 50

// 1語が長くなりすぎていないこと（plan-h30 §5）。
//
// バグ注入で確認済み: rawWords のどれか1語を51打鍵以上にすると、このテストが落ちる。
func TestPlaceholderWords_WordIsNotTooLong(t *testing.T) {
	for lvl, ws := range placeholderWords() {
		for _, w := range ws {
			if w.KeystrokeCount > MaxKeystrokesPerWord {
				t.Errorf("段階 %d の %q は %d 打鍵。上限は %d 打鍵（1語を長くして難度を上げない。"+
					"難度は orderCount で上げる・plan-h30）", lvl, w.Text, w.KeystrokeCount, MaxKeystrokesPerWord)
			}
		}
	}
}

// 各レベル内の打鍵数に幅があること（plan-h30 §5）。
//
// h30 以前は level 9 以上がテンプレの機械生成で、同一レベルの語が**全部同じ打鍵数**だった
// （level 17 は 20語すべて 85打鍵）。同じ難度の語しか出ないと、そのレベルに落ちた瞬間から
// 体感が固定される。ここは「機械生成に戻っていないか」の検出器でもある。
func TestPlaceholderWords_HasSpreadWithinLevel(t *testing.T) {
	const minSpread = 5 // 最短と最長の差（打鍵）
	words := placeholderWords()
	for lvl := 0; lvl <= MaxWordLevel; lvl++ {
		lo, hi := 1<<30, 0
		for _, w := range words[lvl] {
			if w.KeystrokeCount < lo {
				lo = w.KeystrokeCount
			}
			if w.KeystrokeCount > hi {
				hi = w.KeystrokeCount
			}
		}
		// level 0〜2 は語自体が短いので幅も小さい。幅の下限は「レベルの平均の 1/4」を上限に緩める。
		want := minSpread
		if lvl <= 2 {
			want = 2
		}
		if hi-lo < want {
			t.Errorf("段階 %d の打鍵数の幅が %d（%d-%d）しかない。"+
				"同一レベルの語を同じ長さで揃えないこと（plan-h30 §5）", lvl, hi-lo, lo, hi)
		}
	}
}

// h30 で外した旧語が、現行辞書に混ざっていないこと（消し忘れ検出・plan-h30 §5）。
//
// retired.go は「戻せるように」旧語を保持しているだけで、出題されてはいけない。
// DB seed(v3) はこのリストを DELETE 対象にするので、現行辞書と重なっていると
// 「入れた直後に消す」ことになる。
func TestPlaceholderWords_ExcludesRetiredWords(t *testing.T) {
	current := make(map[string]int)
	for lvl, ws := range placeholderWords() {
		for _, w := range ws {
			current[w.Text] = lvl
		}
	}
	if len(retiredWords) == 0 {
		t.Fatal("retiredWords が空。**戻すための削除対象リストを消さないこと**（plan-h30 §3.2）")
	}
	for lvl, ws := range retiredWords {
		for _, text := range ws {
			if got, ok := current[text]; ok {
				t.Errorf("旧語 %q（retired level %d）が現行辞書 level %d に残っている", text, lvl, got)
			}
		}
	}
}

// 旧語のリストが「戻せる」形で残っていること（plan-h30 §3.2）。
func TestRetiredEntries_IsRestorable(t *testing.T) {
	entries := RetiredEntries()
	if len(entries) != 260 {
		t.Fatalf("旧語は 260 語のはず: got %d", len(entries))
	}
	for _, e := range entries {
		if e.Text == "" || e.Reading != e.Text || e.KeystrokeCount <= 0 || e.Level < 5 || e.Level > MaxWordLevel {
			t.Fatalf("再 upsert できない形の旧語: %+v", e)
		}
	}
}

// StaticPool.MaxLevel が辞書の上端と一致すること。
// ここがズレると internal/sim の「難度の頭打ち」検出が誤った段階を見る。
func TestStaticPool_MaxLevelMatchesDictionary(t *testing.T) {
	if got := NewStaticPool().MaxLevel(); got != MaxWordLevel {
		t.Fatalf("MaxLevel = %d, want %d", got, MaxWordLevel)
	}
}

// 辞書の上端と heat.maxLevel の既定が一致すること。
//
// game は odai を import できない（依存が逆流する）ので、この2つは手で揃えるしかない。
// ズレると heatLevel が辞書に無い段階まで上がり、火力を上げてもお題が変わらなくなる（#75）。
// 機械的な保証はここだけなので消さないこと。
func TestMaxWordLevelMatchesGame(t *testing.T) {
	if got, want := MaxWordLevel, game.DefaultParameters().Heat.MaxLevel; got != want {
		t.Fatalf("odai.MaxWordLevel=%d だが game.DefaultParameters().Heat.MaxLevel=%d。"+
			"辞書に段階を足したら params.go の Heat.MaxLevel も揃えること", got, want)
	}
}
