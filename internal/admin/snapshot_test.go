package admin

import (
	"encoding/json"
	"math/rand"
	"testing"

	"takoda99/internal/game"
)

type fakeWords struct{}

func (fakeWords) Next(int, *rand.Rand) game.Word { return game.Word{Text: "たこ", KeystrokeCount: 4} }

func newSession(n int) *game.Session {
	p := game.DefaultParameters()
	p.Matching.ReadyCountdownMs = 0
	p.Matching.RosterWaitMs = 0
	inits := make([]game.PlayerInit, n)
	for i := range inits {
		id := game.PlayerId(string(rune('a' + i)))
		inits[i] = game.PlayerInit{Id: id, DisplayName: string(id)}
	}
	return game.NewSession("m-test", p, fakeWords{}, rand.New(rand.NewSource(7)), inits)
}

// BuildSnapshot は Start 後に matchId/phase/restPool/客総数/店数を正しく反映する。
func TestBuildSnapshot_Basic(t *testing.T) {
	s := newSession(3)
	s.Start(0)

	snap := BuildSnapshot(s)
	if snap.MatchId != "m-test" {
		t.Fatalf("MatchId=%q, want m-test", snap.MatchId)
	}
	if snap.Phase != "Early" {
		t.Fatalf("Phase=%q, want Early", snap.Phase)
	}
	if snap.AliveCount != 3 || len(snap.Stores) != 3 {
		t.Fatalf("AliveCount=%d stores=%d, want 3/3", snap.AliveCount, len(snap.Stores))
	}
	total := snap.Customers.Normal + snap.Customers.Bonus + snap.Customers.Claimer + snap.Customers.Buzz
	if total != s.Params().Customer.Total {
		t.Fatalf("Customers 総和=%d, want %d", total, s.Params().Customer.Total)
	}
	if snap.RestPool != s.Params().Customer.Total {
		t.Fatalf("RestPool=%d, want %d（分配前は全員 restPool）", snap.RestPool, s.Params().Customer.Total)
	}
	// restByAttr の総和も Total（分配前は全員 restPool）。
	rest := snap.RestByAttr.Normal + snap.RestByAttr.Bonus + snap.RestByAttr.Claimer + snap.RestByAttr.Buzz
	if rest != s.Params().Customer.Total {
		t.Fatalf("RestByAttr 総和=%d, want %d", rest, s.Params().Customer.Total)
	}
}

// SnapshotEnvelope は type=AdminSnapshot で包み、payload が AdminSnapshot に復号できる。
func TestSnapshotEnvelope_TypeAndPayload(t *testing.T) {
	s := newSession(2)
	s.Start(0)

	env, ok := SnapshotEnvelope(s)
	if !ok {
		t.Fatal("SnapshotEnvelope ok=false")
	}
	if env.Type != TypeAdminSnapshot {
		t.Fatalf("Type=%q, want %q", env.Type, TypeAdminSnapshot)
	}
	var snap AdminSnapshot
	if err := json.Unmarshal(env.Payload, &snap); err != nil {
		t.Fatalf("payload 復号失敗: %v", err)
	}
	if len(snap.Stores) != 2 {
		t.Fatalf("復号後 stores=%d, want 2", len(snap.Stores))
	}
	// 生存店は finalRank キーを出さない（omitempty＋nil）。
	for _, st := range snap.Stores {
		if st.Alive && st.FinalRank != nil {
			t.Fatalf("生存店に finalRank が入っている: %+v", st)
		}
	}
}
