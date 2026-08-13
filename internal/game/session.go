package game

import (
	"fmt"
	"math"
	"math/rand"
	"sort"

	"takoda99/internal/proto"
)

// session.go は【層1コア】1試合の状態機械＋権威データ。純粋・Tick(dt)駆動（時計は持たず room が dt を渡す）。
//
// たこ焼き経営 BR: 99店が客を捌き合う。直接攻撃なし。
// Tick loop: stepPhase → stepDistribute → stepRank → stepHeat → stepStorm → checkFinish
//
// 本戦ルール（plan-h21）で順位を決める値が **評価（EMA×パーセンタイル正規化の相対値）→
// スコア（累積の絶対値）** に変わった。あわせて信用・我慢ゲージ・客属性の評価効果を削除した。
// スコアは ApplyOrderServed で加算されるので、tick 側にスコアの処理は無い（stepRank は並べるだけ）。
//
// ⚠ stepStorm / checkFinish は**まだ予選仕様**（tick周期の下位%淘汰・生存1店で終了）。
// 20秒等間隔の時刻足切りと120秒全店脱落への置き換えは plan-h22。

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
//
// 本戦（plan-h21）で**客は逃げない**。我慢ゲージ（patienceMaxMs / patienceLeftMs /
// patienceStartedAtMs）は削除した。一度出たお題は必ず打ち切られる。
// 属性は見た目の出し分け専用で、スコアには一切影響しない。
type customer struct {
	attribute      proto.CustomerAttribute
	orderCount     int
	keystrokeTotal int
	assignedStore  *PlayerId
}

// servedStats は1店の提供集計（リザルト用）。
//
// accuracySum は「客ごとの精度の平均」を出すためのもので、
// **全体の打鍵数に対するミス率とは別物**（客ごとに打鍵数が違うため）。
// 後者を出すには keystrokes/misses の生の合計が要るので両方持つ。
type servedStats struct {
	count       int
	accuracySum float64
	elapsedSum  int64

	keystrokes int // 提供した客の打鍵数の合計
	misses     int // 同ミス数の合計
	fastestMs  int // 1客を捌いた最短(ms)。未提供なら 0
	slowestMs  int // 同最長(ms)

	// takoyaki は作ったたこ焼きの総数（= 提供した客の orderCount の累計）。
	// count（提供した**客**の数）とは別物。スコアの加点対象はこちら。
	takoyaki int

	// leftCount は我慢切れで帰られた客の数（取りこぼし）。
	//
	// 本戦（plan-h21）で客が逃げなくなったため**常に 0**。リザルトの表示互換のために
	// 集計フィールドだけ残している（proto.MatchStats.LeftCount も同様）。
	leftCount int

	// byAttr は属性ごとの捌き／取りこぼしの内訳。添字は attrIndex。
	// Left 側は上記と同じ理由で常に 0。
	byAttr [attrCount]proto.AttributeTally
}

// 属性の添字。proto.CustomerAttribute は文字列なので、集計配列の添字へ写す。
const (
	attrIdxNormal = iota
	attrIdxBonus
	attrIdxClaimer
	attrIdxBuzz
	attrCount
)

func attrIndex(a proto.CustomerAttribute) int {
	switch a {
	case proto.AttrBonus:
		return attrIdxBonus
	case proto.AttrClaimer:
		return attrIdxClaimer
	case proto.AttrBuzz:
		return attrIdxBuzz
	default:
		return attrIdxNormal
	}
}

// storeState は1店分の権威状態。
//
// 本戦（plan-h21）で順位を決めるのは score ただ1つ。
// creditLife（信用）・evalRaw/evalNormalized（相対評価）・buzzBonus（属性加点）・
// prevStar（星表示）は削除した。復活させないこと。
type storeState struct {
	id   PlayerId
	name string

	// score は順位を決める累積値。
	// WeightTakoyaki×たこ焼き数 − WeightMiss×ミス数 の累計で、**0 でクランプしない**（§1.1）。
	// 0 で止めると下位が全員ぴったり 0 に密集し、足切りで「どの店を切るか」が
	// タイブレーク頼みの恣意的なものになる。負値は「ミスが多かった」という正直な情報。
	score int

	rank        int
	served      servedStats
	alive       bool
	finalRank   int
	elimination string
	survivedMs  int64
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
	customerSeq      int
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
	for _, in := range inits {
		s.stores[in.Id] = &storeState{
			id:    in.Id,
			name:  in.DisplayName,
			score: 0,
			alive: true,
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

// Params は現在のパラメータを返す。
func (s *Session) Params() GameParameters { return s.params }

// Start は WaitingStart→Running へ遷移し、客プール(300)を生成して各店へ MatchStart を配る。
func (s *Session) Start(startsAtServerMs int64) []Outbound {
	if s.state != WaitingStart {
		return nil
	}
	s.state = Running
	s.elapsedMs = -int64(s.params.Matching.ReadyCountdownMs)
	s.initCustomers()
	stores := s.summaries()
	out := make([]Outbound, 0, len(s.order))
	for _, sid := range s.order {
		out = append(out, to(sid, proto.MatchStart{
			MatchId:          s.id,
			SelfStoreId:      sid,
			Params:           s.publicParams(),
			Phase:            s.phase,
			Stores:           stores,
			StartsAtServerMs: startsAtServerMs,
		}))
	}
	return out
}

// ApplyOrderServed は提供完了(OrderServed)を処理する。
func (s *Session) ApplyOrderServed(from PlayerId, r proto.OrderServed) []Outbound {
	if s.state != Running {
		return nil
	}
	if s.elapsedMs < 0 {
		// フライング入力（REQ-04 の開始前カウントダウン中の入力）は無視する
		return nil
	}
	st := s.stores[from]
	c := s.customers[r.CustomerId]
	q := s.storeQueues[from]
	if st == nil || !st.alive || c == nil || c.assignedStore == nil || *c.assignedStore != from ||
		len(q) == 0 || q[0] != r.CustomerId {
		return nil
	}

	// サニティ検証（本戦でも残す）。elapsedMs はスコアには使わないが、
	// あり得ない申告で統計が汚れるのを防ぐため下限クランプは続ける。
	floor := s.params.Sanity.MinMsPerWord * c.orderCount
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

	// ── スコア加算（本戦の順位を決める唯一の値・plan-h21 §1）──
	//
	// 属性による加減点は無い。速度の項も持たない（速さは「時間内に何個作れたか」に表れる）。
	// **0 でクランプしない**（§1.1）。
	sp := s.params.Score
	st.score += sp.WeightTakoyaki*c.orderCount - sp.WeightMiss*miss

	st.served.takoyaki += c.orderCount
	st.served.count++
	st.served.accuracySum += accuracy
	st.served.elapsedSum += int64(elapsed)
	st.served.keystrokes += keys
	st.served.misses += miss
	if st.served.fastestMs == 0 || elapsed < st.served.fastestMs {
		st.served.fastestMs = elapsed
	}
	if elapsed > st.served.slowestMs {
		st.served.slowestMs = elapsed
	}
	st.served.byAttr[attrIndex(c.attribute)].Served++

	s.releaseToRest(r.CustomerId)

	return append([]Outbound(nil), to(from, s.evaluationUpdate(st)))
}

// evaluationUpdate は1店ぶんの EvaluationUpdate を組み立てる。
//
// **自店の順位はこれが権威**（proto の RankingSnapshot/Delta は他店を含む表示用）。
// 相対評価が廃止されたので evalRaw / normalized / starRating / starDelta は入れない
// （proto では定義が残るがゼロ値のまま送る）。
func (s *Session) evaluationUpdate(st *storeState) proto.EvaluationUpdate {
	return proto.EvaluationUpdate{
		Score:      st.score,
		Rank:       st.rank,
		AliveCount: s.aliveCount,
	}
}

// Tick は時間を dt (ms) 進め、状態機械を1ステップ駆動する。
func (s *Session) Tick(dt int) []Outbound {
	if s.state != Running {
		return nil
	}
	s.elapsedMs += int64(dt)
	if s.elapsedMs < 0 {
		// REQ-04 の開始前カウントダウン中。ゲームは進行しない。
		return nil
	}

	s.tick++

	var out []Outbound
	out = s.stepPhase(out)
	out = s.stepDistribute(out)
	out = s.stepRank(out)
	out = s.stepHeat(out)
	out = s.stepStorm(out)
	out = s.checkFinish(out)
	return out
}

// ── 試合ループの各ステップ ──────────────────────────────────

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

	for _, cid := range distributable {
		if len(candidates) == 0 {
			break
		}
		// 重みは行列の短さだけ（plan-h21 §4）。スコア/評価は一切見ない。
		// これは予選の「全店 evalNormalized=0」の分岐（試合開始直後に毎回通っていた経路）を
		// 常用にしたもので、新規ロジックではない。
		weights := make([]float64, len(candidates))
		for i, cd := range candidates {
			weights[i] = 1.0 / float64(cd.queueLen+1)
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
// stepRank は生存店をスコア降順に並べて rank を振り、各店へ EvaluationUpdate を返す。
//
// パーセンタイル正規化は廃止した（plan-h21 §2）。順位は「スコアが高い順」であって、
// 相対位置ではない。同値のタイブレークは weakerForRank（§2.1）。
//
// 配信頻度の間引きは h23 の担当。ここでは毎tick返す。
func (s *Session) stepRank(out []Outbound) []Outbound {
	alive := s.aliveStores()
	if len(alive) == 0 {
		return out
	}

	// 強い順（＝ weakerForRank の逆）。sort.SliceStable + 決定的な全順序なので、
	// map の反復順に依存せず毎回同じ並びになる。
	sortStrongestFirst(alive)

	for i, st := range alive {
		st.rank = i + 1
	}
	for _, st := range alive {
		out = append(out, to(st.id, s.evaluationUpdate(st)))
	}
	return out
}

// aliveStores は生存店を s.order の順（＝決定的な順）で集める。
// s.stores（map）を直接走査しないこと。Go の map 反復順はランダムで、
// シード固定の matchsim が再現しなくなる。
func (s *Session) aliveStores() []*storeState {
	alive := make([]*storeState, 0, s.aliveCount)
	for _, sid := range s.order {
		if st := s.stores[sid]; st.alive {
			alive = append(alive, st)
		}
	}
	return alive
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

	// 上下限で挟む。
	//
	// 上限: heat.maxLevel はお題辞書に語彙がある最大段階に合わせて設定する値。
	// 超えても WordSource が下の段階へ降りるだけで難度は変わらないが、クライアントへ
	// 配る heatLevel が実態と食い違い、運営UIの maxLevel が「効かないツマミ」になる。
	// 下限: heat.base に負値が入ると heatLevel が負になり、WordSource の下降ループが
	// 1回も回らずフォールバック語だけが出続ける（試合として壊れる）。
	if hp.MaxLevel > 0 && newHeat > hp.MaxLevel {
		newHeat = hp.MaxLevel
	}
	if newHeat < 0 {
		newHeat = 0
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

		// selfAtRisk は店ごとに異なるので broadcast できない（宛先別に配る）。
		// 「今 storm が起きたら淘汰される集合」を淘汰本体と同じロジックで求める。
		atRisk := s.cullTargets()
		for _, sid := range s.order {
			if !s.stores[sid].alive {
				continue
			}
			out = append(out, to(sid, proto.ForcedEliminationWarning{
				UntilTick:    remaining,
				ThresholdPct: sp.ThresholdPct,
				SelfAtRisk:   atRisk[sid],
			}))
		}
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

// cullCandidates は「今 storm が起きたら淘汰される店」を弱い順に返す。
//
// 予告(selfAtRisk)と実行(executeCull)で別々に判定すると、予告が嘘になったり
// 「警告が出ていないのに落ちる」が起きる。両者はこの1関数を共有する。
func (s *Session) cullCandidates() []*storeState {
	sp := s.params.Storm

	alive := s.aliveStores()
	if len(alive) <= 1 {
		return nil
	}

	cullCount := int(float64(len(alive))*sp.ThresholdPct + 0.999999)
	if cullCount < 1 {
		cullCount = 1
	}
	if cullCount >= len(alive) {
		cullCount = len(alive) - 1
	}

	sortWeakestFirst(alive)
	return alive[:cullCount]
}

// cullTargets は淘汰対象の店IDの集合（予告の selfAtRisk 用）。
func (s *Session) cullTargets() map[PlayerId]bool {
	targets := s.cullCandidates()
	m := make(map[PlayerId]bool, len(targets))
	for _, st := range targets {
		m[st.id] = true
	}
	return m
}

func (s *Session) executeCull(out []Outbound) []Outbound {
	// 対象の選定は予告(selfAtRisk)と同じ関数を使う。ここがズレると予告が嘘になる。
	culled := s.cullCandidates()
	if len(culled) == 0 {
		return out
	}

	// 淘汰対象の中での順位は、自滅と同じ総合判定で決める。
	sortWeakestFirst(culled)

	for i := 0; i < len(culled); i++ {
		st := culled[i]
		st.alive = false
		st.survivedMs = s.elapsedMs
		s.aliveCount--

		for _, cid := range s.storeQueues[st.id] {
			s.releaseToRest(cid)
		}
		s.storeQueues[st.id] = nil

		st.finalRank = s.aliveCount + 1
		st.elimination = string(proto.ElimCull)

		out = append(out, broadcastMsg(proto.StoreEliminated{
			StoreId:   st.id,
			Reason:    proto.ElimCull,
			FinalRank: st.finalRank,
		}))
		out = append(out, to(st.id, s.buildPersonalResult(st)))
	}

	return out
}

// rankAccuracy はタイブレーク用の正確性（1 − 総ミス数/総打鍵数）。
//
// リザルトの avgAccuracy（客ごとの精度の平均・buildMatchStats）とは別物。
// 客ごとに打鍵数が違うので両者は一致しない。順位に使うのは生の合計ベースのこちら。
//
// 🔴 **未提供店（keystrokes=0）は 0 を返す**（＝最下位側）。20秒地点では「1件も提供していない店」が
// 必ず出るので、ここでゼロ除算すると初日に必ず落ちる。
func (st *storeState) rankAccuracy() float64 {
	if st.served.keystrokes <= 0 {
		return 0
	}
	return 1 - float64(st.served.misses)/float64(st.served.keystrokes)
}

// rankAvgElapsedMs はタイブレーク用の平均所要（小さいほど速い＝強い）。
//
// 🔴 **未提供店（count=0）は +∞ を返す**（＝最下位側）。rankAccuracy と同じ理由。
func (st *storeState) rankAvgElapsedMs() float64 {
	if st.served.count == 0 {
		return math.Inf(1)
	}
	return float64(st.served.elapsedSum) / float64(st.served.count)
}

// weakerForRank は「a の方が下位か」を返す（本戦・plan-h21 §2.1）。
//
// 順位付け（stepRank）と足切り対象の選定（cullCandidates）で**同じ判定を共有する**。
// ここが割れると「順位表では上なのに切られた」が起きる。
//
// 比較順: スコア → 正確性 → 速度 → storeId
//
//	1. score が低い方が下位
//	2. 同値 → 正確性（1 − 総ミス/総打鍵）が低い方が下位。未提供店は 0
//	3. 同値 → 平均所要が大きい（遅い）方が下位。未提供店は +∞
//	4. 同値 → storeId（決定性の最終担保）
//
// 🔴 **4段目を省略しない。** 完全同値が残ると並びが揺れ、シード固定の matchsim が
// 再現しなくなってバランス調整とテストが信用できなくなる。
func weakerForRank(a, b *storeState) bool {
	if a.score != b.score {
		return a.score < b.score
	}
	if aa, ba := a.rankAccuracy(), b.rankAccuracy(); aa != ba {
		return aa < ba
	}
	if ae, be := a.rankAvgElapsedMs(), b.rankAvgElapsedMs(); ae != be {
		return ae > be
	}
	return a.id > b.id
}

// sortWeakestFirst は弱い順に並べる（先頭ほど下位＝先に脱落扱い）。
func sortWeakestFirst(stores []*storeState) {
	sort.SliceStable(stores, func(i, j int) bool { return weakerForRank(stores[i], stores[j]) })
}

// sortStrongestFirst は強い順に並べる（先頭が1位）。weakerForRank の逆順で、
// 判定そのものは共有する（順位と足切りで基準がズレないようにするため）。
func sortStrongestFirst(stores []*storeState) {
	sort.SliceStable(stores, func(i, j int) bool { return weakerForRank(stores[j], stores[i]) })
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
			st.survivedMs = s.elapsedMs
			break
		}
	}

	for _, pid := range s.order {
		st := s.stores[pid]
		if st.alive {
			// 優勝者には PersonalResult と MatchEnd の両方を送る
			out = append(out, to(pid, s.buildPersonalResult(st)))
		}
		// 全体へは MatchEnd (空) を送る
		out = append(out, to(pid, proto.MatchEnd{}))
	}
	return out
}

// buildPersonalResult は自店の脱落確定時に本人へ送るリザルトを組む。
//
// Reason は入れない（本戦は脱落経路が足切りの1本だけになったので、理由を出す意味が無い）。
// creditLeft / evalRaw / evalNormalized も廃止（proto では定義が残るがゼロ値のまま送る）。
func (s *Session) buildPersonalResult(st *storeState) proto.PersonalResult {
	return proto.PersonalResult{
		FinalRank:  st.finalRank,
		Stats:      s.buildMatchStats(st),
		SurvivedMs: st.survivedMs,
		// Score は順位を決めた値そのもの。TakoyakiCount は作った個数で、
		// Stats.ServedCount（提供した**客**の数）とは別物。
		Score:         st.score,
		TakoyakiCount: st.served.takoyaki,
	}
}

func (s *Session) buildMatchStats(st *storeState) proto.MatchStats {
	v := st.served
	stats := proto.MatchStats{
		ServedCount:     v.count,
		LeftCount:       v.leftCount,
		TotalKeystrokes: v.keystrokes,
		TotalMisses:     v.misses,
		FastestMs:       v.fastestMs,
		SlowestMs:       v.slowestMs,
		Normal:          v.byAttr[attrIdxNormal],
		Bonus:           v.byAttr[attrIdxBonus],
		Claimer:         v.byAttr[attrIdxClaimer],
		Buzz:            v.byAttr[attrIdxBuzz],
	}
	// 平均は提供0だとゼロ除算になる。取りこぼしだけの店でも leftCount は返す。
	if v.count > 0 {
		stats.AvgAccuracy = v.accuracySum / float64(v.count)
		stats.AvgElapsedMs = int(v.elapsedSum / int64(v.count))
	}
	return stats
}

// ── 客システム ──────────────────────────────────────────────

func (s *Session) initCustomers() {
	total := s.params.Customer.Total
	s.restPool = make([]proto.CustomerId, 0, total)
	for i := 0; i < total; i++ {
		cid := proto.CustomerId(fmt.Sprintf("c-%d", i+1))
		spec := s.rollAttribute()
		s.customers[cid] = &customer{
			attribute:  spec.Attribute,
			orderCount: spec.OrderCount,
		}
		s.restPool = append(s.restPool, cid)
	}
	s.customerSeq = total
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
	// 我慢ゲージは廃止（客は逃げない）。patience* には値を入れない。
	return to(store, proto.CustomerView{
		CustomerId: cid,
		Attribute:  c.attribute,
		OrderCount: c.orderCount,
		Words:      words,
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

	// 新しいIDを振ってからプールに戻す（クライアントの重複IDバグ回避）
	s.customerSeq++
	newCid := proto.CustomerId(fmt.Sprintf("c-%d", s.customerSeq))
	s.customers[newCid] = c
	delete(s.customers, cid)

	s.restPool = append(s.restPool, newCid)
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
			StoreId:     st.id,
			DisplayName: st.name,
			Rank:        st.rank,
			Score:       st.score,
			Alive:       st.alive,
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

// publicParams は MatchStart で配る公開サブセット。
//
// 廃止フィールド（initialLife / stormThresholdPct / patience*）には値を入れない。
// CullSchedule は h22（時刻足切り）で GameParameters に入ってから詰める。
func (s *Session) publicParams() proto.GameParametersPublicSubset {
	return proto.GameParametersPublicSubset{
		MaxStores: len(s.order),
		// スコア算出はサーバー権威。これを配るのは「+100」等の加点演出のためだけ。
		ScoreWeightTakoyaki:      s.params.Score.WeightTakoyaki,
		ScoreWeightMiss:          s.params.Score.WeightMiss,
		FinalStageAliveThreshold: s.params.Presentation.FinalStageAliveThreshold,
		FinalRushAliveThreshold:  s.params.Presentation.FinalRushAliveThreshold,
	}
}

// ── リザルト ──────────────────────────────────────────────

type StoreResult struct {
	StoreId     PlayerId
	DisplayName string
	FinalRank   int
	Elimination string
	// Score は最終スコア（順位を決めた値）。TakoyakiCount は作った個数。
	// 旧 CreditLife / EvalRaw / EvalNormalized は本戦で廃止（plan-h21）。
	Score         int
	TakoyakiCount int
	SurvivedMs    int64
	Stats         proto.MatchStats
}

func (s *Session) Results() []StoreResult {
	results := make([]StoreResult, 0, len(s.order))
	for _, pid := range s.order {
		st := s.stores[pid]
		results = append(results, StoreResult{
			StoreId:       st.id,
			DisplayName:   st.name,
			FinalRank:     st.finalRank,
			Elimination:   st.elimination,
			Score:         st.score,
			TakoyakiCount: st.served.takoyaki,
			SurvivedMs:    st.survivedMs,
			Stats:         s.buildMatchStats(st),
		})
	}
	return results
}

func (s *Session) Id() proto.MatchId { return s.id }
func (s *Session) AliveCount() int   { return s.aliveCount }
func (s *Session) ElapsedMs() int64  { return s.elapsedMs }

// ── 観測(admin)向けの純粋 getter（plan-h02）──
//
// いずれも副作用のない読み出しで、全店横断の状態と時系列を返すだけ（判定式ではない）。
// game コアの純粋性は保たれる（hub/slog を import しない）。room の単一 goroutine から
// publish 直後に呼ばれる前提（session を触るのは room だけなのでデータ競合しない）。

// AttrCounts は客属性別の人数（観測用）。Normal/Bonus(おばちゃん)/Claimer/Buzz(JK)。
type AttrCounts struct {
	Normal  int `json:"normal"`
	Bonus   int `json:"bonus"`
	Claimer int `json:"claimer"`
	Buzz    int `json:"buzz"`
}

func (a *AttrCounts) add(attr proto.CustomerAttribute) {
	switch attr {
	case proto.AttrBonus:
		a.Bonus++
	case proto.AttrClaimer:
		a.Claimer++
	case proto.AttrBuzz:
		a.Buzz++
	default:
		a.Normal++
	}
}

// StormView は下位淘汰(storm)の観測用ビュー。
type StormView struct {
	Warning      bool    // 予告中（stepStorm が予告を出してから実行までの間）
	UntilTick    int     // 実行までの残りtick（Warning 中のみ有効）
	ThresholdPct float64 // 淘汰される正規化評価下位の割合
}

// StoreBoardRow は1店の観測用の行（AdminSnapshot 用の素材）。
//
// 本戦（plan-h21）で CreditLife / EvalNormalized は Score に置き換わった。
// スコア分布ビュー等の本格的な観測 v2 化は h25。
type StoreBoardRow struct {
	Id          PlayerId
	Name        string
	Alive       bool
	Rank        int
	FinalRank   int // 0 = 生存中（脱落済みのみ正）
	Score       int
	QueueLen    int
	ServedCount int
	AtRisk      bool       // 今 storm が起きたら淘汰される圏内か
	QueueByAttr AttrCounts // 行列中の客の属性内訳（客フロー可視化用）
}

// Phase は現在の局面（Early/Mid/Late）を返す。
func (s *Session) Phase() proto.Phase { return s.phase }

// HeatLevel は現在の火力（お題難度）レベルを返す。
func (s *Session) HeatLevel() int { return s.heatLevel }

// RestPoolCount は未割当客（たべたべエリア）の数を返す。
func (s *Session) RestPoolCount() int { return len(s.restPool) }

// RestPoolByAttr は未割当客の属性内訳を返す。
func (s *Session) RestPoolByAttr() AttrCounts {
	var a AttrCounts
	for _, cid := range s.restPool {
		if c := s.customers[cid]; c != nil {
			a.add(c.attribute)
		}
	}
	return a
}

// CustomerMix は在場（restPool＋全行列）の客を属性別に集計して返す。
// 客は served/left でも新IDで restPool に戻る（自滅しない限り総数一定）ので、
// customers レジストリ全体を走査すれば在場総数の内訳になる。
func (s *Session) CustomerMix() AttrCounts {
	var a AttrCounts
	for _, c := range s.customers {
		a.add(c.attribute)
	}
	return a
}

// StormState は storm 予告の状態を返す。
func (s *Session) StormState() StormView {
	sp := s.params.Storm
	v := StormView{Warning: s.stormWarnSent, ThresholdPct: sp.ThresholdPct}
	if s.stormWarnSent {
		if u := sp.IntervalTicks - s.stormTickCounter; u > 0 {
			v.UntilTick = u
		}
	}
	return v
}

// StoreBoard は99店の観測用の行を order 順で返す。行列長・提供数・storm対象圏・
// 行列の属性内訳を含む。AtRisk は「今 storm が起きたら淘汰される店」（予告と同一ロジック）。
func (s *Session) StoreBoard() []StoreBoardRow {
	atRisk := s.cullTargets()
	out := make([]StoreBoardRow, 0, len(s.order))
	for _, sid := range s.order {
		st := s.stores[sid]
		row := StoreBoardRow{
			Id:          st.id,
			Name:        st.name,
			Alive:       st.alive,
			Rank:        st.rank,
			Score:       st.score,
			QueueLen:    len(s.storeQueues[sid]),
			ServedCount: st.served.count,
			AtRisk:      st.alive && atRisk[sid],
		}
		if !st.alive && st.finalRank > 0 {
			row.FinalRank = st.finalRank
		}
		for _, cid := range s.storeQueues[sid] {
			if c := s.customers[cid]; c != nil {
				row.QueueByAttr.add(c.attribute)
			}
		}
		out = append(out, row)
	}
	return out
}
