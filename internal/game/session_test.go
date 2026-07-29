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
func (fakeWords) NextTrap(*rand.Rand) Word  { return Word{Text: "ながいおだい", KeystrokeCount: 12} }

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
