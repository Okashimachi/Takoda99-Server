package transport

import (
	"encoding/json"

	"takoda99/internal/game"
	"takoda99/internal/proto"
)

// StatePublisher は盤面スナップの配信方針。room が tick ごとに呼び、実装が間引きを判断する。
type StatePublisher interface {
	Publish(nowMs int64, stores []proto.StoreSummary, aliveCount int, conns map[proto.StoreId]Connection)
}

// FullPublisher は全店分のフルスナップを一定間隔で全員へ配る素朴な実装。
type FullPublisher struct {
	intervalMs int64
	lastMs     int64
	published  bool
}

// NewFullPublisher は配信間隔(ms)を指定して作る。<=0 は 250ms。
func NewFullPublisher(intervalMs int) *FullPublisher {
	if intervalMs <= 0 {
		intervalMs = game.DefaultParameters().Session.PublishIntervalMs
	}
	return &FullPublisher{intervalMs: int64(intervalMs)}
}

func (p *FullPublisher) Publish(nowMs int64, stores []proto.StoreSummary, aliveCount int, conns map[proto.StoreId]Connection) {
	if p.published && nowMs-p.lastMs < p.intervalMs {
		return
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
