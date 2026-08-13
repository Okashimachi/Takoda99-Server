package game

import (
	"testing"

	"takoda99/internal/proto"
)

// findRow は StoreBoard から指定 id の行を返す。
func findRow(rows []StoreBoardRow, id PlayerId) (StoreBoardRow, bool) {
	for _, r := range rows {
		if r.Id == id {
			return r, true
		}
	}
	return StoreBoardRow{}, false
}

// 観測 getter が客の属性内訳・行列長・提供数・restPool を正しく集計する。
func TestObserve_CustomerMixAndStoreBoard(t *testing.T) {
	s := newTestSession(3)

	// s-1 の行列に Normal×2 + Buzz×1 を置く。
	placeAssigned(s, "c-a", "s-1", proto.AttrNormal, 1, 4)
	placeAssigned(s, "c-b", "s-1", proto.AttrNormal, 1, 4)
	placeAssigned(s, "c-c", "s-1", proto.AttrBuzz, 1, 4)
	// restPool に Bonus×1 + Claimer×1。
	s.customers["c-d"] = &customer{attribute: proto.AttrBonus}
	s.customers["c-e"] = &customer{attribute: proto.AttrClaimer}
	s.restPool = append(s.restPool, "c-d", "c-e")
	// s-1 の提供数。
	s.stores["s-1"].served.count = 7

	// RestPool 系
	if got := s.RestPoolCount(); got != 2 {
		t.Fatalf("RestPoolCount=%d, want 2", got)
	}
	if got := s.RestPoolByAttr(); got != (AttrCounts{Bonus: 1, Claimer: 1}) {
		t.Fatalf("RestPoolByAttr=%+v, want {Bonus:1,Claimer:1}", got)
	}

	// CustomerMix（在場総数 = 行列3 + restPool2）
	if got := s.CustomerMix(); got != (AttrCounts{Normal: 2, Bonus: 1, Claimer: 1, Buzz: 1}) {
		t.Fatalf("CustomerMix=%+v, want {Normal:2,Bonus:1,Claimer:1,Buzz:1}", got)
	}

	// StoreBoard の s-1 行
	rows := s.StoreBoard()
	if len(rows) != 3 {
		t.Fatalf("StoreBoard len=%d, want 3", len(rows))
	}
	r1, ok := findRow(rows, "s-1")
	if !ok {
		t.Fatal("s-1 の行が無い")
	}
	if r1.QueueLen != 3 {
		t.Fatalf("s-1 QueueLen=%d, want 3", r1.QueueLen)
	}
	if r1.QueueByAttr != (AttrCounts{Normal: 2, Buzz: 1}) {
		t.Fatalf("s-1 QueueByAttr=%+v, want {Normal:2,Buzz:1}", r1.QueueByAttr)
	}
	if r1.ServedCount != 7 {
		t.Fatalf("s-1 ServedCount=%d, want 7", r1.ServedCount)
	}
	// 空の店は行列0
	r2, _ := findRow(rows, "s-2")
	if r2.QueueLen != 0 || r2.QueueByAttr != (AttrCounts{}) {
		t.Fatalf("s-2 は空のはず: len=%d attr=%+v", r2.QueueLen, r2.QueueByAttr)
	}
}

// 脱落店は StoreBoard で FinalRank>0・Alive=false になり、生存店は FinalRank=0。
func TestObserve_StoreBoardFinalRank(t *testing.T) {
	s := newTestSession(2)
	st := s.stores["s-2"]
	st.alive = false
	st.finalRank = 42

	rows := s.StoreBoard()
	dead, _ := findRow(rows, "s-2")
	if dead.Alive || dead.FinalRank != 42 {
		t.Fatalf("s-2 は脱落(finalRank=42)のはず: %+v", dead)
	}
	live, _ := findRow(rows, "s-1")
	if !live.Alive || live.FinalRank != 0 {
		t.Fatalf("s-1 は生存(finalRank=0)のはず: %+v", live)
	}
}


// StoreBoard の AtRisk が次の足切りの対象集合(cullTargetIds)と一致する（配線確認）。
func TestObserve_StoreBoardAtRiskMatchesCull(t *testing.T) {
	s := newTestSession(5)
	// 5店なので、既定の第1ステージ（目標75）だと切る数が負になり対象0になる。
	// 対象が出る目標に差し替える。
	s.params.Cull.Stages[0] = CullStage{AtMs: 20000, TargetAliveCount: 3}
	// 生存店に異なるスコアを与える（弱い順が決まるように）。
	scores := map[PlayerId]int{"s-1": 900, "s-2": 100, "s-3": 500, "s-4": 200, "s-5": 700}
	for id, sc := range scores {
		s.stores[id].score = sc
	}

	want := s.cullTargetIds()
	if len(want) == 0 {
		t.Fatal("前提: 淘汰対象が1店以上あるはず")
	}
	rows := s.StoreBoard()
	got := map[PlayerId]bool{}
	for _, r := range rows {
		if r.AtRisk {
			got[r.Id] = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("AtRisk 数=%d, want %d (%v vs %v)", len(got), len(want), got, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("cullTargets の %s が AtRisk になっていない", id)
		}
	}
}

// Phase / HeatLevel / RestPool は Start 後に妥当な初期値を返す。
func TestObserve_PhaseHeatAndInitialPool(t *testing.T) {
	s := newTestSession(3)
	s.Start(0)

	if s.Phase() != proto.PhaseEarly {
		t.Fatalf("Phase=%v, want Early", s.Phase())
	}
	// Start で total 客が restPool に積まれる（分配前）。
	if s.RestPoolCount() != s.params.Customer.Total {
		t.Fatalf("RestPoolCount=%d, want %d", s.RestPoolCount(), s.params.Customer.Total)
	}
	// CustomerMix の総和は Total に一致。
	mix := s.CustomerMix()
	if mix.Normal+mix.Bonus+mix.Claimer+mix.Buzz != s.params.Customer.Total {
		t.Fatalf("CustomerMix 総和=%d, want %d", mix.Normal+mix.Bonus+mix.Claimer+mix.Buzz, s.params.Customer.Total)
	}
	if s.HeatLevel() < 0 {
		t.Fatalf("HeatLevel=%d, want >=0", s.HeatLevel())
	}
}

// CullState は次のステージ番号・残り時間・目標生存数・カットラインを返す。
func TestObserve_CullState(t *testing.T) {
	s := newTestSession(3)
	stages := s.params.Cull.Stages

	// 開始直後は第1ステージへ向かっている。
	v := s.CullState()
	if v.StageIndex != 1 || v.StageTotal != len(stages) {
		t.Fatalf("StageIndex/Total=%d/%d, want 1/%d", v.StageIndex, v.StageTotal, len(stages))
	}
	if v.UntilMs != stages[0].AtMs {
		t.Fatalf("UntilMs=%d, want %d", v.UntilMs, stages[0].AtMs)
	}
	if v.TargetAliveCount != stages[0].TargetAliveCount {
		t.Fatalf("TargetAliveCount=%d, want %d", v.TargetAliveCount, stages[0].TargetAliveCount)
	}
	if v.CutLineRank != stages[0].TargetAliveCount+1 {
		t.Fatalf("CutLineRank=%d, want %d", v.CutLineRank, stages[0].TargetAliveCount+1)
	}

	// 時間が進むと残りが減る。
	s.elapsedMs = int64(stages[0].AtMs) - 3000
	if got := s.CullState().UntilMs; got != 3000 {
		t.Fatalf("UntilMs=%d, want 3000", got)
	}

	// 最終ステージだけ CutLineRank=2（表示層・plan-h22 §3.2）。
	s.cullStageIdx = len(stages) - 1
	if got := s.CullState().CutLineRank; got != 2 {
		t.Fatalf("最終ステージの CutLineRank=%d, want 2", got)
	}

	// 全ステージ消化済みなら StageIndex=0。
	s.cullStageIdx = len(stages)
	if got := s.CullState(); got.StageIndex != 0 || got.UntilMs != 0 {
		t.Fatalf("消化済みで %+v, want StageIndex=0 UntilMs=0", got)
	}
}
