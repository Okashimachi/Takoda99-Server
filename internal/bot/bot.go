// Package bot はサーバー内で自動入力する仮想クライアント（CPU）。
//
// 人間と同じ transport.Connection に乗り、session からは区別されない。人数補完・デモ・
// 切断補完の"燃料"。強さは Config（提供間隔・ミス率）で表現する。
//
// ⚠ たこ焼き版の実装（`OrderServed` を自動生成＝`CustomerArrived` の客に応じて提供報告する）は
// tako-J で入れる。現状は【骨組み】：受信を読み流し、切断/ctx/MatchEnd で終了するだけの no-op。
package bot

import (
	"context"

	"textro99/internal/proto"
	"textro99/internal/transport"
)

// Config は Bot の強さ・振る舞い。数値は呼び出し側が与える（ロジックに直書きしない）。
type Config struct {
	ServeIntervalMs int     // 1注文を打ち切るのにかける平均ms（tako-J で使用）
	MissRate        float64 // ミス確率(0..1)（tako-J で使用）
}

// DefaultConfig は無難な既定値。
func DefaultConfig() Config { return Config{ServeIntervalMs: 500, MissRate: 0.05} }

// Bot は1接続を自動操作する。
type Bot struct {
	conn transport.Connection
}

// RandSource は Bot が使う乱数源（*rand.Rand を満たす最小 interface）。
type RandSource interface {
	Float64() float64
}

// New は Bot を作る。conn は自分の（クライアント側）接続。
// cfg/rng は tako-J（OrderServed 自動生成）で使う。現状の no-op では保持しない。
func New(conn transport.Connection, cfg Config, rng RandSource) *Bot {
	_ = cfg
	_ = rng
	return &Bot{conn: conn}
}

// Run は Bot を駆動する。現状は受信を読み流し、MatchEnd 受信・切断・ctx キャンセルで終了する。
//
// 終了時に接続を Close する。これにより、脱落して読むのをやめた Bot へサーバーが
// ブロードキャストし続けても Send が即エラー（ブロックしない）になり、room ループが詰まらない。
func (b *Bot) Run(ctx context.Context) {
	defer func() { _ = b.conn.Close() }()
	for {
		select {
		case <-ctx.Done():
			return
		case env, ok := <-b.conn.Receive():
			if !ok {
				return
			}
			if env.Type == proto.TypeMatchEnd {
				return
			}
			// TODO(tako-J): CustomerArrived を貯め、ServeIntervalMs 間隔で OrderServed を自動送出する。
		}
	}
}
