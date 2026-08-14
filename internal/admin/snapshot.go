package admin

import (
	"encoding/json"

	"takoda99/internal/game"
	"takoda99/internal/proto"
)

// TypeAdminSnapshot は /admin/ws で AdminSnapshot を包む Envelope の type タグ（plan-h00 §4）。
// h01 の "StoreListUpdate" を置き換える新 type。front は未知 type を無視する前方互換なので、
// h01 の front でも壊れない（描画されないだけ）。
const TypeAdminSnapshot = "AdminSnapshot"

// AdminSnapshot は運営/開発者向けの観測スナップショット（plan-h02）。
//
// **proto 契約ではない**（Unity クライアントには送らない内部 DTO）。Takoda99-Proto に足さない。
// room が publish 直後に session の純粋 getter を読んで組み立て、/admin/ws へ配信する。
type AdminSnapshot struct {
	MatchId    string       `json:"matchId"`
	ElapsedMs  int64        `json:"elapsedMs"`
	Phase      string       `json:"phase"` // Early/Mid/Late
	HeatLevel  int          `json:"heatLevel"`
	AliveCount int          `json:"aliveCount"`
	RestPool   int          `json:"restPool"` // 未割当客（たべたべエリア）数
	Cull       AdminCull    `json:"cull"`
	Customers  AdminMix     `json:"customers"`  // 在場の属性別総数
	RestByAttr AdminMix     `json:"restByAttr"` // restPool の属性別内訳（客フロー用）
	Stores     []AdminStore `json:"stores"`     // 99店
}

// AdminCull は次の時刻足切りの状態（本戦・plan-h22）。
//
// 予選の AdminStorm（warning / untilTick / thresholdPct）を置き換えたもの。
// 本戦の予告は常時なので「予告中かどうか」のフラグは無い。
// h21 の Score 化と同じく、これは**コンパイルを通すための最小対応**。
// 足切りの可視化（タイムライン・カットライン帯）は h25。
type AdminCull struct {
	StageIndex       int `json:"stageIndex"` // 次のステージ（1始まり）。消化済みなら 0
	StageTotal       int `json:"stageTotal"`
	UntilMs          int `json:"untilMs"`
	TargetAliveCount int `json:"targetAliveCount"`
	CutLineRank      int `json:"cutLineRank"`
}

// AdminMix は客属性別の人数（Normal/Bonus=おばちゃん/Claimer/Buzz=JK）。
type AdminMix struct {
	Normal  int `json:"normal"`
	Bonus   int `json:"bonus"`
	Claimer int `json:"claimer"`
	Buzz    int `json:"buzz"`
}

func mixOf(a game.AttrCounts) AdminMix {
	return AdminMix{Normal: a.Normal, Bonus: a.Bonus, Claimer: a.Claimer, Buzz: a.Buzz}
}

// AdminStore は1店の観測情報（店舗盤面＋スコア分布＋客フロー用）。
//
// 本戦（h21 で score 化 / h22 で cull 化 / h25 で分布ビュー用の内訳追加）。
// 信用(creditLife)・相対評価(evalNormalized)は廃止済みで、ここには存在しない。
type AdminStore struct {
	StoreId     string `json:"storeId"`
	DisplayName string `json:"displayName"`
	Alive       bool   `json:"alive"`
	Rank        int    `json:"rank"`
	FinalRank   *int   `json:"finalRank,omitempty"` // 脱落済みのみ

	// Score は順位を決める値（負値あり）。TakoyakiCount / MissCount はその内訳。
	// 「速いがミスも多い店」と「遅いが正確な店」のどちらが勝つかを見る（h26 の P3）。
	Score         int `json:"score"`
	TakoyakiCount int `json:"takoyakiCount"`
	MissCount     int `json:"missCount"`

	// IsBot は Bot（CPU補完）かどうか。**ボットが上位を占めていないか**の観測に要る（h26 の P1）。
	//
	// game は Bot と人間を区別しない（AGENTS.md §4.2）ので、合成ルート（app.RunMatch）が
	// 持つ botIds を room 経由で渡してもらう。store.Result.IsBot と同じ流儀。
	IsBot bool `json:"isBot"`

	QueueLen    int      `json:"queueLen"`
	ServedCount int      `json:"servedCount"`
	AtRisk      bool     `json:"atRisk"`
	QueueByAttr AdminMix `json:"queueByAttr"`
}

// BuildSnapshot は session の純粋 getter を読んで AdminSnapshot を組む。
//
// room の単一 goroutine（publish 直後）から呼ばれる前提。session を触るのは room だけなので
// getter 読み出しはデータ競合しない（plan-h02 §1.3）。
func BuildSnapshot(s *game.Session, botIds map[game.PlayerId]bool) AdminSnapshot {
	board := s.StoreBoard()
	cull := s.CullState()

	stores := make([]AdminStore, 0, len(board))
	for _, r := range board {
		as := AdminStore{
			StoreId:       string(r.Id),
			DisplayName:   r.Name,
			Alive:         r.Alive,
			Rank:          r.Rank,
			Score:         r.Score,
			TakoyakiCount: r.TakoyakiCount,
			MissCount:     r.MissCount,
			IsBot:         botIds[r.Id],
			QueueLen:      r.QueueLen,
			ServedCount:   r.ServedCount,
			AtRisk:        r.AtRisk,
			QueueByAttr:   mixOf(r.QueueByAttr),
		}
		if !r.Alive && r.FinalRank > 0 {
			fr := r.FinalRank
			as.FinalRank = &fr
		}
		stores = append(stores, as)
	}

	return AdminSnapshot{
		MatchId:    string(s.Id()),
		ElapsedMs:  s.ElapsedMs(),
		Phase:      string(s.Phase()),
		HeatLevel:  s.HeatLevel(),
		AliveCount: s.AliveCount(),
		RestPool:   s.RestPoolCount(),
		Cull: AdminCull{
			StageIndex:       cull.StageIndex,
			StageTotal:       cull.StageTotal,
			UntilMs:          cull.UntilMs,
			TargetAliveCount: cull.TargetAliveCount,
			CutLineRank:      cull.CutLineRank,
		},
		Customers:  mixOf(s.CustomerMix()),
		RestByAttr: mixOf(s.RestPoolByAttr()),
		Stores:     stores,
	}
}

// SnapshotEnvelope は AdminSnapshot を /admin/ws のワイヤ形式 proto.Envelope に包む。
// マーシャル失敗時は ok=false（呼び出し側は Broadcast をスキップ）。
func SnapshotEnvelope(s *game.Session, botIds map[game.PlayerId]bool) (proto.Envelope, bool) {
	snap := BuildSnapshot(s, botIds)
	payload, err := json.Marshal(snap)
	if err != nil {
		return proto.Envelope{}, false
	}
	return proto.Envelope{Type: TypeAdminSnapshot, Payload: payload}, true
}
