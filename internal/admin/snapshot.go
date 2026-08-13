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
	Storm      AdminStorm   `json:"storm"`
	Customers  AdminMix     `json:"customers"`  // 在場の属性別総数
	RestByAttr AdminMix     `json:"restByAttr"` // restPool の属性別内訳（客フロー用）
	Stores     []AdminStore `json:"stores"`     // 99店
}

// AdminStorm は下位淘汰(storm)予告の状態。
type AdminStorm struct {
	Warning      bool    `json:"warning"`
	UntilTick    int     `json:"untilTick"`
	ThresholdPct float64 `json:"thresholdPct"`
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

// AdminStore は1店の観測情報（店舗盤面＋客フロー用）。
//
// 本戦（plan-h21）で creditLife / evalNormalized は score に置き換わった。
// これは「消したフィールドの参照を付け替えてコンパイルを通す」最小対応で、
// スコア分布ビュー等の本格的な v2 化は h25。**webdist/app.js はまだ旧フィールドを
// 読んでいるので、体力バー・評価バーの表示は一時的に空になる**（描画は壊れない）。
type AdminStore struct {
	StoreId     string   `json:"storeId"`
	DisplayName string   `json:"displayName"`
	Alive       bool     `json:"alive"`
	Rank        int      `json:"rank"`
	FinalRank   *int     `json:"finalRank,omitempty"` // 脱落済みのみ
	Score       int      `json:"score"`
	QueueLen    int      `json:"queueLen"`
	ServedCount int      `json:"servedCount"`
	AtRisk      bool     `json:"atRisk"`
	QueueByAttr AdminMix `json:"queueByAttr"`
}

// BuildSnapshot は session の純粋 getter を読んで AdminSnapshot を組む。
//
// room の単一 goroutine（publish 直後）から呼ばれる前提。session を触るのは room だけなので
// getter 読み出しはデータ競合しない（plan-h02 §1.3）。
func BuildSnapshot(s *game.Session) AdminSnapshot {
	board := s.StoreBoard()
	storm := s.StormState()

	stores := make([]AdminStore, 0, len(board))
	for _, r := range board {
		as := AdminStore{
			StoreId:     string(r.Id),
			DisplayName: r.Name,
			Alive:       r.Alive,
			Rank:        r.Rank,
			Score:       r.Score,
			QueueLen:    r.QueueLen,
			ServedCount: r.ServedCount,
			AtRisk:      r.AtRisk,
			QueueByAttr: mixOf(r.QueueByAttr),
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
		Storm: AdminStorm{
			Warning:      storm.Warning,
			UntilTick:    storm.UntilTick,
			ThresholdPct: storm.ThresholdPct,
		},
		Customers:  mixOf(s.CustomerMix()),
		RestByAttr: mixOf(s.RestPoolByAttr()),
		Stores:     stores,
	}
}

// SnapshotEnvelope は AdminSnapshot を /admin/ws のワイヤ形式 proto.Envelope に包む。
// マーシャル失敗時は ok=false（呼び出し側は Broadcast をスキップ）。
func SnapshotEnvelope(s *game.Session) (proto.Envelope, bool) {
	snap := BuildSnapshot(s)
	payload, err := json.Marshal(snap)
	if err != nil {
		return proto.Envelope{}, false
	}
	return proto.Envelope{Type: TypeAdminSnapshot, Payload: payload}, true
}
