package store

import (
	"context"
	"testing"
)

// Noop は ResultStore を満たし、Save は常に nil。
func TestNoop_SaveReturnsNil(t *testing.T) {
	var s ResultStore = Noop{}
	if err := s.Save(context.Background(), Result{MatchId: "m1", PlayerId: "a", Rank: 1}); err != nil {
		t.Fatalf("Noop.Save は nil を返すべき: %v", err)
	}
}
