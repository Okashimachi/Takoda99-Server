package game

import (
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
	assignedStore  *PlayerId // 割り当て先の店。nil=未割当（restPool）
}

// storeState は1店分の権威状態。リザルト統計(servedStats)は tako-E（提供処理）で追加する。
type storeState struct {
	id             PlayerId
	name           string
	creditLife     int     // 信用(HP)。客の離脱でのみ減少・0で自滅脱落（tako-F）
	evalRaw        float64 // 評価EMA（正規化前・tako-E で更新）
	evalNormalized float64 // 生存店内パーセンタイル 0..1（tako-G で更新）
	rank           int     // 生存店内の評価順位（tako-G）
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
// 客レジストリ・行列・たべたべエリアは空で初期化する（客の 300 初期化は tako-D）。
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
			Phase:       s.phase,
			Stores:      stores,
		}))
	}
	return out
}

// ApplyOrderServed は提供完了(OrderServed)を処理する（サニティ→評価EMA→行列除去）。tako-E で実装。
func (s *Session) ApplyOrderServed(from PlayerId, r proto.OrderServed) []Outbound {
	_ = from
	_ = r
	return nil
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

// stepEvaluate は tako-E で積まれた提供結果を集計し evalRaw(EMA) を更新する。tako-E。
func (s *Session) stepEvaluate(out []Outbound) []Outbound { return out }

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
