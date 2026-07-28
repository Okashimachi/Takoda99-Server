package odai

// romaji.go はかな文字列の「正準ローマ字打鍵数」を算出する暫定実装。
//
// 打鍵数はサーバーが算出し、お題単語の長さ/難度やサニティ検証（1単語あたり最短時間）に使う。
// 本物のローマ字テーブルは後日 proto の共有データから来る予定で、それまではこの関数が
// 「かな→ローマ字長」を担う（差し替え口）。方針は最短系:
//   し=si(2) ち=ti(2) つ=tu(2) ふ=hu(2) じ=zi(2) / 拗音 きゃ=kya(3) / 促音 っ=子音重ね(+1)
//   撥音 ん=n(1) / 母音=1 / その他の基本かな=2 / 長音符 ー=+1 / 英数字=1。

// keystrokes は kana（ひらがな/カタカナ＋英数字混じり）を打つのに要するローマ字打鍵数を返す。
func keystrokes(s string) int {
	total := 0
	for _, r := range s {
		// カタカナ→ひらがなへ正規化（長音符 ー(0x30FC) は範囲外なのでそのまま）。
		if r >= 0x30A1 && r <= 0x30F6 {
			r -= 0x60
		}
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			total++ // 英数字は1打鍵
		case r == 0x30FC: // ー 長音符
			total++
		case isSmallKana(r): // 拗音ゃゅょ・促音っ・小母音 → +1
			total++
		case r == 0x3093: // ん
			total++
		case isVowel(r): // あいうえお
			total++
		case r >= 0x3041 && r <= 0x3096: // その他の基本かな
			total += 2
		default:
			total++ // 未知文字は1打鍵として数える
		}
	}
	return total
}

func isVowel(r rune) bool {
	switch r {
	case 0x3042, 0x3044, 0x3046, 0x3048, 0x304A: // あ い う え お
		return true
	}
	return false
}

func isSmallKana(r rune) bool {
	switch r {
	case 0x3041, 0x3043, 0x3045, 0x3047, 0x3049, // ぁぃぅぇぉ
		0x3063,         // っ
		0x3083, 0x3085, 0x3087: // ゃゅょ
		return true
	}
	return false
}
