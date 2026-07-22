package targeting

import (
	"math/rand"
	"testing"

	"textro99/internal/game"
)

func ctxOf(self string, alive []game.PlayerView, seed int64) game.TargetingContext {
	return game.TargetingContext{
		SelfId: self,
		Alive:  alive,
		Rng:    rand.New(rand.NewSource(seed)),
	}
}

// Others は自分を除外する。
func TestContext_Others_ExcludesSelf(t *testing.T) {
	c := ctxOf("me", []game.PlayerView{{PlayerId: "me"}, {PlayerId: "a"}, {PlayerId: "b"}}, 1)
	got := c.Others()
	if len(got) != 2 {
		t.Fatalf("len(Others)=%d, want 2", len(got))
	}
	for _, p := range got {
		if p.PlayerId == "me" {
			t.Fatalf("Others に自分が含まれている")
		}
	}
}

// 作戦4: 常に自分以外を1名返す。
func TestRandomStrategy_ReturnsOneOther(t *testing.T) {
	c := ctxOf("me", []game.PlayerView{{PlayerId: "me"}, {PlayerId: "a"}, {PlayerId: "b"}}, 42)
	got := RandomStrategy{}.SelectTargets(c)
	if len(got) != 1 || got[0] == "me" {
		t.Fatalf("got %v, want 自分以外1名", got)
	}
}

// 母集団が空（自分だけ生存）なら不発。
func TestRandomStrategy_NoOthers(t *testing.T) {
	c := ctxOf("me", []game.PlayerView{{PlayerId: "me"}}, 1)
	if got := (RandomStrategy{}).SelectTargets(c); len(got) != 0 {
		t.Fatalf("got %v, want 空（不発）", got)
	}
}

// 作戦3: badgeCount 最大を選ぶ。
func TestBadgeHunterStrategy_PicksMaxBadge(t *testing.T) {
	c := ctxOf("me", []game.PlayerView{
		{PlayerId: "me", BadgeCount: 9},
		{PlayerId: "a", BadgeCount: 2},
		{PlayerId: "b", BadgeCount: 5},
	}, 1)
	got := BadgeHunterStrategy{}.SelectTargets(c)
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("got %v, want [b]（自分の9は除外し、他者最大のb=5）", got)
	}
}

// Registry: 未登録idは空、登録済みは委譲。
func TestRegistry_Resolve(t *testing.T) {
	r := NewRegistry()
	r.Register(RandomStrategy{})
	c := ctxOf("me", []game.PlayerView{{PlayerId: "me"}, {PlayerId: "a"}}, 1)

	if got := r.Resolve(4, c); len(got) != 1 || got[0] != "a" {
		t.Fatalf("id=4: got %v, want [a]", got)
	}
	if got := r.Resolve(7, c); got != nil {
		t.Fatalf("未登録id=7: got %v, want nil", got)
	}
}
