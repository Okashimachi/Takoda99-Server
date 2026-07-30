package game

import (
	"fmt"
	"math/rand"

	"textro99/internal/proto"
)

// session.go は【層1コア】1試合の状態機械＋権威データ。純粋・Tick(dt)駆動（時計は持たず room が dt を渡す）。
//
// tako-B: データ構造（客レジストリ/行列/たべたべエリア/店舗状態/matchState）と状態機械を用意する。
// 実ロジック（客初期化=tako-D / 提供=tako-E / 我慢・離脱・信用=tako-F / 評価・分配=tako-G /
// フェーズ・火力・下位淘汰=tako-H / 終了・順位=tako-I）は後続で載せる。

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

// customer は客1人の権威状態（customerRegistry の値）。属性は試合中不変。
// 実体は複製せず、所属は assignedStore（nil=たべたべエリア）と行列ID配列で表す。
type customer struct {
	attribute      proto.CustomerAttribute
	patienceMaxMs  int
	patienceLeftMs int
	orderCount     int       // 打つ単語数（属性別・来店時のお題本数）
	keystrokeTotal int       // 来店時に発行した全語の正準打鍵数の合計（精度算出の分母・tako-E）
	assignedStore  *PlayerId // 割り当て先の店。nil=未割当（restPool）
}

// servedStats は1店の提供集計（リザルト用・tako-E）。平均は結果確定時(tako-I)に算出する。
type servedStats struct {
	count       int     // 提供した注文数
	accuracySum float64 // 精度の総和（÷count で平均精度）
	elapsedSum  int64   // 所要msの総和（÷count で平均所要）
}

// storeState は1店分の権威状態。
type storeState struct {
	id             PlayerId
	name           string
	creditLife     int     // 信用(HP)。客の離脱でのみ減少・0で自滅脱落（tako-F）
	evalRaw        float64 // 評価EMA（正規化前・tako-E で更新）
	buzzBonus      float64 // JK(Buzz)満足の一時加点（毎tick減衰・tako-E）
	evalNormalized float64 // 生存店内パーセンタイル 0..1（tako-G で更新）
	rank           int     // 生存店内の評価順位（tako-G）
	served         servedStats
	lastServeMs    int64 // 直近の提供を受理した試合内時刻（提供間隔サニティ・tako-E）
	alive          bool
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

	// 客の権威データ（単一情報源）。移動は ID配列の増減のみ（実体を複製・破棄しない）。
	customers   map[proto.CustomerId]*customer  // 客レジストリ（tako-D で 300 初期化）
	storeQueues map[PlayerId][]proto.CustomerId // 各店の行列（先頭=対応中）
	restPool    []proto.CustomerId              // たべたべエリア（未割当）

	stores map[PlayerId]*storeState
	order  []PlayerId // 安定順

	// matchState（heatLevel は tako-H 火力で追加）
	state      SessionState
	phase      proto.Phase
	elapsedMs  int64
	tick       int
	aliveCount int
}

// NewSession は WaitingStart 状態の試合を作る。店舗を初期ライフ／評価初期値で用意し、
// 客レジストリ・行列・たべたべエリアは空で初期化する（客の生成は Start 時の initCustomers）。
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

// Snapshot は99店概況と生存数を返す（publisher が盤面の定期配信に使う）。
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

// ApplyOrderServed は提供完了(OrderServed)を処理する（tako-E）。
// サニティ検証 → 提供スコア(perOrder) → 評価EMA反映 → 満足客を行列から除去 → EvaluationUpdate 配信。
// 判定の権威はサーバー：クライアント計測(elapsedMs/missCount)は性善説で受けるが下限クランプ＋逸脱棄却する。
func (s *Session) ApplyOrderServed(from PlayerId, r proto.OrderServed) []Outbound {
	if s.state != Running {
		return nil
	}
	st := s.stores[from]
	c := s.customers[r.CustomerId]
	// サニティ①：該当客が存在し、この店に割当済み（＝対応中）か。逸脱は棄却。
	if st == nil || !st.alive || c == nil || c.assignedStore == nil || *c.assignedStore != from {
		return nil
	}
	// サニティ②：提供間隔が短すぎない（2件目以降のみ）。1語分の最小所要より速い連投は棄却。
	minGap := int64(s.params.Eval.MinMsPerWord)
	if st.served.count > 0 && s.elapsedMs-st.lastServeMs < minGap {
		return nil
	}

	ep := s.params.Eval
	// サニティ③：elapsedMs を下限（minMsPerWord×orderCount）へクランプ。missCount は [0, 総打鍵] へ。
	floor := ep.MinMsPerWord * c.orderCount
	elapsed := r.ElapsedMs
	if elapsed < floor {
		elapsed = floor
	}
	if elapsed <= 0 {
		elapsed = 1 // 0除算保険（floor<=0 の異常設定）
	}
	keys := c.keystrokeTotal
	if keys <= 0 {
		keys = 1 // 0除算保険（打鍵数不明の異常）
	}
	miss := r.MissCount
	if miss < 0 {
		miss = 0
	}
	if miss > keys {
		miss = keys
	}

	// 提供スコア：perOrder = w_acc*精度 + w_spd*速度。
	accuracy := 1 - float64(miss)/float64(keys)                                   // 0..1
	speed := clampF(float64(ep.SpeedBaselineMs)/float64(elapsed), 0, ep.SpeedCap) // 0..speedCap
	perOrder := ep.WeightAccuracy*accuracy + ep.WeightSpeed*speed

	// 評価EMA反映。JK(Buzz)は一時加点（上限クランプ・減衰は stepEvaluate）。
	st.evalRaw = ep.EmaAlpha*perOrder + (1-ep.EmaAlpha)*st.evalRaw
	if c.attribute == proto.AttrBuzz {
		st.buzzBonus = clampF(st.buzzBonus+ep.BuzzBonus, 0, ep.BuzzCap)
	}

	// リザルト集計。
	st.served.count++
	st.served.accuracySum += accuracy
	st.served.elapsedSum += int64(elapsed)
	st.lastServeMs = s.elapsedMs

	// 満足：対応中の客を行列から除き、たべたべエリアへ戻す（次tickで tako-G の分配対象）。
	s.releaseToRest(r.CustomerId)

	// 評価更新を提供店へ配信（normalized/rank は tako-G が次tickで付与）。
	return append([]Outbound(nil), to(from, proto.EvaluationUpdate{
		EvalRaw:    s.evalScore(st),
		Normalized: st.evalNormalized,
		Rank:       st.rank,
		AliveCount: s.aliveCount,
	}))
}

// evalScore は正規化/順位付けに使う実効評価＝EMA基準＋JK一時加点。
func (s *Session) evalScore(st *storeState) float64 { return st.evalRaw + st.buzzBonus }

// clampF は v を [lo,hi] に収める。
func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Tick は時間を dt 進め、試合ループの各ステップを試合進行仕様 §4 の順序で呼ぶ。
// 各ステップは現状ほぼ no-op のフックで、実ロジックは担当 issue が埋める（tako-D〜H/I）。
// 出力は宛先つき []Outbound として集約して返し、room が Envelope 化して配信する。
// dt は room が渡す（session は時計を持たない）。大きな dt でも回る＝tako-L のシミュレータが反復呼びできる。
func (s *Session) Tick(dtMs int) []Outbound {
	if s.state != Running {
		return nil
	}
	s.elapsedMs += int64(dtMs)
	s.tick++

	var out []Outbound
	out = s.stepPhase(out)          // 1. フェーズ判定（Early/Mid/Late）      → tako-H
	out = s.stepDistribute(out)     // 2. 客分配（restPool→行列・CustomerArrived）→ tako-G(+D)
	out = s.stepPatience(dtMs, out) // 3. 我慢ゲージ減算 → 離脱（CustomerLeft/信用）→ tako-F
	out = s.stepEvaluate(out)       // 4. 評価再計算（perOrder の集計/EMA）      → tako-E
	out = s.stepNormalize(out)      // 5. 正規化 → rank（EvaluationUpdate）      → tako-G
	out = s.stepHeat(out)           // 6. 火力更新（DifficultyUpdate）           → tako-H
	out = s.stepStorm(out)          // 7. 下位淘汰の判定・予告（StoreEliminated/警告）→ tako-H
	out = s.checkFinish(out)        // 終了条件（生存1/時間切れ）。リザルト確定は tako-I
	return out
}

// ── 試合ループの各ステップ（tako-C は順序骨格のみ。中身は担当 issue が実装）──────────
// いずれも権威状態を更新し、必要な S2C を out へ append して返す（accumulator）。

// stepPhase は elapsedMs からフェーズ（Early/Mid/Late）を判定し、変化時に PhaseChange を配る。tako-H。
func (s *Session) stepPhase(out []Outbound) []Outbound { return out }

// stepDistribute は restPool の客と補充が要る店へ客を割り当て、CustomerArrived を配る。tako-G(+D)。
func (s *Session) stepDistribute(out []Outbound) []Outbound { return out }

// stepPatience は各店の行列先頭客の我慢ゲージを dt 減算し、0 で離脱（CustomerLeft＋信用減）させる。
// 属性(Normal/Bonus/Claimer/Buzz)で発火可否を分岐しない（#29 の詰まりガード）。tako-F。
func (s *Session) stepPatience(dtMs int, out []Outbound) []Outbound { _ = dtMs; return out }

// stepEvaluate は評価の時間減衰を進める（tako-E）。
// evalRaw 本体は提供イベント(ApplyOrderServed)で即時反映するため、ここでは JK 一時加点(buzzBonus)を
// 毎tick減衰させるだけ。微小値は 0 に丸めて残留を防ぐ。配信は次の提供/正規化(tako-G)に委ねる。
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

// stepNormalize は生存店内で evalRaw をパーセンタイル化(evalNormalized)＋rank 確定し、EvaluationUpdate を配る。tako-G。
func (s *Session) stepNormalize(out []Outbound) []Outbound { return out }

// stepHeat は全体火力(heatLevel)を更新し、変化時に DifficultyUpdate を配る。tako-H。
func (s *Session) stepHeat(out []Outbound) []Outbound { return out }

// stepStorm は下位淘汰(storm)の予告・確定を行い、ForcedEliminationWarning/StoreEliminated を配る。tako-H。
func (s *Session) stepStorm(out []Outbound) []Outbound { return out }

// checkFinish は終了条件を判定し、満たせば Finished へ遷移する。
// 生存1（BR勝者確定）と時間切れの2条件。順位確定・MatchEnd 配信は tako-I が担う。
//   - 生存1: len(order)>1 で始まった試合のみ（単独店の solo/dev セッションは即終了させない）。
//   - 時間切れ: MatchTimeLimitMs>0（0=無効＝solo/dev の idle 継続）。
func (s *Session) checkFinish(out []Outbound) []Outbound {
	limit := s.params.Session.MatchTimeLimitMs
	timeUp := limit > 0 && s.elapsedMs >= int64(limit)
	lastAlive := len(s.order) > 1 && s.aliveCount <= 1
	if timeUp || lastAlive {
		s.state = Finished
	}
	return out
}

// ── 客システム（tako-D）──────────────────────────────────────

// initCustomers は客プール（customerTotal 人）を生成し、属性・我慢・注文数を付与して
// たべたべエリア(restPool) に積む。Start 時に一度だけ呼ぶ。実際に「いつ・どの店へ」来店させるかは
// 分配(tako-G)。Claimer の中盤解禁などフェーズ制御は tako-G/H。
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

// attributeSpecs は属性仕様を固定順（Normal→Bonus→Claimer→Buzz）で返す。
// 重み抽選の決定性のため順序を固定する。
func (s *Session) attributeSpecs() []AttributeSpec {
	c := s.params.Customer
	return []AttributeSpec{c.Normal, c.Bonus, c.Claimer, c.Buzz}
}

// rollAttribute は出現率の重みに従って属性仕様を1つ引く（rng・順序で決定的）。
func (s *Session) rollAttribute() AttributeSpec {
	specs := s.attributeSpecs()
	total := 0
	for _, a := range specs {
		total += a.Weight
	}
	if total <= 0 {
		return specs[0] // 保険（重みが全て0の異常設定でもパニックさせない）
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

// admitCustomer は客1人を store へ来店させる単位処理（分配 tako-G が呼ぶ）。
// 行列へ割り当て、お題単語を orderCount 本発行し、その店への CustomerArrived を返す。
// 客が存在しない場合は ok=false。
func (s *Session) admitCustomer(cid proto.CustomerId, store PlayerId) (Outbound, bool) {
	c := s.customers[cid]
	if c == nil {
		return Outbound{}, false
	}
	s.assignCustomer(cid, store) // 行列末尾へ＋我慢ゲージ満タン
	words := make([]string, 0, c.orderCount)
	keystrokes := 0
	for i := 0; i < c.orderCount; i++ {
		w := s.words.Next(s.wordLevel(), s.rng)
		words = append(words, w.Text)
		keystrokes += w.KeystrokeCount
	}
	c.keystrokeTotal = keystrokes // 精度算出の分母（tako-E の提供処理が参照）
	view := proto.CustomerView{
		CustomerId:    cid,
		Attribute:     c.attribute,
		OrderCount:    c.orderCount,
		Words:         words,
		PatienceMaxMs: c.patienceMaxMs,
	}
	return to(store, view), true // CustomerArrived(=CustomerView) を来店店へ
}

// wordLevel はお題難度の実効レベル。火力(Heat)由来にするのは tako-H。当面は基準レベル 0。
func (s *Session) wordLevel() int { return 0 }

// ── 客の移動ヘルパ（実体を複製せず ID配列の増減のみ・一貫性バグ回避）──────────

// assignCustomer は客を（restPool から取り除き）store の行列末尾へ割り当てる。tako-D/G が使用。
func (s *Session) assignCustomer(cid proto.CustomerId, store PlayerId) {
	c := s.customers[cid]
	if c == nil {
		return
	}
	s.restPool = removeCustomer(s.restPool, cid)
	s.storeQueues[store] = append(s.storeQueues[store], cid)
	c.assignedStore = &store
	c.patienceLeftMs = c.patienceMaxMs // 来店で我慢ゲージ満タン
}

// releaseToRest は客を現在の割り当て先の行列から取り除き、たべたべエリアへ戻す。tako-F/H が使用。
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

// removeCustomer は id配列から cid を1件取り除く（順序保持）。
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

// publicParams はクライアント表示用の公開サブセット。
func (s *Session) publicParams() proto.GameParametersPublicSubset {
	return proto.GameParametersPublicSubset{
		MatchTimeLimitMs: s.params.Session.MatchTimeLimitMs,
		InitialLife:      s.params.Credit.InitialLife,
		MaxStores:        len(s.order),
	}
}
