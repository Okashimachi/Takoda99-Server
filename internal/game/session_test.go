package game

import (
	"math/rand"
	"testing"

	"textro99/internal/proto"
)

// ── テスト用スタブ（game は部品を import できないので、interface をテスト内で実装）──

type stubWords struct{}

func (stubWords) Next(int, *rand.Rand) Word  { return Word{Text: "ねこ", KeystrokeCount: 4} }
func (stubWords) NextTrap(*rand.Rand) Word    { return Word{Text: "trap", KeystrokeCount: 10} }

// stubStrategy: Id と、対象を返す関数を差し替えられるテスト用作戦。
type stubStrategy struct {
	id int
	fn func(TargetingContext) []PlayerId
}

func (s stubStrategy) Id() int                                { return s.id }
func (s stubStrategy) SelectTargets(c TargetingContext) []PlayerId { return s.fn(c) }

func newTestSession(t *testing.T, ids ...string) *Session {
	t.Helper()
	inits := make([]PlayerInit, len(ids))
	for i, id := range ids {
		inits[i] = PlayerInit{Id: id, DisplayName: id}
	}
	// 作戦4=先頭のOtherを狙う決定的スタブ。
	reg := map[int]TargetingStrategy{
		4: stubStrategy{id: 4, fn: func(c TargetingContext) []PlayerId {
			o := c.Others()
			if len(o) == 0 {
				return nil
			}
			return []PlayerId{o[0].PlayerId}
		}},
	}
	return NewSession("m1", DefaultParameters(), reg, stubWords{}, rand.New(rand.NewSource(1)), inits)
}

// Msg を型で探すヘルパ。
func find[T any](out []Outbound) (T, PlayerId, bool) {
	for _, o := range out {
		if m, ok := o.Msg.(T); ok {
			return m, o.To.PlayerId, true
		}
	}
	var zero T
	return zero, "", false
}

func TestSession_StartEmitsMatchStart(t *testing.T) {
	s := newTestSession(t, "a", "b")
	if s.State() != WaitingStart {
		t.Fatalf("初期状態=%v, want WaitingStart", s.State())
	}
	out := s.Start()
	if s.State() != Running {
		t.Fatalf("Start後=%v, want Running", s.State())
	}
	// 各プレイヤーに MatchStart（初期お題つき）。
	count := 0
	for _, o := range out {
		if ms, ok := o.Msg.(proto.MatchStart); ok {
			count++
			if ms.SelfPlayerId != o.To.PlayerId || ms.InitialDaken.DakenId == "" || len(ms.Players) != 2 {
				t.Fatalf("MatchStart 不備: %+v", ms)
			}
		}
	}
	if count != 2 {
		t.Fatalf("MatchStart 数=%d, want 2", count)
	}
}

func TestSession_ApplyDakenClear_ComboAndNextDaken(t *testing.T) {
	s := newTestSession(t, "a", "b")
	out := s.Start()
	// a の初期お題IDを取得。
	var dakenId proto.DakenId
	for _, o := range out {
		if ms, ok := o.Msg.(proto.MatchStart); ok && o.To.PlayerId == "a" {
			dakenId = ms.InitialDaken.DakenId
		}
	}

	res := s.ApplyDakenClear("a", proto.DakenClearReport{DakenId: dakenId, MissCount: 0})
	cu, _, ok := find[proto.ComboUpdated](res)
	if !ok || cu.Reason != proto.ComboClear || cu.Delta != 14 { // base10 + 4打鍵
		t.Fatalf("ComboUpdated=%+v ok=%v, want delta=14 Clear", cu, ok)
	}
	di, _, ok := find[proto.DakenIssued](res)
	if !ok || len(di.Daken) != 1 || di.Daken[0].DakenId == dakenId {
		t.Fatalf("次のお題が来ていない: %+v", di)
	}
}

func TestSession_ApplyDakenClear_UnknownDakenIdIgnored(t *testing.T) {
	s := newTestSession(t, "a", "b")
	s.Start()
	if res := s.ApplyDakenClear("a", proto.DakenClearReport{DakenId: "bogus"}); res != nil {
		t.Fatalf("未発行dakenIdは無視されるべき: %+v", res)
	}
}

func TestSession_ApplyAttack_NoComboFails(t *testing.T) {
	s := newTestSession(t, "a", "b")
	s.Start()
	res := s.ApplyAttack("a", proto.AttackRequest{})
	af, _, ok := find[proto.AttackFailed](res)
	if !ok || af.Reason != proto.FailNoCombo {
		t.Fatalf("コンボ0で NoCombo が返るべき: %+v ok=%v", af, ok)
	}
}

func TestSession_ApplyAttack_EmitsWarningAndConsumes(t *testing.T) {
	s := newTestSession(t, "a", "b")
	out := s.Start()
	// a にコンボを持たせる。
	var da proto.DakenId
	for _, o := range out {
		if ms, ok := o.Msg.(proto.MatchStart); ok && o.To.PlayerId == "a" {
			da = ms.InitialDaken.DakenId
		}
	}
	s.ApplyDakenClear("a", proto.DakenClearReport{DakenId: da}) // combo=14

	res := s.ApplyAttack("a", proto.AttackRequest{})
	// 全消費で ComboUpdated(Consumed)。
	if cu, _, ok := find[proto.ComboUpdated](res); !ok || cu.Reason != proto.ComboConsumed || cu.ComboValue != 0 {
		t.Fatalf("全消費 ComboUpdated 不備: %+v ok=%v", cu, ok)
	}
	// b へ AttackIncoming。
	ai, toPid, ok := find[proto.AttackIncoming](res)
	if !ok || toPid != "b" || ai.AttackerId != "a" || ai.GraceMs != DefaultParameters().Attack.WarningGraceMs {
		t.Fatalf("AttackIncoming 不備: %+v to=%s ok=%v", ai, toPid, ok)
	}
}

func TestSession_ApplyAttack_NoTargetKeepsCombo(t *testing.T) {
	// 生存が自分だけ → 対象不成立でコンボ非消費。
	s := newTestSession(t, "a", "b")
	out := s.Start()
	var da proto.DakenId
	for _, o := range out {
		if ms, ok := o.Msg.(proto.MatchStart); ok && o.To.PlayerId == "a" {
			da = ms.InitialDaken.DakenId
		}
	}
	s.ApplyDakenClear("a", proto.DakenClearReport{DakenId: da}) // combo=14
	s.eliminate("b")

	res := s.ApplyAttack("a", proto.AttackRequest{})
	if af, _, ok := find[proto.AttackFailed](res); !ok || af.Reason != proto.FailNoTarget {
		t.Fatalf("対象不成立で NoTarget が返るべき: %+v ok=%v", af, ok)
	}
	if s.players["a"].p.Combo() != 14 {
		t.Fatalf("不発時コンボは消費されない: got %d, want 14", s.players["a"].p.Combo())
	}
}

func TestSession_TickFinishesWhenOneLeft(t *testing.T) {
	s := newTestSession(t, "a", "b")
	s.Start()
	if res := s.Tick(150); res != nil { // まだ2人
		t.Fatalf("2人生存中は終了しない: %+v", res)
	}
	s.eliminate("b")
	res := s.Tick(150)
	if s.State() != Finished {
		t.Fatalf("生存1人で Finished になるべき: %v", s.State())
	}
	go1, toPid, ok := find[proto.GameOver](res)
	if !ok || toPid != "a" || go1.Rank != 1 {
		t.Fatalf("優勝者へ GameOver(rank1): %+v to=%s ok=%v", go1, toPid, ok)
	}
}
