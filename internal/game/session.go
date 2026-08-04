package game

import (
	"fmt"
	"math/rand"
	"sort"

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
	served      servedStats
	alive       bool
	finalRank   int
	elimination string
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

	heatLevel        int
	stormTickCounter int
	stormWarnSent    bool
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

func (s *Session) stepPhase(out []Outbound) []Outbound {
	pp := s.params.Phase
	switch s.phase {
	case proto.PhaseEarly:
		if s.aliveCount <= pp.MidAliveThreshold || s.elapsedMs >= int64(pp.MidTimeMs) {
			s.phase = proto.PhaseMid
			out = append(out, broadcastMsg(proto.PhaseChange{Phase: proto.PhaseMid}))
		}
	case proto.PhaseMid:
		if s.aliveCount <= pp.LateAliveThreshold || s.elapsedMs >= int64(pp.LateTimeMs) {
			s.phase = proto.PhaseLate
			out = append(out, broadcastMsg(proto.PhaseChange{Phase: proto.PhaseLate}))
		}
	}
	return out
}
func (s *Session) stepDistribute(out []Outbound) []Outbound {
	threshold := s.params.Distribution.QueueRefillThreshold

	type candidate struct {
		id       PlayerId
		queueLen int
	}
	candidates := make([]candidate, 0, len(s.order))
	for _, sid := range s.order {
		st := s.stores[sid]
		if !st.alive {
			continue
		}
		ql := len(s.storeQueues[sid])
		if ql < threshold {
			candidates = append(candidates, candidate{id: sid, queueLen: ql})
		}
	}
	if len(candidates) == 0 {
		return out
	}

	distributable := make([]proto.CustomerId, 0, len(s.restPool))
	for _, cid := range s.restPool {
		c := s.customers[cid]
		if c == nil {
			continue
		}
		if s.phase == proto.PhaseEarly && c.attribute == proto.AttrClaimer {
			continue
		}
		distributable = append(distributable, cid)
	}
	if len(distributable) == 0 {
		return out
	}

	allZero := true
	for _, sid := range s.order {
		st := s.stores[sid]
		if st.alive && st.evalNormalized != 0 {
			allZero = false
			break
		}
	}

	for _, cid := range distributable {
		if len(candidates) == 0 {
			break
		}
		weights := make([]float64, len(candidates))
		floor := s.params.Distribution.WeightFloor
		for i, cd := range candidates {
			if allZero {
				weights[i] = 1.0 / float64(cd.queueLen+1)
			} else {
				weights[i] = (floor + s.stores[cd.id].evalNormalized) / float64(cd.queueLen+1)
			}
		}
		totalW := 0.0
		for _, w := range weights {
			totalW += w
		}
		if totalW <= 0 {
			break
		}
		idx := s.weightedSelect(weights)
		chosen := &candidates[idx]

		ob, ok := s.admitCustomer(cid, chosen.id)
		if ok {
			out = append(out, ob)
		}

		chosen.queueLen++
		if chosen.queueLen >= threshold {
			candidates = append(candidates[:idx], candidates[idx+1:]...)
		}
	}
	return out
}

func (s *Session) weightedSelect(weights []float64) int {
	total := 0.0
	for _, w := range weights {
		if w > 0 {
			total += w
		}
	}
	if total <= 0 {
		return s.rng.Intn(len(weights))
	}
	r := s.rng.Float64() * total
	for i, w := range weights {
		if w > 0 {
			r -= w
			if r <= 0 {
				return i
			}
		}
	}
	return len(weights) - 1
}
func (s *Session) stepPatience(dtMs int, out []Outbound) []Outbound {
	effectiveDt := dtMs
	if s.phase == proto.PhaseLate && s.params.Patience.LateMul > 0 {
		effectiveDt = int(float64(dtMs) / s.params.Patience.LateMul)
	}

	// 同一tickで信用が尽きた店をいったん溜める。
	// ここで即座に脱落させると、順位が s.order（店IDの並び）で決まってしまい、
	// 「同時に落ちた店の優劣」が実力と無関係になる。
	var collapsed []*storeState

	for _, sid := range s.order {
		st := s.stores[sid]
		if !st.alive {
			continue
		}
		q := s.storeQueues[sid]
		if len(q) == 0 {
			continue
		}
		head := s.customers[q[0]]
		head.patienceLeftMs -= effectiveDt
		if head.patienceLeftMs <= 0 {
			var died bool
			out, died = s.processLeave(sid, q[0], out)
			if died {
				collapsed = append(collapsed, st)
			}
		}
	}

	return s.resolveCollapses(collapsed, out)
}

// resolveCollapses は同一tickで信用が尽きた店をまとめて脱落させる。
//
// 総合判定（weakerForRank）で弱い順に並べ、弱い店から先に落とす＝下位の順位を与える。
// バトルロイヤルなので**全員が同時に倒れても優勝者は必ず1店残す**。
// その場合、いちばん強い店は脱落させずに生存させ、checkFinish が正規の優勝者として扱う
// （「優勝者なのに脱落理由が付く」状態を作らない）。
func (s *Session) resolveCollapses(collapsed []*storeState, out []Outbound) []Outbound {
	if len(collapsed) == 0 {
		return out
	}

	sortWeakestFirst(collapsed)

	// 生存店が全滅する場合は、最強の1店を残す（勝者不在にしない）。
	if len(collapsed) == s.aliveCount {
		collapsed = collapsed[:len(collapsed)-1]
	}

	for _, st := range collapsed {
		out = s.selfCollapse(st.id, out)
	}
	return out
}

// processLeave は客の離脱を確定し信用を減らす。
// 信用が尽きた場合は died=true を返すだけで、脱落の確定は呼び出し側
// （resolveCollapses）が同一tick分をまとめて総合判定してから行う。
func (s *Session) processLeave(store PlayerId, cid proto.CustomerId, out []Outbound) ([]Outbound, bool) {
	c := s.customers[cid]

	out = append(out, to(store, proto.CustomerLeft{
		CustomerId: cid,
		Reason:     proto.LeaveTimeout,
	}))

	s.releaseToRest(cid)

	loss := s.params.Credit.LeaveLoss.For(c.attribute)
	st := s.stores[store]
	st.creditLife -= loss

	out = append(out, to(store, proto.CreditUpdate{
		Life:   st.creditLife,
		Delta:  -loss,
		Reason: proto.CreditCustomerLeft,
	}))

	return out, st.creditLife <= 0
}

func (s *Session) selfCollapse(store PlayerId, out []Outbound) []Outbound {
	st := s.stores[store]

	st.alive = false
	s.aliveCount--

	for len(s.storeQueues[store]) > 0 {
		cid := s.storeQueues[store][0]
		s.releaseToRest(cid)
	}

	st.finalRank = s.aliveCount + 1
	st.elimination = string(proto.ElimSelfCollapse)

	out = append(out, broadcastMsg(proto.StoreEliminated{
		StoreId:   store,
		Reason:    proto.ElimSelfCollapse,
		FinalRank: st.finalRank,
	}))

	return out
}

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

func (s *Session) stepNormalize(out []Outbound) []Outbound {
	type entry struct {
		store *storeState
		score float64
	}
	alive := make([]entry, 0, s.aliveCount)
	for _, sid := range s.order {
		st := s.stores[sid]
		if st.alive {
			alive = append(alive, entry{store: st, score: s.evalScore(st)})
		}
	}
	n := len(alive)
	if n == 0 {
		return out
	}

	for i := 1; i < n; i++ {
		key := alive[i]
		j := i - 1
		for j >= 0 && alive[j].score > key.score {
			alive[j+1] = alive[j]
			j--
		}
		alive[j+1] = key
	}

	for i, e := range alive {
		if n <= 1 {
			e.store.evalNormalized = 1.0
			e.store.rank = 1
		} else {
			e.store.evalNormalized = float64(i) / float64(n-1)
			e.store.rank = n - i
		}
	}

	for _, e := range alive {
		out = append(out, to(e.store.id, proto.EvaluationUpdate{
			EvalRaw:    s.evalScore(e.store),
			Normalized: e.store.evalNormalized,
			Rank:       e.store.rank,
			AliveCount: s.aliveCount,
		}))
	}
	return out
}
func (s *Session) stepHeat(out []Outbound) []Outbound {
	hp := s.params.Heat
	maxStores := len(s.order)

	newHeat := hp.Base + int(hp.PerAliveDrop*float64(maxStores-s.aliveCount))
	switch s.phase {
	case proto.PhaseEarly:
		newHeat += hp.PhaseEarly
	case proto.PhaseMid:
		newHeat += hp.PhaseMid
	case proto.PhaseLate:
		newHeat += hp.PhaseLate
	}

	if newHeat != s.heatLevel {
		s.heatLevel = newHeat
		out = append(out, broadcastMsg(proto.DifficultyUpdate{HeatLevel: s.heatLevel}))
	}
	return out
}

func (s *Session) stepStorm(out []Outbound) []Outbound {
	sp := s.params.Storm
	if sp.IntervalTicks <= 0 {
		return out
	}
	s.stormTickCounter++

	warnAt := sp.IntervalTicks - sp.WarnTicks
	if warnAt < 1 {
		warnAt = 1
	}
	if s.stormTickCounter == warnAt && !s.stormWarnSent {
		s.stormWarnSent = true
		remaining := sp.IntervalTicks - s.stormTickCounter
		out = append(out, broadcastMsg(proto.ForcedEliminationWarning{
			UntilTick:    remaining,
			ThresholdPct: sp.ThresholdPct,
		}))
	}

	if s.stormTickCounter < sp.IntervalTicks {
		return out
	}

	s.stormTickCounter = 0
	s.stormWarnSent = false

	if s.aliveCount <= 1 {
		return out
	}

	out = s.executeCull(out)
	return out
}

func (s *Session) executeCull(out []Outbound) []Outbound {
	sp := s.params.Storm

	alive := make([]*storeState, 0, s.aliveCount)
	for _, sid := range s.order {
		if s.stores[sid].alive {
			alive = append(alive, s.stores[sid])
		}
	}
	if len(alive) <= 1 {
		return out
	}

	cullCount := int(float64(len(alive))*sp.ThresholdPct + 0.999999)
	if cullCount < 1 {
		cullCount = 1
	}
	if cullCount >= len(alive) {
		cullCount = len(alive) - 1
	}

	sortStoresByWeakest(alive)
	culled := alive[:cullCount]

	// 淘汰対象の中での順位も、自滅と同じ総合判定で決める。
	sortWeakestFirst(culled)

	for i := 0; i < len(culled); i++ {
		st := culled[i]
		st.alive = false
		s.aliveCount--

		for _, cid := range s.storeQueues[st.id] {
			c := s.customers[cid]
			if c != nil {
				c.assignedStore = nil
				s.restPool = append(s.restPool, cid)
			}
		}
		s.storeQueues[st.id] = nil

		st.finalRank = s.aliveCount + 1
		st.elimination = string(proto.ElimCull)

		out = append(out, broadcastMsg(proto.StoreEliminated{
			StoreId:   st.id,
			Reason:    proto.ElimCull,
			FinalRank: st.finalRank,
		}))
	}

	return out
}

func sortStoresByWeakest(stores []*storeState) {
	sort.SliceStable(stores, func(i, j int) bool {
		a, b := stores[i], stores[j]
		if a.evalNormalized != b.evalNormalized {
			return a.evalNormalized < b.evalNormalized
		}
		return a.creditLife < b.creditLife
	})
}

// avgAccuracy は平均精度（提供実績が無ければ0）。
func (st *storeState) avgAccuracy() float64 {
	if st.served.count == 0 {
		return 0
	}
	return st.served.accuracySum / float64(st.served.count)
}

// weakerForRank は「同時に脱落した店同士の総合的な優劣」を返す（a の方が下位なら true）。
//
// 最終順位は脱落順で決まるが、同一tickで複数店が同時に落ちた分は順序が付かない。
// そこを店IDの並び順のような無意味な基準で決めないための総合判定
// （AGENTS「評価は順位計算に使わない。同時脱落のタイブレークにのみ使う」）。
//
// 比較順: 残信用 → 正規化評価 → 生評価 → 提供数 → 平均精度 → id
// 最後に id を見るのは、全部同値でも順序が揺れないようにするため（決定的なリプレイのため）。
func weakerForRank(a, b *storeState) bool {
	if a.creditLife != b.creditLife {
		return a.creditLife < b.creditLife
	}
	if a.evalNormalized != b.evalNormalized {
		return a.evalNormalized < b.evalNormalized
	}
	if a.evalRaw != b.evalRaw {
		return a.evalRaw < b.evalRaw
	}
	if a.served.count != b.served.count {
		return a.served.count < b.served.count
	}
	if aa, ba := a.avgAccuracy(), b.avgAccuracy(); aa != ba {
		return aa < ba
	}
	return a.id > b.id
}

// sortWeakestFirst は弱い順に並べる（先頭ほど下位＝先に脱落扱い）。
func sortWeakestFirst(stores []*storeState) {
	sort.SliceStable(stores, func(i, j int) bool { return weakerForRank(stores[i], stores[j]) })
}

func (s *Session) checkFinish(out []Outbound) []Outbound {
	if len(s.order) <= 1 {
		return out
	}
	if s.aliveCount > 1 {
		return out
	}

	s.state = Finished

	for _, pid := range s.order {
		if st := s.stores[pid]; st.alive {
			st.finalRank = 1
			st.elimination = ""
			break
		}
	}

	for _, pid := range s.order {
		st := s.stores[pid]
		out = append(out, to(pid, proto.MatchEnd{
			FinalRank: st.finalRank,
			Stats:     s.buildMatchStats(st),
		}))
	}
	return out
}

func (s *Session) buildMatchStats(st *storeState) proto.MatchStats {
	if st.served.count == 0 {
		return proto.MatchStats{}
	}
	return proto.MatchStats{
		ServedCount:  st.served.count,
		AvgAccuracy:  st.served.accuracySum / float64(st.served.count),
		AvgElapsedMs: int(st.served.elapsedSum / int64(st.served.count)),
	}
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
	return to(store, proto.CustomerView{
		CustomerId:    cid,
		Attribute:     c.attribute,
		OrderCount:    c.orderCount,
		Words:         words,
		PatienceMaxMs: c.patienceMaxMs,
	}), true
}

func (s *Session) wordLevel() int { return s.heatLevel }

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
		sum := proto.StoreSummary{
			StoreId:        st.id,
			DisplayName:    st.name,
			EvalNormalized: st.evalNormalized,
			Rank:           st.rank,
			CreditLife:     st.creditLife,
			Alive:          st.alive,
		}
		// finalRank は脱落済みの店だけに入れる（生存店では JSON にキーごと出さない）。
		// 0 を送ると「順位0」という存在しない順位をクライアントに渡すことになる。
		if !st.alive && st.finalRank > 0 {
			fr := st.finalRank
			sum.FinalRank = &fr
		}
		out = append(out, sum)
	}
	return out
}

func (s *Session) publicParams() proto.GameParametersPublicSubset {
	return proto.GameParametersPublicSubset{
		InitialLife: s.params.Credit.InitialLife,
		MaxStores:   len(s.order),
		// 順位バーに「淘汰圏」の帯を常時描くために配る（既存の storm 設定をそのまま公開）。
		StormThresholdPct: s.params.Storm.ThresholdPct,
		// TODO(tako-K): 終盤/最終盤の演出切替しきい値。対応する GameParameters の項目は
		// tako-K で定義する。それまでは 0（クライアントは演出切替を行わない）。
		FinalStageAliveThreshold: 0,
		FinalRushAliveThreshold:  0,
	}
}

// ── リザルト ──────────────────────────────────────────────

type StoreResult struct {
	StoreId     PlayerId
	DisplayName string
	FinalRank   int
	Elimination string
	CreditLife  int
	EvalRaw     float64
	Stats       proto.MatchStats
}

func (s *Session) Results() []StoreResult {
	results := make([]StoreResult, 0, len(s.order))
	for _, pid := range s.order {
		st := s.stores[pid]
		results = append(results, StoreResult{
			StoreId:     st.id,
			DisplayName: st.name,
			FinalRank:   st.finalRank,
			Elimination: st.elimination,
			CreditLife:  st.creditLife,
			EvalRaw:     st.evalRaw,
			Stats:       s.buildMatchStats(st),
		})
	}
	return results
}

func (s *Session) Id() proto.MatchId { return s.id }
func (s *Session) AliveCount() int   { return s.aliveCount }
func (s *Session) ElapsedMs() int64  { return s.elapsedMs }
