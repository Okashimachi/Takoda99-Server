package game

import (
	"context"
	"math/rand"

	"textro99/internal/proto"
)

// ports.go は【層2・継ぎ目】。コア game が外部（層3部品）に要求する interface を
// コア自身が所有する（DIP）。game は targeting/odai/config の実体を知らず、この形の
// ものを注入してもらって呼ぶだけ。部品側が game を import してこれらを実装する。
// 依存は常に 部品 → game の一方向（.golangci.yml の depguard で機械強制）。

// PlayerId はコア内の識別子。契約(proto)の PlayerId と同一（string）。
type PlayerId = proto.PlayerId

// ── ターゲティング（作戦0〜9）──────────────────────────────

// PlayerView は生存者1人分のスナップショット（ターゲット判定に要る最小フィールド）。
type PlayerView struct {
	PlayerId        PlayerId
	ComboValue      int
	DakenStackCount int
	DakenStackLimit int
	BadgeCount      int
	// IncomingWarnings はこのプレイヤー宛に現在 Pending 中の予告数（作戦8/9用・決定F）。
	IncomingWarnings int
}

// TargetingContext は作戦解決の唯一の入力。攻撃者本人の文脈＋全生存者。
type TargetingContext struct {
	SelfId PlayerId
	Alive  []PlayerView // 生存者全員（自分含む）。Others() で自分を除く。
	// PendingAttackers は自分に予告中の相手を新しい順に並べたもの（作戦1カウンター・決定F）。
	PendingAttackers []PlayerId
	// LastImpactorId は直近で自分に着弾させた相手（作戦5リベンジ・決定F）。無ければ nil。
	LastImpactorId *PlayerId
	// Rng は乱択・タイブレーク用。決定的テスト/シミュレーションのため注入する。
	Rng *rand.Rand
}

// Others は自分以外の生存者を返す（ほとんどの作戦の母集団）。
func (c TargetingContext) Others() []PlayerView {
	out := make([]PlayerView, 0, len(c.Alive))
	for _, p := range c.Alive {
		if p.PlayerId != c.SelfId {
			out = append(out, p)
		}
	}
	return out
}

// TargetingStrategy は1作戦。SelectTargets は対象IDの集合を返す。
//
// 【不変条件】空=不発。作戦1〜9 は 0件か1件。複数件を返してよいのは作戦0(全体割り)だけで、
// その場合 session が威力を floor(power/N) で均等分配する。ターゲティングは威力に触れない。
type TargetingStrategy interface {
	Id() int
	SelectTargets(ctx TargetingContext) []PlayerId
}

// ── お題供給 ───────────────────────────────────────────────

// Word は1つの出題語。KeystrokeCount は正準ローマ字打鍵数（コンボ加算に使う・決定C）。
type Word struct {
	Text           string
	KeystrokeCount int
}

// WordSource はダケン供給の口。実効難易度に応じた通常語と、トラップ煽り長文を返す。
type WordSource interface {
	Next(effectiveLevel int, rng *rand.Rand) Word
	NextTrap(rng *rand.Rand) Word
}

// ── 設定取得 ───────────────────────────────────────────────

// ConfigProvider は GameParameters を起動時取得する。
// Load は使用可能な GameParameters を必ず返す（失敗時も内蔵デフォルト＋err・決定E）。
type ConfigProvider interface {
	Load(ctx context.Context) (GameParameters, error)
}
