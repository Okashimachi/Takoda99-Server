# Plan-12: Observability（構造化ログ・メトリクス）

> **目的**: ハッカソン当日のトラブルシュートと、試合進行の可視化のための最低限のログ・メトリクスを入れる。
> **対応issue**: 新規（#48）
> **依存**: Plan-01（基盤移行）。インクリメンタルに追加可。
> **参照**: なし（新規）

---

## 0. 前提知識

### 読むべきファイル

| ファイル | 内容 |
|---|---|
| `cmd/server/main.go` | エントリポイント。現在は `log.Printf` を使用 |
| `internal/room/room.go` | 試合駆動。dispatch / Run |
| `internal/matchmaking/matchmaking.go` | マッチングプール |
| `internal/app/app.go` | RunMatch（試合構築） |
| `internal/game/session.go` | game コア（**ここにはログを入れない**） |

### 設計方針

- **構造化ログ**（JSON）を stdout に出す → Render のログビューアで `msg=` フィルタ可能
- 外部ログ基盤（Datadog, Grafana 等）は使わない（ハッカソン規模では過剰）
- Go の `log/slog`（標準ライブラリ, Go 1.21+）を使用
- **game コア（`internal/game/`）にはログを入れない**。純粋ロジックは Outbound で返すだけ。ログは room/app 層で Outbound を配信するときに記録する

---

## 1. slog の初期化

### cmd/server/main.go の先頭

```go
import (
	"log/slog"
	"os"
)

func main() {
	// JSON 構造化ログを stdout に出力
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// 以降、旧 log.Printf を slog.Info / slog.Error に置換
	// ...
}
```

---

## 2. ログポイント一覧

### 2.1 接続/切断（cmd/server/main.go）

```go
// /ws ハンドラの接続成功時
slog.Info("ws_connect",
	"storeId", string(id),
	"remoteAddr", r.RemoteAddr,
)

// 接続が切れた時（readConn の goroutine 終了時 or conn.Close コールバック）
slog.Info("ws_disconnect",
	"storeId", string(id),
	"reason", "close",
)
```

出力例:
```json
{"time":"...","level":"INFO","msg":"ws_connect","storeId":"p-1","remoteAddr":"192.168.1.5:52431"}
{"time":"...","level":"INFO","msg":"ws_disconnect","storeId":"p-1","reason":"close"}
```

### 2.2 マッチング（internal/matchmaking/matchmaking.go）

```go
// Join 時
slog.Info("matchmaking_join",
	"waitingCount", len(m.pool)+1,
)

// 試合開始時（Start コールバック呼び出し直前）
slog.Info("matchmaking_start",
	"playerCount", humanCount,
	"botCount", botCount,
	"totalPlayers", len(players),
)
```

出力例:
```json
{"time":"...","level":"INFO","msg":"matchmaking_join","waitingCount":15}
{"time":"...","level":"INFO","msg":"matchmaking_start","playerCount":42,"botCount":57,"totalPlayers":99}
```

### 2.3 試合進行（internal/room/room.go の dispatch 内）

dispatch で Outbound を配信するときに、特定のメッセージ型をログに記録する。

```go
func (r *Room) dispatch(out []game.Outbound) {
	for _, o := range out {
		// ログ対象のイベントを記録
		r.logEvent(o)

		env, ok := envelopeOf(o.Msg)
		if !ok {
			continue
		}
		// ... 既存の配信処理 ...
	}
}

func (r *Room) logEvent(o game.Outbound) {
	switch msg := o.Msg.(type) {
	case proto.PhaseChange:
		slog.Info("phase_change",
			"matchId", string(r.session.Id()),
			"phase", string(msg.Phase),
			"aliveCount", r.session.AliveCount(),
		)
	case proto.StoreEliminated:
		slog.Info("store_eliminated",
			"matchId", string(r.session.Id()),
			"storeId", string(msg.StoreId),
			"reason", string(msg.Reason),
			"finalRank", msg.FinalRank,
		)
	case proto.MatchEnd:
		if !o.To.Broadcast && o.To.PlayerId != "" {
			// MatchEnd は全員に個別送信されるので、最初の1回だけログ
			if msg.FinalRank == 1 {
				slog.Info("match_end",
					"matchId", string(r.session.Id()),
					"winnerStoreId", string(o.To.PlayerId),
					"durationMs", r.session.ElapsedMs(),
				)
			}
		}
	}
}
```

> **注**: `r.session.Id()` / `r.session.AliveCount()` / `r.session.ElapsedMs()` は Session に公開メソッドを追加する必要がある（下記 Step 参照）。

出力例:
```json
{"time":"...","level":"INFO","msg":"phase_change","matchId":"m-1","phase":"Mid","aliveCount":75}
{"time":"...","level":"INFO","msg":"store_eliminated","matchId":"m-1","storeId":"p-5","reason":"SelfCollapse","finalRank":80}
{"time":"...","level":"INFO","msg":"match_end","matchId":"m-1","winnerStoreId":"p-12","durationMs":95000}
```

### 2.4 エラー

```go
// config 取得失敗（cmd/server/main.go の loadDeps 内）
slog.Error("config_load_failed",
	"err", err.Error(),
	"fallback", "defaults",
)

// 試合結果保存失敗（internal/app/app.go の saveResults 内）
slog.Error("result_save_failed",
	"matchId", matchId,
	"err", err.Error(),
)
```

---

## 3. Session に公開メソッドを追加

`internal/game/session.go` に追加（ログ用。純粋な getter なので game コアの純粋性は維持）:

```go
// Id は試合IDを返す。
func (s *Session) Id() proto.MatchId { return s.id }

// AliveCount は現在の生存店数を返す。
func (s *Session) AliveCount() int { return s.aliveCount }

// ElapsedMs は試合経過時間（ms）を返す。
func (s *Session) ElapsedMs() int64 { return s.elapsedMs }
```

---

## 4. メトリクス（stdout 定期出力）

### 4.1 メトリクス goroutine

`cmd/server/main.go` に追加。アクティブ接続数と試合数を30秒ごとにログ出力する。

```go
// メトリクスカウンタ（main 関数のスコープで定義）
var (
	activeConns   atomic.Int64
	activeMatches atomic.Int64
)

// 接続時/切断時に increment/decrement
// /ws ハンドラ:
//   接続成功時: activeConns.Add(1)
//   切断時:     activeConns.Add(-1)
// RunMatch 呼び出し前後:
//   開始時: activeMatches.Add(1)
//   終了時: activeMatches.Add(-1)

func startMetrics(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				slog.Info("metrics",
					"activeConnections", activeConns.Load(),
					"activeMatches", activeMatches.Load(),
				)
			}
		}
	}()
}
```

`main()` 内で呼ぶ:

```go
func main() {
	// ... slog 初期化後 ...
	startMetrics(ctx)
	// ...
}
```

出力例:
```json
{"time":"...","level":"INFO","msg":"metrics","activeConnections":99,"activeMatches":1}
```

### 4.2 RunMatch のラッパー

`app.RunMatch` の前後でカウンタを操作:

```go
// cmd/server/main.go の solo/match 内で
go func() {
	activeMatches.Add(1)
	defer activeMatches.Add(-1)
	app.RunMatch(ctx, loadDeps(), players)
}()
```

---

## 5. /healthz の拡張

現在の `/healthz` は `200 "ok"` を返すだけ。JSON で詳細情報を返す。

```go
// cmd/server/main.go
var startTime = time.Now()

http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"status":            "ok",
		"activeConnections": activeConns.Load(),
		"activeMatches":     activeMatches.Load(),
		"uptime":            time.Since(startTime).Round(time.Second).String(),
	})
})
```

出力例:
```json
{
  "status": "ok",
  "activeConnections": 99,
  "activeMatches": 1,
  "uptime": "1h23m0s"
}
```

---

## 6. 旧 log.Printf の slog 置換一覧

`cmd/server/main.go` 内の全 `log.Printf` を `slog.Info` / `slog.Error` に置換する:

| 旧 | 新 |
|---|---|
| `log.Printf("solo: 試合開始 human=%s bots=%d", id, *bots)` | `slog.Info("match_solo_start", "humanId", string(id), "bots", *bots)` |
| `log.Printf("match: 試合開始 players=%d", len(players))` | `slog.Info("match_start", "playerCount", len(players))` |
| `log.Printf("match: 参加 %s", id)` | `slog.Info("ws_connect", "storeId", string(id), "remoteAddr", r.RemoteAddr)` |
| `log.Printf("config: 起動時取得失敗...")` | `slog.Error("config_load_failed", "err", err.Error(), "fallback", "defaults")` |
| `log.Printf("config: DB接続失敗...")` | `slog.Error("db_connect_failed", "err", err.Error(), "fallback", "defaults")` |
| `log.Printf("textro99 server: mode=%s addr=%s", ...)` | `slog.Info("server_start", "mode", *mode, "addr", *addr)` |
| `log.Fatalf("listen: %v", err)` | `slog.Error("listen_failed", "err", err.Error()); os.Exit(1)` |

---

## 7. 注意事項

### game コアにログを入れない

`internal/game/` パッケージは pure logic。`slog` を import しない。

ログが必要な情報は Outbound（`proto.PhaseChange`, `proto.StoreEliminated` 等）として返し、room 層の `dispatch` でログに記録する。

### slog はパッケージレベルで使う

ハッカソン規模では `slog.SetDefault` して `slog.Info()` をグローバルに使えば十分。
DI で logger を渡す設計は過剰。

### Render のログ閲覧

Render のダッシュボードの Logs タブで stdout がリアルタイム閲覧できる。
JSON なので `msg=metrics` や `msg=store_eliminated` でフィルタ可能。

---

## 8. ローカル確認

```bash
# ビルド確認
go build ./...

# vet
go vet ./...

# ローカル起動してログ出力を確認
go run ./cmd/server --mode solo --bots 2 2>&1 | head -30
# → JSON 形式のログが stdout に出る

# /healthz の確認
curl -s http://localhost:8080/healthz | jq .
# → {"status":"ok","activeConnections":...,"activeMatches":...,"uptime":"..."}

# 30秒待ってメトリクスログが出るか確認
go run ./cmd/server --mode solo --bots 0 2>&1 | grep metrics
# → {"time":"...","level":"INFO","msg":"metrics","activeConnections":0,"activeMatches":0}
```

---

## 9. 完了条件

- [ ] `slog` で JSON 構造化ログが stdout に出る
- [ ] 旧 `log.Printf` が全て `slog.Info` / `slog.Error` に置換されている
- [ ] 接続/切断ログ: `ws_connect` / `ws_disconnect` が出る
- [ ] マッチング開始ログ: `matchmaking_start` が playerCount/botCount 付きで出る
- [ ] フェーズ移行ログ: `phase_change` が出る
- [ ] 脱落ログ: `store_eliminated` が storeId/reason/finalRank 付きで出る
- [ ] 試合終了ログ: `match_end` が winnerStoreId/durationMs 付きで出る
- [ ] エラーログ: config/result の失敗が slog.Error で出る
- [ ] 30秒ごとのメトリクス出力（`msg=metrics`）が出る
- [ ] `/healthz` が JSON で activeConnections/activeMatches/uptime を返す
- [ ] game コア（`internal/game/`）は `slog` を import していない
- [ ] `go build ./...` が通る
- [ ] `go vet ./...` が通る
