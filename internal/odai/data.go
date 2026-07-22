package odai

import "textro99/internal/game"

// ┌──────────────────────────────────────────────────────────────────┐
// │ 【後輩の主担当】このファイルのプレースホルダ辞書を用意・拡充するのがタスクの中心。      │
// │ テーマ（寿司モチーフ）は変更予定なので、確定した単語である必要はない。ダミーでよい。    │
// │ ただし各語の KeystrokeCount は「その語を正準ローマ字で打つ打鍵数」を入れること（決定C）。│
// └──────────────────────────────────────────────────────────────────┘
//
// 例: "ねこ"→"neko"=4 / "さくら"→"sakura"=6 / "がっこう"→"gakkou"=6。
// 本物のローマ字テーブルは後日 proto の共有データから来る。それまでは手数え(概算)でよい。

// placeholderWords は難易度段階(0..maxLevel=10) ごとの候補語を返す。
// 段階が上がるほど長く・濁音/促音/記号を混ぜる方針（02_詳細企画書.md 2章）。後輩が 0〜10 を埋める。
func placeholderWords() map[int][]game.Word {
	return map[int][]game.Word{
		0: {{Text: "ねこ", KeystrokeCount: 4}, {Text: "いぬ", KeystrokeCount: 3}},
		1: {{Text: "さくら", KeystrokeCount: 6}, {Text: "みかん", KeystrokeCount: 5}},
		2: {{Text: "がっこう", KeystrokeCount: 6}, {Text: "でんしゃ", KeystrokeCount: 7}},
		// TODO(後輩): 3〜10 を追加。長文化・濁音/半濁音/促音/拗音、後半は記号/数字混じり。
	}
}

// placeholderTraps はトラップダケン（煽り長文）の候補を返す。後輩が増やす。
func placeholderTraps() []game.Word {
	return []game.Word{
		{Text: "そんなたいぷそくどでかてるとおもってるんですか", KeystrokeCount: 44},
		// TODO(後輩): 煽り文を数個追加。
	}
}

// fallbackWord / fallbackTrap はデータが空段階でも動くための保険（通常は使われない）。
var (
	fallbackWord = game.Word{Text: "ぷれーすほるだー", KeystrokeCount: 16}
	fallbackTrap = game.Word{Text: "とらっぷぷれーすほるだー", KeystrokeCount: 24}
)
