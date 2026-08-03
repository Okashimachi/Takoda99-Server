package game

import (
	"fmt"
	"math/rand"
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
