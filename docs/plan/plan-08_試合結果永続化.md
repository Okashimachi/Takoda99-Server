# Plan-08: 試合結果の永続化

> **目的**: 試合結果を Postgres に保存し、バランス分析・BOT 調整・プレイヤー統計の基盤を作る。
> **対応issue**: #14, #15
> **依存**: Plan-05（MatchEnd / リザルト完成）, Plan-06（DB 基盤）
> **参照**: 試合進行仕様 §10, #14 issue 本文

---

## 0. 前提知識

### 読むべきファイル

| ファイル | 内容 |
|---|---|
| `internal/store/store.go` | ResultStore インターフェース（現在は Noop 実装のみ） |
| `internal/game/session.go` | StoreResult 型 / Results() メソッド（Plan-05 で追加） |
| `internal/app/app.go` | Deps 構造体（Store フィールド）、RunMatch |
| `internal/room/room.go` | Room.Run() の試合終了後の処理 |
| `internal/db/pool.go` | DB 接続プール（既存） |
| `internal/db/config.go` | ConfigStore の Migrate パターン（参考） |
| `cmd/server/main.go` | chooseProvider / DB 接続の配線 |

### 関連概念

- **DIP**: `store.ResultStore` は差し替え可能なインターフェース。Noop（テスト/ローカル）と PostgresStore（本番）を合成ルートで切り替える。
- **ベストエフォート**: 保存失敗は log に記録するが試合を壊さない。
- **config_hash**: 試合適用パラメータの SHA256 先頭8文字。「この設定で平均何分で決着したか」をトラッキングできる。

---

## 1. 現状のコード

### store.go（internal/store/store.go）

```go
package store

import "context"

type Result struct {
	MatchId         string
	PlayerId        string
	DisplayName     string
	Rank            int
	KoCount         int       // ← 旧 Textro 用。たこ焼き版では不要
	FinalBadgeCount int       // ← 旧 Textro 用。たこ焼き版では不要
}

type ResultStore interface {
	Save(ctx context.Context, r Result) error  // ← 1件ずつ。バッチ化が望ましい
}

type Noop struct{}
func (Noop) Save(context.Context, Result) error { return nil }
```

**問題点**:
1. `Result` が旧 Textro のフィールド（KoCount / FinalBadgeCount）を持つ
2. `Save` が1件ずつ（99店分を99回呼ぶのは非効率）

### app.go（internal/app/app.go:24–29）

```go
type Deps struct {
	Params game.GameParameters
	Words  game.WordSource
	Store  store.ResultStore  // ← 既に注入口がある
	Clock  room.Clock
}
```

### RunMatch（app.go:53–64）

```go
func RunMatch(ctx context.Context, d Deps, players []matchmaking.Player) {
	// ... session と room を組み立てて ...
	rm.Run(ctx)
	// ← Run 終了後（試合終了後）にここで Save を呼べる
}
```

### cmd/server/main.go

現在は `baseDeps.Store = store.Noop{}` 固定。DB 接続がある場合に PostgresStore を差し込む配線が必要。

---

## 2. DB スキーマ

### match テーブル

```sql
CREATE TABLE IF NOT EXISTS match (
    id            TEXT PRIMARY KEY,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    duration_ms   INT NOT NULL,
    human_count   INT NOT NULL,
    bot_count     INT NOT NULL,
    winner_id     TEXT,
    config_hash   TEXT NOT NULL
);
```

### store_result テーブル

```sql
CREATE TABLE IF NOT EXISTS store_result (
    match_id       TEXT NOT NULL REFERENCES match(id),
    store_id       TEXT NOT NULL,
    display_name   TEXT NOT NULL DEFAULT '',
    final_rank     INT NOT NULL,
    elimination    TEXT NOT NULL DEFAULT '',
    credit_life    INT NOT NULL DEFAULT 0,
    eval_raw       FLOAT NOT NULL DEFAULT 0,
    served_count   INT NOT NULL DEFAULT 0,
    avg_accuracy   FLOAT NOT NULL DEFAULT 0,
    avg_elapsed_ms INT NOT NULL DEFAULT 0,
    is_bot         BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (match_id, store_id)
);
```

---

## 3. 実装手順

### Step 1: store.Result を たこ焼き版に更新

`internal/store/store.go` を以下に書き換え:

```go
package store

import "context"

// Result は1店の最終結果。
type Result struct {
	StoreId      string
	DisplayName  string
	FinalRank    int
	Elimination  string  // "SelfCollapse" / "Cull" / ""
	CreditLife   int
	EvalRaw      float64
	ServedCount  int
	AvgAccuracy  float64
	AvgElapsedMs int
	IsBot        bool
}

// MatchResult は1試合の結果全体。
type MatchResult struct {
	MatchId    string
	DurationMs int64
	HumanCount int
	BotCount   int
	WinnerId   string
	ConfigHash string
	Results    []Result
}

// ResultStore は試合結果を保存する差し替え可能な口。
type ResultStore interface {
	SaveMatch(ctx context.Context, m MatchResult) error
}

// Noop は何もしない実装。
type Noop struct{}

func (Noop) SaveMatch(context.Context, MatchResult) error { return nil }

var _ ResultStore = Noop{}
```

### Step 2: PostgresStore の実装

新規ファイル `internal/db/result.go`:

```go
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"takoda99/internal/store"
)

// ResultStore は Postgres に試合結果を書き込む。
type ResultStore struct {
	pool *pgxpool.Pool
}

func NewResultStore(pool *pgxpool.Pool) *ResultStore {
	return &ResultStore{pool: pool}
}

// Migrate は match + store_result テーブルを作成する。
func (rs *ResultStore) Migrate(ctx context.Context) error {
	ddl := `
	CREATE TABLE IF NOT EXISTS match (
		id            TEXT PRIMARY KEY,
		started_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		finished_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		duration_ms   INT NOT NULL,
		human_count   INT NOT NULL,
		bot_count     INT NOT NULL,
		winner_id     TEXT,
		config_hash   TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS store_result (
		match_id       TEXT NOT NULL REFERENCES match(id),
		store_id       TEXT NOT NULL,
		display_name   TEXT NOT NULL DEFAULT '',
		final_rank     INT NOT NULL,
		elimination    TEXT NOT NULL DEFAULT '',
		credit_life    INT NOT NULL DEFAULT 0,
		eval_raw       FLOAT NOT NULL DEFAULT 0,
		served_count   INT NOT NULL DEFAULT 0,
		avg_accuracy   FLOAT NOT NULL DEFAULT 0,
		avg_elapsed_ms INT NOT NULL DEFAULT 0,
		is_bot         BOOLEAN NOT NULL DEFAULT FALSE,
		PRIMARY KEY (match_id, store_id)
	);`
	_, err := rs.pool.Exec(ctx, ddl)
	return err
}

// SaveMatch は1試合の結果をトランザクションで保存する。
func (rs *ResultStore) SaveMatch(ctx context.Context, m store.MatchResult) error {
	tx, err := rs.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`INSERT INTO match (id, duration_ms, human_count, bot_count, winner_id, config_hash)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		m.MatchId, m.DurationMs, m.HumanCount, m.BotCount, m.WinnerId, m.ConfigHash,
	)
	if err != nil {
		return fmt.Errorf("insert match: %w", err)
	}

	for _, r := range m.Results {
		_, err = tx.Exec(ctx,
			`INSERT INTO store_result
			 (match_id, store_id, display_name, final_rank, elimination,
			  credit_life, eval_raw, served_count, avg_accuracy, avg_elapsed_ms, is_bot)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			m.MatchId, r.StoreId, r.DisplayName, r.FinalRank, r.Elimination,
			r.CreditLife, r.EvalRaw, r.ServedCount, r.AvgAccuracy, r.AvgElapsedMs, r.IsBot,
		)
		if err != nil {
			return fmt.Errorf("insert store_result %s: %w", r.StoreId, err)
		}
	}

	return tx.Commit(ctx)
}

var _ store.ResultStore = (*ResultStore)(nil)
```

### Step 3: config_hash の算出

`internal/game/params.go` に追加:

```go
import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// ConfigHash は GameParameters の JSON を SHA256 して先頭8文字を返す。
func (gp GameParameters) ConfigHash() string {
	b, _ := json.Marshal(gp)
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:4]) // 8文字
}
```

### Step 4: RunMatch で Save を呼ぶ

`internal/app/app.go` の RunMatch を修正:

```go
func RunMatch(ctx context.Context, d Deps, players []matchmaking.Player) {
	inits := make([]game.PlayerInit, 0, len(players))
	conns := make(map[game.PlayerId]transport.Connection, len(players))
	botIds := make(map[game.PlayerId]bool)
	for _, p := range players {
		inits = append(inits, game.PlayerInit{Id: p.Id, DisplayName: string(p.Id)})
		conns[p.Id] = p.Conn
		if p.IsBot {
			botIds[p.Id] = true
		}
	}
	matchId := nextMatchID()
	sess := game.NewSession(matchId, d.Params, d.Words, newRng(), inits)
	pub := transport.NewFullPublisher(d.Params.Session.PublishIntervalMs)
	rm := room.New(sess, conns, d.Params.Session.TickIntervalMs, d.Clock, pub)
	rm.Run(ctx)

	// 試合終了後: 結果を永続化（ベストエフォート）
	saveResults(ctx, d, sess, matchId, botIds)
}

func saveResults(ctx context.Context, d Deps, sess *game.Session, matchId string, botIds map[game.PlayerId]bool) {
	results := sess.Results()
	if len(results) == 0 {
		return
	}

	humanCount, botCount := 0, 0
	storeResults := make([]store.Result, 0, len(results))
	var winnerId string

	for _, r := range results {
		isBot := botIds[r.StoreId]
		if isBot {
			botCount++
		} else {
			humanCount++
		}
		if r.FinalRank == 1 {
			winnerId = string(r.StoreId)
		}
		storeResults = append(storeResults, store.Result{
			StoreId:      string(r.StoreId),
			DisplayName:  r.DisplayName,
			FinalRank:    r.FinalRank,
			Elimination:  r.Elimination,
			CreditLife:   r.CreditLife,
			EvalRaw:      r.EvalRaw,
			ServedCount:  r.Stats.ServedCount,
			AvgAccuracy:  r.Stats.AvgAccuracy,
			AvgElapsedMs: r.Stats.AvgElapsedMs,
			IsBot:        isBot,
		})
	}

	mr := store.MatchResult{
		MatchId:    matchId,
		DurationMs: sess.ElapsedMs(),
		HumanCount: humanCount,
		BotCount:   botCount,
		WinnerId:   winnerId,
		ConfigHash: d.Params.ConfigHash(),
		Results:    storeResults,
	}

	if err := d.Store.SaveMatch(ctx, mr); err != nil {
		log.Printf("result: 保存失敗（試合は正常終了済み）: %v", err)
	}
}
```

### Step 5: matchmaking.Player に IsBot フラグを追加

`internal/matchmaking/matchmaking.go` の Player に追加:

```go
type Player struct {
	Id   game.PlayerId
	Conn transport.Connection
	IsBot bool
}
```

### Step 6: NewBotPlayer で IsBot を設定

`internal/app/app.go`:

```go
func NewBotPlayer(ctx context.Context, id game.PlayerId, cfg bot.Config) matchmaking.Player {
	srv, cli := transport.Pipe()
	b := bot.New(cli, cfg, newRng())
	go b.Run(ctx)
	return matchmaking.Player{Id: id, Conn: srv, IsBot: true}
}
```

### Step 7: Session に ElapsedMs() を公開

`internal/game/session.go` に追加:

```go
// ElapsedMs は試合経過時間（ms）を返す。
func (s *Session) ElapsedMs() int64 { return s.elapsedMs }
```

### Step 8: cmd/server/main.go で PostgresStore を配線

`chooseProvider` の DB 成功パスに ResultStore の配線を追加。
現在の構造では `chooseProvider` がプロバイダだけ返しているので、ResultStore も返すか、pool を共有する。

```go
// main() 内で chooseProvider の代わりに:
var resultStore store.ResultStore = store.Noop{}

if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
	pool, err := db.NewPool(ctx, dsn)
	if err == nil {
		rs := db.NewResultStore(pool)
		if err := rs.Migrate(ctx); err != nil {
			log.Printf("result: マイグレーション失敗: %v", err)
		} else {
			resultStore = rs
		}
	}
}

// loadDeps 内で:
d.Store = resultStore
```

> **注**: 実際のリファクタは chooseProvider を拡張するか、pool を外に出して共有する。
> `db.ConfigStore` と `db.ResultStore` は同じ pool を使う設計。

---

## 4. テスト

### TestPostgresResultStore（結合テスト）

DB が必要なので `//go:build integration` タグを付ける。

ファイル: `internal/db/result_test.go`

```go
//go:build integration

package db_test

import (
	"context"
	"os"
	"testing"

	"takoda99/internal/db"
	"takoda99/internal/store"
)

func TestPostgresResultStore_SaveMatch(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}

	rs := db.NewResultStore(pool)
	if err := rs.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	mr := store.MatchResult{
		MatchId:    "test-1",
		DurationMs: 95000,
		HumanCount: 2,
		BotCount:   97,
		WinnerId:   "p-1",
		ConfigHash: "abcd1234",
		Results: []store.Result{
			{StoreId: "p-1", DisplayName: "Store1", FinalRank: 1, CreditLife: 2, EvalRaw: 0.8, ServedCount: 10, AvgAccuracy: 0.95, AvgElapsedMs: 3500},
			{StoreId: "p-2", DisplayName: "Store2", FinalRank: 2, Elimination: "SelfCollapse", CreditLife: 0, ServedCount: 5, AvgAccuracy: 0.7, AvgElapsedMs: 4200, IsBot: true},
		},
	}

	if err := rs.SaveMatch(ctx, mr); err != nil {
		t.Fatalf("SaveMatch: %v", err)
	}

	// 検証: SELECT で確認
	var count int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM store_result WHERE match_id=$1", "test-1").Scan(&count)
	if count != 2 {
		t.Errorf("store_result count=%d want 2", count)
	}

	// クリーンアップ
	pool.Exec(ctx, "DELETE FROM store_result WHERE match_id=$1", "test-1")
	pool.Exec(ctx, "DELETE FROM match WHERE id=$1", "test-1")
}
```

### TestNoop（ユニットテスト）

```go
func TestNoop_SaveMatch(t *testing.T) {
	n := store.Noop{}
	err := n.SaveMatch(context.Background(), store.MatchResult{})
	if err != nil {
		t.Errorf("Noop.SaveMatch should return nil, got %v", err)
	}
}
```

### TestConfigHash

```go
func TestConfigHash(t *testing.T) {
	p := DefaultParameters()
	h := p.ConfigHash()
	if len(h) != 8 {
		t.Errorf("hash length=%d want 8", len(h))
	}
	// 同じパラメータなら同じハッシュ
	h2 := p.ConfigHash()
	if h != h2 {
		t.Errorf("hash not deterministic: %s != %s", h, h2)
	}
	// 変えたら違うハッシュ
	p.Credit.InitialLife = 999
	h3 := p.ConfigHash()
	if h == h3 {
		t.Error("hash should differ with changed params")
	}
}
```

---

## 5. ローカル確認

```bash
# ビルド確認
go build ./...

# ユニットテスト
go test ./internal/store/ -v
go test ./internal/game/ -v -run "TestConfigHash"

# 結合テスト（ローカル Postgres が必要）
TEST_DATABASE_URL="postgres://localhost:5432/takoda99_test?sslmode=disable" \
  go test ./internal/db/ -v -tags integration -run "TestPostgresResultStore"

# solo モードで試合を回して、Save が呼ばれるか確認（DB なしなら Noop でログなし）
DATABASE_URL="postgres://..." go run ./cmd/server --mode solo --bots 2

# vet
go vet ./...
```

---

## 6. 完了条件

- [ ] `store.Result` がたこ焼き版のフィールドに更新されている（KoCount/Badge 削除、credit/eval/served 追加）
- [ ] `store.MatchResult` / `store.ResultStore.SaveMatch` のバッチ I/F になっている
- [ ] `match` + `store_result` テーブルの DDL がマイグレーションで作成される
- [ ] `db.ResultStore.SaveMatch` がトランザクションで1試合分をまとめて INSERT する
- [ ] `GameParameters.ConfigHash()` が SHA256 先頭8文字を返す
- [ ] `RunMatch` 終了後に `SaveMatch` が呼ばれる
- [ ] 保存失敗時は log に記録するが panic/試合破壊しない
- [ ] `matchmaking.Player.IsBot` でBot/人間を区別し、`store_result.is_bot` に反映
- [ ] `Noop` が新しい `SaveMatch` I/F を実装し、既存テスト/ローカル起動が壊れない
- [ ] `go build ./...` が通る
- [ ] 結合テスト: DB に結果が正しく保存される
