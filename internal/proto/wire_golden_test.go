package proto

import (
	"encoding/json"
	"testing"
)

// TestWireGolden は server が送受信する on-wire JSON の形を canonical proto v0.8.0 に対して固定する。
//
// v0.8.0（本戦ルール）で期待値を更新した。変わったのは2点:
//   - フィールドの追加（score / cullSchedule / ranking 系 / untilMs 等）
//   - **既存フィールドの並び順**（廃止フィールドが struct の末尾へ移されたため）
//
// 廃止（Deprecated）フィールドは方式Bで型定義に残っているので JSON にも出続ける。
// **ここにゼロ値で出ていることは「サーバーがもう値を入れない」ことの固定**であって、
// 使ってよいという意味ではない（h21〜h23 でサーバー側の参照そのものを消す）。
func TestWireGolden(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		// ── S2C ──
		{"MatchStart/v0.8.0 は cullSchedule と score 重みを配る",
			MatchStart{
				MatchId: "m-1", SelfStoreId: "s1",
				Params: GameParametersPublicSubset{
					MaxStores: 99,
					CullSchedule: []CullStageView{
						{AtMs: 20000, TargetAliveCount: 75},
						{AtMs: 120000, TargetAliveCount: 0},
					},
					ScoreWeightTakoyaki: 100, ScoreWeightMiss: 30,
					FinalStageAliveThreshold: 20, FinalRushAliveThreshold: 10,
				},
				Phase:  PhaseEarly,
				Stores: []StoreSummary{{StoreId: "s1", DisplayName: "s1", Rank: 0, Alive: true, Score: 0}},
			},
			`{"matchId":"m-1","selfStoreId":"s1","params":{"maxStores":99,"cullSchedule":[{"atMs":20000,"targetAliveCount":75},{"atMs":120000,"targetAliveCount":0}],"scoreWeightTakoyaki":100,"scoreWeightMiss":30,"finalStageAliveThreshold":20,"finalRushAliveThreshold":10,"initialLife":0,"stormThresholdPct":0,"patienceLateMul":0,"patienceAlertMs":0},"phase":"Early","stores":[{"storeId":"s1","displayName":"s1","rank":0,"alive":true,"score":0,"evalNormalized":0,"creditLife":0}],"startsAtServerMs":0}`},
		{"CustomerArrived/我慢ゲージは廃止（サーバーは値を入れない）",
			CustomerView{CustomerId: "c-1", Attribute: AttrNormal, OrderCount: 2, Words: []string{"ねこ", "いぬ"}},
			`{"customerId":"c-1","attribute":"Normal","orderCount":2,"words":["ねこ","いぬ"],"patienceMaxMs":0,"patienceStartedAtServerMs":0}`},
		{"CustomerLeft",
			CustomerLeft{CustomerId: "c-1", Reason: LeaveTimeout},
			`{"customerId":"c-1","reason":"Timeout"}`},
		{"CreditUpdate",
			CreditUpdate{Life: 2, Delta: -1, Reason: CreditCustomerLeft},
			`{"life":2,"delta":-1,"reason":"CustomerLeft"}`},
		{"EvaluationUpdate/自店の順位の権威は score と rank",
			EvaluationUpdate{Score: 12300, Rank: 3, AliveCount: 50},
			`{"score":12300,"rank":3,"aliveCount":50,"evalRaw":0,"normalized":0,"starRating":0,"starDelta":0}`},
		{"DifficultyUpdate",
			DifficultyUpdate{HeatLevel: 5},
			`{"heatLevel":5}`},
		{"PhaseChange",
			PhaseChange{Phase: PhaseMid},
			`{"phase":"Mid"}`},
		{"StoreListUpdate/生存店はfinalRankを出さない",
			StoreListUpdate{Stores: []StoreSummary{{StoreId: "s1", DisplayName: "s1", Alive: true, Score: 4200}}, AliveCount: 1},
			`{"stores":[{"storeId":"s1","displayName":"s1","rank":0,"alive":true,"score":4200,"evalNormalized":0,"creditLife":0}],"aliveCount":1}`},
		{"StoreListUpdate/脱落店はfinalRankを出す",
			StoreListUpdate{Stores: []StoreSummary{{StoreId: "s2", DisplayName: "s2", Alive: false, Score: -60, FinalRank: ptrInt(42)}}, AliveCount: 1},
			`{"stores":[{"storeId":"s2","displayName":"s2","rank":0,"alive":false,"score":-60,"evalNormalized":0,"creditLife":0,"finalRank":42}],"aliveCount":1}`},
		{"ForcedEliminationWarning/常時配信の秒読みとカットライン",
			ForcedEliminationWarning{
				UntilMs: 8200, StageIndex: 3, StageTotal: 6, CutLineRank: 36,
				CutStoreIds: []StoreId{"s-40", "s-41"}, SelfAtRisk: true,
			},
			`{"untilMs":8200,"stageIndex":3,"stageTotal":6,"cutLineRank":36,"cutStoreIds":["s-40","s-41"],"selfAtRisk":true,"untilTick":0,"thresholdPct":0}`},
		{"StoreEliminated/v0.8.0 の脱落経路は足切りの1本だけ",
			StoreEliminated{StoreId: "s1", Reason: ElimCull, FinalRank: 50},
			`{"storeId":"s1","reason":"Cull","finalRank":50}`},
		{"StoreEliminatedBatch/1回の足切りは1メッセージに畳む",
			StoreEliminatedBatch{
				StageIndex: 1,
				Entries: []StoreEliminated{
					{StoreId: "s-98", Reason: ElimCull, FinalRank: 98},
					{StoreId: "s-99", Reason: ElimCull, FinalRank: 99},
				},
			},
			`{"stageIndex":1,"entries":[{"storeId":"s-98","reason":"Cull","finalRank":98},{"storeId":"s-99","reason":"Cull","finalRank":99}]}`},
		{"RankingSnapshot/全量。生存店は現在順位・脱落店は確定順位",
			RankingSnapshot{Entries: []RankingEntry{
				{StoreId: "s-1", Rank: 1, Score: 12300, Alive: true},
				{StoreId: "s-2", Rank: 99, Score: -60, Alive: false},
			}},
			`{"entries":[{"storeId":"s-1","rank":1,"score":12300,"alive":true},{"storeId":"s-2","rank":99,"score":-60,"alive":false}]}`},
		{"RankingDelta/差分は rank を持たない",
			RankingDelta{Entries: []RankingChange{{StoreId: "s-1", Score: 12400, Alive: true}}},
			`{"entries":[{"storeId":"s-1","score":12400,"alive":true}]}`},
		{"PersonalResult/優勝（score と takoyakiCount が本体・ミス総数は stats 側）",
			PersonalResult{
				FinalRank: 1,
				Stats: MatchStats{
					ServedCount: 10, AvgAccuracy: 0.95, AvgElapsedMs: 2000,
					TotalKeystrokes: 120, TotalMisses: 6,
					FastestMs: 1500, SlowestMs: 3000,
					Normal: AttributeTally{Served: 7}, Buzz: AttributeTally{Served: 3},
				},
				SurvivedMs: 120000, Score: 12300, TakoyakiCount: 34,
			},
			`{"finalRank":1,"stats":{"servedCount":10,"avgAccuracy":0.95,"avgElapsedMs":2000,"leftCount":0,"totalKeystrokes":120,"totalMisses":6,"fastestMs":1500,"slowestMs":3000,"normal":{"served":7,"left":0},"bonus":{"served":0,"left":0},"claimer":{"served":0,"left":0},"buzz":{"served":3,"left":0}},"survivedMs":120000,"score":12300,"takoyakiCount":34,"creditLeft":0,"evalRaw":0,"evalNormalized":0}`},
		{"PersonalResult/足切り脱落（reason は入れない＝省略される）",
			PersonalResult{FinalRank: 57, SurvivedMs: 90000, Score: -120, TakoyakiCount: 8},
			`{"finalRank":57,"stats":{"servedCount":0,"avgAccuracy":0,"avgElapsedMs":0,"leftCount":0,"totalKeystrokes":0,"totalMisses":0,"fastestMs":0,"slowestMs":0,"normal":{"served":0,"left":0},"bonus":{"served":0,"left":0},"claimer":{"served":0,"left":0},"buzz":{"served":0,"left":0}},"survivedMs":90000,"score":-120,"takoyakiCount":8,"creditLeft":0,"evalRaw":0,"evalNormalized":0}`},
		{"MatchEnd/全体終了",
			MatchEnd{},
			`{}`},
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
