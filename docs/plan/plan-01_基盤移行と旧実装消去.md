# Plan-01: 基盤移行と旧実装消去

> **目的**: Textro99-Server（バグ修正済みインフラ）を Takoda99-Server へ完全コピーし、旧タイピング対戦ゲームロジックを除去した上で、たこ焼き経営バトルロイヤル版 `game/` の骨格を配置する。この Plan が完了すると `go build && go test` が通り、`--mode solo` で WebSocket 接続 → MatchStart 受信まで動く状態になる。

---

## 1. 前提知識

この Plan を実行する前に以下を把握しておくこと。

| 対象 | 場所 | 読む目的 |
|---|---|---|
| Takoda99-Docs（設計正典） | `github.com/Okashimachi/Takoda99-Docs` | ゲームルール・パラメータ仕様・サーバー仕様の全体像 |
| Takoda99-Proto（契約正典） | `github.com/Okashimachi/Takoda99-Proto` | メッセージ型・列挙・DTO の定義。server の `internal/proto/messages.go` はここの薄いラッパ |
| Textro99-Server（コピー元） | `/Users/ryu/kindai/2026/THEHACK/Textro99-Server` | バグ修正済みインフラ層。game/ は旧メカニクス（直接攻撃型タイピング対戦）|
| Takoda99-Server（コピー先） | `/Users/ryu/kindai/2026/THEHACK/Takoda99-Server` | 現状は旧 Textro99 ベースに tako-B～E の骨格を加えた中途半端な状態 |
| AGENTS.md | 各リポジトリのルート | 層アーキテクチャ・依存ルール・コーディング規約 |

### ゲーム概要（たこ焼き経営BR）

- 99 のたこ焼き屋が 300 人の客を取り合う
- 直接攻撃なし。客に正確・高速にタイピングで注文を提供して評価を上げる
- 客には我慢ゲージがあり、待たせると離脱。離脱で店の信用（ライフ）が減る
- 信用 0 で自滅脱落。定期的な下位淘汰（storm）で最下位が強制脱落
- 最後の 1 店が優勝

---

## 2. 現状

### 2.1 Textro99-Server（コピー元）の状態

**インフラ層（バグ修正済み・そのまま持ち込む）:**

| パッケージ | 主要ファイル | 概要 |
|---|---|---|
| `internal/transport/` | `connection.go`, `publisher.go`, `inmemory.go` | WebSocket 接続抽象・Pipe・StatePublisher |
| `internal/room/` | `room.go` | 1試合の駆動goroutine（inbox + tick ループ） |
| `internal/matchmaking/` | `matchmaking.go` | プール型マッチメイカー |
| `internal/config/` | `loader.go`, `remote.go` | ConfigProvider（HTTP/デフォルト） |
| `internal/configapi/` | `handler.go` | config-front 用 REST API |
| `internal/db/` | `pool.go`, `config.go` | Postgres 接続プール・設定永続化 |
| `internal/store/` | `store.go` | 試合結果永続化 interface |
| `internal/odai/` | `pool.go`, `data.go`, `romaji.go` | お題単語プール |
| `internal/app/` | `app.go` | 試合組み立て（session + room + bot の配線） |
| `internal/bot/` | `bot.go` | CPU 自動入力クライアント |
| `cmd/server/` | `main.go` | 合成ルート（WebSocket ハンドラ・config選択） |

**旧ゲームロジック（削除対象）:**

| ファイル | 内容 |
|---|---|
| `internal/game/attack.go` | 威力算出 (`attackPower`, `powerToDakenCount`) |
| `internal/game/combo.go` | コンボ蓄積・減衰 (`Player`, `ApplyDakenClear`, `ResetCombo`) |
| `internal/game/offset.go` | クリア起点攻撃・予告ライフサイクル (`fireClearAttack`, `emitWarnings`, `newWarning`, `removeWarning`) |
| `internal/game/stack.go` | ダケンスタック・トラップ誘発・脱落確定 (`landReceived`, `addStack`, `eliminateWithKO`, `resolveEliminations`, `weaker`) |
| `internal/game/difficulty.go` | 難易度（全体＋個人コンボ連動）・時間切れ・予告着弾 (`personalLevel`, `effectiveLevel`, `advanceGlobalDifficulty`, `expireTimeouts`, `expireWarnings`) |
| `internal/game/session.go` | 旧状態機械（ダケンキュー・予告・KO・バッジ等） |
| `internal/game/ports.go` | `TargetingStrategy`, `TargetingContext`, `PlayerView`（削除）、`Word`, `WordSource`, `ConfigProvider`（残す） |
| `internal/game/params.go` | 旧パラメータ群（`ComboParams`, `AttackParams`, `StackParams`, `DifficultyParams`, `OdaiParams`） |
| `internal/game/combat_test.go` | 攻撃・スタック結合テスト |
| `internal/game/combo_test.go` | コンボ単体テスト |
| `internal/game/session_test.go` | 旧 session テスト |
| `internal/targeting/` | ディレクトリ丸ごと（10種の作戦: random, neighbor, counter, revenge, tallpoppy, finisher, badge, pacifist, pileon, split + strategy.go + テスト2本） |
| `cmd/balancesim/` | 旧バランスシミュレータ |
| `cmd/matchsim/` | 旧マッチシミュレータ |

**テストファイル（保持するもの）:**

以下のテストは旧ゲームロジックに依存しないため、そのまま保持する:
- `internal/transport/connection_test.go`, `inmemory_test.go`, `origin_test.go`, `publisher_test.go`
- `internal/room/room_test.go`（room→session の interface 変更で修正が要る）
- `internal/matchmaking/matchmaking_test.go`
- `internal/config/loader_test.go`
- `internal/configapi/handler_test.go`
- `internal/db/config_test.go`
- `internal/odai/data_test.go`, `pool_test.go`
- `internal/store/store_test.go`
- `internal/app/app_test.go`, `e2e_wire_test.go`, `headless_test.go`, `scale_test.go`（session 変更で修正が要る）
- `internal/bot/bot_test.go`（メッセージ型変更で修正が要る）
- `internal/proto/wire_golden_test.go`（proto 差し替えで修正が要る）

### 2.2 Takoda99-Server（コピー先）の現状

- `go.mod`: module パスが `textro99`（未変更）、Takoda99-Proto v0.2.0 を参照
- `internal/proto/messages.go`: Takoda99-Proto のたこ焼き版型（StoreId, CustomerId, Phase, 等）を再輸出済み
- `internal/game/`: tako-B～E の骨格あり（session.go に customer/storeState 構造体、Tick loop、ApplyOrderServed、initCustomers 等）
- `internal/game/params.go`: 旧項目（Combo/Attack/Stack/Difficulty/Odai）がたこ焼き版（Credit/Customer/Eval）と混在
- バグ修正済みインフラを持っていない（transport/room/matchmaking/config/app/bot が旧バージョン）

### 2.3 go.mod の差分

**Textro99-Server（コピー元）:**
```
module textro99
go 1.25.0
require github.com/Okashimachi/Textro99-Proto v0.1.2-0.20260731153747-42fb05e2925e
```

**Takoda99-Server（移行後の目標）:**
```
module takoda99
go 1.25.0
require github.com/Okashimachi/Takoda99-Proto v0.2.0
```

---

## 3. 実装手順

### Phase 1: 完全コピー（Textro99-Server → Takoda99-Server）

Takoda99-Server の `.git/` 以外を全削除し、Textro99-Server の `.git/` 以外を全コピーする。

```bash
cd /Users/ryu/kindai/2026/THEHACK/Takoda99-Server

# 1. .git/ 以外を全削除
find . -mindepth 1 -maxdepth 1 ! -name '.git' -exec rm -rf {} +

# 2. Textro99-Server の .git/ 以外を全コピー
rsync -a --exclude='.git' /Users/ryu/kindai/2026/THEHACK/Textro99-Server/ .

# 3. 確認: この時点で Textro99-Server と同一内容
go build ./...
go test ./...
```

**確認ポイント**: ビルドとテストが Textro99-Server と同様に通ること。`go.mod` の module が `textro99`、Proto が `Textro99-Proto` になっていること。

---

### Phase 2: モジュールパス変更（textro99 → takoda99）

#### 2-1. go.mod の module パスを変更

```bash
cd /Users/ryu/kindai/2026/THEHACK/Takoda99-Server
sed -i '' 's/^module textro99$/module takoda99/' go.mod
```

#### 2-2. 全 .go ファイルの import パスを一括置換

```bash
cd /Users/ryu/kindai/2026/THEHACK/Takoda99-Server
find . -name '*.go' -exec sed -i '' 's|"textro99/internal/|"takoda99/internal/|g' {} +
```

#### 2-3. ビルド確認

```bash
go build ./...
```

この時点ではまだ `Textro99-Proto` を参照しているが、module パス変更だけでビルドが通ることを確認する。

---

### Phase 3: Proto 参照の切り替え（Textro99-Proto → Takoda99-Proto）

#### 3-1. go.mod の require を変更

```bash
cd /Users/ryu/kindai/2026/THEHACK/Takoda99-Server

# Textro99-Proto の行を削除し、Takoda99-Proto を追加
go mod edit -droprequire github.com/Okashimachi/Textro99-Proto
go mod edit -require github.com/Okashimachi/Takoda99-Proto@v0.2.0
```

#### 3-2. internal/proto/messages.go を書き換え

`internal/proto/messages.go` を以下の内容に完全書き換えする。Takoda99-Proto の型のみを再輸出する:

```go
// Package proto は canonical な契約リポジトリ github.com/Okashimachi/Takoda99-Proto を
// server 内から参照するための薄いラッパ。canonical の型・定数を type alias / const で再輸出し、
// server 側の import パスを "takoda99/internal/proto" に固定する。
//
// canonical に新メッセージ/型が増えたら、この再輸出リストにも1行追加する
// （未追加の型を使うと "undefined" の明示的コンパイルエラーになる）。型の追加・変更・削除は
// canonical 側で人間（りーせ）承認を得てから行う。
package proto

import canon "github.com/Okashimachi/Takoda99-Proto/proto"

// ── 共通ID ────────────────────────────────────────────────
type (
	StoreId    = canon.StoreId
	CustomerId = canon.CustomerId
	MatchId    = canon.MatchId
)

// ── 列挙 ──────────────────────────────────────────────────
type (
	CustomerAttribute = canon.CustomerAttribute
	Phase             = canon.Phase
	EliminationReason = canon.EliminationReason
	LeaveReason       = canon.LeaveReason
	CreditReason      = canon.CreditReason
)

const (
	AttrNormal  = canon.AttrNormal
	AttrBonus   = canon.AttrBonus
	AttrClaimer = canon.AttrClaimer
	AttrBuzz    = canon.AttrBuzz

	PhaseEarly = canon.PhaseEarly
	PhaseMid   = canon.PhaseMid
	PhaseLate  = canon.PhaseLate

	ElimSelfCollapse = canon.ElimSelfCollapse
	ElimCull         = canon.ElimCull

	LeaveTimeout = canon.LeaveTimeout

	CreditCustomerLeft = canon.CreditCustomerLeft
)

// ── 共通DTO ────────────────────────────────────────────────
type (
	StoreSummary               = canon.StoreSummary
	CustomerView               = canon.CustomerView
	MatchStats                 = canon.MatchStats
	GameParametersPublicSubset = canon.GameParametersPublicSubset
	Envelope                   = canon.Envelope
)

// ── メッセージ種別タグ ────────────────────────────────────
const (
	// C2S
	TypeOrderServed      = canon.TypeOrderServed
	TypeMatchmakingJoin  = canon.TypeMatchmakingJoin
	TypeMatchmakingLeave = canon.TypeMatchmakingLeave

	// S2C
	TypeMatchStart               = canon.TypeMatchStart
	TypeCustomerArrived          = canon.TypeCustomerArrived
	TypeCustomerLeft             = canon.TypeCustomerLeft
	TypeCreditUpdate             = canon.TypeCreditUpdate
	TypeEvaluationUpdate         = canon.TypeEvaluationUpdate
	TypeDifficultyUpdate         = canon.TypeDifficultyUpdate
	TypePhaseChange              = canon.TypePhaseChange
	TypeStoreListUpdate          = canon.TypeStoreListUpdate
	TypeForcedEliminationWarning = canon.TypeForcedEliminationWarning
	TypeStoreEliminated          = canon.TypeStoreEliminated
	TypeMatchEnd                 = canon.TypeMatchEnd
	TypeMatchmakingStatus        = canon.TypeMatchmakingStatus
)

// ── C2S ───────────────────────────────────────────────────
type (
	OrderServed      = canon.OrderServed
	MatchmakingJoin  = canon.MatchmakingJoin
	MatchmakingLeave = canon.MatchmakingLeave
)

// ── S2C ───────────────────────────────────────────────────
type (
	MatchStart               = canon.MatchStart
	CustomerArrived          = canon.CustomerArrived
	CustomerLeft             = canon.CustomerLeft
	CreditUpdate             = canon.CreditUpdate
	EvaluationUpdate         = canon.EvaluationUpdate
	DifficultyUpdate         = canon.DifficultyUpdate
	PhaseChange              = canon.PhaseChange
	StoreListUpdate          = canon.StoreListUpdate
	ForcedEliminationWarning = canon.ForcedEliminationWarning
	StoreEliminated          = canon.StoreEliminated
	MatchEnd                 = canon.MatchEnd
	MatchmakingStatus        = canon.MatchmakingStatus
)
```

#### 3-3. go mod tidy

```bash
go mod tidy
```

**確認ポイント**: この時点ではまだ旧 game/ が Textro99-Proto の型（DakenId, WarningId 等）を使っているため、ビルドは通らない。次の Phase で旧ゲームロジックを削除して解消する。

---

### Phase 4: 旧ゲームロジックの削除

#### 4-1. internal/targeting/ ディレクトリを丸ごと削除

```bash
rm -rf /Users/ryu/kindai/2026/THEHACK/Takoda99-Server/internal/targeting
```

対象ファイル（全12ファイル）:
- `strategy.go` — TargetingStrategy interface 登録
- `random.go` — 作戦4（ランダム）
- `neighbor.go` — 作戦7（隣狙い）
- `counter.go` — 作戦1（カウンター）
- `revenge.go` — 作戦5（リベンジ）
- `tallpoppy.go` — 作戦6（出る杭）
- `finisher.go` — 作戦2（トドメ）
- `badge.go` — 作戦3（バッジ狩り）
- `pacifist.go` — 作戦9（無抵抗狩り）
- `pileon.go` — 作戦8（便乗）
- `split.go` — 作戦0（全体割り）
- `strategy_test.go`, `strategy_more_test.go` — テスト

#### 4-2. internal/game/ から旧ファイルを削除

```bash
cd /Users/ryu/kindai/2026/THEHACK/Takoda99-Server/internal/game
rm -f attack.go combo.go offset.go stack.go difficulty.go
rm -f combat_test.go combo_test.go session_test.go
```

対象ファイル（8ファイル）:
| ファイル | 削除理由 |
|---|---|
| `attack.go` | 直接攻撃の威力算出。たこ焼き版には攻撃がない |
| `combo.go` | コンボ蓄積・減衰の Player 型。たこ焼き版では評価EMAに置換 |
| `offset.go` | クリア起点攻撃・予告ライフサイクル。攻撃がないため不要 |
| `stack.go` | ダケンスタック・トラップ・KO・同時脱落タイブレーク。信用/淘汰に置換 |
| `difficulty.go` | 旧難易度（全体+個人コンボ連動）・時間切れ・予告着弾。火力(Heat)に置換 |
| `combat_test.go` | attack + stack の結合テスト |
| `combo_test.go` | combo の単体テスト |
| `session_test.go` | 旧 session の全テスト |

#### 4-3. cmd/balancesim/ と cmd/matchsim/ を削除

```bash
rm -rf /Users/ryu/kindai/2026/THEHACK/Takoda99-Server/cmd/balancesim
rm -rf /Users/ryu/kindai/2026/THEHACK/Takoda99-Server/cmd/matchsim
```

旧メカニクスに完全依存しており、たこ焼き版シミュレータは後続 Plan で別途作成する。

---

### Phase 5: game/ の新規骨格配置

Phase 4 で `session.go`, `params.go`, `ports.go` が削除されているので、以下の内容で新規作成する。`params_validate_test.go` は params.go の新 Validate に合わせて書き直す。

#### 5-1. internal/game/ports.go（新規作成）

```go
package game

import (
	"context"
	"math/rand"

	"takoda99/internal/proto"
)

// ports.go は【層2・継ぎ目】。コア game が外部（層3部品）に要求する interface を
// コア自身が所有する（DIP）。game は odai/config の実体を知らず、この形のものを注入して
// もらって呼ぶだけ。依存は常に 部品 → game の一方向（.golangci.yml の depguard で機械強制）。

// PlayerId はコア内の店舗識別子。契約(proto)の StoreId と同一（string）。
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
}

// ── 設定取得 ───────────────────────────────────────────────

// ConfigProvider は GameParameters を起動時取得する。
// Load は使用可能な GameParameters を必ず返す（失敗時も内蔵デフォルト＋err）。
type ConfigProvider interface {
	Load(ctx context.Context) (GameParameters, error)
}
```

旧版との差分:
- `TargetingStrategy`, `TargetingContext`, `PlayerView` を完全削除（攻撃がないため）
- `WordSource` の `NextTrap` を削除（トラップダケンがないため）
- import パスが `takoda99/internal/proto`

#### 5-2. internal/game/params.go（新規作成）

```go
// Package game は【層1・コア】試合の権威。純粋な計算のみで、ネットワーク・時間・I/O を持たない。
//
// たこ焼き経営BR の状態機械（Tick(dt) で進む）を内包する。game は他の internal 部品/スパインを
// import しない（proto は契約として参照可）。継ぎ目は ports.go（DIP）。すべての調整値は
// GameParameters 経由。
package game

import (
	"fmt"

	"takoda99/internal/proto"
)

// GameParameters は数値バランスの全項目。正典は
// Takoda99-Docs/03_サーバー仕様/04_パラメータ仕様.md。
// サーバーが起動時に config(ConfigProvider) 経由で外部取得し、失敗時は DefaultParameters()。
// クライアントへは MatchStart で公開サブセット（proto側）に絞って配信する。
type GameParameters struct {
	Session      SessionParams      `json:"session"`
	Matching     MatchingParams     `json:"matching"`
	Credit       CreditParams       `json:"credit"`
	Customer     CustomerParams     `json:"customer"`
	Eval         EvalParams         `json:"eval"`
	Phase        PhaseParams        `json:"phase"`
	Heat         HeatParams         `json:"heat"`
	Storm        StormParams        `json:"storm"`
	Distribution DistributionParams `json:"distribution"`
	Patience     PatienceParams     `json:"patience"`
	Bot          BotParams          `json:"bot"`
}

// SessionParams: 試合ループの調整値。tick 周期・状態配信間隔もハードコードせずここで持つ。
type SessionParams struct {
	TickIntervalMs    int `json:"tickIntervalMs"`
	PublishIntervalMs int `json:"publishIntervalMs"` // 99店ミニ盤面の配信間隔（tickより低頻度で帯域を抑える）
	MatchTimeLimitMs  int `json:"matchTimeLimitMs"`  // 試合の制限時間。0=無効（solo/dev の idle 継続用）
}

// MatchingParams: マッチング（試合前）。minPlayers は当日運用で下げられるよう可変性が重要。
type MatchingParams struct {
	MinPlayers       int `json:"minPlayers"`
	MaxPlayers       int `json:"maxPlayers"`
	StartCountdownMs int `json:"startCountdownMs"`
	MinFill          int `json:"minFill"`
}

// LeaveLoss: 属性別の離脱ペナルティ。map ではなく固定フィールドにして
// GameParameters の == 比較可能性を保つ。
type LeaveLoss struct {
	Normal  int `json:"normal"`
	Bonus   int `json:"bonus"`
	Claimer int `json:"claimer"`
	Buzz    int `json:"buzz"`
}

// For は属性に対応する減少量を返す。
func (ll LeaveLoss) For(attr proto.CustomerAttribute) int {
	switch attr {
	case proto.AttrBonus:
		return ll.Bonus
	case proto.AttrClaimer:
		return ll.Claimer
	case proto.AttrBuzz:
		return ll.Buzz
	default:
		return ll.Normal
	}
}

// CreditParams: 信用（ライフ）。客の離脱でのみ減少・0で自滅脱落。詳細は Plan-02。
type CreditParams struct {
	InitialLife int       `json:"initialLife"` // 初期信用（例:3）
	LeaveLoss   LeaveLoss `json:"leaveLoss"`   // 属性別の離脱ペナルティ
}

// CustomerParams: 客システム（総数・属性ごとの出現率/我慢/注文数）。
// 属性は proto で閉じた4種なので固定フィールドで持つ（GameParameters の == 比較可能性を保つ）。
type CustomerParams struct {
	Total   int           `json:"total"` // 客総数（例:300）
	Normal  AttributeSpec `json:"normal"`
	Bonus   AttributeSpec `json:"bonus"`
	Claimer AttributeSpec `json:"claimer"`
	Buzz    AttributeSpec `json:"buzz"`
}

// AttributeSpec: 1属性分の生成パラメータ。
type AttributeSpec struct {
	Attribute      proto.CustomerAttribute `json:"attribute"`
	Weight         int                     `json:"weight"`         // 出現率の相対重み（Σで正規化）
	PatienceBaseMs int                     `json:"patienceBaseMs"` // 我慢ゲージ最大の基準
	OrderCount     int                     `json:"orderCount"`     // 打つ単語数（Buzz は多め）
}

// EvalParams: 提供スコア→評価EMA の調整値。
type EvalParams struct {
	EmaAlpha        float64 `json:"emaAlpha"`        // 評価EMA の係数（0..1・大きいほど直近重視）
	WeightAccuracy  float64 `json:"weightAccuracy"`  // 提供スコアの精度重み w_acc
	WeightSpeed     float64 `json:"weightSpeed"`     // 提供スコアの速度重み w_spd
	SpeedBaselineMs int     `json:"speedBaselineMs"` // 速度=baseline/elapsed が 1.0 になる基準所要
	SpeedCap        float64 `json:"speedCap"`        // 速度の上限（速すぎる報告の頭打ち）
	MinMsPerWord    int     `json:"minMsPerWord"`    // サニティ下限：1語あたり最小所要（elapsed 下限＝×orderCount）
	BuzzBonus       float64 `json:"buzzBonus"`       // JK(Buzz)満足時の一時加点
	BuzzDecay       float64 `json:"buzzDecay"`       // 一時加点の毎tick乗算減衰（0..1）
	BuzzCap         float64 `json:"buzzCap"`         // 一時加点の上限
}

// PhaseParams: フェーズ遷移（Early → Mid → Late）。詳細は Plan-04。
// 生存「数」の閾値で持つ（比率ではない。実装が aliveCount と直接比較できるため）。
type PhaseParams struct {
	MidAliveThreshold  int `json:"midAliveThreshold"`  // 生存数がこれ以下で Mid
	LateAliveThreshold int `json:"lateAliveThreshold"` // 生存数がこれ以下で Late
	MidTimeMs          int `json:"midTimeMs"`          // 経過時間がこれ以上で Mid（生存数と OR）
	LateTimeMs         int `json:"lateTimeMs"`         // 経過時間がこれ以上で Late
}

// HeatParams: 火力（お題難易度の全体上昇）。詳細は Plan-04。
// heatLevel = Base + int(PerAliveDrop*(maxStores-alive)) + PhaseXxx
// ※ フェーズ別加算を []int（スライス）にすると GameParameters の == 比較が壊れるため
//    個別フィールドで持つ（params.go 冒頭の comparable 制約）。
type HeatParams struct {
	Base         int     `json:"base"`         // 火力基礎値
	PerAliveDrop float64 `json:"perAliveDrop"` // 生存1人減るごとの加算量
	PhaseEarly   int     `json:"phaseEarly"`   // Early の火力加算
	PhaseMid     int     `json:"phaseMid"`     // Mid の火力加算
	PhaseLate    int     `json:"phaseLate"`    // Late の火力加算
}

// StormParams: 下位淘汰（定期的に下位%を強制脱落）。詳細は Plan-04。
// 周期は tick 数で持つ（session は時計を持たず tick 駆動のため）。
type StormParams struct {
	IntervalTicks int     `json:"intervalTicks"` // 実行間隔（tick数。例:40≒6秒 @150ms）
	WarnTicks     int     `json:"warnTicks"`     // 実行の何tick前に予告するか
	ThresholdPct  float64 `json:"thresholdPct"`  // 下位何%を強制脱落（0.0〜1.0）
}

// DistributionParams: 客の分配（restPool→店の行列）。詳細は Plan-03。
// 重み = (WeightFloor + evalNormalized) / (行列長+1)
type DistributionParams struct {
	QueueRefillThreshold int     `json:"queueRefillThreshold"` // 行列がこの数未満の店を分配対象にする
	WeightFloor          float64 `json:"weightFloor"`          // 重みの下駄（最下位店の客ゼロを防ぐ）
}

// PatienceParams: 我慢ゲージの調整。詳細は Plan-02。
// Late では effectiveDt = dtMs / LateMul で実効経過を拡大する（LateMul<1.0 で速く減る）。
type PatienceParams struct {
	LateMul float64 `json:"lateMul"` // 終盤の我慢ゲージ短縮倍率（0<x<1.0 で速く減る）
	AlertMs int     `json:"alertMs"` // 離脱アラート閾値ms（表示用）
}

// BotParams: CPU（Bot）の強さ。合成ルートが bot.Config へ写して各Botに渡す。
type BotParams struct {
	ServeIntervalMs int     `json:"serveIntervalMs"` // 1注文を提供するのにかける間隔ms
	MissRate        float64 `json:"missRate"`         // 注文ごとのミス確率(0..1)
}

// Validate は破綻値を弾く最小限の検証。config 取得（RemoteLoader / DB / config-front POST）で
// 共通に使う。コア game が GameParameters の不変条件を所有する（検証ロジックの単一ソース）。
func (gp GameParameters) Validate() error {
	if gp.Customer.Total <= 0 {
		return fmt.Errorf("customer.total は正である必要 (got %d)", gp.Customer.Total)
	}
	if gp.Credit.InitialLife <= 0 {
		return fmt.Errorf("credit.initialLife は正である必要 (got %d)", gp.Credit.InitialLife)
	}
	if gp.Session.TickIntervalMs <= 0 {
		return fmt.Errorf("session.tickIntervalMs は正である必要 (got %d)", gp.Session.TickIntervalMs)
	}
	if gp.Bot.ServeIntervalMs <= 0 {
		return fmt.Errorf("bot.serveIntervalMs は正である必要 (got %d)", gp.Bot.ServeIntervalMs)
	}
	if gp.Bot.MissRate < 0 || gp.Bot.MissRate > 1 {
		return fmt.Errorf("bot.missRate は 0..1 である必要 (got %v)", gp.Bot.MissRate)
	}
	if gp.Heat.MaxLevel <= 0 {
		return fmt.Errorf("heat.maxLevel は正である必要 (got %d)", gp.Heat.MaxLevel)
	}
	return nil
}

// DefaultParameters はリモートコンフィグ取得失敗時のフォールバック内蔵デフォルト。
// 値は 04_パラメータ仕様.md の初期仮値（すべて実測調整前のサンプル）。
func DefaultParameters() GameParameters {
	return GameParameters{
		Session: SessionParams{
			TickIntervalMs:    150,
			PublishIntervalMs: 250,    // 約4Hz
			// 制限時間は廃止（proto v0.3.0 / #33）。決着保証は下位淘汰(storm)が担う。
			// 0=無効。Plan-05 で checkFinish から時間切れ判定そのものを削除する。
			MatchTimeLimitMs:  0,
		},
		Matching: MatchingParams{
			MinPlayers:       20,
			MaxPlayers:       99,
			StartCountdownMs: 15000,
			MinFill:          99,
		},
		Credit: CreditParams{
			InitialLife: 3,
			LeaveLoss:   LeaveLoss{Normal: 1, Bonus: 1, Claimer: 1, Buzz: 2},
		},
		Customer: CustomerParams{
			Total:   300,
			Normal:  AttributeSpec{Attribute: proto.AttrNormal, Weight: 70, PatienceBaseMs: 8000, OrderCount: 2},
			Bonus:   AttributeSpec{Attribute: proto.AttrBonus, Weight: 15, PatienceBaseMs: 9000, OrderCount: 2},
			Claimer: AttributeSpec{Attribute: proto.AttrClaimer, Weight: 10, PatienceBaseMs: 6000, OrderCount: 1},
			Buzz:    AttributeSpec{Attribute: proto.AttrBuzz, Weight: 5, PatienceBaseMs: 12000, OrderCount: 4},
		},
		Eval: EvalParams{
			EmaAlpha:        0.3,
			WeightAccuracy:  0.5,
			WeightSpeed:     0.5,
			SpeedBaselineMs: 4000,
			SpeedCap:        2.0,
			MinMsPerWord:    200,
			BuzzBonus:       0.2,
			BuzzDecay:       0.98,
			BuzzCap:         0.5,
		},
		Phase: PhaseParams{
			MidAliveThreshold:  70,    // 99人→残り70人以下で中盤
			LateAliveThreshold: 30,    // 残り30人以下で終盤
			MidTimeMs:          30000, // 30秒経過でも中盤
			LateTimeMs:         90000, // 90秒経過でも終盤
		},
		Heat: HeatParams{
			Base:         0,
			PerAliveDrop: 0.1,
			PhaseEarly:   0,
			PhaseMid:     3,
			PhaseLate:    8,
		},
		Storm: StormParams{
			IntervalTicks: 40,   // 40tick≒6秒（TickIntervalMs=150）
			WarnTicks:     10,   // 10tick≒1.5秒前に予告
			ThresholdPct:  0.10, // 下位10%を強制脱落
		},
		Distribution: DistributionParams{
			QueueRefillThreshold: 3,
			WeightFloor:          0.25, // 最下位店の客ゼロを防ぐ（Plan-03 §2.5）
		},
		Patience: PatienceParams{
			LateMul: 0.6,  // Late で実質1.67倍速
			AlertMs: 2000, // 残り2秒で警告色
		},
		Bot: BotParams{
			ServeIntervalMs: 800,  // 旧 ClearIntervalMs 相当
			MissRate:        0.05, // 5%
		},
	}
}
```

#### 5-3. internal/game/session.go（新規作成）

```go
package game

import (
	"fmt"
	"math/rand"

	"takoda99/internal/proto"
)

// session.go は【層1コア】1試合の状態機械＋権威データ。純粋・Tick(dt)駆動（時計は持たず room が dt を渡す）。
//
// たこ焼き経営 BR: 99店が300客を捌き合う。直接攻撃なし。
// Tick loop: stepPhase → stepDistribute → stepPatience → stepEvaluate → stepNormalize → stepHeat → stepStorm → checkFinish
// 各ステップは no-op stub。実ロジックは Plan-02～05 で実装する。

// SessionState は試合の状態。
type SessionState int

const (
	WaitingStart SessionState = iota
	Running
	Finished
)

// Recipient は Outbound の宛先。Broadcast=true で全員。
type Recipient struct {
	PlayerId  PlayerId
	Broadcast bool
}

// Outbound は session が返す「宛先つきメッセージ」。Msg は proto.<Message> の値で、
// room が Envelope に包んで実際の接続へ送る（game は通信を知らない）。
type Outbound struct {
	To  Recipient
	Msg any
}

func to(pid PlayerId, msg any) Outbound { return Outbound{To: Recipient{PlayerId: pid}, Msg: msg} }
func broadcastMsg(msg any) Outbound     { return Outbound{To: Recipient{Broadcast: true}, Msg: msg} }

// customer は客1人の権威状態。属性は試合中不変。
type customer struct {
	attribute      proto.CustomerAttribute
	patienceMaxMs  int
	patienceLeftMs int
	orderCount     int       // 打つ単語数（属性別・来店時のお題本数）
	keystrokeTotal int       // 来店時に発行した全語の正準打鍵数の合計（精度算出の分母）
	assignedStore  *PlayerId // 割り当て先の店。nil=未割当（restPool）
}

// servedStats は1店の提供集計（リザルト用）。
type servedStats struct {
	count       int     // 提供した注文数
	accuracySum float64 // 精度の総和（÷count で平均精度）
	elapsedSum  int64   // 所要msの総和（÷count で平均所要）
}

// storeState は1店分の権威状態。
type storeState struct {
	id             PlayerId
	name           string
	creditLife     int     // 信用(HP)。客の離脱でのみ減少・0で自滅脱落
	evalRaw        float64 // 評価EMA（正規化前）
	buzzBonus      float64 // JK(Buzz)満足の一時加点（毎tick減衰）
	evalNormalized float64 // 生存店内パーセンタイル 0..1
	rank           int     // 生存店内の評価順位
	served         servedStats
	alive          bool

	// 脱落確定時に書き込む（Plan-02 の自滅／Plan-04 の下位淘汰／Plan-05 の優勝者確定）。
	// Plan-05 の Results()/MatchEnd がここを読むので、脱落処理は必ずこの2つを埋めること。
	finalRank   int    // 最終順位。0=未確定（生存中）。1=優勝
	elimination string // "SelfCollapse" / "Cull" / ""（優勝者・未脱落）
}

// PlayerInit は NewSession に渡す初期店舗情報。
type PlayerInit struct {
	Id          PlayerId
	DisplayName string
}

// Session は1試合。words はDIPで注入される部品実装。
type Session struct {
	id     proto.MatchId
	params GameParameters
	words  WordSource
	rng    *rand.Rand

	// 客の権威データ（単一情報源）。移動は ID配列の増減のみ（実体を複製・破棄しない）。
	customers   map[proto.CustomerId]*customer
	storeQueues map[PlayerId][]proto.CustomerId // 各店の行列（先頭=対応中）
	restPool    []proto.CustomerId              // たべたべエリア（未割当）

	stores map[PlayerId]*storeState
	order  []PlayerId // 安定順

	state      SessionState
	phase      proto.Phase
	elapsedMs  int64
	tick       int
	aliveCount int
}

// NewSession は WaitingStart 状態の試合を作る。店舗を初期ライフ／評価初期値で用意し、
// 客レジストリ・行列・たべたべエリアは空で初期化する（客の生成は Start 時の initCustomers）。
func NewSession(id proto.MatchId, params GameParameters, words WordSource, rng *rand.Rand, inits []PlayerInit) *Session {
	s := &Session{
		id: id, params: params, words: words, rng: rng,
		customers:   make(map[proto.CustomerId]*customer),
		storeQueues: make(map[PlayerId][]proto.CustomerId, len(inits)),
		restPool:    nil,
		stores:      make(map[PlayerId]*storeState, len(inits)),
		state:       WaitingStart,
		phase:       proto.PhaseEarly,
	}
	life := params.Credit.InitialLife
	for _, in := range inits {
		s.stores[in.Id] = &storeState{
			id:         in.Id,
			name:       in.DisplayName,
			creditLife: life,
			evalRaw:    0,
			alive:      true,
		}
		s.storeQueues[in.Id] = nil
		s.order = append(s.order, in.Id)
	}
	s.aliveCount = len(inits)
	return s
}

// State は現在の状態を返す。
func (s *Session) State() SessionState { return s.state }

// Snapshot は99店概況と生存数を返す（publisher が盤面の定期配信に使う）。
func (s *Session) Snapshot() ([]proto.StoreSummary, int) {
	return s.summaries(), s.aliveCount
}

// Start は WaitingStart→Running へ遷移し、客プール(300)を生成して各店へ MatchStart を配る。
func (s *Session) Start() []Outbound {
	if s.state != WaitingStart {
		return nil
	}
	s.state = Running
	s.initCustomers()
	stores := s.summaries()
	out := make([]Outbound, 0, len(s.order))
	for _, sid := range s.order {
		out = append(out, to(sid, proto.MatchStart{
			MatchId:     s.id,
			SelfStoreId: sid,
			Params:      s.publicParams(),
			Phase:       s.phase,
			Stores:      stores,
		}))
	}
	return out
}

// ApplyOrderServed は提供完了(OrderServed)を処理する。
// サニティ検証 → 提供スコア(perOrder) → 評価EMA反映 → 満足客を行列から除去 → EvaluationUpdate 配信。
func (s *Session) ApplyOrderServed(from PlayerId, r proto.OrderServed) []Outbound {
	if s.state != Running {
		return nil
	}
	st := s.stores[from]
	c := s.customers[r.CustomerId]
	q := s.storeQueues[from]
	if st == nil || !st.alive || c == nil || c.assignedStore == nil || *c.assignedStore != from ||
		len(q) == 0 || q[0] != r.CustomerId {
		return nil
	}

	ep := s.params.Eval
	floor := ep.MinMsPerWord * c.orderCount
	elapsed := r.ElapsedMs
	if elapsed < floor {
		elapsed = floor
	}
	if elapsed <= 0 {
		elapsed = 1
	}
	keys := c.keystrokeTotal
	if keys <= 0 {
		keys = 1
	}
	miss := r.MissCount
	if miss < 0 {
		miss = 0
	}
	if miss > keys {
		miss = keys
	}

	accuracy := 1 - float64(miss)/float64(keys)
	speed := clampF(float64(ep.SpeedBaselineMs)/float64(elapsed), 0, ep.SpeedCap)
	perOrder := ep.WeightAccuracy*accuracy + ep.WeightSpeed*speed

	st.evalRaw = ep.EmaAlpha*perOrder + (1-ep.EmaAlpha)*st.evalRaw
	if c.attribute == proto.AttrBuzz {
		st.buzzBonus = clampF(st.buzzBonus+ep.BuzzBonus, 0, ep.BuzzCap)
	}

	st.served.count++
	st.served.accuracySum += accuracy
	st.served.elapsedSum += int64(elapsed)

	s.releaseToRest(r.CustomerId)

	return append([]Outbound(nil), to(from, proto.EvaluationUpdate{
		EvalRaw:    s.evalScore(st),
		Normalized: st.evalNormalized,
		Rank:       st.rank,
		AliveCount: s.aliveCount,
	}))
}

func (s *Session) evalScore(st *storeState) float64 { return st.evalRaw + st.buzzBonus }

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Tick は時間を dt 進め、試合ループの各ステップを順序で呼ぶ。
// 各ステップは no-op stub。実ロジックは Plan-02～05 で実装する。
func (s *Session) Tick(dtMs int) []Outbound {
	if s.state != Running {
		return nil
	}
	s.elapsedMs += int64(dtMs)
	s.tick++

	var out []Outbound
	out = s.stepPhase(out)          // 1. フェーズ判定（Early/Mid/Late）
	out = s.stepDistribute(out)     // 2. 客分配（restPool→行列・CustomerArrived）
	out = s.stepPatience(dtMs, out) // 3. 我慢ゲージ減算 → 離脱（CustomerLeft/信用）
	out = s.stepEvaluate(out)       // 4. 評価再計算（BuzzDecay）
	out = s.stepNormalize(out)      // 5. 正規化 → rank（EvaluationUpdate）
	out = s.stepHeat(out)           // 6. 火力更新（DifficultyUpdate）
	out = s.stepStorm(out)          // 7. 下位淘汰の判定・予告
	out = s.checkFinish(out)        // 8. 終了条件
	return out
}

// ── 試合ループの各ステップ（no-op stub）────────────────────────

func (s *Session) stepPhase(out []Outbound) []Outbound      { return out }
func (s *Session) stepDistribute(out []Outbound) []Outbound  { return out }
func (s *Session) stepPatience(dtMs int, out []Outbound) []Outbound { _ = dtMs; return out }

// stepEvaluate は評価の時間減衰を進める。
// evalRaw 本体は提供イベント(ApplyOrderServed)で即時反映するため、ここでは JK 一時加点(buzzBonus)を
// 毎tick減衰させるだけ。
func (s *Session) stepEvaluate(out []Outbound) []Outbound {
	decay := s.params.Eval.BuzzDecay
	for _, st := range s.stores {
		if !st.alive || st.buzzBonus == 0 {
			continue
		}
		st.buzzBonus *= decay
		if st.buzzBonus < 1e-4 {
			st.buzzBonus = 0
		}
	}
	return out
}

func (s *Session) stepNormalize(out []Outbound) []Outbound { return out }
func (s *Session) stepHeat(out []Outbound) []Outbound      { return out }
func (s *Session) stepStorm(out []Outbound) []Outbound     { return out }

// checkFinish は終了条件を判定する。骨組みでは生存1で Finished にするだけ。
// 順位確定・MatchEnd 配信は Plan-05 でここを差し替えて実装する。
// ※ 制限時間（MatchTimeLimitMs）は廃止済みのため判定に使わない（既定値0・storm が決着を保証）。
func (s *Session) checkFinish(out []Outbound) []Outbound {
	if len(s.order) > 1 && s.aliveCount <= 1 {
		s.state = Finished
	}
	return out
}

// ── 客システム ──────────────────────────────────────────────

func (s *Session) initCustomers() {
	total := s.params.Customer.Total
	s.restPool = make([]proto.CustomerId, 0, total)
	for i := 0; i < total; i++ {
		cid := proto.CustomerId(fmt.Sprintf("c-%d", i+1))
		spec := s.rollAttribute()
		s.customers[cid] = &customer{
			attribute:     spec.Attribute,
			patienceMaxMs: spec.PatienceBaseMs,
			orderCount:    spec.OrderCount,
		}
		s.restPool = append(s.restPool, cid)
	}
}

func (s *Session) attributeSpecs() []AttributeSpec {
	c := s.params.Customer
	return []AttributeSpec{c.Normal, c.Bonus, c.Claimer, c.Buzz}
}

func (s *Session) rollAttribute() AttributeSpec {
	specs := s.attributeSpecs()
	total := 0
	for _, a := range specs {
		total += a.Weight
	}
	if total <= 0 {
		return specs[0]
	}
	r := s.rng.Intn(total)
	for _, a := range specs {
		if r < a.Weight {
			return a
		}
		r -= a.Weight
	}
	return specs[len(specs)-1]
}

func (s *Session) admitCustomer(cid proto.CustomerId, store PlayerId) (Outbound, bool) {
	c := s.customers[cid]
	if c == nil {
		return Outbound{}, false
	}
	s.assignCustomer(cid, store)
	words := make([]string, 0, c.orderCount)
	keystrokes := 0
	for i := 0; i < c.orderCount; i++ {
		w := s.words.Next(s.wordLevel(), s.rng)
		words = append(words, w.Text)
		keystrokes += w.KeystrokeCount
	}
	c.keystrokeTotal = keystrokes
	view := proto.CustomerView{
		CustomerId:    cid,
		Attribute:     c.attribute,
		OrderCount:    c.orderCount,
		Words:         words,
		PatienceMaxMs: c.patienceMaxMs,
	}
	return to(store, view), true
}

func (s *Session) wordLevel() int { return 0 }

// ── 客の移動ヘルパ ────────────────────────────────────────────

func (s *Session) assignCustomer(cid proto.CustomerId, store PlayerId) {
	c := s.customers[cid]
	if c == nil {
		return
	}
	s.restPool = removeCustomer(s.restPool, cid)
	s.storeQueues[store] = append(s.storeQueues[store], cid)
	c.assignedStore = &store
	c.patienceLeftMs = c.patienceMaxMs
}

func (s *Session) releaseToRest(cid proto.CustomerId) {
	c := s.customers[cid]
	if c == nil {
		return
	}
	if c.assignedStore != nil {
		q := s.storeQueues[*c.assignedStore]
		s.storeQueues[*c.assignedStore] = removeCustomer(q, cid)
	}
	c.assignedStore = nil
	s.restPool = append(s.restPool, cid)
}

func removeCustomer(ids []proto.CustomerId, cid proto.CustomerId) []proto.CustomerId {
	for i, x := range ids {
		if x == cid {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}

// ── サマリ ────────────────────────────────────────────────

func (s *Session) summaries() []proto.StoreSummary {
	out := make([]proto.StoreSummary, 0, len(s.order))
	for _, sid := range s.order {
		st := s.stores[sid]
		out = append(out, proto.StoreSummary{
			StoreId:        st.id,
			DisplayName:    st.name,
			EvalNormalized: st.evalNormalized,
			Rank:           st.rank,
			CreditLife:     st.creditLife,
			Alive:          st.alive,
		})
	}
	return out
}

func (s *Session) publicParams() proto.GameParametersPublicSubset {
	return proto.GameParametersPublicSubset{
		MatchTimeLimitMs: s.params.Session.MatchTimeLimitMs,
		InitialLife:      s.params.Credit.InitialLife,
		MaxStores:        len(s.order),
	}
}
```

#### 5-4. internal/game/params_validate_test.go（書き直し）

旧 Validate テストを削除し、新 Validate に合わせて書き直す。テスト対象は `Customer.Total > 0`, `Credit.InitialLife > 0`, `Session.TickIntervalMs > 0`, `Bot.ServeIntervalMs > 0`, `Bot.MissRate in [0,1]`, `Heat.MaxLevel > 0`。

---

### Phase 6: インフラ層の調整

Phase 5 で `game.Session` のシグネチャが変わったため、それに依存するファイルを修正する。

#### 6-1. internal/room/room.go

**変更点:**
- import パスを `takoda99/internal/...` に変更（Phase 2 で済んでいるはず）
- `handle()` のメッセージ振り分けをたこ焼き版に変更:
  - 旧: `TypeDakenClearReport` → `session.ApplyDakenClear` / `TypeStrategySelect` → `session.ApplyStrategy`
  - 新: `TypeOrderServed` → `session.ApplyOrderServed`
- `envelopeOf()` の型マッチをたこ焼き版メッセージに変更:
  - 削除: `Welcome`, `DakenIssued`, `DakenExpired`, `ComboUpdated`, `DifficultyUpdated`, `AttackIncoming`, `DakenStackUpdated`, `KoNotified`, `GameOver`
  - 追加: `CustomerArrived` (= `CustomerView`), `CustomerLeft`, `CreditUpdate`, `EvaluationUpdate`, `DifficultyUpdate`, `PhaseChange`, `StoreListUpdate`, `ForcedEliminationWarning`, `StoreEliminated`, `MatchEnd`
  - 保持: `MatchStart`, `PlayerListUpdated` → `StoreListUpdate`, `MatchmakingStatus`
- `Snapshot()` の返り値が `[]proto.StoreSummary` に変わるため、`publisher` の型を確認して合わせる

```go
// handle のたこ焼き版
func (r *Room) handle(in inbound) []game.Outbound {
	switch in.env.Type {
	case proto.TypeOrderServed:
		var m proto.OrderServed
		if json.Unmarshal(in.env.Payload, &m) == nil {
			return r.session.ApplyOrderServed(in.pid, m)
		}
	}
	return nil
}
```

`envelopeOf` の新しい型マッチ:

```go
func envelopeOf(msg any) (proto.Envelope, bool) {
	var typ string
	switch msg.(type) {
	case proto.MatchStart:
		typ = proto.TypeMatchStart
	case proto.CustomerView: // CustomerArrived
		typ = proto.TypeCustomerArrived
	case proto.CustomerLeft:
		typ = proto.TypeCustomerLeft
	case proto.CreditUpdate:
		typ = proto.TypeCreditUpdate
	case proto.EvaluationUpdate:
		typ = proto.TypeEvaluationUpdate
	case proto.DifficultyUpdate:
		typ = proto.TypeDifficultyUpdate
	case proto.PhaseChange:
		typ = proto.TypePhaseChange
	case proto.StoreListUpdate:
		typ = proto.TypeStoreListUpdate
	case proto.ForcedEliminationWarning:
		typ = proto.TypeForcedEliminationWarning
	case proto.StoreEliminated:
		typ = proto.TypeStoreEliminated
	case proto.MatchEnd:
		typ = proto.TypeMatchEnd
	case proto.MatchmakingStatus:
		typ = proto.TypeMatchmakingStatus
	default:
		return proto.Envelope{}, false
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return proto.Envelope{}, false
	}
	return proto.Envelope{Type: typ, Payload: data}, true
}
```

#### 6-2. internal/app/app.go

**変更点:**
- `Deps` から `Strategies map[int]game.TargetingStrategy` を削除
- `DefaultStrategies()` 関数を削除
- `DefaultDeps()` から `Strategies` と `targeting` import を削除
- `RunMatch()` の `game.NewSession()` 呼び出しを新シグネチャに合わせる:
  - 旧: `game.NewSession(id, params, strategies, words, rng, inits)`
  - 新: `game.NewSession(id, params, words, rng, inits)`
- `NewBotPlayer()` の `bot.Config` フィールドを新名に:
  - 旧: `ClearIntervalMs`
  - 新: `ServeIntervalMs`

修正後の `Deps`:

```go
type Deps struct {
	Params game.GameParameters
	Words  game.WordSource
	Store  store.ResultStore
	Clock  room.Clock
}
```

修正後の `DefaultDeps`:

```go
func DefaultDeps() Deps {
	return Deps{
		Params: game.DefaultParameters(),
		Words:  odai.NewStaticPool(),
		Store:  store.Noop{},
		Clock:  room.RealClock{},
	}
}
```

修正後の `RunMatch` 内の session 生成:

```go
sess := game.NewSession(nextMatchID(), d.Params, d.Words, newRng(), inits)
```

#### 6-3. internal/bot/bot.go

**変更点:**
- 旧ダケンクリア報告 → たこ焼き版の `OrderServed` 報告に変更
- `Config.ClearIntervalMs` → `Config.ServeIntervalMs` にリネーム
- `pending []proto.DakenId` → 来店中の客の ID を保持する方式に変更
- `onMessage()` で `MatchStart`, `CustomerArrived` (= `CustomerView`), `MatchEnd` を処理
- `act()` で `OrderServed` を送信

修正後の `Config`:

```go
type Config struct {
	ServeIntervalMs int     // 1注文を提供するのにかける平均ms
	MissRate        float64 // ミス確率(0..1)
}

func DefaultConfig() Config { return Config{ServeIntervalMs: 800, MissRate: 0.05} }
```

修正後の `Bot` 構造体:

```go
type Bot struct {
	conn       transport.Connection
	cfg        Config
	rng        *rand.Rand
	pendingCid []proto.CustomerId // 保持中（未提供）の客ID
}
```

修正後の `onMessage`:

```go
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
	case proto.TypeMatchEnd:
		return true
	}
	return false
}
```

修正後の `act`:

```go
func (b *Bot) act() {
	if len(b.pendingCid) == 0 {
		return
	}
	cid := b.pendingCid[0]
	b.pendingCid = b.pendingCid[1:]

	miss := 0
	if b.rng.Float64() < b.cfg.MissRate {
		miss = 1
	}
	b.send(proto.TypeOrderServed, proto.OrderServed{
		CustomerId: cid,
		ElapsedMs:  b.cfg.ServeIntervalMs,
		MissCount:  miss,
	})
}
```

#### 6-4. cmd/server/main.go

**変更点:**
- import から `targeting` パッケージを削除
- `botConfig()` の `ClearIntervalMs` → `ServeIntervalMs` にリネーム
- `log.Printf("textro99 server: ...")` → `log.Printf("takoda99 server: ...")`
- `welcome()` 関数: 旧 `proto.Welcome` が Takoda99-Proto に存在するか確認し、なければ MatchmakingJoin を直接使う方式に変更（Proto 側の定義次第）
- `awaitJoinName()`: `proto.TypeMatchmakingJoin` を使用（変更不要のはず）

修正後の `botConfig`:

```go
botConfig := func() bot.Config {
	p, _ := provider.Load(ctx)
	return bot.Config{
		ServeIntervalMs: p.Bot.ServeIntervalMs,
		MissRate:        p.Bot.MissRate,
	}
}
```

#### 6-5. transport/publisher.go の型合わせ

`StatePublisher.Publish()` の第2引数が `[]proto.PlayerSummary` から `[]proto.StoreSummary` に変わる。publisher.go と publisher_test.go の型を更新する。

#### 6-6. odai/pool.go の WordSource interface 合わせ

`WordSource` interface から `NextTrap` が消えたため、`odai.StaticPool` が `NextTrap` を持っている場合は削除する（持っていなければ変更不要）。

#### 6-7. config/loader.go

`DefaultLoader.Load()` が返す `GameParameters` が新しい構造になるため、`game.DefaultParameters()` を呼んでいるだけであれば変更不要。

#### 6-8. テストファイルの修正

旧 game メカニクスに依存するテストファイルの修正方針:

| ファイル | 修正内容 |
|---|---|
| `internal/game/params_validate_test.go` | 新 Validate ルールに合わせて書き直し |
| `internal/room/room_test.go` | session の型・メソッド変更に追従。テスト内で使う proto メッセージ型を Takoda99-Proto の型に変更 |
| `internal/app/app_test.go` | Deps の Strategies 削除に追従 |
| `internal/app/e2e_wire_test.go` | session 生成・メッセージ型の変更に追従 |
| `internal/app/headless_test.go` | 同上 |
| `internal/app/scale_test.go` | 同上 |
| `internal/bot/bot_test.go` | OrderServed 報告への変更に追従 |
| `internal/proto/wire_golden_test.go` | Takoda99-Proto の型でゴールデンテストを書き直し |
| `internal/transport/publisher_test.go` | StoreSummary 型への変更に追従 |

---

## 4. ローカル確認

すべての Phase が完了したら以下を順に実行する。

```bash
cd /Users/ryu/kindai/2026/THEHACK/Takoda99-Server

# 1. ビルド確認
go build ./...

# 2. テスト確認
go test ./...

# 3. 静的解析
go vet ./...

# 4. ローカル起動（solo モード）
go run ./cmd/server --mode solo --bots 3

# 別ターミナルで WebSocket 接続テスト
# wscat -c ws://localhost:8080/ws
# → 接続後、MatchmakingJoin を送り、MatchStart を受信できることを確認
```

起動確認で見るポイント:
- `takoda99 server: mode=solo addr=:8080` のログ出力
- WebSocket 接続→MatchStart 受信まで動作
- Bot が CustomerArrived を受けて OrderServed を返す（エラーログが出ない）
- Ctrl+C で正常終了

---

## 5. 完了条件

- [ ] Textro99-Server のインフラ層（transport/room/matchmaking/config/configapi/db/store/odai/app/bot）のバグ修正済みコードが Takoda99-Server に入っている
- [ ] 旧ゲームロジックが完全に除去されている:
  - [ ] `internal/game/attack.go` 削除
  - [ ] `internal/game/combo.go` 削除
  - [ ] `internal/game/offset.go` 削除
  - [ ] `internal/game/stack.go` 削除
  - [ ] `internal/game/difficulty.go` 削除
  - [ ] `internal/game/combat_test.go` 削除
  - [ ] `internal/game/combo_test.go` 削除
  - [ ] `internal/targeting/` ディレクトリ削除
  - [ ] `cmd/balancesim/` 削除
  - [ ] `cmd/matchsim/` 削除
- [ ] `go.mod` の module パスが `takoda99` になっている
- [ ] 全 `.go` ファイルの import パスが `"takoda99/internal/..."` になっている
- [ ] Proto 参照が `github.com/Okashimachi/Takoda99-Proto` になっている
- [ ] `internal/proto/messages.go` が Takoda99-Proto のたこ焼き版型のみを再輸出している
- [ ] `GameParameters` がたこ焼き版フィールドのみになっている（Session, Matching, Credit, Customer, Eval, Phase, Heat, Storm, Distribution, Patience, Bot）
- [ ] 旧パラメータ（Combo, Attack, Stack, Difficulty, Odai）が完全に除去されている
- [ ] Tick loop の骨格（8ステップの no-op stub）が配置されている
- [ ] `ApplyOrderServed` が動作する
- [ ] `room.go` の handle/envelopeOf がたこ焼き版メッセージに対応している
- [ ] `bot.go` が OrderServed を送信する方式に変わっている
- [ ] `app.go` から targeting 依存が除去されている
- [ ] `go build ./...` が通る
- [ ] `go test ./...` が通る
- [ ] `go vet ./...` がクリーン
- [ ] `--mode solo` で起動し、WebSocket 接続→MatchStart 受信まで確認

---

## 6. 触らないもの（後続Planの範囲）

| 範囲 | 担当 Plan |
|---|---|
| stepPatience / stepDistribute / stepNormalize の実ロジック | Plan-02（我慢離脱信用）, Plan-03（客分配と評価正規化） |
| stepPhase / stepHeat / stepStorm の実ロジック | Plan-04（フェーズ火力下位淘汰） |
| 脱落順位・MatchEnd 配信 | Plan-05（脱落順位リザルト） |
| パラメータの具体的な初期値チューニング | Plan-06 |
| シミュレータの再実装 | Plan-06 以降 |
| Proto への変更 | 必要になった時点で別途承認 |

---

## 7. リスクと対策

| リスク | 影響 | 対策 |
|---|---|---|
| cp 時に Textro99-Server のバグ修正を取りこぼす | インフラに既知バグが残る | 完全 cp（rsync --exclude='.git'）なので差分漏れは構造的に起きない |
| 旧テストを消しすぎてインフラ層のカバレッジが落ちる | リグレッション検知力の低下 | game/ 配下のテストのみ削除。transport/room/matchmaking/config のテストは全保持 |
| room_test.go / app_test.go が session 変更で壊れる | `go test` が通らない | Phase 6-8 で明示的に修正対象として列挙。コンパイルエラーで漏れを検知 |
| bot が旧メカニクスに依存していてコンパイルが通らない | ビルド不能 | Phase 6-3 で OrderServed 報告に全面書き換え |
| proto の再輸出ラッパで型の漏れ | コンパイルエラー | Takoda99-Proto の型を全列挙して再輸出。未追加は "undefined" で即検知 |
| odai の WordSource が NextTrap を実装している | interface 不一致でコンパイルエラー | Phase 6-6 で確認。NextTrap メソッドがあれば削除 |
| `go.work` でローカルの Proto を参照している場合 | module 解決がローカルに引きずられる | `go.work` のエントリが Takoda99-Proto を指していることを確認 |
| パラメータ型に slice/map を入れると GameParameters の `==` 比較が壊れる | config の差分検出・backfill（Plan-06）が破綻 | 全型を comparable に保つ。フェーズ別加算は `PhaseEarly/Mid/Late` の個別フィールド、属性別損失は `LeaveLoss` struct（本プランで対応済み） |
