package game

import (
	"math/rand"

	"textro99/internal/proto"
)

// session.go は【層1コア】1試合の状態機械。純粋・Tick(dt)駆動（時計は持たず room が dt を渡す）。
//
// たこ焼き版の実ロジック（客レジストリ/行列/客分配/評価EMA・正規化/信用/我慢・離脱/フェーズ/
// 火力/下位淘汰/リザルト）は tako-B 以降で段階的に実装する。現状は【骨組み】：
// 状態機械（WaitingStart/Running/Finished）＋ MatchStart 配信＋空の Tick / ApplyOrderServed。

// SessionState は試合の状態。
type SessionState int

const (
	WaitingStart SessionState = iota
	Running
	Finished
)

// Recipient は Outbound の宛先。Broadcast=true で全員。
type Recipient struct {
	PlayerId  PlayerId
	Broadcast bool
}

// Outbound は session が返す「宛先つきメッセージ」。Msg は proto.<Message> の値で、
// room が Envelope に包んで実際の接続へ送る（game は通信を知らない）。
type Outbound struct {
	To  Recipient
	Msg any
}

func to(pid PlayerId, msg any) Outbound { return Outbound{To: Recipient{PlayerId: pid}, Msg: msg} }

// storeState は1店分の横断状態。たこ焼き版で creditLife/evalRaw/evalNormalized/rank/servedStats を
// 加える（tako-B）。現状は骨組みの最小フィールドのみ。
type storeState struct {
	id    PlayerId
	name  string
	alive bool
}

// PlayerInit は NewSession に渡す初期店舗情報。
type PlayerInit struct {
	Id          PlayerId
	DisplayName string
}

// Session は1試合。words はDIPで注入される部品実装。
type Session struct {
	id     proto.MatchId
	params GameParameters
	words  WordSource
	rng    *rand.Rand

	stores     map[PlayerId]*storeState
	order      []PlayerId
	state      SessionState
	elapsedMs  int64
	aliveCount int
}

// NewSession は WaitingStart 状態の試合を作る。客・お題は Start / Tick 側（tako-C以降）で扱う。
func NewSession(id proto.MatchId, params GameParameters, words WordSource, rng *rand.Rand, inits []PlayerInit) *Session {
	s := &Session{
		id: id, params: params, words: words, rng: rng,
		stores: make(map[PlayerId]*storeState, len(inits)),
		state:  WaitingStart,
	}
	for _, in := range inits {
		s.stores[in.Id] = &storeState{id: in.Id, name: in.DisplayName, alive: true}
		s.order = append(s.order, in.Id)
	}
	s.aliveCount = len(inits)
	return s
}

// State は現在の状態を返す。
func (s *Session) State() SessionState { return s.state }

// Snapshot は99店概況と生存数を返す（publisher が盤面の定期配信に使う）。
func (s *Session) Snapshot() ([]proto.StoreSummary, int) {
	return s.summaries(), s.aliveCount
}

// Start は WaitingStart→Running へ遷移し、各店へ MatchStart を配る。
func (s *Session) Start() []Outbound {
	if s.state != WaitingStart {
		return nil
	}
	s.state = Running
	stores := s.summaries()
	out := make([]Outbound, 0, len(s.order))
	for _, sid := range s.order {
		out = append(out, to(sid, proto.MatchStart{
			MatchId:     s.id,
			SelfStoreId: sid,
			Params:      s.publicParams(),
			Phase:       proto.PhaseEarly,
			Stores:      stores,
		}))
	}
	return out
}

// ApplyOrderServed は提供完了(OrderServed)を処理する（サニティ→評価EMA→行列除去）。
// tako-E で実装。現状は無処理。
func (s *Session) ApplyOrderServed(from PlayerId, r proto.OrderServed) []Outbound {
	_ = from
	_ = r
	return nil
}

// Tick は時間を dt 進める。実ループ（フェーズ/分配/我慢/離脱/評価/火力/storm/配信）は
// tako-C 以降で実装する。現状は経過時間の加算のみ。
func (s *Session) Tick(dtMs int) []Outbound {
	if s.state != Running {
		return nil
	}
	s.elapsedMs += int64(dtMs)
	return nil
}

// ── ヘルパ ────────────────────────────────────────────────

func (s *Session) summaries() []proto.StoreSummary {
	out := make([]proto.StoreSummary, 0, len(s.order))
	for _, sid := range s.order {
		st := s.stores[sid]
		out = append(out, proto.StoreSummary{
			StoreId:     st.id,
			DisplayName: st.name,
			Alive:       st.alive,
		})
	}
	return out
}

// publicParams はクライアント表示用の公開サブセット。tako-K で新スキーマから正しくマップする。
func (s *Session) publicParams() proto.GameParametersPublicSubset {
	return proto.GameParametersPublicSubset{
		MaxStores: len(s.order),
	}
}
