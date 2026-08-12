// Package admin は【スパイン】観測基盤。運営/開発者向けの読み取り専用の観測ストリームを
// ファンアウトする Hub と、その静的ダッシュボードの同梱配信を持つ（plan-h01/h02）。
//
// 依存の向きは admin → transport（Connection を使う）の一方向。transport は admin を知らない
// （循環しない）。game(コア) は admin を import しない（純粋性維持・AGENTS.md §1.4）。
// 観測は客向け /ws とは独立した別系統で、客向け配信が痩せても影響を受けない。
package admin

import (
	"sync"

	"takoda99/internal/proto"
	"takoda99/internal/transport"
)

// Hub は観測用の読み取り専用ファンアウト。プロセス共有・単一インスタンス。
// room（試合ごとの goroutine）が Broadcast し、/admin/ws ハンドラが Register/Unregister する。
//
// 現状 1部屋 1試合なので単一 Hub で足りる（複数試合並走時の混線は将来課題・plan-h01 §1.2）。
type Hub struct {
	mu    sync.RWMutex
	conns map[transport.Connection]struct{}
}

// NewHub は空の Hub を作る。
func NewHub() *Hub {
	return &Hub{conns: make(map[transport.Connection]struct{})}
}

// Register は観測 conn を登録する（/admin/ws 接続時）。
func (h *Hub) Register(c transport.Connection) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}

// Unregister は観測 conn を登録解除する（conn.Done() 発火時 / Broadcast の Send 失敗時）。
func (h *Hub) Unregister(c transport.Connection) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

// Broadcast は登録中の全 conn へ payload を Send する（room goroutine から呼ばれる）。
//
// transport.wsConnection.Send はキュー満杯で自動的に接続を切る（slow-consumer eviction,
// connection.go:155）。詰まった観測 conn は Send がエラー（ErrConnClosed）を返すので、
// ここで Unregister して map から除く（放置すると死んだ conn が溜まる）。
// Send は非同期キューなので、observe の詰まりが room の単一 goroutine を止めない。
func (h *Hub) Broadcast(payload proto.Envelope) {
	// 送信対象のスナップショットを取り、ロック外で Send する
	// （Send 中に Register/Unregister が来ても deadlock しないように）。
	h.mu.RLock()
	targets := make([]transport.Connection, 0, len(h.conns))
	for c := range h.conns {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	var dead []transport.Connection
	for _, c := range targets {
		if err := c.Send(payload); err != nil {
			dead = append(dead, c)
		}
	}
	for _, c := range dead {
		h.Unregister(c)
	}
}

// Count は登録中の観測 conn 数を返す（監視・テスト用）。
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}
