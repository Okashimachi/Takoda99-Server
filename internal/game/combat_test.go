package game

import (
	"testing"

	"textro99/internal/proto"
)

// #21a 威力算出・個数変換（純関数）
func TestAttackPower(t *testing.T) {
	p := DefaultParameters().Attack // ratio1.0, perBadge0.1, cap1.0
	cases := []struct {
		combo, badge, want int
	}{
		{100, 0, 100},
		{100, 5, 150}, // ×1.5
		{100, 20, 200}, // cap +100%
		{45, 0, 45},
	}
	for _, c := range cases {
		if got := attackPower(c.combo, c.badge, p); got != c.want {
			t.Fatalf("attackPower(%d,%d)=%d, want %d", c.combo, c.badge, got, c.want)
		}
	}
}

func TestPowerToDakenCount(t *testing.T) {
	p := DefaultParameters().Attack // rate 0.1
	for _, c := range []struct{ power, want int }{{45, 4}, {150, 15}, {80, 8}, {5, 0}, {0, 0}} {
		if got := powerToDakenCount(c.power, p); got != c.want {
			t.Fatalf("powerToDakenCount(%d)=%d, want %d", c.power, got, c.want)
		}
	}
}

// #21b 完全相殺: 威力が予告以上なら予告は消え着弾しない。
func TestOffset_Full(t *testing.T) {
	s := newTestSession(t, "a", "b")
	s.Start()
	s.newWarning("a", "b", 50)     // b に威力50の予告
	s.players["b"].p.combo = 50    // b の威力=50
	res := s.ApplyAttack("b", proto.AttackRequest{})

	or, _, ok := find[proto.OffsetResolved](res)
	if !ok || or.OffsetAmount != 50 || or.RemainderDakenCount != 0 {
		t.Fatalf("完全相殺 OffsetResolved 不備: %+v ok=%v", or, ok)
	}
	if len(s.warnings) != 0 {
		t.Fatalf("完全相殺後は予告が消える: %d 残", len(s.warnings))
	}
	if s.players["b"].stack != 0 {
		t.Fatalf("完全相殺で着弾なし: stack=%d", s.players["b"].stack)
	}
}

// #21b 部分相殺: 残余威力を個数変換して即着弾。
func TestOffset_Partial(t *testing.T) {
	s := newTestSession(t, "a", "b")
	s.Start()
	s.newWarning("a", "b", 80)
	s.players["b"].p.combo = 50
	res := s.ApplyAttack("b", proto.AttackRequest{})

	or, _, ok := find[proto.OffsetResolved](res)
	if !ok || or.OffsetAmount != 50 || or.RemainderDakenCount != 3 { // floor(30*0.1)=3
		t.Fatalf("部分相殺 OffsetResolved 不備: %+v ok=%v", or, ok)
	}
	if s.players["b"].stack != 3 {
		t.Fatalf("残余3個が着弾すべき: stack=%d", s.players["b"].stack)
	}
	if di, _, ok := find[proto.DakenIssued](res); !ok || len(di.Daken) != 3 || di.Daken[0].Type != proto.DakenEnemySent {
		t.Fatalf("EnemySent×3 が来ていない: %+v ok=%v", di, ok)
	}
	if lp := s.players["b"].lastImpactor; lp == nil || *lp != "a" {
		t.Fatalf("直近着弾者が a になるべき: %v", lp)
	}
}

// #21c トラップ誘発（ハイウォーターマーク）
func TestStack_TrapMilestone(t *testing.T) {
	s := newTestSession(t, "a", "b")
	s.Start()
	a := s.players["a"]

	res := s.addStack(a, 6) // 6/5=1 > 0 → トラップ1個
	di, _, ok := find[proto.DakenIssued](res)
	if !ok || di.Daken[0].Type != proto.DakenTrap {
		t.Fatalf("トラップが発生すべき: %+v ok=%v", di, ok)
	}
	if su, _, ok := find[proto.DakenStackUpdated](res); !ok || su.Count != 6 || !su.TrapPending {
		t.Fatalf("DakenStackUpdated 不備: %+v ok=%v", su, ok)
	}
	// 同マイルストーン内（7）では再誘発しない。
	res2 := s.addStack(a, 1)
	if _, _, ok := find[proto.DakenIssued](res2); ok {
		t.Fatalf("同マイルストーンでトラップ再発してはいけない")
	}
}

// #21c 脱落＋バッジ総取り
func TestStack_EliminationTransfersBadges(t *testing.T) {
	s := newTestSession(t, "a", "b")
	s.Start()
	s.players["b"].badges = 2

	// a が b へ着弾させ続け、上限到達で脱落 → KO実行者は a。
	res := s.landReceived(s.players["b"], s.params.Stack.Limit, "a")

	ko, _, ok := find[proto.KoNotified](res)
	if !ok || ko.VictimId != "b" || ko.AttackerId == nil || *ko.AttackerId != "a" || ko.BadgesTransferred != 2 {
		t.Fatalf("KoNotified 不備: %+v ok=%v", ko, ok)
	}
	if s.players["a"].badges != 2 {
		t.Fatalf("バッジ総取り: a.badges=%d, want 2", s.players["a"].badges)
	}
	if s.players["b"].alive {
		t.Fatalf("b は脱落しているべき")
	}
}

// #21d 時間切れ→積み残し
func TestDifficulty_TimeoutCarryover(t *testing.T) {
	s := newTestSession(t, "a", "b")
	s.Start()
	res := s.Tick(6000) // base制限時間5000超過

	if _, _, ok := find[proto.DakenExpired](res); !ok {
		t.Fatalf("DakenExpired が来ていない")
	}
	if s.players["a"].stack != 1 {
		t.Fatalf("積み残し+1すべき: a.stack=%d", s.players["a"].stack)
	}
}

// #21d 全体難易度上昇
func TestDifficulty_GlobalRise(t *testing.T) {
	s := newTestSession(t, "a", "b")
	s.Start()
	res := s.Tick(30000) // globalIntervalMs 到達
	if s.globalLevel != 1 {
		t.Fatalf("全体難易度=%d, want 1", s.globalLevel)
	}
	if _, _, ok := find[proto.DifficultyUpdated](res); !ok {
		t.Fatalf("DifficultyUpdated が来ていない")
	}
}

// #21b 予告が猶予超過で着弾する
func TestExpireWarnings_Lands(t *testing.T) {
	s := newTestSession(t, "a", "b")
	s.Start()
	s.newWarning("a", "b", 14) // floor(14*0.1)=1 個着弾予定
	res := s.Tick(2000)         // grace 1500 超過

	if s.players["b"].stack != 1 {
		t.Fatalf("着弾で stack+1 すべき: %d", s.players["b"].stack)
	}
	if di, _, ok := find[proto.DakenIssued](res); !ok || di.Daken[0].Type != proto.DakenEnemySent {
		t.Fatalf("EnemySent 着弾が来ていない: %+v ok=%v", di, ok)
	}
	if len(s.warnings) != 0 {
		t.Fatalf("着弾後は予告が消える: %d 残", len(s.warnings))
	}
}
