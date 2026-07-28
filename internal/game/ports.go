package game

import (
	"context"
	"math/rand"

	"textro99/internal/proto"
)

// ports.go は【層2・継ぎ目】。コア game が外部（層3部品）に要求する interface を
// コア自身が所有する（DIP）。game は odai/config の実体を知らず、この形のものを注入して
// もらって呼ぶだけ。依存は常に 部品 → game の一方向（.golangci.yml の depguard で機械強制）。

// PlayerId はコア内の店舗識別子。契約(proto)の StoreId と同一（string）。
// （たこ焼き版で「プレイヤー＝店舗」。名称の StoreId 統一は後続で整理）
type PlayerId = proto.StoreId

// ── お題供給 ───────────────────────────────────────────────

// Word は1つの出題語。KeystrokeCount は正準ローマ字打鍵数。
type Word struct {
	Text           string
	KeystrokeCount int
}

// WordSource はお題単語供給の口。実効難易度（火力）に応じた語を返す。
type WordSource interface {
	Next(effectiveLevel int, rng *rand.Rand) Word
	NextTrap(rng *rand.Rand) Word
}

// ── 設定取得 ───────────────────────────────────────────────

// ConfigProvider は GameParameters を起動時取得する。
// Load は使用可能な GameParameters を必ず返す（失敗時も内蔵デフォルト＋err）。
type ConfigProvider interface {
	Load(ctx context.Context) (GameParameters, error)
}
