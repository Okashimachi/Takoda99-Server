package transport

import (
	"encoding/json"

	"takoda99/internal/game"
	"takoda99/internal/proto"
)

// StatePublisher は全店ランキングの配信方針。room が tick ごとに呼び、実装が間引きを判断する。
//
// 差し替え可能な形を保つのが設計意図（AGENTS.md §4.3）。全量⇔差分の切り替えも
// この継ぎ目の内側で完結する。
type StatePublisher interface {
	// Publish は必要なら全店ランキングを配る。間引きの判断は実装が行う。
	Publish(nowMs int64, entries []proto.RankingEntry, conns map[proto.StoreId]Connection)

	// MarkSnapshotSent は「同じ全量が別経路で既に配られた」ことを伝える。
	//
	// 足切り直後の全量は **game が Outbound の順序契約の中で**流す（plan-h23 §3.1 の4）。
	// それを知らせないと、直後の定期配信で同じものを二重に配り、
	// 差分のベースラインも古いままになる。
	MarkSnapshotSent(nowMs int64, entries []proto.RankingEntry)
}

// RankingPublisher は全店ランキングを間引いて配る。
//
// 予選の StoreListUpdate（99店フルを 250ms ごと・57KB/s/client ≒ 45Mbps・1試合675MB）を
// 置き換えたもの（#81）。全量1Hz なら 6KB/s/client ≒ 4.8Mbps・1試合71MB で、
// 会場Wi-Fi にも GCP 無料枠にも収まる。
//
// 差分（RankingDelta）は既定 OFF。有効化すると 2.4KB/s まで落ちるが、まず全量で
// 確実に動かすのが方針（plan-h23 §1.2）。契約は proto v0.8.0 に既にあるので、
// proto を再度触らずに切り替えられる。
type RankingPublisher struct {
	snapshotIntervalMs int64
	deltaIntervalMs    int64
	deltaEnabled       bool

	lastSnapshotMs int64
	lastDeltaMs    int64
	started        bool

	// lastSent は前回配った各店の状態。差分の判定に使う。
	//
	// score は整数アキュムレータで OrderServed の瞬間しか動かないので、
	// **epsilon は要らない**（厳密比較でよい・plan-h23 §1.3）。
	lastSent map[proto.StoreId]proto.RankingChange
}

// NewRankingPublisher は配信間隔を指定して作る。<=0 は既定値。
func NewRankingPublisher(p game.PublishParams) *RankingPublisher {
	def := game.DefaultParameters().Publish
	if p.RankingIntervalMs <= 0 {
		p.RankingIntervalMs = def.RankingIntervalMs
	}
	if p.RankingDeltaIntervalMs <= 0 {
		p.RankingDeltaIntervalMs = def.RankingDeltaIntervalMs
	}
	return &RankingPublisher{
		snapshotIntervalMs: int64(p.RankingIntervalMs),
		deltaIntervalMs:    int64(p.RankingDeltaIntervalMs),
		deltaEnabled:       p.RankingDeltaEnabled,
		lastSent:           make(map[proto.StoreId]proto.RankingChange),
	}
}

func (p *RankingPublisher) Publish(nowMs int64, entries []proto.RankingEntry, conns map[proto.StoreId]Connection) {
	if len(entries) == 0 {
		return
	}

	// 全量が来たら差分のベースラインもそこで揃うので、全量を優先して判定する。
	if !p.started || nowMs-p.lastSnapshotMs >= p.snapshotIntervalMs {
		p.sendSnapshot(nowMs, entries, conns)
		return
	}
	if !p.deltaEnabled || nowMs-p.lastDeltaMs < p.deltaIntervalMs {
		return
	}
	p.sendDelta(nowMs, entries, conns)
}

func (p *RankingPublisher) MarkSnapshotSent(nowMs int64, entries []proto.RankingEntry) {
	p.started = true
	p.lastSnapshotMs = nowMs
	p.lastDeltaMs = nowMs
	p.rebaseline(entries)
}

func (p *RankingPublisher) sendSnapshot(nowMs int64, entries []proto.RankingEntry, conns map[proto.StoreId]Connection) {
	env, ok := envelope(proto.TypeRankingSnapshot, proto.RankingSnapshot{Entries: entries})
	if !ok {
		return
	}
	p.MarkSnapshotSent(nowMs, entries)
	broadcast(env, conns)
}

func (p *RankingPublisher) sendDelta(nowMs int64, entries []proto.RankingEntry, conns map[proto.StoreId]Connection) {
	changed := make([]proto.RankingChange, 0, 8)
	for _, e := range entries {
		c := proto.RankingChange{StoreId: e.StoreId, Score: e.Score, Alive: e.Alive}
		if prev, ok := p.lastSent[e.StoreId]; ok && prev == c {
			continue
		}
		changed = append(changed, c)
	}
	p.lastDeltaMs = nowMs
	if len(changed) == 0 {
		return
	}
	// 差分は Rank を持たない（相対値なので1店の変動で間の全店がずれ、差分の利点が消える）。
	// クライアントは Score でソートして表示順を復元し、自店の権威 Rank は EvaluationUpdate から取る。
	env, ok := envelope(proto.TypeRankingDelta, proto.RankingDelta{Entries: changed})
	if !ok {
		return
	}
	for _, c := range changed {
		p.lastSent[c.StoreId] = c
	}
	broadcast(env, conns)
}

func (p *RankingPublisher) rebaseline(entries []proto.RankingEntry) {
	for _, e := range entries {
		p.lastSent[e.StoreId] = proto.RankingChange{StoreId: e.StoreId, Score: e.Score, Alive: e.Alive}
	}
}

func envelope(typ string, msg any) (proto.Envelope, bool) {
	data, err := json.Marshal(msg)
	if err != nil {
		return proto.Envelope{}, false
	}
	return proto.Envelope{Type: typ, Payload: data}, true
}

func broadcast(env proto.Envelope, conns map[proto.StoreId]Connection) {
	for _, c := range conns {
		_ = c.Send(env)
	}
}

var _ StatePublisher = (*RankingPublisher)(nil)
