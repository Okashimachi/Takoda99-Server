package proto

import (
	"encoding/json"
	"testing"
)

func TestWireGolden(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		// ── S2C ──
		{"MatchStart",
			MatchStart{
				MatchId: "m-1", SelfStoreId: "s1",
				Params: GameParametersPublicSubset{MatchTimeLimitMs: 0, InitialLife: 3, MaxStores: 99},
				Phase:  PhaseEarly,
				Stores: []StoreSummary{{StoreId: "s1", DisplayName: "s1", EvalNormalized: 0, Rank: 0, CreditLife: 3, Alive: true}},
			},
			`{"matchId":"m-1","selfStoreId":"s1","params":{"matchTimeLimitMs":0,"initialLife":3,"maxStores":99},"phase":"Early","stores":[{"storeId":"s1","displayName":"s1","evalNormalized":0,"rank":0,"creditLife":3,"alive":true}]}`},
		{"CustomerArrived",
			CustomerView{CustomerId: "c-1", Attribute: AttrNormal, OrderCount: 2, Words: []string{"ねこ", "いぬ"}, PatienceMaxMs: 8000},
			`{"customerId":"c-1","attribute":"Normal","orderCount":2,"words":["ねこ","いぬ"],"patienceMaxMs":8000}`},
		{"CustomerLeft",
			CustomerLeft{CustomerId: "c-1", Reason: LeaveTimeout},
			`{"customerId":"c-1","reason":"Timeout"}`},
		{"CreditUpdate",
			CreditUpdate{Life: 2, Delta: -1, Reason: CreditCustomerLeft},
			`{"life":2,"delta":-1,"reason":"CustomerLeft"}`},
		{"EvaluationUpdate",
			EvaluationUpdate{EvalRaw: 0.5, Normalized: 0.8, Rank: 3, AliveCount: 50},
			`{"evalRaw":0.5,"normalized":0.8,"rank":3,"aliveCount":50}`},
		{"DifficultyUpdate",
			DifficultyUpdate{HeatLevel: 5},
			`{"heatLevel":5}`},
		{"PhaseChange",
			PhaseChange{Phase: PhaseMid},
			`{"phase":"Mid"}`},
		{"StoreListUpdate",
			StoreListUpdate{Stores: []StoreSummary{{StoreId: "s1", DisplayName: "s1", Alive: true, CreditLife: 3}}, AliveCount: 1},
			`{"stores":[{"storeId":"s1","displayName":"s1","evalNormalized":0,"rank":0,"creditLife":3,"alive":true}],"aliveCount":1}`},
		{"ForcedEliminationWarning",
			ForcedEliminationWarning{UntilTick: 10, ThresholdPct: 0.1},
			`{"untilTick":10,"thresholdPct":0.1}`},
		{"StoreEliminated",
			StoreEliminated{StoreId: "s1", Reason: ElimSelfCollapse, FinalRank: 50},
			`{"storeId":"s1","reason":"SelfCollapse","finalRank":50}`},
		{"MatchEnd",
			MatchEnd{FinalRank: 1, Stats: MatchStats{ServedCount: 10, AvgAccuracy: 0.95, AvgElapsedMs: 2000}},
			`{"finalRank":1,"stats":{"servedCount":10,"avgAccuracy":0.95,"avgElapsedMs":2000}}`},
		{"MatchmakingStatus/待機",
			MatchmakingStatus{WaitingCount: 3, MinPlayers: 20},
			`{"waitingCount":3,"minPlayers":20}`},

		// ── C2S ──
		{"OrderServed",
			OrderServed{CustomerId: "c-1", ElapsedMs: 1200, MissCount: 2, ClientTimestamp: 12345},
			`{"customerId":"c-1","elapsedMs":1200,"missCount":2,"clientTimestamp":12345}`},
	}

	for _, c := range cases {
		got, err := json.Marshal(c.v)
		if err != nil {
			t.Errorf("%s: marshal error: %v", c.name, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("%s: ワイヤー形式が契約とズレている\n  got  %s\n  want %s", c.name, got, c.want)
		}
	}
}
