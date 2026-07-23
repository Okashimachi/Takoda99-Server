package odai

import "textro99/internal/game"

// data.go は【#11】出題語のプレースホルダ辞書。難易度段階0〜10で段階的に難しくする。
//
// テーマ（寿司モチーフ）は変更予定なので中身はダミーの一般語でよい。大事なのは
//   ①段階が上がるほど難しい（長さ→濁音/半濁音→促音→拗音→カタカナ→数字混じり）
//   ②KeystrokeCount が正準ローマ字打鍵数（romaji.go の keystrokes で自動算出。手計算しない）
// 記号は使わない（英数字・カナまで）。本物の単語・テーマはアート/企画確定後に差し替える。

// rawWords は段階(0..maxLevel)ごとの語（text のみ）。打鍵数は keystrokes() で算出する。
var rawWords = map[int][]string{
	0:  {"ねこ", "いぬ", "そら", "うみ", "やま"},                 // 2字・清音
	1:  {"さくら", "ひかり", "みどり", "ことり", "はなび"},         // 3字・清音
	2:  {"だんご", "ぱんだ", "でんわ", "ばなな", "ぶどう"},         // 濁音・半濁音
	3:  {"ながぐつ", "だいこん", "みずうみ", "ばくだん", "でんぐり"},   // 4字・濁音多め
	4:  {"がっこう", "けっか", "さっか", "ばった", "ねっこ"},         // 促音(っ)
	5:  {"きゃべつ", "しゅくだい", "きょうしつ", "じてんしゃ", "びょういん"}, // 拗音(ゃゅょ)
	6:  {"テレビ", "ラジオ", "カメラ", "パソコン", "ストーブ"},         // カタカナ導入
	7:  {"コンピュータ", "ジャングル", "ハンバーグ", "ショッピング", "キャラメル"}, // カナ＋濁促拗
	8:  {"れべる8", "みっしょん3", "ぽいんと10", "すこあ5", "たいむ2"},   // 数字混じり
	9:  {"プログラミング", "アプリケーション", "インターネット", "キーボード", "モニター"}, // カタカナ長め
	10: {"すーぱーこんぼ99", "ファイナルバトル100", "でんせつのけん3", "マキシマムぱわー", "レジェンド2000"}, // 全部盛り
}

// rawTraps は煽り長文（テーマ非依存）。通常語より打鍵数が多い。
var rawTraps = []string{
	"そんなたいぷそくどでかてるとおもってるんですか",
	"もしかしてまだそこでもたついてるんですか",
	"そのていどのじつりょくでよくきましたね",
}

func placeholderWords() map[int][]game.Word {
	out := make(map[int][]game.Word, len(rawWords))
	for lvl, words := range rawWords {
		out[lvl] = toWords(words)
	}
	return out
}

func placeholderTraps() []game.Word { return toWords(rawTraps) }

func toWords(texts []string) []game.Word {
	out := make([]game.Word, 0, len(texts))
	for _, t := range texts {
		out = append(out, game.Word{Text: t, KeystrokeCount: keystrokes(t)})
	}
	return out
}

// fallbackWord / fallbackTrap はデータが空段階でも動くための保険（通常は使われない）。
var (
	fallbackWord = game.Word{Text: "ぷれーすほるだー", KeystrokeCount: keystrokes("ぷれーすほるだー")}
	fallbackTrap = game.Word{Text: "とらっぷぷれーすほるだー", KeystrokeCount: keystrokes("とらっぷぷれーすほるだー")}
)
