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
