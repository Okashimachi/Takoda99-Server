# Plan-06: config基盤とDB（Takoda版）

> **目的**: Takoda99専用のPostgres DBをセットアップし、config-front を別URLでデプロイする。GameParameters をたこ焼き版のフルスキーマへ差し替え、config-front の params.ts と同期させる。
> **対応issue**: #11(tako-K) + 新規issue
> **依存**: Plan-01（基盤移行後）。
> DB/Vercel の配線（Step 1・5・7）は Plan-02〜05 と並行して進められるが、
> **パラメータのフルスキーマ確定（Step 2〜4・6）は Plan-02/03/04 の型定義が正典**なので、
> それらの完了後に写しを作る。先に config-front を作ると型がズレる。
> **参照**: パラメータ仕様, config-front 運用メモ, Textro99 での BackfillLegacyDefaults 教訓

---

## 1. 前提知識

### config の全体像

Takoda99-Server のパラメータ管理は3段構成で動いている。

1. **GameParameters**（`internal/game/params.go`）: 全パラメータの構造体。コア game が所有し、`Validate()` で不変条件を担保する。`DefaultParameters()` が内蔵デフォルト。
2. **ConfigProvider**（`internal/game/ports.go`）: 起動時取得のDIPインタフェース。`Load(ctx) (GameParameters, error)` を満たす実装が3つ:
   - `config.DefaultLoader`（`internal/config/loader.go`）: 常に内蔵デフォルトを返す。
   - `config.RemoteLoader`（`internal/config/remote.go`）: HTTP GET で JSON を取得。
   - `db.ConfigStore`（`internal/db/config.go`）: Postgres の `game_config` テーブル（JSONB 単一行）から取得。
3. **configapi ハンドラ**（`internal/configapi/handler.go`）: `/api/params` エンドポイント。GET で現在値を返し、POST（`X-Admin-Token` ヘッダ必須）で保存。CORS は `CONFIG_FRONT_ORIGIN` で制御。`configapi.Store` インタフェースを経由し、`db.ConfigStore` が満たす。

### 合成ルート（cmd/server/main.go）での配線

```
chooseProvider(): DATABASE_URL > CONFIG_URL > DefaultLoader の優先度で ConfigProvider を選ぶ
  ├─ DATABASE_URL あり → db.NewPool → db.NewConfigStore → Migrate → ConfigStore を返す
  ├─ CONFIG_URL あり  → config.NewRemoteLoader を返す
  └─ どちらもなし     → config.DefaultLoader を返す

provider が db.ConfigStore のとき:
  /api/params ← configapi.NewHandler(cfgStore, CONFIG_ADMIN_TOKEN, CONFIG_FRONT_ORIGIN)
provider が db.ConfigStore でないとき:
  /api/params ← configapi.NewHandler(nil, ...) → GET/POST は 503
```

loadDeps() は毎マッチ生成時に `provider.Load(ctx)` を呼ぶため、config-front での変更は**次の試合から**再起動なしで反映される（進行中の試合は固定）。matching パラメータは起動時スナップショットのため**再起動が必要**。

### 現状の GameParameters

`internal/game/params.go` にて、旧Textro項目（Combo/Attack/Stack/Difficulty/Odai）とたこ焼き版の新項目（Credit/Customer/Eval）が混在している。
たこ焼き版で必要な Phase/Heat/Storm/Distribution/Patience/Bot セクションはまだ存在しない。

### config-front の現状

- リポジトリ: `Okashimachi/config-front`
- 既存デプロイ: `config-front-self.vercel.app`（Textro99用）
- `lib/params.ts` に TypeScript 型定義があり、Go 側の `GameParameters` と手動同期する必要がある。
- 環境変数 `NEXT_PUBLIC_API_URL` でサーバーの接続先を切り替える。

---

## 2. 現状のコード

### `internal/db/pool.go` — DB接続プール

```go
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error)
// pgxpool.New → Ping → エラーなら返す（合成ルートがデフォルトで起動継続）
```

### `internal/db/config.go` — ConfigStore

```go
type ConfigStore struct { pool *pgxpool.Pool }

func (s *ConfigStore) Migrate(ctx context.Context) error
// CREATE TABLE IF NOT EXISTS game_config (id int PK, params jsonb, updated_at timestamptz)
// ON CONFLICT DO NOTHING で seed（既存レコードを壊さない）

func (s *ConfigStore) Load(ctx context.Context) (game.GameParameters, error)
// SELECT params → Unmarshal → Validate → 失敗時も内蔵デフォルト + err

func (s *ConfigStore) Save(ctx context.Context, gp game.GameParameters) error
// Validate → Marshal → UPSERT
```

### `internal/configapi/handler.go` — /api/params

```go
func NewHandler(store Store, token string, allowOrigins []string) http.Handler
// GET:  store.Load → JSON 返却（公開）
// POST: X-Admin-Token 検証 → JSON デコード → Validate → store.Save
// CORS: CONFIG_FRONT_ORIGIN（カンマ区切り可）
```

### 環境変数（cmd/server/main.go が参照）

| 変数名 | 用途 |
|---|---|
| `DATABASE_URL` | Postgres 接続文字列。最優先の ConfigProvider |
| `CONFIG_URL` | HTTP エンドポイント（RemoteLoader 用。DB がなければ次善） |
| `CONFIG_ADMIN_TOKEN` | `/api/params` POST の認証トークン |
| `CONFIG_FRONT_ORIGIN` | CORS 許可オリジン（カンマ区切り、空="*"） |
| `ALLOWED_ORIGINS` | /ws WebSocket の許可オリジン（別用途だが同じ parseCSV パターン） |

---

## 3. 実装手順

### Step 1: Postgres インスタンスの作成

選択肢と比較:

| サービス | 無料枠 | 特徴 |
|---|---|---|
| Render Postgres | 90日間無料 → 有料 $7/月〜 | サーバーと同じプラットフォーム、レイテンシ最小 |
| Supabase | 500MB / 2プロジェクト | 管理UIあり、API不要でも便利。自動停止あり |
| Neon | 0.5GB / 3ブランチ | サーバーレス、コールドスタートあり |

推奨: **Neon**（無料枠が期限なし、接続文字列が DATABASE_URL としてそのまま使える、ハッカソン規模で十分）。Render Postgres は90日後に課金が発生するため、短期イベントでは注意。

作成手順（Neon の場合）:
```bash
# 1. https://neon.tech でプロジェクト作成
#    リージョン: ap-northeast-1（東京）
#    DB名: takoda99

# 2. 接続文字列をコピー（Dashboard → Connection Details）
#    postgresql://takoda99_owner:xxxx@ep-xxx.ap-northeast-1.aws.neon.tech/takoda99?sslmode=require

# 3. Render の環境変数に設定
#    DATABASE_URL = (上の接続文字列)
#    CONFIG_ADMIN_TOKEN = (任意のランダム文字列。openssl rand -hex 32 で生成)
#    CONFIG_FRONT_ORIGIN = https://takoda99-config.vercel.app
```

### Step 2: GameParameters のスキーマ差し替え

`internal/game/params.go` で旧フィールドを削除し、たこ焼き版のフルスキーマにする:

```go
type GameParameters struct {
    Session      SessionParams      `json:"session"`
    Matching     MatchingParams     `json:"matching"`
    Credit       CreditParams       `json:"credit"`
    Customer     CustomerParams     `json:"customer"`
    Eval         EvalParams         `json:"eval"`
    Phase        PhaseParams        `json:"phase"`        // tako-H: フェーズ遷移
    Heat         HeatParams         `json:"heat"`         // tako-H: 火力制御
    Storm        StormParams        `json:"storm"`        // tako-H: 下位淘汰
    Distribution DistributionParams `json:"distribution"` // tako-G: 客分配
    Patience     PatienceParams     `json:"patience"`     // tako-F: 我慢ゲージ
    Bot          BotParams          `json:"bot"`          // Bot調整
}
```

旧フィールド（`Combo`, `Attack`, `Stack`, `Difficulty`, `Odai`）は**全削除**。これらの型定義・デフォルト値・バリデーションも削除する。

新セクションのパラメータ型。**これらの定義の正典は各実装プラン**であり、
本プランで別案を作らないこと（config-front はこのスキーマの写しにすぎない）:

| 型 | 正典プラン | 備考 |
|---|---|---|
| `CreditParams`(+`LeaveLoss`) | Plan-02 | 信用・属性別の離脱ペナルティ |
| `PatienceParams` | Plan-02 | 我慢ゲージ |
| `DistributionParams` | Plan-03 | 客分配 |
| `PhaseParams` / `HeatParams` / `StormParams` | Plan-04 | フェーズ・火力・下位淘汰 |
| `BotParams` | 本プラン（新規） | Bot の強さ調整 |

```go
// ── Plan-02 が定義 ──────────────────────────────
type LeaveLoss struct {
    Normal  int `json:"normal"`
    Bonus   int `json:"bonus"`
    Claimer int `json:"claimer"`
    Buzz    int `json:"buzz"`
}

type CreditParams struct {
    InitialLife int       `json:"initialLife"` // 初期信用
    LeaveLoss   LeaveLoss `json:"leaveLoss"`   // 属性別の離脱ペナルティ
}

type PatienceParams struct {
    LateMul float64 `json:"lateMul"` // 終盤の我慢ゲージ短縮倍率（<1.0 で速く減る）
    AlertMs int     `json:"alertMs"` // 離脱アラート閾値（表示用）
}

// ── Plan-03 が定義 ──────────────────────────────
type DistributionParams struct {
    QueueRefillThreshold int     `json:"queueRefillThreshold"` // 行列がこの数未満の店を分配対象にする
    WeightFloor          float64 `json:"weightFloor"`          // 重みの下駄（最下位店の客ゼロを防ぐ）
}

// ── Plan-04 が定義 ──────────────────────────────
type PhaseParams struct {
    MidAliveThreshold  int `json:"midAliveThreshold"`  // Early→Mid の生存数閾値
    LateAliveThreshold int `json:"lateAliveThreshold"` // Mid→Late の生存数閾値
    MidTimeMs          int `json:"midTimeMs"`          // Early→Mid の経過時間閾値
    LateTimeMs         int `json:"lateTimeMs"`         // Mid→Late の経過時間閾値
}

type HeatParams struct {
    Base         int     `json:"base"`         // 火力基礎値
    PerAliveDrop float64 `json:"perAliveDrop"` // 生存1人減るごとの加算量
    PhaseEarly   int     `json:"phaseEarly"`   // Early の火力加算
    PhaseMid     int     `json:"phaseMid"`     // Mid の火力加算
    PhaseLate    int     `json:"phaseLate"`    // Late の火力加算
}

type StormParams struct {
    IntervalTicks int     `json:"intervalTicks"` // 実行間隔（tick数）
    WarnTicks     int     `json:"warnTicks"`     // 何tick前に予告するか
    ThresholdPct  float64 `json:"thresholdPct"`  // 下位何%を強制脱落（0.0〜1.0）
}

// ── 本プランで新規追加 ──────────────────────────
type BotParams struct {
    BaseAccuracy    float64 `json:"baseAccuracy"`    // Bot の基準精度
    BaseElapsedMs   int     `json:"baseElapsedMs"`   // Bot の基準所要ms
    AccuracyJitter  float64 `json:"accuracyJitter"`  // 精度のゆらぎ幅
    ElapsedJitterMs int     `json:"elapsedJitterMs"` // 所要のゆらぎ幅(ms)
}
```

**全型が `==` 比較可能（comparable）であること**が制約。`map` や slice をフィールドに
使わない（既存 params.go のコメント「すべて全項目 comparable に保つ」を踏襲）。
そのため `LeaveLoss` は属性→値の map ではなく固定4フィールドの struct、
`HeatParams` のフェーズ別加算も map ではなく個別フィールドにしている。



### Step 3: DefaultParameters() と Validate() の更新

```go
func DefaultParameters() GameParameters {
    return GameParameters{
        Session: SessionParams{
            TickIntervalMs:    150,
            PublishIntervalMs: 250,
            MatchTimeLimitMs:  0, // 制限時間は廃止(proto v0.3.0)
        },
        Matching: MatchingParams{
            MinPlayers:       20,
            MaxPlayers:       99,
            StartCountdownMs: 15000,
        },
        Credit: CreditParams{
            InitialLife: 3,
            LeaveLoss:   LeaveLoss{Normal: 1, Bonus: 1, Claimer: 1, Buzz: 2},
        },
        Customer: CustomerParams{ /* 既存のまま */ },
        Eval:     EvalParams{ /* 既存のまま */ },
        Phase: PhaseParams{
            MidAliveThreshold: 70, LateAliveThreshold: 30,
            MidTimeMs: 30000, LateTimeMs: 90000,
        },
        Heat: HeatParams{
            Base: 0, PerAliveDrop: 0.1,
            PhaseEarly: 0, PhaseMid: 3, PhaseLate: 8,
        },
        Storm:        StormParams{IntervalTicks: 40, WarnTicks: 10, ThresholdPct: 0.10},
        Distribution: DistributionParams{QueueRefillThreshold: 3, WeightFloor: 0.25},
        Patience:     PatienceParams{LateMul: 0.6, AlertMs: 2000},
        Bot:          BotParams{BaseAccuracy: 0.85, BaseElapsedMs: 3000, AccuracyJitter: 0.1, ElapsedJitterMs: 500},
    }
}

func (gp GameParameters) Validate() error {
    // 旧バリデーション(stack.limit, difficulty.maxLevel)を削除
    if gp.Customer.Total <= 0 {
        return fmt.Errorf("customer.total は正である必要 (got %d)", gp.Customer.Total)
    }
    if gp.Credit.InitialLife <= 0 {
        return fmt.Errorf("credit.initialLife は正である必要 (got %d)", gp.Credit.InitialLife)
    }
    if gp.Distribution.QueueRefillThreshold <= 0 {
        return fmt.Errorf("distribution.queueRefillThreshold は正である必要 (got %d)", gp.Distribution.QueueRefillThreshold)
    }
    if gp.Storm.ThresholdPct < 0 || gp.Storm.ThresholdPct > 1 {
        return fmt.Errorf("storm.thresholdPct は 0..1 の範囲である必要 (got %f)", gp.Storm.ThresholdPct)
    }
    if gp.Patience.LateMul <= 0 {
        return fmt.Errorf("patience.lateMul は正である必要 (got %f)", gp.Patience.LateMul)
    }
    return nil
}
```

### Step 4: BackfillLegacyDefaults への備え

Textro での教訓: 新セクション追加時、DB上の旧 JSONB にそのセクションが存在しない場合、`json.Unmarshal` はゼロ値で埋める。ゼロ値が `Validate()` を通らない場合、`Load()` がデフォルトにフォールバックし、DB上の他セクションの値が無視される。

対処パターン（`internal/db/config.go` の `Load` に追加）:

```go
func (s *ConfigStore) Load(ctx context.Context) (game.GameParameters, error) {
    def := game.DefaultParameters()
    // ... Unmarshal 後 ...
    backfillDefaults(&gp, def)
    if err := gp.Validate(); err != nil {
        return def, fmt.Errorf("db: config 検証: %w", err)
    }
    return gp, nil
}

// backfillDefaults は DB から読んだ gp のゼロ値セクションをデフォルトで埋める。
// 新セクション追加後も DB 上の旧レコードで起動できるようにする。
func backfillDefaults(gp *game.GameParameters, def game.GameParameters) {
    if gp.Phase == (game.PhaseParams{}) {
        gp.Phase = def.Phase
    }
    if gp.Heat == (game.HeatParams{}) {
        gp.Heat = def.Heat
    }
    if gp.Storm == (game.StormParams{}) {
        gp.Storm = def.Storm
    }
    if gp.Distribution == (game.DistributionParams{}) {
        gp.Distribution = def.Distribution
    }
    if gp.Patience == (game.PatienceParams{}) {
        gp.Patience = def.Patience
    }
    if gp.Bot == (game.BotParams{}) {
        gp.Bot = def.Bot
    }
    if gp.Credit.LeaveLoss == (game.LeaveLoss{}) {
        gp.Credit.LeaveLoss = def.Credit.LeaveLoss
    }
}

// ※ この `==` 比較が使えるのは全パラメータ型が comparable だから。
//    map や slice をフィールドに入れるとこのパターンが壊れる。
```

Plan-01 で DB を空から始めるため初回はこの問題に当たらないが、**今後のセクション追加に備えてパターンを入れておく**。

### Step 5: config-front の Vercel デプロイ

```bash
# 1. Vercel CLI でプロジェクトをリンク（config-front リポジトリ内で）
cd config-front
vercel link
# → 新規プロジェクト名: takoda99-config

# 2. 環境変数を設定
vercel env add NEXT_PUBLIC_API_URL
# → https://takoda99-server.onrender.com  (Render のサーバーURL)

vercel env add NEXT_PUBLIC_ADMIN_TOKEN
# → (CONFIG_ADMIN_TOKEN と同じ値)

# 3. デプロイ
vercel --prod
# → https://takoda99-config.vercel.app
```

### Step 6: config-front の `lib/params.ts` 差し替え

Go 側の `GameParameters` と同じ構造を TypeScript で定義する。旧セクションを削除し、新セクションを追加:

```typescript
export interface GameParameters {
  session: SessionParams;
  matching: MatchingParams;
  credit: CreditParams;
  customer: CustomerParams;
  eval: EvalParams;
  phase: PhaseParams;
  heat: HeatParams;
  storm: StormParams;
  distribution: DistributionParams;
  patience: PatienceParams;
  bot: BotParams;
}

// 各セクションの interface は Go 側のフィールド名（json タグ）と1対1で一致させる
export interface CreditParams {
  initialLife: number;
  leaveLoss: { normal: number; bonus: number; claimer: number; buzz: number };
}

export interface PatienceParams {
  lateMul: number;
  alertMs: number;
}

export interface DistributionParams {
  queueRefillThreshold: number;
  weightFloor: number;
}

export interface PhaseParams {
  midAliveThreshold: number;
  lateAliveThreshold: number;
  midTimeMs: number;
  lateTimeMs: number;
}

export interface HeatParams {
  base: number;
  perAliveDrop: number;
  phaseEarly: number;
  phaseMid: number;
  phaseLate: number;
}

export interface StormParams {
  intervalTicks: number;
  warnTicks: number;
  thresholdPct: number;
}

export interface BotParams {
  baseAccuracy: number;
  baseElapsedMs: number;
  accuracyJitter: number;
  elapsedJitterMs: number;
}
```

> **同期のルール**: Go 側の型を変えたらこの TS も必ず同時に直す。
> フィールド名は Go の `json:"..."` タグと完全一致（camelCase）。
> ズレると config-front の保存が Validate で弾かれるか、ゼロ値で上書きされる。

デフォルト値オブジェクトも Go の `DefaultParameters()` と完全一致させる。フォーム UI は各セクションをアコーディオン/タブで区切り、日本語ラベルを付ける。

### Step 7: Render 側の環境変数設定

Render Dashboard → Environment:

```
DATABASE_URL          = postgresql://takoda99_owner:xxxx@ep-xxx.neon.tech/takoda99?sslmode=require
CONFIG_ADMIN_TOKEN    = (openssl rand -hex 32 で生成した値)
CONFIG_FRONT_ORIGIN   = https://takoda99-config.vercel.app
ALLOWED_ORIGINS       = https://takoda99.vercel.app,http://localhost:5173
```

---

## 4. ローカル確認

```bash
# ローカル Postgres（Docker）
docker run -d --name takoda-pg \
  -e POSTGRES_USER=takoda -e POSTGRES_PASSWORD=dev -e POSTGRES_DB=takoda99 \
  -p 5432:5432 postgres:16

# サーバー起動（DB接続あり）
DATABASE_URL="postgres://takoda:dev@localhost:5432/takoda99?sslmode=disable" \
CONFIG_ADMIN_TOKEN="devtoken" \
go run ./cmd/server --mode solo

# GET でデフォルト値が返る
curl http://localhost:8080/api/params | jq .

# POST で更新（旧セクションが消え新セクションのみであること確認）
curl -X POST http://localhost:8080/api/params \
  -H "Content-Type: application/json" \
  -H "X-Admin-Token: devtoken" \
  -d '{"session":{"tickIntervalMs":100,"publishIntervalMs":200,"matchTimeLimitMs":0},"matching":{"minPlayers":2,"maxPlayers":99,"startCountdownMs":10000},"credit":{"initialLife":5},"customer":{"total":300,"normal":{"attribute":"Normal","weight":70,"patienceBaseMs":8000,"orderCount":2},"bonus":{"attribute":"Bonus","weight":15,"patienceBaseMs":9000,"orderCount":2},"claimer":{"attribute":"Claimer","weight":10,"patienceBaseMs":6000,"orderCount":1},"buzz":{"attribute":"Buzz","weight":5,"patienceBaseMs":12000,"orderCount":4}},"eval":{"emaAlpha":0.3,"weightAccuracy":0.5,"weightSpeed":0.5,"speedBaselineMs":4000,"speedCap":2.0,"minMsPerWord":200,"buzzBonus":0.2,"buzzDecay":0.98,"buzzCap":0.5},"phase":{"midStartMs":60000,"lateStartMs":120000},"heat":{"initialLevel":0,"maxLevel":4,"intervalMs":30000,"levelsPerStep":1},"storm":{"enabled":true,"intervalMs":30000,"warningMs":5000,"cullCount":1,"minAliveToStart":10},"distribution":{"queueCapacity":3,"refillThreshold":1,"distributePerTick":5,"claimerUnlockPhase":1},"patience":{"decayMultiplierMid":1.2,"decayMultiplierLate":1.5},"bot":{"baseAccuracy":0.85,"baseElapsedMs":3000,"accuracyJitter":0.1,"elapsedJitterMs":500}}'

# GET で更新値が返る
curl http://localhost:8080/api/params | jq .session

# Validate() の検証（不正値で 400 が返ること）
curl -s -o /dev/null -w "%{http_code}" -X POST http://localhost:8080/api/params \
  -H "Content-Type: application/json" \
  -H "X-Admin-Token: devtoken" \
  -d '{"customer":{"total":0}}'
# → 400
```

---

## 5. 完了条件

- [ ] Takoda用の Postgres が稼働し `DATABASE_URL` で接続できる
- [ ] `game_config` テーブルが自動作成され、`DefaultParameters()` で seed される
- [ ] config-front が `takoda99-config.vercel.app` でデプロイされ、サーバーの `/api/params` に接続している
- [ ] `GameParameters` が新スキーマのみ（旧 Combo/Attack/Stack/Difficulty/Odai は削除）
- [ ] config-front の `lib/params.ts` が Go 側と一致している
- [ ] config-front で全セクション（Session/Matching/Credit/Customer/Eval/Phase/Heat/Storm/Distribution/Patience/Bot）を編集 → 保存 → GET で反映
- [ ] `DefaultParameters()` と `Validate()` が新スキーマに対応
- [ ] `backfillDefaults` パターンが入っている（新セクション追加時のゼロ値対策）
- [ ] 反映タイミング: 試合系パラメータ = 次の試合から反映、matching = 要再起動
