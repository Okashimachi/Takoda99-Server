# Plan-17: Observability の実装（構造化ログ・メトリクス）

> **目的**: 当日のトラブルシュートと試合進行の可視化のため、構造化ログとメトリクスを入れる。
> **対応issue**: #48
> **優先度**: **高**。当日「何が起きているか分からない」状態を避ける。
> **依存**: なし（インクリメンタルに追加可）
> **前身**: `docs/plan/plan-12_observability.md`（設計）。**本プランは GCP 移行後の実装版**

---

## 0. Plan-12 との関係

Plan-12 は Render 前提で書かれた設計。ホスティングが **GCP Compute Engine + systemd** に移ったので、
ログの見方が変わる（Render のログビューア → `journalctl`）。設計方針は Plan-12 のままで、
本プランは**実際に手を動かす手順と GCP 固有の差分**を持つ。

| | Plan-12（Render） | 本プラン（GCP） |
|---|---|---|
| ログの出力先 | stdout → Render ログビューア | stdout → **journald** |
| 閲覧 | ダッシュボード | `journalctl -u takoda99` |
| フィルタ | Web UI | `journalctl ... \| jq 'select(.msg=="...")'` |

---

## 1. 現状

```bash
grep -rl "log/slog" --include="*.go" .    # → 何もヒットしない
```

**slog は未導入**。`cmd/server/main.go` は `log.Printf` を使っている。
`/healthz` は `200 "ok"` を返すだけ。

---

## 2. 実装手順

### Step 1: slog を初期化（cmd/server/main.go）

```go
import "log/slog"

func main() {
	// JSON 構造化ログを stdout へ。journald が拾う。
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	// ...
}
```

> **journald と時刻の重複**: journald 自身がタイムスタンプを付けるので slog の `time` と二重になるが、
> `jq` で扱うには JSON 側にも時刻がある方が便利なので消さない。

### Step 2: メトリクスカウンタ

**接続数のカウンタは新設しない。** `connLimiter` が既に持っている:

```bash
grep -n "func (cl \*connLimiter) active" cmd/server/main.go
```

```go
limiter.active()   // ← 現在の同時接続数。これをそのまま使う
```

`activeConns` を別に作ると `acquire` / `release` / `releaseOnDisconnect` の3箇所と
二重管理になり、必ずズレる。**試合数だけ新設する**:

```go
var (
	activeMatches atomic.Int64
	startTime     = time.Now()
)
```

### Step 3: 旧 log.Printf を置換

`cmd/server/main.go` の全 `log.Printf` を置き換える。

| 旧 | 新 |
|---|---|
| `log.Printf("solo: 試合開始 human=%s bots=%d", id, *bots)` | `slog.Info("match_start", "mode", "solo", "humanId", string(id), "bots", *bots)` |
| `log.Printf("match: 試合開始 players=%d", len(players))` | `slog.Info("match_start", "mode", "match", "playerCount", len(players))` |
| `log.Printf("match: 参加 %s", id)` | `slog.Info("ws_connect", "storeId", string(id), "remoteAddr", r.RemoteAddr)` |
| `log.Printf("config: ...失敗...")` | `slog.Error("config_load_failed", "err", err.Error(), "fallback", "defaults")` |
| `log.Printf("config: DB接続失敗...")` | `slog.Error("db_connect_failed", "err", err.Error())` |
| `log.Printf("takoda99 server: mode=%s addr=%s", ...)` | `slog.Info("server_start", "mode", *mode, "addr", *addr)` |
| `log.Fatalf("listen: %v", err)` | `slog.Error("listen_failed", "err", err.Error()); os.Exit(1)` |

```bash
grep -n "log\.Printf\|log\.Fatal" cmd/server/main.go   # 残っていないか最後に確認
```

### Step 4: 試合イベントのログ（internal/room）

**game コアにはログを入れない**（純粋性を壊す）。room が Outbound を配信するときに記録する。

```go
func (r *Room) dispatch(out []game.Outbound) {
	for _, o := range out {
		r.logEvent(o)
		// ...既存の配信処理...
	}
}

func (r *Room) logEvent(o game.Outbound) {
	switch msg := o.Msg.(type) {
	case proto.PhaseChange:
		if !o.To.Broadcast {
			return // ブロードキャストは1回だけ来るので重複しないが、念のため
		}
		slog.Info("phase_change",
			"matchId", string(r.session.Id()),
			"phase", string(msg.Phase),
			"aliveCount", r.session.AliveCount(),
			"elapsedMs", r.session.ElapsedMs())

	case proto.StoreEliminated:
		if !o.To.Broadcast {
			return
		}
		slog.Info("store_eliminated",
			"matchId", string(r.session.Id()),
			"storeId", string(msg.StoreId),
			"reason", string(msg.Reason),
			"finalRank", msg.FinalRank,
			"aliveCount", r.session.AliveCount())

	case proto.MatchEnd:
		// MatchEnd は全員へ個別送信されるので、優勝者のぶんだけログる
		if msg.FinalRank == 1 {
			slog.Info("match_end",
				"matchId", string(r.session.Id()),
				"winnerStoreId", string(o.To.PlayerId),
				"durationMs", r.session.ElapsedMs())
		}
	}
}
```

> **`Broadcast` の判定を入れる理由**: `broadcastMsg` は Outbound を1つしか返さないので
> 実際には重複しないが、将来 per-player 配信に変わったときに 99 行のログが出るのを防ぐ。

### Step 5: 結果保存の失敗（internal/app）

```go
if err := d.Store.SaveMatch(ctx, mr); err != nil {
	slog.Error("result_save_failed", "matchId", matchId, "err", err.Error())
}
```

### Step 6: マッチング（internal/matchmaking）

```go
slog.Info("matchmaking_start",
	"playerCount", humanCount, "botCount", botCount, "totalPlayers", len(players))
```

参加/離脱は接続ログと重複するので**入れない**（99人だとノイズになる）。

### Step 7: 定期メトリクス

```go
// limiter は main で作った connLimiter を渡す（接続数の単一情報源）。
func startMetrics(ctx context.Context, limiter *connLimiter) {
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				slog.Info("metrics",
					"activeConnections", limiter.active(),
					"activeMatches", activeMatches.Load(),
					"goroutines", runtime.NumGoroutine(),
					"heapMB", ms.HeapAlloc/1024/1024)
			}
		}
	}()
}
```

`goroutines` と `heapMB` は**リーク検出に効く**。試合が終わっても goroutine が減らなければ
接続の後始末が漏れている。e2e-micro は RAM 1GB なので heap も見る価値がある。

### Step 8: /healthz の拡張

```go
http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":            "ok",
		"activeConnections": limiter.active(),
		"activeMatches":     activeMatches.Load(),
		"goroutines":        runtime.NumGoroutine(),
		"uptime":            time.Since(startTime).Round(time.Second).String(),
	})
})
```

> **Caddy のヘルスチェックを壊さないこと**。現状 `docs/deploy.md` の疎通確認は
> `curl .../healthz` の**200 を見ている**ので、JSON にしても問題ない。
> ただし本文で `ok` を grep している箇所があれば直す。

---

## 3. GCP でのログの見方（当日用）

`docs/deploy.md` の運用コマンドに追記する。

```bash
# 全ログを追う
sudo journalctl -u takoda99 -f
```

```bash
# 試合イベントだけ
sudo journalctl -u takoda99 -f -o cat | jq -c 'select(.msg｜test("match_|phase_|store_"))'
```

```bash
# メトリクスだけ
sudo journalctl -u takoda99 -o cat | jq -c 'select(.msg=="metrics")'
```

```bash
# エラーだけ
sudo journalctl -u takoda99 -o cat | jq -c 'select(.level=="ERROR")'
```

> `-o cat` を付けると journald のプレフィックスが外れて生の JSON になり、`jq` に流せる。
> VM に `jq` が無ければ `sudo apt-get install -y jq`。

---

## 4. やらないこと

- 外部ログ基盤（Datadog / Grafana / Cloud Logging へのエクスポート）— ハッカソン規模では過剰
- リクエストごとのトレース ID — 試合単位の `matchId` で十分
- ログレベルの動的変更 — 再起動で足りる

---

## 5. 注意事項

### game コアにログを入れない

`internal/game/` は純粋ロジック。`log/slog` を import しない。
ログに出したい情報は Outbound（`PhaseChange` / `StoreEliminated` 等）として返し、room で記録する。

```bash
grep -rn "log/slog" internal/game/    # 何もヒットしないこと
```

### ログ量の見積もり

99人×1試合で出る行数の目安:

| イベント | 回数 |
|---|---|
| ws_connect / disconnect | 198 |
| phase_change | 2 |
| store_eliminated | 98 |
| match_end | 1 |
| metrics（30秒ごと・3分試合） | 6 |

**約300行/試合**。journald の既定設定で問題ない。毎tickのログは絶対に入れない。

---

## 6. ローカル確認

```bash
go build ./... && go vet ./...
```

```bash
go run ./cmd/server --mode solo --bots 3 2>&1 | head -20
```

JSON が出ること。

```bash
curl -s http://localhost:8080/healthz | jq .
```

```bash
grep -rn "log/slog" internal/game/ || echo "OK: game コアにログなし"
```

```bash
grep -n "log\.Printf\|log\.Fatal" cmd/server/main.go || echo "OK: 旧ログなし"
```

---

## 7. 完了条件

- [ ] `slog` の JSON ログが stdout に出る
- [ ] `cmd/server/main.go` から `log.Printf` / `log.Fatalf` が消えている
- [ ] `ws_connect` / `ws_disconnect` が出る
- [ ] `matchmaking_start` が playerCount/botCount 付きで出る
- [ ] `phase_change` / `store_eliminated` / `match_end` が matchId 付きで出る
- [ ] `config_load_failed` / `result_save_failed` が `slog.Error` で出る
- [ ] 30秒ごとに `metrics`（接続数・試合数・goroutine数・heapMB）が出る
- [ ] 接続数は `connLimiter.active()` を単一情報源にしている（カウンタを二重に持たない）
- [ ] `/healthz` が JSON で接続数・試合数・goroutines・uptime を返す
- [ ] `internal/game/` が `log/slog` を import していない
- [ ] `docs/deploy.md` の運用コマンドに `journalctl` + `jq` のフィルタ例が追記されている
- [ ] 1試合のログ量が数百行に収まる（毎tickのログが無い）
