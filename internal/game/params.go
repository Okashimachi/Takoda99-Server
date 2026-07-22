// Package game は【層1・コア】戦闘の権威。純粋な計算のみで、ネットワーク・時間・I/O を持たない。
//
// per-player の判定式（combo/attack/offset/stack/difficulty）と、全体を調停する session
// 状態機械（Tick(dt) で進む）を内包する。game は他の internal 部品/スパインを import しない
// （proto は契約として参照可）。継ぎ目は ports.go（DIP）。すべての調整値は GameParameters 経由。
package game

// GameParameters は数値バランスの全項目。正典は
// Textro99-Docs/03_サーバー仕様/04_パラメータ仕様.md。
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

// SessionParams: 試合ループの調整値。tick 周期もハードコードせずここで持つ（決定4）。
type SessionParams struct {
	TickIntervalMs int `json:"tickIntervalMs"`
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
			TickIntervalMs: 150,
		},
	}
}
