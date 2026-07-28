package transport

import (
	"encoding/json"

	"textro99/internal/proto"
)

// publisher.go は99店ミニ盤面の状態配信。即時イベントとは別に、盤面スナップは tick より低頻度で
// 配信して帯域(O(99×99))を抑える。差し替え可能に interface 化し、差分配信への強化は将来行う。

// StatePublisher は盤面スナップの配信方針。room が tick ごとに呼び、実装が間引きを判断する。
type StatePublisher interface {
	// Publish は nowMs 時点のスナップを、必要なら（間引き判断のうえ）全接続へ配信する。
	Publish(nowMs int64, stores []proto.StoreSummary, aliveCount int, conns map[proto.StoreId]Connection)
}

// FullPublisher は全店分のフルスナップ(StoreListUpdate)を一定間隔で全員へ配る素朴な実装。
// 差分配信(StoreDelta)は将来この interface を差し替えて強化する。
type FullPublisher struct {
	intervalMs int64
	lastMs     int64
	published  bool
}

// NewFullPublisher は配信間隔(ms)を指定して作る。<=0 は 250ms。
func NewFullPublisher(intervalMs int) *FullPublisher {
	if intervalMs <= 0 {
		intervalMs = 250
	}
	return &FullPublisher{intervalMs: int64(intervalMs)}
}

func (p *FullPublisher) Publish(nowMs int64, stores []proto.StoreSummary, aliveCount int, conns map[proto.StoreId]Connection) {
	if p.published && nowMs-p.lastMs < p.intervalMs {
		return // 前回配信から間隔未満なら間引く
	}
	p.published = true
	p.lastMs = nowMs

	data, err := json.Marshal(proto.StoreListUpdate{Stores: stores, AliveCount: aliveCount})
	if err != nil {
		return
	}
	env := proto.Envelope{Type: proto.TypeStoreListUpdate, Payload: data}
	for _, c := range conns {
		_ = c.Send(env)
	}
}

var _ StatePublisher = (*FullPublisher)(nil)
