package targeting

import (
	"math/rand"
	"testing"

	"textro99/internal/game"
)

func one(t *testing.T, got []game.PlayerId, want game.PlayerId) {
	t.Helper()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %v, want [%s]", got, want)
	}
}

// 作戦0: 自分以外の全員（複数件）。
func TestSplit_AllOthers(t *testing.T) {
	c := ctxOf("me", []game.PlayerView{{PlayerId: "me"}, {PlayerId: "a"}, {PlayerId: "b"}}, 1)
	got := SplitAttackStrategy{}.SelectTargets(c)
	if len(got) != 2 {
		t.Fatalf("作戦0は自分以外全員(2件)のはず: %v", got)
	}
	for _, id := range got {
		if id == "me" {
			t.Fatal("自分を含んではいけない")
		}
	}
}

// 作戦1: 最新の予告主（生存）を狙う。予告なし→ランダム。死んだ予告主はスキップ。
func TestCounter(t *testing.T) {
	c := game.TargetingContext{
		SelfId: "me", Alive: []game.PlayerView{{PlayerId: "me"}, {PlayerId: "a"}, {PlayerId: "b"}},
		PendingAttackers: []game.PlayerId{"b", "a"}, Rng: rand.New(rand.NewSource(1)),
	}
	one(t, CounterStrategy{}.SelectTargets(c), "b") // 新しい順の先頭 b

	// 先頭が既に脱落 → 次の生存 a。
	c.PendingAttackers = []game.PlayerId{"dead", "a"}
	one(t, CounterStrategy{}.SelectTargets(c), "a")

	// 予告なし → ランダム（他が a のみなら a）。
	c2 := ctxOf("me", []game.PlayerView{{PlayerId: "me"}, {PlayerId: "a"}}, 1)
	one(t, CounterStrategy{}.SelectTargets(c2), "a")
}

// 作戦2: スタック比率(count/limit)最大。
func TestFinisher_MaxStackRatio(t *testing.T) {
	c := ctxOf("me", []game.PlayerView{
		{PlayerId: "me", DakenStackCount: 5, DakenStackLimit: 20},
		{PlayerId: "a", DakenStackCount: 2, DakenStackLimit: 20},
		{PlayerId: "b", DakenStackCount: 15, DakenStackLimit: 20},
	}, 1)
	one(t, FinisherStrategy{}.SelectTargets(c), "b")
}

// 作戦5: 直近着弾者（生存）を狙う。nil/脱落→ランダム。
func TestRevenge(t *testing.T) {
	imp := game.PlayerId("a")
	c := game.TargetingContext{
		SelfId: "me", Alive: []game.PlayerView{{PlayerId: "me"}, {PlayerId: "a"}},
		LastImpactorId: &imp, Rng: rand.New(rand.NewSource(1)),
	}
	one(t, RevengeStrategy{}.SelectTargets(c), "a")

	// 履歴なし → ランダム（b のみ）。
	c2 := ctxOf("me", []game.PlayerView{{PlayerId: "me"}, {PlayerId: "b"}}, 1)
	one(t, RevengeStrategy{}.SelectTargets(c2), "b")

	// 着弾者が脱落済 → ランダム（b のみ）。
	dead := game.PlayerId("gone")
	c3 := game.TargetingContext{SelfId: "me", Alive: []game.PlayerView{{PlayerId: "me"}, {PlayerId: "b"}}, LastImpactorId: &dead, Rng: rand.New(rand.NewSource(1))}
	one(t, RevengeStrategy{}.SelectTargets(c3), "b")
}

// 作戦6: コンボ最大。
func TestTallPoppy_MaxCombo(t *testing.T) {
	c := ctxOf("me", []game.PlayerView{
		{PlayerId: "me", ComboValue: 99}, {PlayerId: "a", ComboValue: 10}, {PlayerId: "b", ComboValue: 50},
	}, 1)
	one(t, TallPoppyStrategy{}.SelectTargets(c), "b") // 自分99は除外、他者最大b
}

// 作戦7: IDソート順で自分の次（ラップ）。生存2人未満→不発。
func TestNeighbor(t *testing.T) {
	// ソート a,b,me → me の次は先頭 a（ラップ）。
	c := ctxOf("me", []game.PlayerView{{PlayerId: "me"}, {PlayerId: "a"}, {PlayerId: "b"}}, 1)
	one(t, NeighborStrategy{}.SelectTargets(c), "a")

	if got := (NeighborStrategy{}).SelectTargets(ctxOf("me", []game.PlayerView{{PlayerId: "me"}}, 1)); len(got) != 0 {
		t.Fatalf("生存2人未満は不発: %v", got)
	}
}

// 作戦8: 被予告最多。全員0→ランダム。
func TestPileOn(t *testing.T) {
	c := ctxOf("me", []game.PlayerView{
		{PlayerId: "me"}, {PlayerId: "a", IncomingWarnings: 1}, {PlayerId: "b", IncomingWarnings: 3},
	}, 1)
	one(t, PileOnStrategy{}.SelectTargets(c), "b")

	c2 := ctxOf("me", []game.PlayerView{{PlayerId: "me"}, {PlayerId: "a"}}, 1) // 全員0
	one(t, PileOnStrategy{}.SelectTargets(c2), "a")
}

// 作戦9: 被予告0の相手を狙う。該当なし→ランダム。
func TestPacifist(t *testing.T) {
	c := ctxOf("me", []game.PlayerView{
		{PlayerId: "me"}, {PlayerId: "a", IncomingWarnings: 2}, {PlayerId: "b", IncomingWarnings: 0},
	}, 1)
	one(t, PacifistHunterStrategy{}.SelectTargets(c), "b")

	c2 := ctxOf("me", []game.PlayerView{{PlayerId: "me"}, {PlayerId: "a", IncomingWarnings: 1}}, 1) // 平和な相手なし
	one(t, PacifistHunterStrategy{}.SelectTargets(c2), "a")
}

// 不変条件: 1〜9 は0/1件、0だけ複数可。
func TestStrategies_CardinalityInvariant(t *testing.T) {
	all := []game.TargetingStrategy{
		SplitAttackStrategy{}, CounterStrategy{}, FinisherStrategy{}, BadgeHunterStrategy{}, RandomStrategy{},
		RevengeStrategy{}, TallPoppyStrategy{}, NeighborStrategy{}, PileOnStrategy{}, PacifistHunterStrategy{},
	}
	alive := []game.PlayerView{
		{PlayerId: "me"}, {PlayerId: "a", IncomingWarnings: 1, ComboValue: 5, DakenStackCount: 3, DakenStackLimit: 20},
		{PlayerId: "b", ComboValue: 9, DakenStackCount: 7, DakenStackLimit: 20},
	}
	for _, s := range all {
		c := ctxOf("me", alive, 1)
		got := s.SelectTargets(c)
		if s.Id() == 0 {
			if len(got) != 2 {
				t.Fatalf("作戦0は全員(2件): got %v", got)
			}
			continue
		}
		if len(got) > 1 {
			t.Fatalf("作戦%d は0/1件のはず: got %v", s.Id(), got)
		}
	}
}
