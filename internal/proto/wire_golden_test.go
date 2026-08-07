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
				Params: GameParametersPublicSubset{InitialLife: 3, MaxStores: 99, StormThresholdPct: 0.1, FinalStageAliveThreshold: 20, FinalRushAliveThreshold: 10, PatienceLateMul: 0.6, PatienceAlertMs: 2000},
				Phase:  PhaseEarly,
				Stores: []StoreSummary{{StoreId: "s1", DisplayName: "s1", EvalNormalized: 0, Rank: 0, CreditLife: 3, Alive: true}},
			},
			`{"matchId":"m-1","selfStoreId":"s1","params":{"initialLife":3,"maxStores":99,"stormThresholdPct":0.1,"finalStageAliveThreshold":20,"finalRushAliveThreshold":10,"patienceLateMul":0.6,"patienceAlertMs":2000},"phase":"Early","stores":[{"storeId":"s1","displayName":"s1","evalNormalized":0,"rank":0,"creditLife":3,"alive":true}],"startsAtServerMs":0}`},
		{"CustomerArrived",
			CustomerView{CustomerId: "c-1", Attribute: AttrNormal, OrderCount: 2, Words: []string{"ねこ", "いぬ"}, PatienceMaxMs: 8000, PatienceStartedAtServerMs: 1500},
			`{"customerId":"c-1","attribute":"Normal","orderCount":2,"words":["ねこ","いぬ"],"patienceMaxMs":8000,"patienceStartedAtServerMs":1500}`},
		{"CustomerLeft",
			CustomerLeft{CustomerId: "c-1", Reason: LeaveTimeout},
			`{"customerId":"c-1","reason":"Timeout"}`},
		{"CreditUpdate",
			CreditUpdate{Life: 2, Delta: -1, Reason: CreditCustomerLeft},
			`{"life":2,"delta":-1,"reason":"CustomerLeft"}`},
		{"EvaluationUpdate",
			EvaluationUpdate{EvalRaw: 0.5, Normalized: 0.8, Rank: 3, AliveCount: 50, StarRating: 4.8, StarDelta: 0.2},
			`{"evalRaw":0.5,"normalized":0.8,"rank":3,"aliveCount":50,"starRating":4.8,"starDelta":0.2}`},
		{"DifficultyUpdate",
			DifficultyUpdate{HeatLevel: 5},
			`{"heatLevel":5}`},
		{"PhaseChange",
			PhaseChange{Phase: PhaseMid},
			`{"phase":"Mid"}`},
		{"StoreListUpdate/生存店はfinalRankを出さない",
			StoreListUpdate{Stores: []StoreSummary{{StoreId: "s1", DisplayName: "s1", Alive: true, CreditLife: 3}}, AliveCount: 1},
			`{"stores":[{"storeId":"s1","displayName":"s1","evalNormalized":0,"rank":0,"creditLife":3,"alive":true}],"aliveCount":1}`},
		{"StoreListUpdate/脱落店はfinalRankを出す",
			StoreListUpdate{Stores: []StoreSummary{{StoreId: "s2", DisplayName: "s2", Alive: false, FinalRank: ptrInt(42)}}, AliveCount: 1},
			`{"stores":[{"storeId":"s2","displayName":"s2","evalNormalized":0,"rank":0,"creditLife":0,"alive":false,"finalRank":42}],"aliveCount":1}`},
		{"ForcedEliminationWarning",
			ForcedEliminationWarning{UntilTick: 10, ThresholdPct: 0.1, SelfAtRisk: true},
			`{"untilTick":10,"thresholdPct":0.1,"selfAtRisk":true}`},
		{"StoreEliminated",
			StoreEliminated{StoreId: "s1", Reason: ElimSelfCollapse, FinalRank: 50},
			`{"storeId":"s1","reason":"SelfCollapse","finalRank":50}`},
		{"MatchEnd/優勝（reason は省略される）",
			MatchEnd{
				FinalRank: 1,
				Stats: MatchStats{
					ServedCount: 10, AvgAccuracy: 0.95, AvgElapsedMs: 2000,
					LeftCount: 2, TotalKeystrokes: 120, TotalMisses: 6,
					FastestMs: 1500, SlowestMs: 3000,
					Normal: AttributeTally{Served: 7, Left: 2}, Buzz: AttributeTally{Served: 3},
				},
				MatchElapsedMs: 145000, CreditLeft: 8, EvalRaw: 0.72, EvalNormalized: 1,
			},
			`{"finalRank":1,"stats":{"servedCount":10,"avgAccuracy":0.95,"avgElapsedMs":2000,"leftCount":2,"totalKeystrokes":120,"totalMisses":6,"fastestMs":1500,"slowestMs":3000,"normal":{"served":7,"left":2},"bonus":{"served":0,"left":0},"claimer":{"served":0,"left":0},"buzz":{"served":3,"left":0}},"matchElapsedMs":145000,"creditLeft":8,"evalRaw":0.72,"evalNormalized":1}`},
		{"MatchEnd/自滅（reason あり）",
			MatchEnd{FinalRank: 57, Reason: ElimSelfCollapse, MatchElapsedMs: 90000},
			`{"finalRank":57,"stats":{"servedCount":0,"avgAccuracy":0,"avgElapsedMs":0,"leftCount":0,"totalKeystrokes":0,"totalMisses":0,"fastestMs":0,"slowestMs":0,"normal":{"served":0,"left":0},"bonus":{"served":0,"left":0},"claimer":{"served":0,"left":0},"buzz":{"served":0,"left":0}},"reason":"SelfCollapse","matchElapsedMs":90000,"creditLeft":0,"evalRaw":0,"evalNormalized":0}`},
		{"MatchmakingStatus/待機（宛先ごとに selfStoreId が違う）",
			MatchmakingStatus{
				WaitingCount: 2, MinPlayers: 20, SelfStoreId: "p-2",
				Participants: []MatchmakingParticipant{
					{StoreId: "p-1", DisplayName: "たこ焼き", IsBot: false},
					{StoreId: "p-2", DisplayName: "ゲスト2", IsBot: false},
				},
			},
			`{"waitingCount":2,"minPlayers":20,"selfStoreId":"p-2","participants":[{"storeId":"p-1","displayName":"たこ焼き","isBot":false},{"storeId":"p-2","displayName":"ゲスト2","isBot":false}]}`},

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

func ptrInt(v int) *int { return &v }
