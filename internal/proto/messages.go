// Package proto は canonical な契約リポジトリ github.com/Okashimachi/Takoda99-Proto を
// server 内から参照するための薄いラッパ。canonical の型・定数を type alias / const で再輸出し、
// server 側の import パスを "takoda99/internal/proto" に固定する。
//
// canonical に新メッセージ/型が増えたら、この再輸出リストにも1行追加する
// （未追加の型を使うと "undefined" の明示的コンパイルエラーになる）。型の追加・変更・削除は
// canonical 側で人間（りーせ）承認を得てから行う。
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
	LeaveReason       = canon.LeaveReason
	CreditReason      = canon.CreditReason
)

const (
	AttrNormal  = canon.AttrNormal
	AttrBonus   = canon.AttrBonus
	AttrClaimer = canon.AttrClaimer
	AttrBuzz    = canon.AttrBuzz

	PhaseEarly = canon.PhaseEarly
	PhaseMid   = canon.PhaseMid
	PhaseLate  = canon.PhaseLate

	ElimSelfCollapse = canon.ElimSelfCollapse
	ElimCull         = canon.ElimCull

	LeaveTimeout = canon.LeaveTimeout

	CreditCustomerLeft = canon.CreditCustomerLeft
)

// ── 共通DTO ────────────────────────────────────────────────
type (
	StoreSummary               = canon.StoreSummary
	CustomerView               = canon.CustomerView
	MatchStats                 = canon.MatchStats
	AttributeTally             = canon.AttributeTally
	GameParametersPublicSubset = canon.GameParametersPublicSubset
	Envelope                   = canon.Envelope
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
	TypeCustomerLeft             = canon.TypeCustomerLeft
	TypeCreditUpdate             = canon.TypeCreditUpdate
	TypeEvaluationUpdate         = canon.TypeEvaluationUpdate
	TypeDifficultyUpdate         = canon.TypeDifficultyUpdate
	TypePhaseChange              = canon.TypePhaseChange
	TypeStoreListUpdate          = canon.TypeStoreListUpdate
	TypeForcedEliminationWarning = canon.TypeForcedEliminationWarning
	TypeStoreEliminated          = canon.TypeStoreEliminated
	TypePersonalResult           = canon.TypePersonalResult
	TypeMatchEnd                 = canon.TypeMatchEnd
	TypeMatchmakingStatus        = canon.TypeMatchmakingStatus
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
	CustomerLeft             = canon.CustomerLeft
	CreditUpdate             = canon.CreditUpdate
	EvaluationUpdate         = canon.EvaluationUpdate
	DifficultyUpdate         = canon.DifficultyUpdate
	PhaseChange              = canon.PhaseChange
	StoreListUpdate          = canon.StoreListUpdate
	ForcedEliminationWarning = canon.ForcedEliminationWarning
	StoreEliminated          = canon.StoreEliminated
	PersonalResult           = canon.PersonalResult
	MatchEnd                 = canon.MatchEnd
	MatchmakingStatus        = canon.MatchmakingStatus
	MatchmakingParticipant   = canon.MatchmakingParticipant
)
