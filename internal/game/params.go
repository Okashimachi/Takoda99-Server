// Package game は【層1・コア】試合の権威。純粋な計算のみで、ネットワーク・時間・I/O を持たない。
//
// たこ焼き経営BR の状態機械（Tick(dt) で進む）を内包する。game は他の internal 部品/スパインを
// import しない（proto は契約として参照可）。継ぎ目は ports.go（DIP）。すべての調整値は
// GameParameters 経由。
package game

import (
	"fmt"

	"takoda99/internal/proto"
)

// GameParameters は数値バランスの全項目。正典は
// Takoda99-Docs/02_共通仕様/03_パラメータ仕様.md。
// サーバーが起動時に config(ConfigProvider) 経由で外部取得し、失敗時は DefaultParameters()。
// クライアントへは MatchStart で公開サブセット（proto側）に絞って配信する。
type GameParameters struct {
	Session      SessionParams      `json:"session"`
	Matching     MatchingParams     `json:"matching"`
	Credit       CreditParams       `json:"credit"`
	Customer     CustomerParams     `json:"customer"`
	Eval         EvalParams         `json:"eval"`
	Phase        PhaseParams        `json:"phase"`
	Heat         HeatParams         `json:"heat"`
	Storm        StormParams        `json:"storm"`
	Distribution DistributionParams `json:"distribution"`
	Patience     PatienceParams     `json:"patience"`
	Bot          BotParams          `json:"bot"`
}

// SessionParams: 試合ループの調整値。tick 周期・状態配信間隔もハードコードせずここで持つ。
type SessionParams struct {
	TickIntervalMs    int `json:"tickIntervalMs"`
	PublishIntervalMs int `json:"publishIntervalMs"`
	MatchTimeLimitMs  int `json:"matchTimeLimitMs"`
}

// MatchingParams: マッチング（試合前）。minPlayers は当日運用で下げられるよう可変性が重要。
type MatchingParams struct {
	MinPlayers       int `json:"minPlayers"`
	MaxPlayers       int `json:"maxPlayers"`
	StartCountdownMs int `json:"startCountdownMs"`
	MinFill          int `json:"minFill"`
}

// LeaveLoss: 属性別の離脱ペナルティ。
type LeaveLoss struct {
	Normal  int `json:"normal"`
	Bonus   int `json:"bonus"`
	Claimer int `json:"claimer"`
	Buzz    int `json:"buzz"`
}

// For は属性に対応する減少量を返す。
func (ll LeaveLoss) For(attr proto.CustomerAttribute) int {
	switch attr {
	case proto.AttrBonus:
		return ll.Bonus
	case proto.AttrClaimer:
		return ll.Claimer
	case proto.AttrBuzz:
		return ll.Buzz
	default:
		return ll.Normal
	}
}

// CreditParams: 信用（ライフ）。客の離脱でのみ減少・0で自滅脱落。
type CreditParams struct {
	InitialLife int       `json:"initialLife"`
	LeaveLoss   LeaveLoss `json:"leaveLoss"`
}

// CustomerParams: 客システム（総数・属性ごとの出現率/我慢/注文数）。
type CustomerParams struct {
	Total   int           `json:"total"`
	Normal  AttributeSpec `json:"normal"`
	Bonus   AttributeSpec `json:"bonus"`
	Claimer AttributeSpec `json:"claimer"`
	Buzz    AttributeSpec `json:"buzz"`
}

// AttributeSpec: 1属性分の生成パラメータ。
type AttributeSpec struct {
	Attribute      proto.CustomerAttribute `json:"attribute"`
	Weight         int                     `json:"weight"`
	PatienceBaseMs int                     `json:"patienceBaseMs"`
	OrderCount     int                     `json:"orderCount"`
}

// EvalParams: 提供スコア→評価EMA の調整値。
type EvalParams struct {
	EmaAlpha        float64 `json:"emaAlpha"`
	WeightAccuracy  float64 `json:"weightAccuracy"`
	WeightSpeed     float64 `json:"weightSpeed"`
	SpeedBaselineMs int     `json:"speedBaselineMs"`
	SpeedCap        float64 `json:"speedCap"`
	MinMsPerWord    int     `json:"minMsPerWord"`
	BuzzBonus       float64 `json:"buzzBonus"`
	BuzzDecay       float64 `json:"buzzDecay"`
	BuzzCap         float64 `json:"buzzCap"`
}

// PhaseParams: フェーズ遷移（Early → Mid → Late）。
type PhaseParams struct {
	MidAliveThreshold  int `json:"midAliveThreshold"`
	LateAliveThreshold int `json:"lateAliveThreshold"`
	MidTimeMs          int `json:"midTimeMs"`
	LateTimeMs         int `json:"lateTimeMs"`
}

// HeatParams: 火力（お題難易度の全体上昇）。
type HeatParams struct {
	Base         int     `json:"base"`
	PerAliveDrop float64 `json:"perAliveDrop"`
	PhaseEarly   int     `json:"phaseEarly"`
	PhaseMid     int     `json:"phaseMid"`
	PhaseLate    int     `json:"phaseLate"`
	MaxLevel     int     `json:"maxLevel"`
}

// StormParams: 下位淘汰（定期的に下位%を強制脱落）。
type StormParams struct {
	IntervalTicks int     `json:"intervalTicks"`
	WarnTicks     int     `json:"warnTicks"`
	ThresholdPct  float64 `json:"thresholdPct"`
}

// DistributionParams: 客の分配（restPool→店の行列）。
type DistributionParams struct {
	QueueRefillThreshold int     `json:"queueRefillThreshold"`
	WeightFloor          float64 `json:"weightFloor"`
}

// PatienceParams: 我慢ゲージの調整。
type PatienceParams struct {
	LateMul float64 `json:"lateMul"`
	AlertMs int     `json:"alertMs"`
}

// BotParams: CPU（Bot）の強さ。
type BotParams struct {
	ServeIntervalMs int     `json:"serveIntervalMs"`
	MissRate        float64 `json:"missRate"`
}

// Validate は破綻値を弾く最小限の検証。
func (gp GameParameters) Validate() error {
	if gp.Customer.Total <= 0 {
		return fmt.Errorf("customer.total は正である必要 (got %d)", gp.Customer.Total)
	}
	if gp.Credit.InitialLife <= 0 {
		return fmt.Errorf("credit.initialLife は正である必要 (got %d)", gp.Credit.InitialLife)
	}
	if gp.Session.TickIntervalMs <= 0 {
		return fmt.Errorf("session.tickIntervalMs は正である必要 (got %d)", gp.Session.TickIntervalMs)
	}
	if gp.Bot.ServeIntervalMs <= 0 {
		return fmt.Errorf("bot.serveIntervalMs は正である必要 (got %d)", gp.Bot.ServeIntervalMs)
	}
	if gp.Bot.MissRate < 0 || gp.Bot.MissRate > 1 {
		return fmt.Errorf("bot.missRate は 0..1 である必要 (got %v)", gp.Bot.MissRate)
	}
	if gp.Heat.MaxLevel <= 0 {
		return fmt.Errorf("heat.maxLevel は正である必要 (got %d)", gp.Heat.MaxLevel)
	}
	return nil
}

// DefaultParameters はリモートコンフィグ取得失敗時のフォールバック内蔵デフォルト。
func DefaultParameters() GameParameters {
	return GameParameters{
		Session: SessionParams{
			TickIntervalMs:    150,
			PublishIntervalMs: 250,
			MatchTimeLimitMs:  0,
		},
		Matching: MatchingParams{
			MinPlayers:       20,
			MaxPlayers:       99,
			StartCountdownMs: 15000,
			MinFill:          99,
		},
		Credit: CreditParams{
			InitialLife: 3,
			LeaveLoss:   LeaveLoss{Normal: 1, Bonus: 1, Claimer: 1, Buzz: 2},
		},
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
		Phase: PhaseParams{
			MidAliveThreshold:  70,
			LateAliveThreshold: 30,
			MidTimeMs:          30000,
			LateTimeMs:         90000,
		},
		Heat: HeatParams{
			Base:         0,
			PerAliveDrop: 0.1,
			PhaseEarly:   0,
			PhaseMid:     3,
			PhaseLate:    8,
			MaxLevel:     20,
		},
		Storm: StormParams{
			IntervalTicks: 40,
			WarnTicks:     10,
			ThresholdPct:  0.10,
		},
		Distribution: DistributionParams{
			QueueRefillThreshold: 3,
			WeightFloor:          0.25,
		},
		Patience: PatienceParams{
			LateMul: 0.6,
			AlertMs: 2000,
		},
		Bot: BotParams{
			ServeIntervalMs: 800,
			MissRate:        0.05,
		},
	}
}
