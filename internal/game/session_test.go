package game

import (
	"fmt"
	"math/rand"
	"testing"

	"textro99/internal/proto"
)

// fakeWords はテスト用の WordSource（game は depguard で odai を import できないため自前で置く）。
type fakeWords struct{}

func (fakeWords) Next(int, *rand.Rand) Word { return Word{Text: "たこ", KeystrokeCount: 4} }
func (fakeWords) NextTrap(*rand.Rand) Word {
	return Word{Text: "ながいおだい", KeystrokeCount: 12}
}

func newTestSession(n int) *Session {
	inits := make([]PlayerInit, n)
	for i := 0; i < n; i++ {
		id := PlayerId(fmt.Sprintf("s-%d", i+1))
		inits[i] = PlayerInit{Id: id, DisplayName: id}
	}
	return NewSession("m-1", DefaultParameters(), fakeWords{}, rand.New(rand.NewSource(1)), inits)
}

func TestNewSession_Init(t *testing.T) {
	life := DefaultParameters().Credit.InitialLife
	s := newTestSession(3)

	if s.State() != WaitingStart {
		t.Fatalf("初期状態は WaitingStart のはず: %v", s.State())
	}
	if s.aliveCount != 3 {
		t.Fatalf("aliveCount=3 のはず: %d", s.aliveCount)
	}
	if s.phase != proto.PhaseEarly {
		t.Fatalf("初期フェーズは Early のはず: %v", s.phase)
	}
	if len(s.customers) != 0 || len(s.restPool) != 0 {
		t.Fatalf("客レジストリ/たべたべエリアは空のはず: customers=%d rest=%d", len(s.customers), len(s.restPool))
	}
	for _, sid := range s.order {
		st := s.stores[sid]
		if st.creditLife != life {
			t.Fatalf("%s: creditLife=initialLife(%d) のはず: %d", sid, life, st.creditLife)
		}
		if st.evalRaw != 0 {
			t.Fatalf("%s: evalRaw=0 のはず: %v", sid, st.evalRaw)
		}
		if !st.alive {
			t.Fatalf("%s: alive=true のはず", sid)
		}
		if _, ok := s.storeQueues[sid]; !ok {
			t.Fatalf("%s: 行列エントリが無い", sid)
		}
	}
}

func TestStart_Transition(t *testing.T) {
	life := DefaultParameters().Credit.InitialLife
	s := newTestSession(3)

	out := s.Start()
	if s.State() != Running {
		t.Fatalf("Start 後は Running のはず: %v", s.State())
	}
	if len(out) != 3 {
		t.Fatalf("各店へ MatchStart（3件）のはず: %d", len(out))
	}
	for _, o := range out {
		ms, ok := o.Msg.(proto.MatchStart)
		if !ok {
			t.Fatalf("MatchStart でない: %T", o.Msg)
		}
		if ms.SelfStoreId != o.To.PlayerId {
			t.Fatalf("selfStoreId(%s) と宛先(%s) が不一致", ms.SelfStoreId, o.To.PlayerId)
		}
		if ms.Phase != proto.PhaseEarly {
			t.Fatalf("MatchStart.phase=Early のはず: %v", ms.Phase)
		}
		if ms.Params.InitialLife != life {
			t.Fatalf("公開paramsのinitialLife=%d のはず: %d", life, ms.Params.InitialLife)
		}
		if len(ms.Stores) != 3 {
			t.Fatalf("stores[] が3店のはず: %d", len(ms.Stores))
		}
	}
	if s.Start() != nil {
		t.Fatal("2回目の Start は nil のはず")
	}
}

func TestCustomerMove(t *testing.T) {
	s := newTestSession(2)
	store := s.order[0]
	cid := proto.CustomerId("c-1")
	s.customers[cid] = &customer{attribute: proto.AttrNormal, patienceMaxMs: 5000}
	s.restPool = append(s.restPool, cid)

	// rest → store
	s.assignCustomer(cid, store)
	if len(s.storeQueues[store]) != 1 || s.storeQueues[store][0] != cid {
		t.Fatalf("行列に入っていない: %v", s.storeQueues[store])
	}
	if len(s.restPool) != 0 {
		t.Fatalf("restPool から消えていない: %v", s.restPool)
	}
	c := s.customers[cid]
	if c.assignedStore == nil || *c.assignedStore != store {
		t.Fatal("assignedStore が設定されていない")
	}
	if c.patienceLeftMs != c.patienceMaxMs {
		t.Fatalf("来店で我慢ゲージ満タンのはず: left=%d max=%d", c.patienceLeftMs, c.patienceMaxMs)
	}

	// store → rest
	s.releaseToRest(cid)
	if len(s.storeQueues[store]) != 0 {
		t.Fatalf("行列から消えていない: %v", s.storeQueues[store])
	}
	if len(s.restPool) != 1 || s.restPool[0] != cid {
		t.Fatalf("restPool に戻っていない: %v", s.restPool)
	}
	if s.customers[cid].assignedStore != nil {
		t.Fatal("assignedStore がクリアされていない")
	}
}

// Tick は Running 以外では何もしない（時間も状態も動かさない）。
func TestTick_NotRunningNoOp(t *testing.T) {
	s := newTestSession(3) // WaitingStart のまま
	if out := s.Tick(150); out != nil {
		t.Fatalf("未開始の Tick は nil のはず: %v", out)
	}
	if s.tick != 0 || s.elapsedMs != 0 {
		t.Fatalf("未開始で時間が進んだ: tick=%d elapsed=%d", s.tick, s.elapsedMs)
	}
}

// 骨格ステップは no-op なので、Tick を繰り返しても落ちず・出力なしで時間だけ進む。
func TestTick_AdvancesNoOutput(t *testing.T) {
	s := newTestSession(3)
	s.Start()
	const n, dt = 5, 150
	for i := 0; i < n; i++ {
		if out := s.Tick(dt); out != nil {
			t.Fatalf("骨格 Tick は出力なしのはず(i=%d): %v", i, out)
		}
	}
	if s.tick != n {
		t.Fatalf("tick が %d 進むはず: %d", n, s.tick)
	}
	if s.elapsedMs != int64(n*dt) {
		t.Fatalf("elapsedMs=%d のはず: %d", n*dt, s.elapsedMs)
	}
	if s.State() != Running {
		t.Fatalf("まだ Running のはず: %v", s.State())
	}
}

// 大きな dt でも落ちない（tako-L のシミュレータが Tick(大dt) を反復呼びする前提）。
func TestTick_BigDt(t *testing.T) {
	s := newTestSession(3)
	s.params.Session.MatchTimeLimitMs = 0 // 時間切れ終了を無効化し dt 耐性だけ見る
	s.Start()
	s.Tick(60000)
	if s.elapsedMs != 60000 || s.tick != 1 {
		t.Fatalf("大dtで elapsed/tick が進むはず: elapsed=%d tick=%d", s.elapsedMs, s.tick)
	}
	if s.State() != Running {
		t.Fatalf("時間切れ無効なので Running のはず: %v", s.State())
	}
}

// 生存1で Finished（中身＝順位確定/MatchEnd は tako-I）。
func TestTick_FinishOnLastAlive(t *testing.T) {
	s := newTestSession(3)
	s.Start()
	s.aliveCount = 1 // storm(tako-H)で1店残った状況を模す
	s.Tick(150)
	if s.State() != Finished {
		t.Fatalf("生存1で Finished のはず: %v", s.State())
	}
}

// 時間切れで Finished。
func TestTick_FinishOnTimeLimit(t *testing.T) {
	s := newTestSession(3)
	s.params.Session.MatchTimeLimitMs = 1000
	s.Start()
	s.Tick(1000)
	if s.State() != Finished {
		t.Fatalf("時間切れで Finished のはず: %v", s.State())
	}
	if out := s.Tick(150); out != nil {
		t.Fatalf("Finished 後の Tick は nil のはず: %v", out)
	}
}

// solo/dev（単独店＋MatchTimeLimitMs=0）は終了せず idle し続ける（設計どおり）。
func TestTick_SoloIdles(t *testing.T) {
	s := newTestSession(1)
	s.params.Session.MatchTimeLimitMs = 0
	s.Start()
	for i := 0; i < 100; i++ {
		s.Tick(1000)
	}
	if s.State() != Running {
		t.Fatalf("solo は idle 継続（Running）のはず: %v", s.State())
	}
}

// Start で客プール（customerTotal 人）が生成され、たべたべエリアに積まれる。
func TestStart_InitsCustomers(t *testing.T) {
	total := DefaultParameters().Customer.Total
	s := newTestSession(3)
	if len(s.customers) != 0 || len(s.restPool) != 0 {
		t.Fatalf("Start 前は空のはず: customers=%d rest=%d", len(s.customers), len(s.restPool))
	}
	s.Start()
	if len(s.customers) != total || len(s.restPool) != total {
		t.Fatalf("客プールが %d 人のはず: customers=%d rest=%d", total, len(s.customers), len(s.restPool))
	}
	for cid, c := range s.customers {
		if c.orderCount <= 0 || c.patienceMaxMs <= 0 {
			t.Fatalf("%s: orderCount/patienceMaxMs が未設定: order=%d patience=%d", cid, c.orderCount, c.patienceMaxMs)
		}
		if c.assignedStore != nil {
			t.Fatalf("%s: 初期は未割当(restPool)のはず", cid)
		}
	}
}

// admitCustomer は来店処理：行列へ割り当て・お題本数=orderCount で CustomerArrived を返す。
func TestAdmitCustomer(t *testing.T) {
	s := newTestSession(2)
	s.Start()
	store := s.order[0]
	cid := s.restPool[0]
	want := s.customers[cid]

	out, ok := s.admitCustomer(cid, store)
	if !ok {
		t.Fatal("admitCustomer が失敗した")
	}
	if out.To.PlayerId != store {
		t.Fatalf("宛先が来店店でない: %s != %s", out.To.PlayerId, store)
	}
	view, ok := out.Msg.(proto.CustomerArrived)
	if !ok {
		t.Fatalf("CustomerArrived でない: %T", out.Msg)
	}
	if view.CustomerId != cid || view.Attribute != want.attribute {
		t.Fatalf("view の client/attr 不一致: %+v", view)
	}
	if view.OrderCount != want.orderCount || len(view.Words) != want.orderCount {
		t.Fatalf("words 本数=orderCount(%d) のはず: orderCount=%d words=%d", want.orderCount, view.OrderCount, len(view.Words))
	}
	if view.PatienceMaxMs != want.patienceMaxMs {
		t.Fatalf("patienceMaxMs 不一致: %d != %d", view.PatienceMaxMs, want.patienceMaxMs)
	}
	// 行列へ入り、restPool から抜けている。
	q := s.storeQueues[store]
	if len(q) != 1 || q[0] != cid {
		t.Fatalf("行列に入っていない: %v", q)
	}
	for _, r := range s.restPool {
		if r == cid {
			t.Fatal("restPool から抜けていない")
		}
	}
}

// 属性分布が出現率（重み）に概ね一致する（シード固定・決定的）。
func TestAttributeDistribution(t *testing.T) {
	s := newTestSession(3)
	s.Start()
	total := len(s.customers)
	counts := map[proto.CustomerAttribute]int{}
	for _, c := range s.customers {
		counts[c.attribute]++
	}
	// 全属性が出現し、各割合が期待重み比の ±10ポイント以内。
	specs := s.attributeSpecs()
	weightSum := 0
	for _, a := range specs {
		weightSum += a.Weight
	}
	for _, a := range specs {
		got := counts[a.Attribute]
		if got == 0 {
			t.Fatalf("属性 %s が1人も出現していない", a.Attribute)
		}
		wantFrac := float64(a.Weight) / float64(weightSum)
		gotFrac := float64(got) / float64(total)
		if diff := gotFrac - wantFrac; diff < -0.1 || diff > 0.1 {
			t.Fatalf("属性 %s の割合が乖離: want~%.2f got %.2f", a.Attribute, wantFrac, gotFrac)
		}
	}
}

// placeAssigned はテスト用に、既知の属性/打鍵数の客を store に割当済みで置く。
func placeAssigned(s *Session, cid proto.CustomerId, store PlayerId, attr proto.CustomerAttribute, orderCount, keystrokes int) {
	s.customers[cid] = &customer{
		attribute:      attr,
		patienceMaxMs:  5000,
		orderCount:     orderCount,
		keystrokeTotal: keystrokes,
	}
	s.restPool = append(s.restPool, cid)
	s.assignCustomer(cid, store)
}

// 提供完了で evalRaw が上がり、対象客が行列から消える（たべたべエリアへ戻る）。
func TestApplyOrderServed_RaisesEvalAndSatisfies(t *testing.T) {
	s := newTestSession(2)
	s.Start()
	store := s.order[0]
	cid := proto.CustomerId("cust-A")
	placeAssigned(s, cid, store, proto.AttrNormal, 2, 10)

	out := s.ApplyOrderServed(store, proto.OrderServed{CustomerId: cid, ElapsedMs: 4000, MissCount: 0})

	st := s.stores[store]
	if st.evalRaw <= 0 {
		t.Fatalf("evalRaw が上がっていない: %v", st.evalRaw)
	}
	if st.served.count != 1 {
		t.Fatalf("servedStats.count=1 のはず: %d", st.served.count)
	}
	// 行列から消え、restPool に戻っている。
	for _, q := range s.storeQueues[store] {
		if q == cid {
			t.Fatal("満足客が行列に残っている")
		}
	}
	if s.customers[cid].assignedStore != nil {
		t.Fatal("assignedStore がクリアされていない")
	}
	// EvaluationUpdate が提供店へ返る。
	if len(out) != 1 {
		t.Fatalf("EvaluationUpdate 1件のはず: %d", len(out))
	}
	ev, ok := out[0].Msg.(proto.EvaluationUpdate)
	if !ok || out[0].To.PlayerId != store {
		t.Fatalf("提供店への EvaluationUpdate でない: %T to=%s", out[0].Msg, out[0].To.PlayerId)
	}
	if ev.EvalRaw <= 0 || ev.AliveCount != 2 {
		t.Fatalf("EvaluationUpdate の値が不正: %+v", ev)
	}
}

// 割当外の客の提供は棄却（nil・状態不変）。
func TestApplyOrderServed_RejectsUnassigned(t *testing.T) {
	s := newTestSession(2)
	s.Start()
	store := s.order[0]
	before := s.stores[store].evalRaw

	// そもそも存在しない客。
	if out := s.ApplyOrderServed(store, proto.OrderServed{CustomerId: "ghost"}); out != nil {
		t.Fatalf("未割当客は棄却されるはず: %v", out)
	}
	// 別店に割り当てられた客を、無関係な店から提供報告。
	cid := proto.CustomerId("cust-B")
	placeAssigned(s, cid, s.order[1], proto.AttrNormal, 1, 5)
	if out := s.ApplyOrderServed(store, proto.OrderServed{CustomerId: cid, ElapsedMs: 3000}); out != nil {
		t.Fatalf("別店の客の提供は棄却されるはず: %v", out)
	}
	if s.stores[store].evalRaw != before {
		t.Fatalf("棄却時に評価が動いた: %v → %v", before, s.stores[store].evalRaw)
	}
}

// elapsedMs 下限クランプ：超人的な速さ(1ms)は floor と同じ扱いになる。
func TestApplyOrderServed_ClampsElapsedFloor(t *testing.T) {
	mk := func(elapsed int) float64 {
		s := newTestSession(2)
		s.Start()
		store := s.order[0]
		cid := proto.CustomerId("c")
		placeAssigned(s, cid, store, proto.AttrNormal, 2, 10)
		s.ApplyOrderServed(store, proto.OrderServed{CustomerId: cid, ElapsedMs: elapsed, MissCount: 0})
		return s.stores[store].evalRaw
	}
	floor := DefaultParameters().Eval.MinMsPerWord * 2 // orderCount=2
	if got, want := mk(1), mk(floor); got != want {
		t.Fatalf("1ms は floor(%dms) にクランプされ同値のはず: got=%v want=%v", floor, got, want)
	}
}

// 提供間隔が短すぎる連投は棄却（2件目以降）。
func TestApplyOrderServed_RejectsTooFrequent(t *testing.T) {
	s := newTestSession(2)
	s.Start()
	store := s.order[0]
	a := proto.CustomerId("a")
	b := proto.CustomerId("b")
	placeAssigned(s, a, store, proto.AttrNormal, 1, 5)
	placeAssigned(s, b, store, proto.AttrNormal, 1, 5)

	// elapsedMs=0（tick未経過）。1件目は受理、直後の2件目は間隔0で棄却。
	if out := s.ApplyOrderServed(store, proto.OrderServed{CustomerId: a, ElapsedMs: 3000}); out == nil {
		t.Fatal("1件目は受理されるはず")
	}
	if out := s.ApplyOrderServed(store, proto.OrderServed{CustomerId: b, ElapsedMs: 3000}); out != nil {
		t.Fatalf("間隔ゼロの2件目は棄却されるはず: %v", out)
	}
	if s.customers[b].assignedStore == nil {
		t.Fatal("棄却された客 b は割当のまま残るはず")
	}
}

// JK(Buzz)加点は毎tick減衰する。
func TestBuzzBonusDecays(t *testing.T) {
	s := newTestSession(2)
	s.Start()
	store := s.order[0]
	cid := proto.CustomerId("jk")
	placeAssigned(s, cid, store, proto.AttrBuzz, 1, 5)
	s.ApplyOrderServed(store, proto.OrderServed{CustomerId: cid, ElapsedMs: 3000, MissCount: 0})

	st := s.stores[store]
	if st.buzzBonus <= 0 {
		t.Fatalf("Buzz 提供で一時加点が付くはず: %v", st.buzzBonus)
	}
	first := st.buzzBonus
	s.Tick(150)
	if st.buzzBonus >= first {
		t.Fatalf("stepEvaluate で減衰するはず: %v → %v", first, st.buzzBonus)
	}
	for i := 0; i < 1000; i++ {
		s.Tick(150)
	}
	if st.buzzBonus != 0 {
		t.Fatalf("十分tick後は 0 に丸まるはず: %v", st.buzzBonus)
	}
}

// MatchStart の公開params に matchTimeLimitMs が乗る（案A の配線確認）。
func TestPublicParams_CarriesMatchTimeLimit(t *testing.T) {
	want := DefaultParameters().Session.MatchTimeLimitMs
	s := newTestSession(3)
	out := s.Start()
	ms, ok := out[0].Msg.(proto.MatchStart)
	if !ok {
		t.Fatalf("MatchStart でない: %T", out[0].Msg)
	}
	if ms.Params.MatchTimeLimitMs != want {
		t.Fatalf("公開paramsの matchTimeLimitMs=%d のはず: %d", want, ms.Params.MatchTimeLimitMs)
	}
}
