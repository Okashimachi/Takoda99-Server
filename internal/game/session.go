package game

import (
	"fmt"
	"math/rand"

	"takoda99/internal/proto"
)

// session.go は【層1コア】1試合の状態機械＋権威データ。純粋・Tick(dt)駆動（時計は持たず room が dt を渡す）。
//
// たこ焼き経営 BR: 99店が300客を捌き合う。直接攻撃なし。
// Tick loop: stepPhase → stepDistribute → stepPatience → stepEvaluate → stepNormalize → stepHeat → stepStorm → checkFinish
// 各ステップは no-op stub。実ロジックは Plan-02～05 で実装する。

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
func broadcastMsg(msg any) Outbound     { return Outbound{To: Recipient{Broadcast: true}, Msg: msg} }

// customer は客1人の権威状態。属性は試合中不変。
type customer struct {
	attribute      proto.CustomerAttribute
	patienceMaxMs  int
	patienceLeftMs int
	orderCount     int
	keystrokeTotal int
	assignedStore  *PlayerId
}

// servedStats は1店の提供集計（リザルト用）。
type servedStats struct {
	count       int
	accuracySum float64
	elapsedSum  int64
}

// storeState は1店分の権威状態。
type storeState struct {
	id             PlayerId
	name           string
	creditLife     int
	evalRaw        float64
	buzzBonus      float64
	evalNormalized float64
	rank           int
	served         servedStats
	alive          bool
	finalRank      int
	elimination    string
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

	customers   map[proto.CustomerId]*customer
	storeQueues map[PlayerId][]proto.CustomerId
	restPool    []proto.CustomerId

	stores map[PlayerId]*storeState
	order  []PlayerId

	state      SessionState
	phase      proto.Phase
	elapsedMs  int64
	tick       int
	aliveCount int
}

// NewSession は WaitingStart 状態の試合を作る。
func NewSession(id proto.MatchId, params GameParameters, words WordSource, rng *rand.Rand, inits []PlayerInit) *Session {
	s := &Session{
		id: id, params: params, words: words, rng: rng,
		customers:   make(map[proto.CustomerId]*customer),
		storeQueues: make(map[PlayerId][]proto.CustomerId, len(inits)),
		restPool:    nil,
		stores:      make(map[PlayerId]*storeState, len(inits)),
		state:       WaitingStart,
		phase:       proto.PhaseEarly,
	}
	life := params.Credit.InitialLife
	for _, in := range inits {
		s.stores[in.Id] = &storeState{
			id:         in.Id,
			name:       in.DisplayName,
			creditLife: life,
			evalRaw:    0,
			alive:      true,
		}
		s.storeQueues[in.Id] = nil
		s.order = append(s.order, in.Id)
	}
	s.aliveCount = len(inits)
	return s
}

// State は現在の状態を返す。
func (s *Session) State() SessionState { return s.state }

// Snapshot は99店概況と生存数を返す。
func (s *Session) Snapshot() ([]proto.StoreSummary, int) {
	return s.summaries(), s.aliveCount
}

// Start は WaitingStart→Running へ遷移し、客プール(300)を生成して各店へ MatchStart を配る。
func (s *Session) Start() []Outbound {
	if s.state != WaitingStart {
		return nil
	}
	s.state = Running
	s.initCustomers()
	stores := s.summaries()
	out := make([]Outbound, 0, len(s.order))
	for _, sid := range s.order {
		out = append(out, to(sid, proto.MatchStart{
			MatchId:     s.id,
			SelfStoreId: sid,
			Params:      s.publicParams(),
			Phase:       s.phase,
			Stores:      stores,
		}))
	}
	return out
}

// ApplyOrderServed は提供完了(OrderServed)を処理する。
func (s *Session) ApplyOrderServed(from PlayerId, r proto.OrderServed) []Outbound {
	if s.state != Running {
		return nil
	}
	st := s.stores[from]
	c := s.customers[r.CustomerId]
	q := s.storeQueues[from]
	if st == nil || !st.alive || c == nil || c.assignedStore == nil || *c.assignedStore != from ||
		len(q) == 0 || q[0] != r.CustomerId {
		return nil
	}

	ep := s.params.Eval
	floor := ep.MinMsPerWord * c.orderCount
	elapsed := r.ElapsedMs
	if elapsed < floor {
		elapsed = floor
	}
	if elapsed <= 0 {
		elapsed = 1
	}
	keys := c.keystrokeTotal
	if keys <= 0 {
		keys = 1
	}
	miss := r.MissCount
	if miss < 0 {
		miss = 0
	}
	if miss > keys {
		miss = keys
	}

	accuracy := 1 - float64(miss)/float64(keys)
	speed := clampF(float64(ep.SpeedBaselineMs)/float64(elapsed), 0, ep.SpeedCap)
	perOrder := ep.WeightAccuracy*accuracy + ep.WeightSpeed*speed

	st.evalRaw = ep.EmaAlpha*perOrder + (1-ep.EmaAlpha)*st.evalRaw
	if c.attribute == proto.AttrBuzz {
		st.buzzBonus = clampF(st.buzzBonus+ep.BuzzBonus, 0, ep.BuzzCap)
	}

	st.served.count++
	st.served.accuracySum += accuracy
	st.served.elapsedSum += int64(elapsed)

	s.releaseToRest(r.CustomerId)

	return append([]Outbound(nil), to(from, proto.EvaluationUpdate{
		EvalRaw:    s.evalScore(st),
		Normalized: st.evalNormalized,
		Rank:       st.rank,
		AliveCount: s.aliveCount,
	}))
}

func (s *Session) evalScore(st *storeState) float64 { return st.evalRaw + st.buzzBonus }

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Tick は時間を dt 進め、試合ループの各ステップを順序で呼ぶ。
func (s *Session) Tick(dtMs int) []Outbound {
	if s.state != Running {
		return nil
	}
	s.elapsedMs += int64(dtMs)
	s.tick++

	var out []Outbound
	out = s.stepPhase(out)
	out = s.stepDistribute(out)
	out = s.stepPatience(dtMs, out)
	out = s.stepEvaluate(out)
	out = s.stepNormalize(out)
	out = s.stepHeat(out)
	out = s.stepStorm(out)
	out = s.checkFinish(out)
	return out
}

// ── 試合ループの各ステップ（no-op stub）────────────────────────

func (s *Session) stepPhase(out []Outbound) []Outbound      { return out }
func (s *Session) stepDistribute(out []Outbound) []Outbound  { return out }
func (s *Session) stepPatience(dtMs int, out []Outbound) []Outbound { _ = dtMs; return out }

func (s *Session) stepEvaluate(out []Outbound) []Outbound {
	decay := s.params.Eval.BuzzDecay
	for _, st := range s.stores {
		if !st.alive || st.buzzBonus == 0 {
			continue
		}
		st.buzzBonus *= decay
		if st.buzzBonus < 1e-4 {
			st.buzzBonus = 0
		}
	}
	return out
}

func (s *Session) stepNormalize(out []Outbound) []Outbound { return out }
func (s *Session) stepHeat(out []Outbound) []Outbound      { return out }
func (s *Session) stepStorm(out []Outbound) []Outbound     { return out }

func (s *Session) checkFinish(out []Outbound) []Outbound {
	if len(s.order) > 1 && s.aliveCount <= 1 {
		s.state = Finished
	}
	return out
}

// ── 客システム ──────────────────────────────────────────────

func (s *Session) initCustomers() {
	total := s.params.Customer.Total
	s.restPool = make([]proto.CustomerId, 0, total)
	for i := 0; i < total; i++ {
		cid := proto.CustomerId(fmt.Sprintf("c-%d", i+1))
		spec := s.rollAttribute()
		s.customers[cid] = &customer{
			attribute:     spec.Attribute,
			patienceMaxMs: spec.PatienceBaseMs,
			orderCount:    spec.OrderCount,
		}
		s.restPool = append(s.restPool, cid)
	}
}

func (s *Session) attributeSpecs() []AttributeSpec {
	c := s.params.Customer
	return []AttributeSpec{c.Normal, c.Bonus, c.Claimer, c.Buzz}
}

func (s *Session) rollAttribute() AttributeSpec {
	specs := s.attributeSpecs()
	total := 0
	for _, a := range specs {
		total += a.Weight
	}
	if total <= 0 {
		return specs[0]
	}
	r := s.rng.Intn(total)
	for _, a := range specs {
		if r < a.Weight {
			return a
		}
		r -= a.Weight
	}
	return specs[len(specs)-1]
}

func (s *Session) admitCustomer(cid proto.CustomerId, store PlayerId) (Outbound, bool) {
	c := s.customers[cid]
	if c == nil {
		return Outbound{}, false
	}
	s.assignCustomer(cid, store)
	words := make([]string, 0, c.orderCount)
	keystrokes := 0
	for i := 0; i < c.orderCount; i++ {
		w := s.words.Next(s.wordLevel(), s.rng)
		words = append(words, w.Text)
		keystrokes += w.KeystrokeCount
	}
	c.keystrokeTotal = keystrokes
	view := proto.CustomerView{
		CustomerId:    cid,
		Attribute:     c.attribute,
		OrderCount:    c.orderCount,
		Words:         words,
		PatienceMaxMs: c.patienceMaxMs,
	}
	return to(store, view), true
}

func (s *Session) wordLevel() int { return 0 }

// ── 客の移動ヘルパ ────────────────────────────────────────────

func (s *Session) assignCustomer(cid proto.CustomerId, store PlayerId) {
	c := s.customers[cid]
	if c == nil {
		return
	}
	s.restPool = removeCustomer(s.restPool, cid)
	s.storeQueues[store] = append(s.storeQueues[store], cid)
	c.assignedStore = &store
	c.patienceLeftMs = c.patienceMaxMs
}

func (s *Session) releaseToRest(cid proto.CustomerId) {
	c := s.customers[cid]
	if c == nil {
		return
	}
	if c.assignedStore != nil {
		q := s.storeQueues[*c.assignedStore]
		s.storeQueues[*c.assignedStore] = removeCustomer(q, cid)
	}
	c.assignedStore = nil
	s.restPool = append(s.restPool, cid)
}

func removeCustomer(ids []proto.CustomerId, cid proto.CustomerId) []proto.CustomerId {
	for i, x := range ids {
		if x == cid {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}

// ── サマリ ────────────────────────────────────────────────

func (s *Session) summaries() []proto.StoreSummary {
	out := make([]proto.StoreSummary, 0, len(s.order))
	for _, sid := range s.order {
		st := s.stores[sid]
		out = append(out, proto.StoreSummary{
			StoreId:        st.id,
			DisplayName:    st.name,
			EvalNormalized: st.evalNormalized,
			Rank:           st.rank,
			CreditLife:     st.creditLife,
			Alive:          st.alive,
		})
	}
	return out
}

func (s *Session) publicParams() proto.GameParametersPublicSubset {
	return proto.GameParametersPublicSubset{
		MatchTimeLimitMs: s.params.Session.MatchTimeLimitMs,
		InitialLife:      s.params.Credit.InitialLife,
		MaxStores:        len(s.order),
	}
}
