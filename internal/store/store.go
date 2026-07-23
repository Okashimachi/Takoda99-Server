// Package store は【層3・差し替え】試合結果の永続化の口。現状は Noop（何もしない）。
//
// 将来ランキング等が必要になったら DB 実装を差し込む。合成ルートが ResultStore を注入し、
// MatchSession の外側（room/合成ルート）が試合終了時に Save を呼ぶ想定。依存は stdlib のみ。
package store

import "context"

// Result は1プレイヤーの最終結果（GameOver 相当）。永続化する最小情報。
type Result struct {
	MatchId         string
	PlayerId        string
	DisplayName     string
	Rank            int
	KoCount         int
	FinalBadgeCount int
}

// ResultStore は試合結果を保存する差し替え可能な口。
type ResultStore interface {
	Save(ctx context.Context, r Result) error
}

// Noop は何もしない実装（永続化未導入の間の既定）。
type Noop struct{}

// Save は何もせず nil を返す。
func (Noop) Save(context.Context, Result) error { return nil }

var _ ResultStore = Noop{}
