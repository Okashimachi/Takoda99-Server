package store

import (
	"context"
	"testing"
)

func TestNoop_SaveMatchReturnsNil(t *testing.T) {
	var s ResultStore = Noop{}
	if err := s.SaveMatch(context.Background(), MatchResult{MatchId: "m1"}); err != nil {
		t.Fatalf("Noop.SaveMatch は nil を返すべき: %v", err)
	}
}
