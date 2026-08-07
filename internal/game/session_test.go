package game

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"takoda99/internal/proto"
)

// ── テストユーティリティ ──────────────────────────────────────

type fakeWords struct{}

func (fakeWords) Next(_ int, _ *rand.Rand) Word {
	return Word{Text: "たこ", KeystrokeCount: 4}
}

func newTestSession(n int) *Session {
	return newTestSessionWith(DefaultParameters(), n)
}

func newTestSessionWith(params GameParameters, n int) *Session {
	params.Matching.ReadyCountdownMs = 0
	params.Matching.RosterWaitMs = 0
	inits := make([]PlayerInit, n)
	for i := range inits {
		id := PlayerId(fmt.Sprintf("s-%d", i+1))
		inits[i] = PlayerInit{Id: id, DisplayName: string(id)}
	}
	s := NewSession("test-match", params, fakeWords{}, rand.New(rand.NewSource(42)), inits)
	s.customerSeq = 1000000
	return s
}

func placeAssigned(s *Session, cid proto.CustomerId, store PlayerId, attr proto.CustomerAttribute, orderCount, keystrokes int) {
	specs := s.attributeSpecs()
	var pMax int
	for _, sp := range specs {
		if sp.Attribute == attr {
			pMax = sp.PatienceBaseMs
			break
		}
	}
	if pMax == 0 {
		pMax = 8000
	}
	c := &customer{
		attribute:      attr,
		patienceMaxMs:  pMax,
		patienceLeftMs: pMax,
		orderCount:     orderCount,
		keystrokeTotal: keystrokes,
		assignedStore:  &store,
	}
	s.customers[cid] = c
	s.storeQueues[store] = append(s.storeQueues[store], cid)
}

// ── テストケース ──────────────────────────────────────────────

func TestSession_CountdownDelay(t *testing.T) {
	params := DefaultParameters()
	params.Matching.ReadyCountdownMs = 5000 // 5秒
	// newTestSessionWith は ReadyCountdownMs を 0 に上書きしてしまうので直接生成する
	inits := make([]PlayerInit, 1)
	inits[0] = PlayerInit{Id: "test-player"}
	s := NewSession("test", params, fakeWords{}, rand.New(rand.NewSource(42)), inits)

	s.Start(0)
	if s.elapsedMs != -5000 {
		t.Fatalf("Start時のelapsedMsが -5000 になっていない: %d", s.elapsedMs)
	}

	out := s.Tick(2000)
	if s.elapsedMs != -3000 {
		t.Fatalf("2秒後のelapsedMsが -3000 になっていない: %d", s.elapsedMs)
	}
	if len(out) != 0 {
		t.Fatalf("カウントダウン中に出力が出てはいけない")
	}

	_ = s.Tick(3000)
	if s.elapsedMs != 0 {
		t.Fatalf("5秒後のelapsedMsが 0 になっていない: %d", s.elapsedMs)
	}

	// 0以降でティックが進めばイベントが出る
	_ = s.Tick(100)
	if s.elapsedMs != 100 {
		t.Fatalf("5.1秒後のelapsedMsが 100 になっていない: %d", s.elapsedMs)
	}
}

func TestStepPatience_BasicLeave(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	store := s.order[0]
	cid := proto.CustomerId("patience-1")
	placeAssigned(s, cid, store, proto.AttrNormal, 1, 5)

	patience := s.customers[cid].patienceMaxMs
	dt := 150

	ticks := (patience / dt) - 1
	for i := 0; i < ticks; i++ {
		out := s.stepPatience(dt, nil)
		if len(out) != 0 {
			t.Fatalf("tick %d: ゲージ残ありで出力があった: %v", i, out)
		}
	}

	remaining := s.customers[cid].patienceLeftMs
	if remaining <= 0 {
		t.Fatalf("まだ離脱していないはず: remaining=%d", remaining)
	}

	out := s.stepPatience(remaining+1, nil)
	if len(out) != 2 {
		t.Fatalf("CustomerLeft + CreditUpdate の2件のはず: %d件", len(out))
	}

	cl, ok := out[0].Msg.(proto.CustomerLeft)
	if !ok {
		t.Fatalf("1件目が CustomerLeft でない: %T", out[0].Msg)
	}
	if cl.CustomerId != cid || cl.Reason != proto.LeaveTimeout {
		t.Fatalf("CustomerLeft の内容が不正: %+v", cl)
	}
	if out[0].To.PlayerId != store {
		t.Fatalf("宛先が該当店でない: %s", out[0].To.PlayerId)
	}

	cu, ok := out[1].Msg.(proto.CreditUpdate)
	if !ok {
		t.Fatalf("2件目が CreditUpdate でない: %T", out[1].Msg)
	}
	if cu.Delta != -1 || cu.Reason != proto.CreditCustomerLeft {
		t.Fatalf("CreditUpdate の内容が不正: %+v", cu)
	}
	expectedLife := DefaultParameters().Credit.InitialLife - 1
	if cu.Life != expectedLife {
		t.Fatalf("Life=%d のはず: %d", expectedLife, cu.Life)
	}

	if c, ok := s.customers[cid]; ok && c.assignedStore != nil {
		t.Fatal("離脱した客の assignedStore がクリアされていない")
	}
}

func TestStepPatience_SelfCollapse(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.params.Credit.InitialLife = 1
	s.params.Credit.LeaveLoss = LeaveLoss{Normal: 1, Bonus: 1, Claimer: 1, Buzz: 2}
	store := s.order[0]
	s.stores[store].creditLife = 1

	cid := proto.CustomerId("collapse-1")
	placeAssigned(s, cid, store, proto.AttrNormal, 1, 5)

	patience := s.customers[cid].patienceMaxMs
	out := s.stepPatience(patience+1, nil)

	// CustomerLeft(1) + CreditUpdate(1) + StoreEliminated(broadcast=1) + PersonalResult(1) = 4件
	if len(out) != 4 {
		t.Fatalf("出力4件のはず: %d件", len(out))
	}

	se, ok := out[2].Msg.(proto.StoreEliminated)
	if !ok {
		t.Fatalf("3件目が StoreEliminated でない: %T", out[2].Msg)
	}
	if se.StoreId != store {
		t.Fatalf("StoreEliminated の storeId が不正: %s", se.StoreId)
	}
	if se.Reason != proto.ElimSelfCollapse {
		t.Fatalf("Reason が SelfCollapse でない: %s", se.Reason)
	}
	if se.FinalRank != 3 {
		t.Fatalf("3店中の脱落 → FinalRank=3 のはず: %d", se.FinalRank)
	}
	if !out[2].To.Broadcast {
		t.Fatal("StoreEliminated は Broadcast のはず")
	}

	if s.stores[store].alive {
		t.Fatal("脱落した店が alive のまま")
	}
	if s.aliveCount != 2 {
		t.Fatalf("aliveCount=2 のはず: %d", s.aliveCount)
	}
	if len(s.storeQueues[store]) != 0 {
		t.Fatalf("脱落店の行列が空でない: %v", s.storeQueues[store])
	}
}

func TestStepPatience_AttributeLeaveLoss(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.params.Credit.LeaveLoss = LeaveLoss{Normal: 1, Bonus: 1, Claimer: 1, Buzz: 2}
	store := s.order[0]
	initLife := s.stores[store].creditLife

	cid := proto.CustomerId("buzz-leave")
	placeAssigned(s, cid, store, proto.AttrBuzz, 4, 20)

	patience := s.customers[cid].patienceMaxMs
	out := s.stepPatience(patience+1, nil)

	var found bool
	for _, o := range out {
		if cu, ok := o.Msg.(proto.CreditUpdate); ok {
			found = true
			if cu.Delta != -2 {
				t.Fatalf("Buzz の離脱ペナルティは -2 のはず: %d", cu.Delta)
			}
			if cu.Life != initLife-2 {
				t.Fatalf("Life=%d のはず: %d", initLife-2, cu.Life)
			}
		}
	}
	if !found {
		t.Fatal("CreditUpdate が見つからない")
	}
}

// 行列に並んでいる客は、対応中(先頭)でなくても我慢が減る。
//
// これが「忙しさ」の表現で、行列を溜めること自体のコストになる。
// 先頭だけ減らすと行列がいくら伸びてもペナルティが無く、
// 客分配の重み ÷(行列長+1) も意味を失う。
func TestStepPatience_AllQueuedDrain(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	store := s.order[0]
	front := proto.CustomerId("front")
	behind := proto.CustomerId("behind")
	placeAssigned(s, front, store, proto.AttrNormal, 1, 5)
	placeAssigned(s, behind, store, proto.AttrNormal, 1, 5)

	frontBefore := s.customers[front].patienceLeftMs
	behindBefore := s.customers[behind].patienceLeftMs

	s.stepPatience(150, nil)

	if s.customers[front].patienceLeftMs != frontBefore-150 {
		t.Fatalf("先頭のゲージが減っていない: before=%d after=%d", frontBefore, s.customers[front].patienceLeftMs)
	}
	if s.customers[behind].patienceLeftMs != behindBefore-150 {
		t.Fatalf("待機中のゲージが減っていない: before=%d after=%d", behindBefore, s.customers[behind].patienceLeftMs)
	}
}

// 同一tickで複数の客が同時に我慢切れしても、全員ぶん処理される。
// 走査中に行列を書き換えると要素を飛ばすため、収集してから処理している。
func TestStepPatience_MultipleLeavesInOneTick(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.params.Credit.InitialLife = 10 // 脱落させずに離脱だけ見る
	store := s.order[0]
	s.stores[store].creditLife = 10

	for i := 0; i < 3; i++ {
		placeAssigned(s, proto.CustomerId(fmt.Sprintf("c-%d", i)), store, proto.AttrNormal, 1, 5)
	}

	out := s.stepPatience(99999, nil)

	leaves := 0
	for _, o := range out {
		if _, ok := o.Msg.(proto.CustomerLeft); ok {
			leaves++
		}
	}
	if leaves != 3 {
		t.Fatalf("3人とも離脱するはず: %d件", leaves)
	}
	if len(s.storeQueues[store]) != 0 {
		t.Fatalf("行列が空になるはず: %v", s.storeQueues[store])
	}
}

func TestStepPatience_ServePreventLeave(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	store := s.order[0]

	first := proto.CustomerId("first")
	second := proto.CustomerId("second")
	placeAssigned(s, first, store, proto.AttrNormal, 1, 5)
	placeAssigned(s, second, store, proto.AttrNormal, 1, 5)

	half := s.customers[first].patienceMaxMs / 2
	s.stepPatience(half, nil)

	s.ApplyOrderServed(store, proto.OrderServed{CustomerId: first, ElapsedMs: 3000, MissCount: 0})

	q := s.storeQueues[store]
	if len(q) != 1 || q[0] != second {
		t.Fatalf("second が先頭に昇格するはず: %v", q)
	}
	// second は待機中も我慢が減っているので満タンではない（新仕様）。
	wantLeft := s.customers[second].patienceMaxMs - half
	if s.customers[second].patienceLeftMs != wantLeft {
		t.Fatalf("second のゲージは待機中も減るはず: left=%d want=%d",
			s.customers[second].patienceLeftMs, wantLeft)
	}

	out := s.stepPatience(150, nil)
	if len(out) != 0 {
		t.Fatalf("ゲージ満タンで離脱するはずがない: %v", out)
	}
}

func TestStepPatience_AllAttributes(t *testing.T) {
	attrs := []struct {
		attr  proto.CustomerAttribute
		order int
		keys  int
	}{
		{proto.AttrNormal, 2, 10},
		{proto.AttrBonus, 2, 10},
		{proto.AttrClaimer, 1, 5},
		{proto.AttrBuzz, 4, 20},
	}

	for _, tc := range attrs {
		t.Run(string(tc.attr), func(t *testing.T) {
			s := newTestSession(2)
			s.state = Running
			s.params.Credit.LeaveLoss = LeaveLoss{Normal: 1, Bonus: 1, Claimer: 1, Buzz: 2}
			store := s.order[0]
			cid := proto.CustomerId("attr-" + string(tc.attr))
			placeAssigned(s, cid, store, tc.attr, tc.order, tc.keys)

			patience := s.customers[cid].patienceMaxMs
			out := s.stepPatience(patience+1, nil)

			var foundLeave bool
			for _, o := range out {
				if cl, ok := o.Msg.(proto.CustomerLeft); ok {
					foundLeave = true
					if cl.CustomerId != cid {
						t.Fatalf("CustomerId 不一致: %s", cl.CustomerId)
					}
				}
			}
			if !foundLeave {
				t.Fatalf("属性 %s で CustomerLeft が発火しなかった", tc.attr)
			}
		})
	}
}

func TestStepPatience_LatePhaseFaster(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.params.Patience.LateMul = 0.5
	store := s.order[0]

	cid := proto.CustomerId("late-1")
	placeAssigned(s, cid, store, proto.AttrNormal, 1, 5)

	dt := 150

	s.phase = proto.PhaseEarly
	s.stepPatience(dt, nil)
	afterEarly := s.customers[cid].patienceLeftMs
	earlyDelta := s.customers[cid].patienceMaxMs - afterEarly
	if earlyDelta != dt {
		t.Fatalf("Early の減算量=%d のはず: %d", dt, earlyDelta)
	}

	s.customers[cid].patienceLeftMs = s.customers[cid].patienceMaxMs

	s.phase = proto.PhaseLate
	s.stepPatience(dt, nil)
	afterLate := s.customers[cid].patienceLeftMs
	lateDelta := s.customers[cid].patienceMaxMs - afterLate
	if lateDelta != int(float64(dt)/0.5) {
		t.Fatalf("Late の減算量=%d のはず: %d", int(float64(dt)/0.5), lateDelta)
	}

	if lateDelta <= earlyDelta {
		t.Fatalf("Late の方が速く減るはず: early=%d late=%d", earlyDelta, lateDelta)
	}
}

func TestStepPatience_DeadStoreSkipped(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	dead := s.order[0]
	alive := s.order[1]

	cidDead := proto.CustomerId("dead-c")
	cidAlive := proto.CustomerId("alive-c")
	placeAssigned(s, cidDead, dead, proto.AttrNormal, 1, 5)
	placeAssigned(s, cidAlive, alive, proto.AttrNormal, 1, 5)

	s.stores[dead].alive = false

	beforeDead := s.customers[cidDead].patienceLeftMs
	s.stepPatience(150, nil)

	if s.customers[cidDead].patienceLeftMs != beforeDead {
		t.Fatalf("脱落店の客のゲージが動いた: before=%d after=%d", beforeDead, s.customers[cidDead].patienceLeftMs)
	}
	if s.customers[cidAlive].patienceLeftMs >= s.customers[cidAlive].patienceMaxMs {
		t.Fatal("生存店の客のゲージが減っていない")
	}
}

func TestBroadcastMsg(t *testing.T) {
	msg := proto.StoreEliminated{StoreId: "test", Reason: proto.ElimSelfCollapse, FinalRank: 4}
	o := broadcastMsg(msg)
	if !o.To.Broadcast {
		t.Fatal("Broadcast=true のはず")
	}
	if o.To.PlayerId != "" {
		t.Fatalf("Broadcast 時 PlayerId は空のはず: %q", o.To.PlayerId)
	}
	if _, ok := o.Msg.(proto.StoreEliminated); !ok {
		t.Fatalf("StoreEliminated でない: %T", o.Msg)
	}
}

func TestLeaveLoss_For(t *testing.T) {
	ll := LeaveLoss{Normal: 1, Bonus: 2, Claimer: 3, Buzz: 4}
	cases := []struct {
		attr proto.CustomerAttribute
		want int
	}{
		{proto.AttrNormal, 1},
		{proto.AttrBonus, 2},
		{proto.AttrClaimer, 3},
		{proto.AttrBuzz, 4},
	}
	for _, tc := range cases {
		got := ll.For(tc.attr)
		if got != tc.want {
			t.Fatalf("For(%s)=%d のはず: %d", tc.attr, tc.want, got)
		}
	}
}

// ── Plan-03: stepDistribute / stepNormalize テスト ─────────────

func TestStepDistribute_EvenInitial(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 5, WeightFloor: 0.25}

	for i := 0; i < 9; i++ {
		cid := proto.CustomerId(fmt.Sprintf("d-%d", i))
		s.customers[cid] = &customer{
			attribute:     proto.AttrNormal,
			patienceMaxMs: 5000,
			orderCount:    1,
		}
		s.restPool = append(s.restPool, cid)
	}

	for _, sid := range s.order {
		s.stores[sid].evalNormalized = 0
	}

	out := s.stepDistribute(nil)
	if len(out) != 9 {
		t.Fatalf("9件の CustomerArrived のはず: %d", len(out))
	}

	for _, sid := range s.order {
		ql := len(s.storeQueues[sid])
		if ql == 0 {
			t.Fatalf("店 %s に1人も分配されていない", sid)
		}
	}

	if len(s.restPool) != 0 {
		t.Fatalf("restPool が空のはず: %d", len(s.restPool))
	}
}

func TestStepDistribute_WeightedByEval(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 100, WeightFloor: 0.25}

	storeA := s.order[0]
	storeB := s.order[1]
	s.stores[storeA].evalNormalized = 1.0
	s.stores[storeB].evalNormalized = 0.01

	for i := 0; i < 100; i++ {
		cid := proto.CustomerId(fmt.Sprintf("w-%d", i))
		s.customers[cid] = &customer{
			attribute:     proto.AttrNormal,
			patienceMaxMs: 5000,
			orderCount:    1,
		}
		s.restPool = append(s.restPool, cid)
	}

	s.stepDistribute(nil)

	qA := len(s.storeQueues[storeA])
	qB := len(s.storeQueues[storeB])

	if qA <= qB {
		t.Fatalf("eval の高い店A(%d) が店B(%d) より多く来客するはず", qA, qB)
	}
}

func TestStepDistribute_QueueLengthSuppression(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 10, WeightFloor: 0.25}

	storeA := s.order[0]
	storeB := s.order[1]
	s.stores[storeA].evalNormalized = 0.5
	s.stores[storeB].evalNormalized = 0.5

	for i := 0; i < 5; i++ {
		cid := proto.CustomerId(fmt.Sprintf("pre-%d", i))
		s.customers[cid] = &customer{
			attribute:     proto.AttrNormal,
			patienceMaxMs: 5000,
			orderCount:    1,
		}
		s.restPool = append(s.restPool, cid)
		s.assignCustomer(cid, storeA)
	}

	for i := 0; i < 20; i++ {
		cid := proto.CustomerId(fmt.Sprintf("q-%d", i))
		s.customers[cid] = &customer{
			attribute:     proto.AttrNormal,
			patienceMaxMs: 5000,
			orderCount:    1,
		}
		s.restPool = append(s.restPool, cid)
	}

	s.stepDistribute(nil)

	qA := len(s.storeQueues[storeA])
	qB := len(s.storeQueues[storeB])

	newA := qA - 5
	if newA >= qB {
		t.Fatalf("行列が短い店B(%d) のほうが多く分配されるはず (店Aの新規=%d)", qB, newA)
	}
}

func TestStepDistribute_ClaimerBlockedInEarly(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.phase = proto.PhaseEarly
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 5, WeightFloor: 0.25}

	for i := 0; i < 5; i++ {
		cid := proto.CustomerId(fmt.Sprintf("cl-%d", i))
		s.customers[cid] = &customer{
			attribute:     proto.AttrClaimer,
			patienceMaxMs: 5000,
			orderCount:    1,
		}
		s.restPool = append(s.restPool, cid)
	}

	out := s.stepDistribute(nil)

	if len(out) != 0 {
		t.Fatalf("Early で Claimer は分配されないはず: %d 件出力", len(out))
	}
	if len(s.restPool) != 5 {
		t.Fatalf("restPool に5人残るはず: %d", len(s.restPool))
	}

	s.phase = proto.PhaseMid
	out = s.stepDistribute(nil)
	if len(out) != 5 {
		t.Fatalf("Mid では Claimer が分配されるはず: %d 件出力", len(out))
	}
}

func TestStepDistribute_EmptyRestPool(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 3, WeightFloor: 0.25}

	out := s.stepDistribute(nil)
	if out != nil {
		t.Fatalf("空 restPool で出力なしのはず: %v", out)
	}
}

func TestStepNormalize_ThreeStores(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.aliveCount = 3

	s.stores[s.order[0]].evalRaw = 1.0
	s.stores[s.order[1]].evalRaw = 2.0
	s.stores[s.order[2]].evalRaw = 3.0

	out := s.stepNormalize(nil)

	if len(out) != 3 {
		t.Fatalf("3件の EvaluationUpdate のはず: %d", len(out))
	}

	st0 := s.stores[s.order[0]]
	st1 := s.stores[s.order[1]]
	st2 := s.stores[s.order[2]]

	if st0.evalNormalized != 0.0 {
		t.Fatalf("最下位の normalized=0.0 のはず: %v", st0.evalNormalized)
	}
	if st1.evalNormalized != 0.5 {
		t.Fatalf("中間の normalized=0.5 のはず: %v", st1.evalNormalized)
	}
	if st2.evalNormalized != 1.0 {
		t.Fatalf("最上位の normalized=1.0 のはず: %v", st2.evalNormalized)
	}

	if st0.rank != 3 {
		t.Fatalf("最下位の rank=3 のはず: %d", st0.rank)
	}
	if st1.rank != 2 {
		t.Fatalf("中間の rank=2 のはず: %d", st1.rank)
	}
	if st2.rank != 1 {
		t.Fatalf("最上位の rank=1 のはず: %d", st2.rank)
	}

	for _, o := range out {
		ev, ok := o.Msg.(proto.EvaluationUpdate)
		if !ok {
			t.Fatalf("EvaluationUpdate でない: %T", o.Msg)
		}
		if ev.AliveCount != 3 {
			t.Fatalf("AliveCount=3 のはず: %d", ev.AliveCount)
		}
	}
}

func TestStepNormalize_SingleStore(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.stores[s.order[0]].alive = true
	s.stores[s.order[0]].evalRaw = 5.0
	s.stores[s.order[1]].alive = false
	s.stores[s.order[2]].alive = false
	s.aliveCount = 1

	out := s.stepNormalize(nil)
	if len(out) != 1 {
		t.Fatalf("1件の EvaluationUpdate のはず: %d", len(out))
	}

	st := s.stores[s.order[0]]
	if st.evalNormalized != 1.0 {
		t.Fatalf("単独店の normalized=1.0 のはず: %v", st.evalNormalized)
	}
	if st.rank != 1 {
		t.Fatalf("単独店の rank=1 のはず: %d", st.rank)
	}
}

func TestStepDistribute_BottomStoreStillGetsCustomers(t *testing.T) {
	s := newTestSession(3)
	s.Start(0)
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 5, WeightFloor: 0.25}

	bottom := s.order[0]
	s.stores[bottom].evalNormalized = 0.0
	s.stores[s.order[1]].evalNormalized = 0.5
	s.stores[s.order[2]].evalNormalized = 1.0

	got := 0
	for i := 0; i < 200 && got == 0; i++ {
		s.stepDistribute(nil)
		got = len(s.storeQueues[bottom])
	}
	if got == 0 {
		t.Fatal("WeightFloor があるので最下位店にも客が来るはず（死のスパイラル回帰）")
	}
}

func TestStepDistribute_ZeroFloorReproducesSpec(t *testing.T) {
	s := newTestSession(3)
	s.Start(0)
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 5, WeightFloor: 0}

	bottom := s.order[0]
	s.stores[bottom].evalNormalized = 0.0
	s.stores[s.order[1]].evalNormalized = 0.5
	s.stores[s.order[2]].evalNormalized = 1.0

	for i := 0; i < 100; i++ {
		s.stepDistribute(nil)
	}
	if len(s.storeQueues[bottom]) != 0 {
		t.Fatal("WeightFloor=0 なら最下位店の重みは0で客は来ないはず")
	}
}

// ── Plan-04: stepPhase / stepHeat / stepStorm テスト ─────────

func filterMsg[T any](out []Outbound) []T {
	var result []T
	for _, o := range out {
		if msg, ok := o.Msg.(T); ok {
			result = append(result, msg)
		}
	}
	return result
}

func TestStepPhase_AliveThreshold(t *testing.T) {
	s := newTestSession(99)
	s.Start(0)
	s.params.Storm.IntervalTicks = 0

	if s.phase != proto.PhaseEarly {
		t.Fatalf("初期は Early のはず: %v", s.phase)
	}

	s.aliveCount = s.params.Phase.MidAliveThreshold
	out := s.Tick(150)
	if s.phase != proto.PhaseMid {
		t.Fatalf("aliveCount=%d で Mid に移行するはず: %v", s.params.Phase.MidAliveThreshold, s.phase)
	}
	phaseChanges := filterMsg[proto.PhaseChange](out)
	if len(phaseChanges) == 0 {
		t.Fatal("PhaseChange が配信されるはず")
	}
	if phaseChanges[0].Phase != proto.PhaseMid {
		t.Fatalf("PhaseChange.Phase=Mid のはず: %v", phaseChanges[0].Phase)
	}

	s.aliveCount = s.params.Phase.LateAliveThreshold
	_ = s.Tick(150)
	if s.phase != proto.PhaseLate {
		t.Fatalf("aliveCount=%d で Late に移行するはず: %v", s.params.Phase.LateAliveThreshold, s.phase)
	}
}

func TestStepPhase_TimeThreshold(t *testing.T) {
	s := newTestSession(99)
	// このテストは「経過時間でフェーズが移る」ことだけを見る。
	// 実 tick の数百倍の dt を1回で流すので、我慢を十分長くしておかないと
	// 行列の客が一斉に離脱して店が全滅し、試合が終了して Tick が素通りする。
	huge := 1 << 30
	s.params.Customer.Normal.PatienceBaseMs = huge
	s.params.Customer.Bonus.PatienceBaseMs = huge
	s.params.Customer.Claimer.PatienceBaseMs = huge
	s.params.Customer.Buzz.PatienceBaseMs = huge
	s.Start(0)
	s.params.Storm.IntervalTicks = 0

	midMs := s.params.Phase.MidTimeMs
	s.Tick(midMs)
	if s.phase != proto.PhaseMid {
		t.Fatalf("elapsedMs=%d で Mid に移行するはず: %v", midMs, s.phase)
	}

	lateMs := s.params.Phase.LateTimeMs - midMs
	s.Tick(lateMs)
	if s.phase != proto.PhaseLate {
		t.Fatalf("elapsedMs=%d で Late に移行するはず: %v", s.params.Phase.LateTimeMs, s.phase)
	}
}

func TestStepHeat_Calculation(t *testing.T) {
	s := newTestSession(99)
	s.Start(0)
	s.params.Storm.IntervalTicks = 0
	hp := s.params.Heat

	s.Tick(150)
	wantEarly := hp.Base + hp.PhaseEarly
	if s.heatLevel != wantEarly {
		t.Fatalf("Early全員生存の fire=%d のはず: %d", wantEarly, s.heatLevel)
	}

	s.aliveCount = 49
	s.phase = proto.PhaseMid
	s.Tick(150)
	wantMid := hp.Base + int(hp.PerAliveDrop*float64(99-49)) + hp.PhaseMid
	if s.heatLevel != wantMid {
		t.Fatalf("Mid, alive=49 の fire=%d のはず: %d", wantMid, s.heatLevel)
	}
}

func TestStepStorm_Cull(t *testing.T) {
	n := 10
	s := newTestSession(n)
	s.Start(0)
	s.params.Storm = StormParams{IntervalTicks: 5, WarnTicks: 2, ThresholdPct: 0.20}
	s.phase = proto.PhaseMid
	s.params.Phase.LateAliveThreshold = 0

	for i, sid := range s.order {
		s.stores[sid].evalNormalized = float64(i) / float64(n-1)
	}

	var lastOut []Outbound
	for i := 0; i < 5; i++ {
		lastOut = s.Tick(150)
	}

	culled := filterMsg[proto.StoreEliminated](lastOut)
	if len(culled) == 0 {
		t.Fatal("storm で StoreEliminated が出るはず")
	}
	if s.aliveCount != n-2 {
		t.Fatalf("10人中2人が淘汰されて8人残るはず: %d", s.aliveCount)
	}
	for _, c := range culled {
		if c.Reason != proto.ElimCull {
			t.Fatalf("Reason=Cull のはず: %v", c.Reason)
		}
	}
}

func TestStepStorm_Warning(t *testing.T) {
	s := newTestSession(10)
	s.Start(0)
	s.params.Storm = StormParams{IntervalTicks: 10, WarnTicks: 3, ThresholdPct: 0.10}
	s.phase = proto.PhaseMid
	s.params.Phase.LateAliveThreshold = 0

	for i := 0; i < 6; i++ {
		out := s.Tick(150)
		warns := filterMsg[proto.ForcedEliminationWarning](out)
		if len(warns) > 0 {
			t.Fatalf("tick %d で警告が出るのは早すぎる", i+1)
		}
	}

	out := s.Tick(150)
	warns := filterMsg[proto.ForcedEliminationWarning](out)
	if len(warns) == 0 {
		t.Fatal("tick 7 で ForcedEliminationWarning が出るはず")
	}
	if warns[0].UntilTick != 3 {
		t.Fatalf("UntilTick=3 のはず: %d", warns[0].UntilTick)
	}

	for i := 8; i <= 9; i++ {
		out := s.Tick(150)
		warns := filterMsg[proto.ForcedEliminationWarning](out)
		if len(warns) > 0 {
			t.Fatalf("tick %d で警告が重複している", i)
		}
	}
}

func TestStepStorm_Tiebreak(t *testing.T) {
	s := newTestSession(5)
	s.Start(0)
	s.params.Storm = StormParams{IntervalTicks: 1, WarnTicks: 0, ThresholdPct: 0.40}
	s.phase = proto.PhaseMid
	s.params.Phase.LateAliveThreshold = 0

	for _, sid := range s.order {
		s.stores[sid].evalNormalized = 0
	}
	s.stores[s.order[0]].creditLife = 1
	s.stores[s.order[1]].creditLife = 2
	s.stores[s.order[2]].creditLife = 3
	s.stores[s.order[3]].creditLife = 4
	s.stores[s.order[4]].creditLife = 5

	out := s.Tick(150)
	culled := filterMsg[proto.StoreEliminated](out)

	if len(culled) < 2 {
		t.Fatalf("2店が淘汰されるはず: %d", len(culled))
	}

	if culled[0].StoreId != s.order[0] {
		t.Fatalf("creditLife=1 の店が最初に脱落するはず: %s", culled[0].StoreId)
	}
	if culled[0].FinalRank <= culled[1].FinalRank {
		t.Fatalf("先に脱落した方が FinalRank が大きいはず: %d vs %d",
			culled[0].FinalRank, culled[1].FinalRank)
	}
}

// ── Plan-05: checkFinish / Results テスト ─────────────────────

func TestCheckFinish_LastOneStanding(t *testing.T) {
	sess := newTestSession(2)
	p1 := PlayerId("s-1")
	p2 := PlayerId("s-2")

	st2 := sess.stores[p2]
	st2.alive = false
	st2.finalRank = 2
	st2.elimination = "SelfCollapse"
	sess.aliveCount = 1

	out := sess.checkFinish(nil)

	if sess.state != Finished {
		t.Fatalf("state=%v, want Finished", sess.state)
	}
	if len(out) !=  3  {
		t.Fatalf("outbound count=%d, want 2", len(out))
	}

	for _, o := range out {
		// Filter MatchEnd
		if _, ok := o.Msg.(proto.MatchEnd); ok { continue }
		me, ok := o.Msg.(proto.PersonalResult)
		if !ok {
			t.Fatalf("unexpected msg type: %T", o.Msg)
		}
		pid := o.To.PlayerId
		if pid == p1 && me.FinalRank != 1 {
			t.Errorf("s-1 rank=%d, want 1", me.FinalRank)
		}
		if pid == p2 && me.FinalRank != 2 {
			t.Errorf("s-2 rank=%d, want 2", me.FinalRank)
		}
	}
}

func TestCheckFinish_ThreePlayerRankOrder(t *testing.T) {
	sess := newTestSession(3)

	sess.stores[PlayerId("s-1")].alive = false
	sess.stores[PlayerId("s-1")].finalRank = 3
	sess.stores[PlayerId("s-1")].elimination = "SelfCollapse"

	sess.stores[PlayerId("s-3")].alive = false
	sess.stores[PlayerId("s-3")].finalRank = 2
	sess.stores[PlayerId("s-3")].elimination = "Cull"

	sess.aliveCount = 1

	out := sess.checkFinish(nil)

	if sess.state != Finished {
		t.Fatal("should be Finished")
	}

	// checkFinish returns PersonalResult ONLY for the winner (s-2).
	// Eliminated players (s-1, s-3) already received PersonalResult when they died.
	ranks := map[PlayerId]int{}
	for _, o := range out {
		if _, ok := o.Msg.(proto.MatchEnd); ok { continue }
		if me, ok := o.Msg.(proto.PersonalResult); ok {
			ranks[o.To.PlayerId] = me.FinalRank
		}
	}
	if ranks[PlayerId("s-2")] != 1 {
		t.Errorf("s-2 rank=%d want 1", ranks[PlayerId("s-2")])
	}
	if _, ok := ranks[PlayerId("s-1")]; ok {
		t.Errorf("s-1 should not receive PersonalResult in checkFinish")
	}
}

func TestCheckFinish_StatsCalculation(t *testing.T) {
	sess := newTestSession(2)
	p1 := PlayerId("s-1")

	sess.stores[p1].served = servedStats{
		count:       3,
		accuracySum: 0.9 + 0.8 + 0.7,
		elapsedSum:  3000 + 4000 + 5000,
	}

	sess.stores[PlayerId("s-2")].alive = false
	sess.stores[PlayerId("s-2")].finalRank = 2
	sess.aliveCount = 1

	out := sess.checkFinish(nil)

	for _, o := range out {
		if o.To.PlayerId != p1 {
			continue
		}
		if _, ok := o.Msg.(proto.MatchEnd); ok { continue }
		me := o.Msg.(proto.PersonalResult)
		if me.Stats.ServedCount != 3 {
			t.Errorf("ServedCount=%d want 3", me.Stats.ServedCount)
		}
		wantAcc := 2.4 / 3.0
		if diff := me.Stats.AvgAccuracy - wantAcc; diff > 0.001 || diff < -0.001 {
			t.Errorf("AvgAccuracy=%.4f want %.4f", me.Stats.AvgAccuracy, wantAcc)
		}
		if me.Stats.AvgElapsedMs != 4000 {
			t.Errorf("AvgElapsedMs=%d want 4000", me.Stats.AvgElapsedMs)
		}
	}
}

func TestCheckFinish_ZeroServed(t *testing.T) {
	sess := newTestSession(2)

	sess.stores[PlayerId("s-2")].alive = false
	sess.stores[PlayerId("s-2")].finalRank = 2
	sess.aliveCount = 1

	out := sess.checkFinish(nil)
	if len(out) != 3 {
		t.Fatalf("out=%d want 3", len(out))
	}

	for _, o := range out {
		if _, ok := o.Msg.(proto.MatchEnd); ok { continue }
		if o.To.PlayerId != PlayerId("s-2") {
			continue
		}
		me, ok := o.Msg.(proto.PersonalResult)
		if !ok { continue }
		if me.Stats.ServedCount != 0 || me.Stats.AvgAccuracy != 0 || me.Stats.AvgElapsedMs != 0 {
			t.Errorf("zero-served stats should be all zeros, got %+v", me.Stats)
		}
	}
}

func TestCheckFinish_SoloDoesNotEnd(t *testing.T) {
	sess := newTestSession(1)
	out := sess.checkFinish(nil)
	if sess.state == Finished {
		t.Fatal("solo session should not finish")
	}
	if len(out) != 0 {
		t.Fatalf("solo should produce no outbound, got %d", len(out))
	}
}

func TestCheckFinish_AllEliminatedSimultaneously(t *testing.T) {
	sess := newTestSession(2)

	for _, pid := range sess.order {
		st := sess.stores[pid]
		st.alive = false
		st.finalRank = 1
	}
	sess.aliveCount = 0

	out := sess.checkFinish(nil)
	if sess.state != Finished {
		t.Fatal("should be Finished when aliveCount=0")
	}
	if len(out) != 2 {
		t.Fatalf("out=%d want 2", len(out))
	}
}

// ── 同時脱落のタイブレーク（総合判定）──

// 同一tickで複数店が自滅した時、順位が店IDの並びでなく実力順になる。
func TestStepPatience_SimultaneousCollapse_RankedByMerit(t *testing.T) {
	s := newTestSession(4)
	s.state = Running
	s.params.Credit.InitialLife = 1
	s.params.Credit.LeaveLoss = LeaveLoss{Normal: 1, Bonus: 1, Claimer: 1, Buzz: 2}

	// s-1..s-3 を同時に自滅させる。s-4 は客を持たないので生き残る。
	// 評価は s-1 が最強、s-3 が最弱。ID順（s-1,s-2,s-3）とは逆になるよう仕込む。
	evals := map[PlayerId]float64{"s-1": 0.9, "s-2": 0.5, "s-3": 0.1}
	for _, sid := range []PlayerId{"s-1", "s-2", "s-3"} {
		s.stores[sid].creditLife = 1
		s.stores[sid].evalNormalized = evals[sid]
		cid := proto.CustomerId("c-" + string(sid))
		placeAssigned(s, cid, sid, proto.AttrNormal, 1, 5)
	}

	out := s.stepPatience(99999, nil)

	got := map[PlayerId]int{}
	for _, o := range out {
		if se, ok := o.Msg.(proto.StoreEliminated); ok {
			got[se.StoreId] = se.FinalRank
		}
	}
	if len(got) != 3 {
		t.Fatalf("3店が脱落するはず: %v", got)
	}
	// 4店中3店脱落 → 弱い順に 4位,3位,2位。
	want := map[PlayerId]int{"s-3": 4, "s-2": 3, "s-1": 2}
	for sid, w := range want {
		if got[sid] != w {
			t.Fatalf("評価順に順位が付いていない: got=%v want=%v", got, want)
		}
	}
	if !s.stores["s-4"].alive {
		t.Fatal("客を持たない s-4 は生存しているはず")
	}
}

// 生存店が全滅する tick でも、最強の1店は残して優勝者にする（バトロワ）。
// 「優勝者なのに脱落理由が付く」状態を作らないこと。
func TestStepPatience_AllCollapse_StrongestSurvivesAsWinner(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.params.Credit.InitialLife = 1
	s.params.Credit.LeaveLoss = LeaveLoss{Normal: 1, Bonus: 1, Claimer: 1, Buzz: 2}

	evals := map[PlayerId]float64{"s-1": 0.2, "s-2": 0.8, "s-3": 0.5}
	for sid, e := range evals {
		s.stores[sid].creditLife = 1
		s.stores[sid].evalNormalized = e
		placeAssigned(s, proto.CustomerId("c-"+string(sid)), sid, proto.AttrNormal, 1, 5)
	}

	out := s.stepPatience(99999, nil)

	elim := map[PlayerId]bool{}
	for _, o := range out {
		if se, ok := o.Msg.(proto.StoreEliminated); ok {
			elim[se.StoreId] = true
		}
	}
	if len(elim) != 2 {
		t.Fatalf("最強の1店は残すので脱落は2店のはず: %v", elim)
	}
	if elim["s-2"] {
		t.Fatal("最も評価の高い s-2 が脱落している（優勝者が残っていない）")
	}
	if s.aliveCount != 1 || !s.stores["s-2"].alive {
		t.Fatalf("s-2 が生存しているはず: aliveCount=%d alive=%v", s.aliveCount, s.stores["s-2"].alive)
	}

	// checkFinish が正規の優勝者として扱う（脱落理由が付かない）。
	out = s.checkFinish(nil)
	if s.state != Finished {
		t.Fatal("aliveCount=1 で試合が終了していない")
	}
	w := s.stores["s-2"]
	if w.finalRank != 1 {
		t.Fatalf("優勝者の finalRank=1 のはず: %d", w.finalRank)
	}
	if w.elimination != "" {
		t.Fatalf("優勝者に脱落理由が付いている: %q", w.elimination)
	}
	_ = out
}

// 総合判定は残信用→評価→提供数→精度の順に見る（決定的であること）。
func TestWeakerForRank_Order(t *testing.T) {
	mk := func(id PlayerId, life int, norm float64, served int, accSum float64) *storeState {
		return &storeState{id: id, creditLife: life, evalNormalized: norm,
			served: servedStats{count: served, accuracySum: accSum}}
	}
	// 信用が少ない方が下位。
	if !weakerForRank(mk("a", 1, 0.9, 100, 99), mk("b", 2, 0.1, 0, 0)) {
		t.Fatal("残信用が少ない方が下位のはず")
	}
	// 信用が同じなら評価。
	if !weakerForRank(mk("a", 1, 0.1, 100, 99), mk("b", 1, 0.9, 0, 0)) {
		t.Fatal("評価が低い方が下位のはず")
	}
	// 信用・評価が同じなら提供数。
	if !weakerForRank(mk("a", 1, 0.5, 3, 3), mk("b", 1, 0.5, 10, 10)) {
		t.Fatal("提供数が少ない方が下位のはず")
	}
	// 全部同じでも id で決定的に順序が付く（揺れない）。
	x, y := mk("a", 1, 0.5, 5, 5), mk("b", 1, 0.5, 5, 5)
	if weakerForRank(x, y) == weakerForRank(y, x) {
		t.Fatal("同値でも決定的な順序が付くべき")
	}
}

// ── proto v0.3.0 追随（#33）──

// summaries() は脱落店にだけ finalRank を入れる。
//
// 生存店に 0 を入れて送ると「順位0」という存在しない順位をクライアントに渡すことになる。
// 契約側は omitempty つきポインタなので、nil のままなら JSON にキーごと出ない。
func TestSummaries_FinalRankOnlyForEliminated(t *testing.T) {
	s := newTestSession(3)
	s.state = Running

	// s-2 だけ脱落済みにする。
	dead := s.stores[PlayerId("s-2")]
	dead.alive = false
	dead.finalRank = 3

	for _, sum := range s.summaries() {
		switch sum.StoreId {
		case "s-2":
			if sum.FinalRank == nil {
				t.Fatal("脱落店に finalRank が入っていない")
			}
			if *sum.FinalRank != 3 {
				t.Fatalf("finalRank=%d, want 3", *sum.FinalRank)
			}
		default:
			if sum.FinalRank != nil {
				t.Fatalf("生存店 %s に finalRank が入っている: %d", sum.StoreId, *sum.FinalRank)
			}
		}
	}

	// 実際の JSON にもキーが出ないことを確認する。
	alive := s.summaries()[0]
	b, err := json.Marshal(alive)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "finalRank") {
		t.Fatalf("生存店の JSON に finalRank キーが出ている: %s", b)
	}
}

// 公開パラメータに制限時間が無く、淘汰しきい値が配られる（proto v0.3.0）。
func TestPublicParams_NoTimeLimitAndHasStormThreshold(t *testing.T) {
	s := newTestSession(3)
	s.params.Storm.ThresholdPct = 0.15

	p := s.publicParams()
	if p.StormThresholdPct != 0.15 {
		t.Fatalf("stormThresholdPct=%v, want 0.15", p.StormThresholdPct)
	}
	if p.MaxStores != 3 || p.InitialLife != s.params.Credit.InitialLife {
		t.Fatalf("公開パラメータが不正: %+v", p)
	}

	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "matchTimeLimitMs") {
		t.Fatalf("公開パラメータに matchTimeLimitMs が残っている: %s", b)
	}
}

// 試合は生存店=1 でのみ終わる。経過時間では終わらない（制限時間は廃止済み）。
func TestCheckFinish_NeverEndsOnElapsedTime(t *testing.T) {
	s := newTestSession(3)
	s.state = Running

	// 十分に長い時間を進めても、生存店が2以上なら終了しない。
	for i := 0; i < 500; i++ {
		s.elapsedMs += 1000
		s.checkFinish(nil)
		if s.state == Finished {
			t.Fatalf("経過時間で試合が終了した（制限時間は廃止済み）: elapsedMs=%d aliveCount=%d",
				s.elapsedMs, s.aliveCount)
		}
	}
}

// ── proto v0.3.0 値算出（#64）──

// 星は rank から一意に決まる。1位=5.0 / 最下位=0.0 / 中位=2.5。
func TestStarRating_MapsRankToStars(t *testing.T) {
	s := newTestSession(99)

	cases := []struct {
		rank int
		want float64
	}{
		{1, 5.0},
		{99, 0.0},
		{50, 2.5},
	}
	for _, c := range cases {
		st := s.stores[s.order[0]]
		st.rank = c.rank
		if got := s.starRating(st); got != c.want {
			t.Fatalf("rank=%d → star=%v, want %v", c.rank, got, c.want)
		}
	}

	// rank 未確定(0)は 0 を返す（星が跳ね上がらない）。
	st := s.stores[s.order[0]]
	st.rank = 0
	if got := s.starRating(st); got != 0 {
		t.Fatalf("rank未確定は0のはず: %v", got)
	}
}

// starDelta は前回配信時からの増減。
func TestEvaluationUpdate_StarDelta(t *testing.T) {
	s := newTestSession(99)
	st := s.stores[s.order[0]]

	st.rank = 99 // 最下位 → 星0
	first := s.evaluationUpdate(st)
	if first.StarRating != 0 || first.StarDelta != 0 {
		t.Fatalf("初回: star=%v delta=%v, want 0/0", first.StarRating, first.StarDelta)
	}

	st.rank = 1 // 首位へ急上昇 → 星5
	second := s.evaluationUpdate(st)
	if second.StarRating != 5 {
		t.Fatalf("star=%v, want 5", second.StarRating)
	}
	if second.StarDelta != 5 {
		t.Fatalf("delta=%v, want 5（0→5の増分）", second.StarDelta)
	}

	// 変化なしなら delta は0。
	third := s.evaluationUpdate(st)
	if third.StarDelta != 0 {
		t.Fatalf("変化なしの delta=%v, want 0", third.StarDelta)
	}
}

// 予告の selfAtRisk は、実際に淘汰される店だけ true になる。
//
// 予告と実行で判定がズレると「警告が出ていないのに落ちる」が起きるため、
// 同じ集合になることをここで固定する。
func TestForcedEliminationWarning_SelfAtRiskMatchesCull(t *testing.T) {
	s := newTestSession(10)
	s.state = Running
	s.params.Storm.ThresholdPct = 0.2 // 10店の20% = 2店が対象

	// 評価に差を付ける（s-1 が最弱、s-10 が最強）。
	for i, sid := range s.order {
		s.stores[sid].evalNormalized = float64(i) / 9.0
		s.stores[sid].rank = 10 - i
	}

	atRisk := s.cullTargets()
	if len(atRisk) != 2 {
		t.Fatalf("淘汰対象は2店のはず: %d店 %v", len(atRisk), atRisk)
	}
	// 最弱2店（s-1, s-2）が対象。
	for _, want := range []PlayerId{"s-1", "s-2"} {
		if !atRisk[want] {
			t.Fatalf("%s が対象に入っていない: %v", want, atRisk)
		}
	}
	if atRisk["s-10"] {
		t.Fatal("最強の s-10 が対象に入っている")
	}

	// 実際に淘汰を実行して、同じ集合が落ちることを確認する。
	out := s.executeCull(nil)
	eliminated := map[PlayerId]bool{}
	for _, o := range out {
		if se, ok := o.Msg.(proto.StoreEliminated); ok {
			eliminated[se.StoreId] = true
		}
	}
	if len(eliminated) != len(atRisk) {
		t.Fatalf("予告と実行で対象数が違う: 予告=%d 実行=%d", len(atRisk), len(eliminated))
	}
	for sid := range atRisk {
		if !eliminated[sid] {
			t.Fatalf("予告された %s が実際には落ちていない", sid)
		}
	}
}

// 演出しきい値が公開パラメータに載る（既定値）。
func TestPublicParams_PresentationThresholds(t *testing.T) {
	s := newTestSession(3)
	p := s.publicParams()
	def := DefaultParameters().Presentation
	if p.FinalStageAliveThreshold != def.FinalStageAliveThreshold {
		t.Fatalf("finalStageAliveThreshold=%d, want %d", p.FinalStageAliveThreshold, def.FinalStageAliveThreshold)
	}
	if p.FinalRushAliveThreshold != def.FinalRushAliveThreshold {
		t.Fatalf("finalRushAliveThreshold=%d, want %d", p.FinalRushAliveThreshold, def.FinalRushAliveThreshold)
	}
	if def.FinalStageAliveThreshold == 0 || def.FinalRushAliveThreshold == 0 {
		t.Fatal("既定値が0のまま（クライアントが演出切替できない）")
	}
}

// CustomerArrived は我慢の起点（サーバー時刻）を伴う。
//
// 我慢は行列に入った瞬間から減るので、起点は来店時刻そのもの。
// クライアントはこれを基準にゲージを描けば受信遅延ぶんズレない。
func TestAdmitCustomer_CarriesPatienceStart(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.initCustomers()
	s.elapsedMs = 4200 // 試合開始から 4.2 秒経過した時点で来店させる

	var cid proto.CustomerId
	for id := range s.customers {
		cid = id
		break
	}
	ob, ok := s.admitCustomer(cid, s.order[0])
	if !ok {
		t.Fatal("admitCustomer が失敗した")
	}
	cv, ok := ob.Msg.(proto.CustomerView)
	if !ok {
		t.Fatalf("CustomerView でない: %T", ob.Msg)
	}
	if cv.PatienceStartedAtServerMs != 4200 {
		t.Fatalf("起点=%d, want 4200", cv.PatienceStartedAtServerMs)
	}
}

// ── 我慢ゲージのタイムアウトを全属性で保証する（#29）──
//
// Textro #78 の類型: 種別で時間切れ判定を分岐した結果、一部の種別がタイムアウトから
// 漏れ、キューの先頭が永遠に消化されず固まった。
//
// Takoda の相当機構は「客の我慢ゲージ → CustomerLeft / 信用減」。属性ごとに離脱の
// 発火可否を分岐すると、漏れた属性の客が行列に居座って店の対応が永久に止まる。
// 属性差は「減算量やペナルティ量」で表現し、**離脱が起きうること自体は全属性で不変**。

// 全4属性が我慢切れで離脱し、信用が減り、行列が次へ進む。
func TestStepPatience_AllAttributesTimeOut(t *testing.T) {
	attrs := []proto.CustomerAttribute{
		proto.AttrNormal, proto.AttrBonus, proto.AttrClaimer, proto.AttrBuzz,
	}

	for _, attr := range attrs {
		t.Run(string(attr), func(t *testing.T) {
			s := newTestSession(2)
			s.state = Running
			s.params.Credit.InitialLife = 10 // 脱落させずに離脱だけを見る
			store := s.order[0]
			s.stores[store].creditLife = 10

			stuck := proto.CustomerId("stuck")
			next := proto.CustomerId("next")
			placeAssigned(s, stuck, store, attr, 1, 5)
			placeAssigned(s, next, store, proto.AttrNormal, 1, 5)

			lifeBefore := s.stores[store].creditLife

			// この属性の我慢時間を超えて進める。
			out := s.stepPatience(s.customers[stuck].patienceMaxMs+1, nil)

			// 1) CustomerLeft が発火する
			leftIDs := map[proto.CustomerId]bool{}
			for _, o := range out {
				if cl, ok := o.Msg.(proto.CustomerLeft); ok {
					leftIDs[cl.CustomerId] = true
				}
			}
			if !leftIDs[stuck] {
				t.Fatalf("%s が離脱しない（この属性がタイムアウトから漏れている）", attr)
			}

			// 2) 信用が減る
			if s.stores[store].creditLife >= lifeBefore {
				t.Fatalf("%s の離脱で信用が減っていない: before=%d after=%d",
					attr, lifeBefore, s.stores[store].creditLife)
			}

			// 3) 行列に居座らない（たべたべエリアへ戻る）
			for _, cid := range s.storeQueues[store] {
				if cid == stuck {
					t.Fatalf("%s が離脱後も行列に残っている: %v", attr, s.storeQueues[store])
				}
			}
			if c, ok := s.customers[stuck]; ok && c.assignedStore != nil {
				t.Fatalf("%s の assignedStore がクリアされていない", attr)
			}
		})
	}
}

// 特殊属性が行列先頭にいても、店が詰まらない。
//
// 「先頭の特殊属性だけ離脱しない」実装だと、後ろの客が永久に対応されず
// その店だけ試合から取り残される（Textro #78 の詰まり）。
func TestStepPatience_SpecialAttributeDoesNotBlockQueue(t *testing.T) {
	for _, attr := range []proto.CustomerAttribute{proto.AttrBonus, proto.AttrClaimer, proto.AttrBuzz} {
		t.Run(string(attr), func(t *testing.T) {
			s := newTestSession(2)
			s.state = Running
			s.params.Credit.InitialLife = 10
			store := s.order[0]
			s.stores[store].creditLife = 10

			// 先頭に特殊属性、後ろに通常客。
			head := proto.CustomerId("head-" + string(attr))
			behind := proto.CustomerId("behind")
			placeAssigned(s, head, store, attr, 1, 5)
			placeAssigned(s, behind, store, proto.AttrNormal, 1, 5)

			// 十分に時間を進めれば、どちらも離脱して行列は空になる。
			s.stepPatience(999999, nil)

			if len(s.storeQueues[store]) != 0 {
				t.Fatalf("%s が先頭のとき行列が詰まった: %v", attr, s.storeQueues[store])
			}
		})
	}
}

// 属性ごとの離脱ペナルティ（信用減少量）が LeaveLoss どおりに効く。
// 「発火の有無」ではなく「ペナルティ量」で属性差を表現する、という設計の確認。
func TestProcessLeave_PenaltyDiffersByAttribute(t *testing.T) {
	loss := LeaveLoss{Normal: 1, Bonus: 2, Claimer: 3, Buzz: 4}
	cases := []struct {
		attr proto.CustomerAttribute
		want int
	}{
		{proto.AttrNormal, 1},
		{proto.AttrBonus, 2},
		{proto.AttrClaimer, 3},
		{proto.AttrBuzz, 4},
	}
	for _, c := range cases {
		t.Run(string(c.attr), func(t *testing.T) {
			s := newTestSession(2)
			s.state = Running
			s.params.Credit.LeaveLoss = loss
			s.params.Credit.InitialLife = 100
			store := s.order[0]
			s.stores[store].creditLife = 100

			cid := proto.CustomerId("c")
			placeAssigned(s, cid, store, c.attr, 1, 5)
			s.stepPatience(s.customers[cid].patienceMaxMs+1, nil)

			got := 100 - s.stores[store].creditLife
			if got != c.want {
				t.Fatalf("%s の信用減=%d, want %d", c.attr, got, c.want)
			}
		})
	}
}

// heatLevel が heat.maxLevel を超えないこと（#75）。
//
// 超えた値を配ると、WordSource は下の段階へ降りるだけなので難度は変わらないのに
// クライアントの heatLevel 表示と運営UIの maxLevel が実態と食い違う。
func TestStepHeat_ClampsToMaxLevel(t *testing.T) {
	params := DefaultParameters()
	params.Heat.MaxLevel = 3
	params.Heat.Base = 0
	params.Heat.PerAliveDrop = 1 // 1店脱落ごとに +1（すぐ上限を超える）
	params.Heat.PhaseEarly = 0
	params.Heat.PhaseMid = 5
	params.Heat.PhaseLate = 10

	s := newTestSessionWith(params, 20)
	s.Start(0)
	maxSeen := 0
	for i := 0; i < 3000 && s.State() != Finished; i++ {
		for _, o := range s.Tick(params.Session.TickIntervalMs) {
			if d, ok := o.Msg.(proto.DifficultyUpdate); ok {
				if d.HeatLevel > maxSeen {
					maxSeen = d.HeatLevel
				}
				if d.HeatLevel > params.Heat.MaxLevel {
					t.Fatalf("heatLevel %d が maxLevel %d を超えた", d.HeatLevel, params.Heat.MaxLevel)
				}
			}
		}
	}
	if maxSeen != params.Heat.MaxLevel {
		t.Fatalf("上限 %d に到達していない（maxSeen=%d）。テストが上限を突いていない", params.Heat.MaxLevel, maxSeen)
	}
}

// heat.base に負値が入っても heatLevel が負にならないこと。
//
// 負のまま WordSource へ渡すと下降ループが1回も回らず、全店がフォールバック語固定になる。
func TestStepHeat_NeverGoesNegative(t *testing.T) {
	params := DefaultParameters()
	params.Heat.Base = -50
	params.Heat.PerAliveDrop = 0
	params.Heat.PhaseEarly = 0
	params.Heat.PhaseMid = 0
	params.Heat.PhaseLate = 0

	s := newTestSessionWith(params, 5)
	s.Start(0)
	for i := 0; i < 500 && s.State() != Finished; i++ {
		for _, o := range s.Tick(params.Session.TickIntervalMs) {
			if d, ok := o.Msg.(proto.DifficultyUpdate); ok && d.HeatLevel < 0 {
				t.Fatalf("heatLevel が負になった: %d", d.HeatLevel)
			}
		}
	}
}

// リザルト統計が実試合の集計と一致すること（Unity のリザルト画面用）。
//
// 提供・取りこぼしを属性込みで数える。ここがズレるとリザルトの数字が嘘になる。
func TestMatchStats_CountsServedAndLeftByAttribute(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0 // 自動分配を止めて、置いた客だけを見る
	s := newTestSessionWith(params, 2)
	s.Start(0)

	// 提供する客（Normal 2人・Buzz 1人）と、放置して帰らせる客（Claimer 1人）。
	placeAssigned(s, "c-1", "s-1", proto.AttrNormal, 2, 10)
	placeAssigned(s, "c-2", "s-1", proto.AttrNormal, 2, 20)
	placeAssigned(s, "c-3", "s-1", proto.AttrBuzz, 4, 30)
	placeAssigned(s, "c-4", "s-1", proto.AttrClaimer, 1, 5)

	// 行列の先頭から順に提供する（先頭以外は弾かれる）。
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-1", ElapsedMs: 2000, MissCount: 1})
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-2", ElapsedMs: 4000, MissCount: 3})
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-3", ElapsedMs: 3000, MissCount: 0})

	// 残った Claimer を我慢切れにする。
	s.customers["c-4"].patienceLeftMs = 1
	s.Tick(params.Session.TickIntervalMs)

	got := s.buildMatchStats(s.stores["s-1"])

	if got.ServedCount != 3 {
		t.Errorf("ServedCount = %d, want 3", got.ServedCount)
	}
	if got.LeftCount != 1 {
		t.Errorf("LeftCount = %d, want 1", got.LeftCount)
	}
	if got.TotalKeystrokes != 60 {
		t.Errorf("TotalKeystrokes = %d, want 60 (10+20+30)", got.TotalKeystrokes)
	}
	if got.TotalMisses != 4 {
		t.Errorf("TotalMisses = %d, want 4 (1+3+0)", got.TotalMisses)
	}
	if got.FastestMs != 2000 {
		t.Errorf("FastestMs = %d, want 2000", got.FastestMs)
	}
	if got.SlowestMs != 4000 {
		t.Errorf("SlowestMs = %d, want 4000", got.SlowestMs)
	}

	if got.Normal.Served != 2 || got.Normal.Left != 0 {
		t.Errorf("Normal = %+v, want {Served:2 Left:0}", got.Normal)
	}
	if got.Buzz.Served != 1 {
		t.Errorf("Buzz.Served = %d, want 1", got.Buzz.Served)
	}
	if got.Claimer.Left != 1 || got.Claimer.Served != 0 {
		t.Errorf("Claimer = %+v, want {Served:0 Left:1}", got.Claimer)
	}
	if got.Bonus.Served != 0 || got.Bonus.Left != 0 {
		t.Errorf("Bonus = %+v, want ゼロ", got.Bonus)
	}

	// 属性別の合計が全体と一致すること（数え漏れ・二重計上の検出）。
	sumServed, sumLeft := 0, 0
	for _, a := range []proto.AttributeTally{got.Normal, got.Bonus, got.Claimer, got.Buzz} {
		sumServed += a.Served
		sumLeft += a.Left
	}
	if sumServed != got.ServedCount {
		t.Errorf("属性別の提供合計 %d != ServedCount %d", sumServed, got.ServedCount)
	}
	if sumLeft != got.LeftCount {
		t.Errorf("属性別の離脱合計 %d != LeftCount %d", sumLeft, got.LeftCount)
	}
}

// 1件も提供していない店でもゼロ除算せず、取りこぼしは返ること。
func TestMatchStats_NoServeIsSafe(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 2)
	s.Start(0)

	placeAssigned(s, "c-1", "s-1", proto.AttrNormal, 1, 5)
	s.customers["c-1"].patienceLeftMs = 1
	s.Tick(params.Session.TickIntervalMs)

	got := s.buildMatchStats(s.stores["s-1"])
	if got.ServedCount != 0 || got.AvgAccuracy != 0 || got.AvgElapsedMs != 0 {
		t.Errorf("提供0なのに平均が入っている: %+v", got)
	}
	if got.LeftCount != 1 {
		t.Errorf("LeftCount = %d, want 1（提供0でも取りこぼしは返す）", got.LeftCount)
	}
	if got.FastestMs != 0 {
		t.Errorf("FastestMs = %d, want 0", got.FastestMs)
	}
}

// MatchEnd が試合の経過時間・残信用・脱落理由を載せること。
// 優勝した店は reason が空（「優勝なのに脱落理由が付く」状態を作らない）。
func TestMatchEnd_CarriesResultContext(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 2)
	s.Start(0)

	// s-2 を自滅させて決着させる。
	s.stores["s-2"].creditLife = 0
	s.resolveCollapses([]*storeState{s.stores["s-2"]}, nil)
	var collapseOut []Outbound
	collapseOut = s.selfCollapse("s-2", collapseOut)

	// 優勝する店に脱落理由が残っている状態を作る。checkFinish がこれを消すこと。
	// （全滅時に最強の1店を生存させる経路など、生存店に理由が付いたまま来る可能性がある。
	//  ここを消さないと「優勝なのに自滅と表示される」リザルトになる）
	s.stores["s-1"].elimination = string(proto.ElimCull)

	out := s.checkFinish(nil)

	ends := map[PlayerId]proto.PersonalResult{}
	for _, o := range out {
		if m, ok := o.Msg.(proto.PersonalResult); ok {
			ends[o.To.PlayerId] = m
		}
	}
	for _, o := range collapseOut {
		if m, ok := o.Msg.(proto.PersonalResult); ok {
			ends[o.To.PlayerId] = m
		}
	}
	if len(ends) != 2 {
		t.Fatalf("PersonalResult should be 2 (winner + collapsed), got %d", len(ends))
	}

	win := ends["s-1"]
	if win.FinalRank != 1 {
		t.Errorf("優勝店の finalRank = %d, want 1", win.FinalRank)
	}
	if win.Reason != "" {
		t.Errorf("優勝店に脱落理由が付いている: %q", win.Reason)
	}
	if win.CreditLeft != params.Credit.InitialLife {
		t.Errorf("CreditLeft = %d, want %d", win.CreditLeft, params.Credit.InitialLife)
	}

	lose := ends["s-2"]
	if lose.Reason != proto.ElimSelfCollapse {
		t.Errorf("自滅店の reason = %q, want %q", lose.Reason, proto.ElimSelfCollapse)
	}
	if lose.CreditLeft != 0 {
		t.Errorf("自滅店の CreditLeft = %d, want 0", lose.CreditLeft)
	}
	if win.SurvivedMs != lose.SurvivedMs {
		t.Errorf("経過時間が店ごとに違う: %d / %d", win.SurvivedMs, lose.SurvivedMs)
	}
}

// 公開パラメータに patience 2項目が載ること（Unity REQ-02）。
// これが無いと Late 以降クライアントの我慢ゲージが約1.67倍ズレる。
func TestPublicParams_CarriesPatience(t *testing.T) {
	params := DefaultParameters()
	params.Patience.LateMul = 0.5
	params.Patience.AlertMs = 3000
	s := newTestSessionWith(params, 2)

	got := s.publicParams()
	if got.PatienceLateMul != 0.5 {
		t.Errorf("PatienceLateMul = %v, want 0.5", got.PatienceLateMul)
	}
	if got.PatienceAlertMs != 3000 {
		t.Errorf("PatienceAlertMs = %d, want 3000", got.PatienceAlertMs)
	}
}
