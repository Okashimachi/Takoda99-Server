// Package proto は canonical な契約リポジトリ github.com/Okashimachi/Takoda99-Proto を
// server 内から参照するための薄いラッパ。canonical の型・定数を type alias / const で再輸出し、
// server 側の import パスを "takoda99/internal/proto" に固定する。
//
// canonical に新メッセージ/型が増えたら、この再輸出リストにも1行追加する
// （未追加の型を使うと "undefined" の明示的コンパイルエラーになる）。型の追加・変更・削除は
// canonical 側で人間（りーせ）承認を得てから行う。
//
// 🔴 **サーバーが送らなくなった型は、ここから再輸出も消すこと**（plan-h23 §5.1）。
// canonical 側は方式B（Deprecated マーカーを付けて定義は残す）なので契約は壊れず、
// 旧クライアントも受け取れる。だが**ラッパが名指しで再輸出している限り staticcheck の
// SA1019 が消えない**ため、「廃止フィールドの参照が消えたか」を lint で検知できなくなる。
//
// 本戦移行（h21〜h23）で以下を削除した。復活させないこと:
//
//	CustomerLeft / LeaveReason / LeaveTimeout          … 客が逃げない（h21）
//	CreditUpdate / CreditReason / CreditCustomerLeft   … 信用制の廃止（h21）
//	ElimSelfCollapse                                   … 自滅の経路が消えた（h21・脱落は Cull のみ）
//	StoreListUpdate / TypeStoreListUpdate              … Ranking 系へ置換（h23）
package proto

import canon "github.com/Okashimachi/Takoda99-Proto/proto"

// ── 共通ID ────────────────────────────────────────────────
type (
	StoreId    = canon.StoreId
	CustomerId = canon.CustomerId
	MatchId    = canon.MatchId
)

// ── 列挙 ──────────────────────────────────────────────────
type (
	CustomerAttribute = canon.CustomerAttribute
	Phase             = canon.Phase
	EliminationReason = canon.EliminationReason
)

const (
	AttrNormal  = canon.AttrNormal
	AttrBonus   = canon.AttrBonus
	AttrClaimer = canon.AttrClaimer
	AttrBuzz    = canon.AttrBuzz

	PhaseEarly = canon.PhaseEarly
	PhaseMid   = canon.PhaseMid
	PhaseLate  = canon.PhaseLate

	ElimCull = canon.ElimCull
)

// ── 共通DTO ────────────────────────────────────────────────
type (
	StoreSummary               = canon.StoreSummary
	CustomerView               = canon.CustomerView
	MatchStats                 = canon.MatchStats
	AttributeTally             = canon.AttributeTally
	GameParametersPublicSubset = canon.GameParametersPublicSubset
	Envelope                   = canon.Envelope

	// CullStageView は段階的足切りの1ステージ（v0.8.0・本選）。
	// GameParametersPublicSubset.CullSchedule の要素。
	CullStageView = canon.CullStageView
)

// ── メッセージ種別タグ ────────────────────────────────────
const (
	// C2S
	TypeOrderServed      = canon.TypeOrderServed
	TypeMatchmakingJoin  = canon.TypeMatchmakingJoin
	TypeMatchmakingLeave = canon.TypeMatchmakingLeave

	// S2C
	TypeMatchStart               = canon.TypeMatchStart
	TypeCustomerArrived          = canon.TypeCustomerArrived
	TypeEvaluationUpdate         = canon.TypeEvaluationUpdate
	TypeDifficultyUpdate         = canon.TypeDifficultyUpdate
	TypePhaseChange              = canon.TypePhaseChange
	TypeForcedEliminationWarning = canon.TypeForcedEliminationWarning
	TypeStoreEliminated          = canon.TypeStoreEliminated
	TypePersonalResult           = canon.TypePersonalResult
	TypeMatchEnd                 = canon.TypeMatchEnd
	TypeMatchmakingStatus        = canon.TypeMatchmakingStatus

	// v0.8.0（本戦）で追加。ランキング配信と足切りの一括通知（h23 で配信側を実装する）。
	TypeStoreEliminatedBatch = canon.TypeStoreEliminatedBatch
	TypeRankingSnapshot      = canon.TypeRankingSnapshot
	TypeRankingDelta         = canon.TypeRankingDelta
)

// ── C2S ───────────────────────────────────────────────────
type (
	OrderServed      = canon.OrderServed
	MatchmakingJoin  = canon.MatchmakingJoin
	MatchmakingLeave = canon.MatchmakingLeave
)

// ── S2C ───────────────────────────────────────────────────
type (
	MatchStart               = canon.MatchStart
	CustomerArrived          = canon.CustomerArrived
	EvaluationUpdate         = canon.EvaluationUpdate
	DifficultyUpdate         = canon.DifficultyUpdate
	PhaseChange              = canon.PhaseChange
	ForcedEliminationWarning = canon.ForcedEliminationWarning
	StoreEliminated          = canon.StoreEliminated
	PersonalResult           = canon.PersonalResult
	MatchEnd                 = canon.MatchEnd
	MatchmakingStatus        = canon.MatchmakingStatus
	MatchmakingParticipant   = canon.MatchmakingParticipant
)

// ── S2C（v0.8.0・本戦で追加） ─────────────────────────────
//
// スコア制・時刻足切りに伴う新メッセージ。配信の実装は h23（plan-h23_配信の再設計）で行う。
// ここでは h21〜h23 が参照できるように再輸出だけしておく。
type (
	// RankingEntry は RankingSnapshot の1行（rank 付きの全量）。
	RankingEntry = canon.RankingEntry
	// RankingSnapshot は全店の順位の全量配信（低頻度・整合性の回復）。
	RankingSnapshot = canon.RankingSnapshot
	// RankingChange は RankingDelta の1行（rank を持たない）。
	RankingChange = canon.RankingChange
	// RankingDelta は変化した店のみの差分配信（高頻度・取りこぼし可）。
	RankingDelta = canon.RankingDelta
	// StoreEliminatedBatch は1回の足切りで脱落した店をまとめて配信する。
	StoreEliminatedBatch = canon.StoreEliminatedBatch
)
