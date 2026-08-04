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
	inits := make([]PlayerInit, n)
	for i := range inits {
		id := PlayerId(fmt.Sprintf("s-%d", i+1))
		inits[i] = PlayerInit{Id: id, DisplayName: string(id)}
	}
	return NewSession("test-match", DefaultParameters(), fakeWords{}, rand.New(rand.NewSource(42)), inits)
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

	if s.customers[cid].assignedStore != nil {
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

	// CustomerLeft(1) + CreditUpdate(1) + StoreEliminated(broadcast=1) = 3件
	if len(out) != 3 {
		t.Fatalf("出力3件のはず: %d件", len(out))
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

func TestStepPatience_HeadOnly(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	store := s.order[0]
	front := proto.CustomerId("front")
	behind := proto.CustomerId("behind")
	placeAssigned(s, front, store, proto.AttrNormal, 1, 5)
	placeAssigned(s, behind, store, proto.AttrNormal, 1, 5)

	behindBefore := s.customers[behind].patienceLeftMs
	frontBefore := s.customers[front].patienceLeftMs

	s.stepPatience(150, nil)

	if s.customers[front].patienceLeftMs >= frontBefore {
		t.Fatalf("先頭のゲージが減っていない: before=%d after=%d", frontBefore, s.customers[front].patienceLeftMs)
	}
	if s.customers[behind].patienceLeftMs != behindBefore {
		t.Fatalf("2番目のゲージが動いた: before=%d after=%d", behindBefore, s.customers[behind].patienceLeftMs)
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
	if s.customers[second].patienceLeftMs != s.customers[second].patienceMaxMs {
		t.Fatalf("second のゲージは満タンのはず: left=%d max=%d",
			s.customers[second].patienceLeftMs, s.customers[second].patienceMaxMs)
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
	s.Start()
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
	s.Start()
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
	s.Start()
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
	s.Start()
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
	s.Start()
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
	s.Start()
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
	s.Start()
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
	s.Start()
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
	if len(out) != 2 {
		t.Fatalf("outbound count=%d, want 2", len(out))
	}

	for _, o := range out {
		me, ok := o.Msg.(proto.MatchEnd)
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

	ranks := map[PlayerId]int{}
	for _, o := range out {
		me := o.Msg.(proto.MatchEnd)
		ranks[o.To.PlayerId] = me.FinalRank
	}
	if ranks[PlayerId("s-1")] != 3 {
		t.Errorf("s-1 rank=%d want 3", ranks[PlayerId("s-1")])
	}
	if ranks[PlayerId("s-2")] != 1 {
		t.Errorf("s-2 rank=%d want 1", ranks[PlayerId("s-2")])
	}
	if ranks[PlayerId("s-3")] != 2 {
		t.Errorf("s-3 rank=%d want 2", ranks[PlayerId("s-3")])
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
		me := o.Msg.(proto.MatchEnd)
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
	if len(out) != 2 {
		t.Fatalf("out=%d want 2", len(out))
	}

	for _, o := range out {
		if o.To.PlayerId != PlayerId("s-2") {
			continue
		}
		me := o.Msg.(proto.MatchEnd)
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
