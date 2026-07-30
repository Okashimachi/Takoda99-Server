// Package game は【層1・コア】戦闘の権威。純粋な計算のみで、ネットワーク・時間・I/O を持たない。
//
// per-player の判定式（combo/attack/offset/stack/difficulty）と、全体を調停する session
// 状態機械（Tick(dt) で進む）を内包する。game は他の internal 部品/スパインを import しない
// （proto は契約として参照可）。継ぎ目は ports.go（DIP）。すべての調整値は GameParameters 経由。
package game

import (
	"fmt"

	"textro99/internal/proto"
)

// GameParameters は数値バランスの全項目。正典は
// Takoda99-Docs/03_サーバー仕様/04_パラメータ仕様.md。
// サーバーが起動時に config(ConfigProvider) 経由で外部取得し、失敗時は DefaultParameters()。
// クライアントへは MatchStart で公開サブセット（proto側）に絞って配信する。
type GameParameters struct {
	Combo      ComboParams      `json:"combo"`
	Attack     AttackParams     `json:"attack"`
	Stack      StackParams      `json:"stack"`
	Difficulty DifficultyParams `json:"difficulty"`
	Odai       OdaiParams       `json:"odai"`
	Matching   MatchingParams   `json:"matching"`
	Session    SessionParams    `json:"session"`

	// たこ焼き版で追加。旧項目(Combo/Attack/Stack/Difficulty/Odai)は tako-K で
	// 評価/信用/フェーズ/火力の新スキーマへ置換予定。
	Credit   CreditParams   `json:"credit"`   // tako-B
	Customer CustomerParams `json:"customer"` // tako-D
	Eval     EvalParams     `json:"eval"`     // tako-E
}

// EvalParams: 提供スコア→評価EMA の調整値（tako-E）。すべて全項目 comparable に保つ（== 比較維持）。
type EvalParams struct {
	EmaAlpha        float64 `json:"emaAlpha"`        // 評価EMA の係数（0..1・大きいほど直近重視）
	WeightAccuracy  float64 `json:"weightAccuracy"`  // 提供スコアの精度重み w_acc
	WeightSpeed     float64 `json:"weightSpeed"`     // 提供スコアの速度重み w_spd
	SpeedBaselineMs int     `json:"speedBaselineMs"` // 速度=baseline/elapsed が 1.0 になる基準所要
	SpeedCap        float64 `json:"speedCap"`        // 速度の上限（速すぎる報告の頭打ち）
	MinMsPerWord    int     `json:"minMsPerWord"`    // サニティ下限：1語あたり最小所要（elapsed 下限＝×orderCount）
	BuzzBonus       float64 `json:"buzzBonus"`       // JK(Buzz)満足時の一時加点
	BuzzDecay       float64 `json:"buzzDecay"`       // 一時加点の毎tick乗算減衰（0..1）
	BuzzCap         float64 `json:"buzzCap"`         // 一時加点の上限
}

// CreditParams: 信用（ライフ）。客の離脱でのみ減少・0で自滅脱落。
// tako-K で leaveLoss(属性別) 等を拡充する。
type CreditParams struct {
	InitialLife int `json:"initialLife"` // 初期信用（例:3。約3回の離脱で脱落）
}

// CustomerParams: 客システム（総数・属性ごとの出現率/我慢/注文数）。tako-D。
// Claimer の中盤解禁など「いつ来店させるか」の制御は分配(tako-G)/フェーズ(tako-H)側が持つ。
// ここは客の生成定義（何人・どんな客か）のみ。
// 属性は proto で閉じた4種なので固定フィールドで持つ（GameParameters の == 比較可能性を保つ）。
type CustomerParams struct {
	Total   int           `json:"total"` // 客総数（例:300）
	Normal  AttributeSpec `json:"normal"`
	Bonus   AttributeSpec `json:"bonus"`
	Claimer AttributeSpec `json:"claimer"`
	Buzz    AttributeSpec `json:"buzz"`
}

// AttributeSpec: 1属性分の生成パラメータ。
type AttributeSpec struct {
	Attribute      proto.CustomerAttribute `json:"attribute"`
	Weight         int                     `json:"weight"`         // 出現率の相対重み（Σで正規化）
	PatienceBaseMs int                     `json:"patienceBaseMs"` // 我慢ゲージ最大の基準
	OrderCount     int                     `json:"orderCount"`     // 打つ単語数（Buzz は多め）
}

// ComboParams: コンボの蓄積・減衰・個人難易度連動。
type ComboParams struct {
	NoMissBaseGain             int `json:"noMissBaseGain"`
	NoMissPerCharGain          int `json:"noMissPerCharGain"`
	MissDecay                  int `json:"missDecay"`
	PersonalDifficultyStep     int `json:"personalDifficultyStep"`
	PersonalDifficultyMaxLevel int `json:"personalDifficultyMaxLevel"`
}

// AttackParams: 威力・相殺・撃ち返し。
type AttackParams struct {
	ComboToPowerRatio       float64 `json:"comboToPowerRatio"`
	PowerToDakenRate        float64 `json:"powerToDakenRate"`
	BadgePowerBonusPerBadge float64 `json:"badgePowerBonusPerBadge"`
	BadgePowerBonusCap      float64 `json:"badgePowerBonusCap"`
	WarningGraceMs          int     `json:"warningGraceMs"`
	MaxReboundChain         int     `json:"maxReboundChain"`
}

// StackParams: ダケンスタックとトラップ誘発。
type StackParams struct {
	Limit               int `json:"limit"`
	TrapTriggerInterval int `json:"trapTriggerInterval"`
	TrapMissPenalty     int `json:"trapMissPenalty"`
}

// DifficultyParams: 全体難易度。
type DifficultyParams struct {
	GlobalIntervalMs int `json:"globalIntervalMs"`
	MaxLevel         int `json:"maxLevel"`
}

// OdaiParams: ダケン個別制限時間。
type OdaiParams struct {
	BaseTimeLimitMs     int `json:"baseTimeLimitMs"`
	PerLevelReductionMs int `json:"perLevelReductionMs"`
	MinTimeLimitMs      int `json:"minTimeLimitMs"`
}

// MatchingParams: マッチング（試合前）。minPlayers は当日運用で下げられるよう可変性が重要。
type MatchingParams struct {
	MinPlayers       int `json:"minPlayers"`
	MaxPlayers       int `json:"maxPlayers"`
	StartCountdownMs int `json:"startCountdownMs"`
}

// SessionParams: 試合ループの調整値。tick 周期・状態配信間隔もハードコードせずここで持つ（決定4）。
type SessionParams struct {
	TickIntervalMs    int `json:"tickIntervalMs"`
	PublishIntervalMs int `json:"publishIntervalMs"` // 99人ミニ盤面の配信間隔（tickより低頻度で帯域を抑える）
	MatchTimeLimitMs  int `json:"matchTimeLimitMs"`  // 試合の制限時間。0=無効（solo/dev の idle 継続用）。tako-C の終了条件が参照
}

// Validate は破綻値を弾く最小限の検証。config 取得（RemoteLoader / DB / config-front POST）で
// 共通に使う。コア game が GameParameters の不変条件を所有する（検証ロジックの単一ソース）。
func (gp GameParameters) Validate() error {
	if gp.Stack.Limit <= 0 {
		return fmt.Errorf("stack.limit は正である必要 (got %d)", gp.Stack.Limit)
	}
	if gp.Difficulty.MaxLevel <= 0 {
		return fmt.Errorf("difficulty.maxLevel は正である必要 (got %d)", gp.Difficulty.MaxLevel)
	}
	if gp.Customer.Total <= 0 {
		return fmt.Errorf("customer.total は正である必要 (got %d)", gp.Customer.Total)
	}
	return nil
}

// DefaultParameters はリモートコンフィグ取得失敗時のフォールバック内蔵デフォルト。
// 値は 04_パラメータ仕様.md の初期仮値（すべて実測調整前のサンプル）。
func DefaultParameters() GameParameters {
	return GameParameters{
		Combo: ComboParams{
			NoMissBaseGain:             10,
			NoMissPerCharGain:          1,
			MissDecay:                  3,
			PersonalDifficultyStep:     20,
			PersonalDifficultyMaxLevel: 5,
		},
		Attack: AttackParams{
			ComboToPowerRatio:       1.0,
			PowerToDakenRate:        0.1,
			BadgePowerBonusPerBadge: 0.1,
			BadgePowerBonusCap:      1.0,
			WarningGraceMs:          1500,
			MaxReboundChain:         3,
		},
		Stack: StackParams{
			Limit:               20,
			TrapTriggerInterval: 5,
			TrapMissPenalty:     3,
		},
		Difficulty: DifficultyParams{
			GlobalIntervalMs: 30000,
			MaxLevel:         10,
		},
		Odai: OdaiParams{
			BaseTimeLimitMs:     5000,
			PerLevelReductionMs: 300,
			MinTimeLimitMs:      2000,
		},
		Matching: MatchingParams{
			MinPlayers:       20,
			MaxPlayers:       99,
			StartCountdownMs: 15000,
		},
		Session: SessionParams{
			TickIntervalMs:    150,
			PublishIntervalMs: 250,    // 約4Hz。即時イベントとは別に、盤面は低頻度スナップ
			MatchTimeLimitMs:  180000, // 3分（実測調整前のサンプル）。0 で無効＝solo/dev の idle 継続
		},
		Credit: CreditParams{InitialLife: 3},
		Customer: CustomerParams{
			Total:   300,
			Normal:  AttributeSpec{Attribute: proto.AttrNormal, Weight: 70, PatienceBaseMs: 8000, OrderCount: 2},
			Bonus:   AttributeSpec{Attribute: proto.AttrBonus, Weight: 15, PatienceBaseMs: 9000, OrderCount: 2},
			Claimer: AttributeSpec{Attribute: proto.AttrClaimer, Weight: 10, PatienceBaseMs: 6000, OrderCount: 1},
			Buzz:    AttributeSpec{Attribute: proto.AttrBuzz, Weight: 5, PatienceBaseMs: 12000, OrderCount: 4},
		},
		Eval: EvalParams{
			EmaAlpha:        0.3,
			WeightAccuracy:  0.5,
			WeightSpeed:     0.5,
			SpeedBaselineMs: 4000,
			SpeedCap:        2.0,
			MinMsPerWord:    200,
			BuzzBonus:       0.2,
			BuzzDecay:       0.98,
			BuzzCap:         0.5,
		},
	}
}
