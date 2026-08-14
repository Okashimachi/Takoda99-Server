// Package store は【層3・差し替え】試合結果の永続化の口。
// Noop（テスト/ローカル）と db.ResultStore（本番）を合成ルートで切り替える。
package store

import "context"

// Result は1店の最終結果。
type Result struct {
	StoreId         string
	DisplayName     string
	FinalRank   int
	Elimination string
	// Score は最終スコア（本戦の順位を決めた値）。TakoyakiCount は作った個数で、
	// ServedCount（提供した**客**の数）とは別物。
	// 旧 CreditLife / EvalRaw / EvalNormalized は本戦で廃止（plan-h21）。
	Score           int
	TakoyakiCount   int
	ServedCount     int
	AvgAccuracy     float64
	AvgElapsedMs    int
	IsBot           bool
	SurvivedMs      int64
	LeftCount       int
	TotalKeystrokes int
	TotalMisses     int
	FastestMs       int
	SlowestMs       int
	NormalServed    int
	NormalLeft      int
	BonusServed     int
	BonusLeft       int
	ClaimerServed   int
	ClaimerLeft     int
	BuzzServed      int
	BuzzLeft        int
}

// OrderAttempt は1注文ぶんの記録（plan-h03）。
//
// 試合×店の集計（Result）より1段細かい粒度で、「1注文をどう捌いたか」を heat 別に残す。
// BOT を人間らしくするには heat 別の速度・ミス率の分布が要る（h04/h05 の燃料）。
type OrderAttempt struct {
	StoreId    string
	CustomerId string
	Attribute  string
	HeatLevel  int
	OrderCount int
	Keystrokes int
	ElapsedMs  int
	MissCount  int
	IsBot      bool
}

// MatchResult は1試合の結果全体。
type MatchResult struct {
	MatchId    string
	DurationMs int64
	HumanCount int
	BotCount   int
	WinnerId   string
	ConfigHash string
	Results    []Result
	// Attempts は注文単位の記録。既定では人間ぶんだけが入る（合成ルートで絞る）。
	Attempts []OrderAttempt
}

// ResultStore は試合結果を保存する差し替え可能な口。
type ResultStore interface {
	SaveMatch(ctx context.Context, m MatchResult) error
}

// Noop は何もしない実装（永続化未導入の間の既定）。
type Noop struct{}

func (Noop) SaveMatch(context.Context, MatchResult) error { return nil }

var _ ResultStore = Noop{}
