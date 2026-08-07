// Package bot は サーバー内で自動入力する仮想クライアント（CPU）。
//
// 人間と同じ transport.Connection に乗り、session からは区別されない。
// 強さは Config（提供間隔・ミス率）で表現する。
package bot

import (
	"context"
	"encoding/json"
	"math/rand"
	"time"

	"takoda99/internal/game"
	"takoda99/internal/proto"
	"takoda99/internal/transport"
)

// Config は Bot の強さ・振る舞い。
type Config struct {
	BaseAccuracy    float64
	BaseElapsedMs   int
	AccuracyJitter  float64
	ElapsedJitterMs int
}

// DefaultConfig は無難な既定値。
func DefaultConfig() Config {
	return Config{BaseAccuracy: 0.85, BaseElapsedMs: 3000, AccuracyJitter: 0.1, ElapsedJitterMs: 500}
}

// Bot は1接続を自動操作する。
type Bot struct {
	conn       transport.Connection
	cfg        Config
	rng        *rand.Rand
	pendingCid []proto.CustomerId
}

// New は Bot を作る。conn は自分の（クライアント側）接続。
func New(conn transport.Connection, cfg Config, rng *rand.Rand) *Bot {
	return &Bot{conn: conn, cfg: cfg, rng: rng}
}

// Run は Bot を駆動する。
func (b *Bot) Run(ctx context.Context) {
	defer func() { _ = b.conn.Close() }()
	iv := time.Duration(b.cfg.BaseElapsedMs) * time.Millisecond
	if iv <= 0 {
		iv = time.Duration(game.DefaultParameters().Bot.BaseElapsedMs) * time.Millisecond
	}
	ticker := time.NewTicker(iv)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case env, ok := <-b.conn.Receive():
			if !ok {
				return
			}
			if b.onMessage(env) {
				return
			}
		case <-ticker.C:
			b.act()
		}
	}
}

func (b *Bot) onMessage(env proto.Envelope) bool {
	switch env.Type {
	case proto.TypeMatchStart:
		// MatchStart には客が含まれない（客は stepDistribute で来店する）
	case proto.TypeCustomerArrived:
		var m proto.CustomerView
		if json.Unmarshal(env.Payload, &m) == nil {
			b.pendingCid = append(b.pendingCid, m.CustomerId)
		}
	case proto.TypeCustomerLeft:
		var m proto.CustomerLeft
		if json.Unmarshal(env.Payload, &m) == nil {
			b.removePending(m.CustomerId)
		}
	case proto.TypeMatchEnd, proto.TypePersonalResult:
		return true
	}
	return false
}

func (b *Bot) act() {
	if len(b.pendingCid) == 0 {
		return
	}
	cid := b.pendingCid[0]
	b.pendingCid = b.pendingCid[1:]

	elapsed := b.cfg.BaseElapsedMs
	if b.cfg.ElapsedJitterMs > 0 {
		elapsed += b.rng.Intn(2*b.cfg.ElapsedJitterMs+1) - b.cfg.ElapsedJitterMs
	}
	if elapsed < 1 {
		elapsed = 1
	}

	acc := b.cfg.BaseAccuracy
	if b.cfg.AccuracyJitter > 0 {
		acc += (b.rng.Float64()*2 - 1) * b.cfg.AccuracyJitter
	}
	miss := 0
	if b.rng.Float64() > acc {
		miss = 1
	}

	b.send(proto.TypeOrderServed, proto.OrderServed{
		CustomerId: cid,
		ElapsedMs:  elapsed,
		MissCount:  miss,
	})
}

func (b *Bot) send(typ string, msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_ = b.conn.Send(proto.Envelope{Type: typ, Payload: data})
}

func (b *Bot) removePending(cid proto.CustomerId) {
	for i, x := range b.pendingCid {
		if x == cid {
			b.pendingCid = append(b.pendingCid[:i], b.pendingCid[i+1:]...)
			return
		}
	}
}
