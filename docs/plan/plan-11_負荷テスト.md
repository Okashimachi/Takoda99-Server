# Plan-11: 負荷テスト（99接続同時）

> **目的**: ハッカソン本番前に99接続同時の試合を通しで回し、性能問題がないことを確認する。
> **対応issue**: 新規
> **依存**: Plan-05（1試合が最後まで回る）, Plan-09（スパイク対策）
> **参照**: Textro99-Server の cmd/balancesim・cmd/matchsim パターン

> ⚠ **本書は Render 時代の設計**。実施版は `docs/plan/plan-18_負荷テストの再現可能化.md`。
> 99人の実機計測結果は `docs/deploy.md` にある。

---

## 1. 前提知識

### 1.1 テストの目的

ハッカソン本番では最大99人が同時接続して1試合をプレイする。以下を事前に確認する:

- ゲームロジック（`game.Session`）が99店を正しく処理できるか
- 300客の初期化・分配・我慢ゲージ・脱落・終了判定が一通り回るか
- メモリ使用量が Render Starter tier（512MB）に収まるか
- WebSocket 経由でも同等の性能が出るか

### 1.2 既存コードの参照

旧 Textro99-Server にあった sim 系コマンドのパターンを流用する:

| コマンド | 概要 |
|---|---|
| `cmd/balancesim` | パラメータのバランスをシミュレートする。tick を高速で回し、脱落順・スコア分布を出力 |
| `cmd/matchsim` | 1試合をヘッドレスで最後まで回す。InMemory 接続で Bot を99体接続し、wall time を計測 |

### 1.3 使用する部品

- **`transport.Pipe()`** (`internal/transport/inmemory.go`): WebSocket を経由しない in-memory 接続ペアを作る。Bot 側とサーバー側の Connection を返す
- **`bot.Bot`** (`internal/bot/bot.go`): 受信を読み流し、MatchEnd で終了する自動クライアント。たこ焼き版の自動操作（CustomerArrived を受けて OrderServed を返す）は tako-J で実装予定。現状は no-op
- **`app.NewBotPlayer()`** (`internal/app/app.go`): Pipe + Bot を組み立てて `matchmaking.Player` を返す
- **`app.RunMatch()`** (`internal/app/app.go`): players リストから Session + Room を構築して試合を最後まで駆動する
- **`room.Clock` / `room.RealClock`** (`internal/room/room.go`): tick の時計を抽象化。テスト用に差し替え可能

### 1.4 テストの種類

| 種類 | 接続方式 | 目標 |
|---|---|---|
| ヘッドレス（InMemory） | `transport.Pipe()` | 99 Bot で1試合を **10秒以内**にシミュレート完了 |
| リモート（WebSocket） | `transport.Dial()` | 99 goroutine がデプロイ済みサーバーに接続。MatchStart を **3秒以内**に全員受信。試合完走 |

---

## 2. ヘッドレステスト: cmd/matchsim

### 2.1 概要

WebSocket を使わず、`transport.Pipe()` で99体の Bot を接続して1試合を最後まで回す。ゲームロジックの純粋な性能を測定する。

### 2.2 ファイル

`cmd/matchsim/main.go`（新規作成）

### 2.3 完全な実装コード

```go
// Command matchsim は99 Bot でヘッドレス1試合を高速シミュレートし、性能を計測する。
//
//	go run ./cmd/matchsim
//	go run ./cmd/matchsim -players 50 -tick 10
package main

import (
	"context"
	"flag"
	"fmt"
	"runtime"
	"time"

	"takoda99/internal/app"
	"takoda99/internal/bot"
	"takoda99/internal/game"
	"takoda99/internal/matchmaking"
	"takoda99/internal/room"
	"takoda99/internal/transport"
)

func main() {
	players := flag.Int("players", 99, "参加店舗数（Bot数）")
	tickMs := flag.Int("tick", 1, "tick間隔(ms)。1 で最速シミュレーション。本番は 150")
	flag.Parse()

	ctx := context.Background()

	// Bot 用の Deps。tick 間隔だけ上書きして高速化する。
	deps := app.DefaultDeps()
	deps.Params.Session.TickIntervalMs = *tickMs
	// 制限時間を有効にする（solo/dev の idle 継続を防ぐ）
	if deps.Params.Session.MatchTimeLimitMs == 0 {
		deps.Params.Session.MatchTimeLimitMs = 180000 // 3分
	}

	// メモリ計測: 開始前
	var memBefore runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	// 99体の Bot を InMemory 接続で用意
	fmt.Printf("=== matchsim: %d players, tick=%dms ===\n", *players, *tickMs)
	fmt.Println("Bot 接続を構築中...")

	var plist []matchmaking.Player
	for i := 0; i < *players; i++ {
		id := game.PlayerId(fmt.Sprintf("bot-%d", i+1))
		p := app.NewBotPlayer(ctx, id, bot.DefaultConfig())
		plist = append(plist, p)
	}

	fmt.Println("試合開始...")
	start := time.Now()

	// RunMatch は同期的に試合を最後まで回す（呼び出し元の goroutine でブロック）。
	// ただし app.RunMatch は go で呼ぶ設計なので、ここでは直接呼ぶ。
	// Room.Run が Finished まで回って戻るので、完了を待てる。
	done := make(chan struct{})
	go func() {
		app.RunMatch(ctx, deps, plist)
		close(done)
	}()

	// タイムアウト付きで完了を待つ
	timeout := time.After(60 * time.Second)
	select {
	case <-done:
		// 正常完了
	case <-timeout:
		fmt.Println("ERROR: 60秒タイムアウト。試合が終了しなかった")
		return
	}

	elapsed := time.Since(start)

	// メモリ計測: 終了後
	var memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memAfter)

	// 結果出力
	fmt.Println()
	fmt.Println("=== 結果 ===")
	fmt.Printf("所要時間:       %v\n", elapsed.Round(time.Millisecond))
	fmt.Printf("参加店舗:       %d\n", *players)
	fmt.Printf("tick間隔:       %dms\n", *tickMs)
	fmt.Println()
	fmt.Println("--- メモリ ---")
	fmt.Printf("HeapAlloc (前): %.2f MB\n", float64(memBefore.HeapAlloc)/1024/1024)
	fmt.Printf("HeapAlloc (後): %.2f MB\n", float64(memAfter.HeapAlloc)/1024/1024)
	fmt.Printf("TotalAlloc:     %.2f MB\n", float64(memAfter.TotalAlloc)/1024/1024)
	fmt.Printf("Mallocs:        %d\n", memAfter.Mallocs-memBefore.Mallocs)
	fmt.Printf("NumGC:          %d\n", memAfter.NumGC-memBefore.NumGC)
	fmt.Println()

	if elapsed < 10*time.Second {
		fmt.Println("OK: 10秒以内に完了")
	} else {
		fmt.Println("WARN: 10秒を超過。ロジックの最適化が必要")
	}
}
```

### 2.4 注意点

- 現在の Bot（`internal/bot/bot.go`）は **no-op**（受信を読み流すだけで OrderServed を返さない）。tako-J で自動操作を実装するまで、Bot が客を捌けないため我慢ゲージ切れ→信用低下→自滅脱落のフローでしか試合が進まない
- それでも Session/Room の tick ループ・客分配・脱落・終了判定は動く。ロジック性能の測定には十分
- tako-J（Bot の OrderServed 自動送出）が入った後に、提供→評価→正規化のフルパスでも性能確認すること

---

## 3. リモート負荷テスト: cmd/loadtest

### 3.1 概要

デプロイ済みサーバーに99本の WebSocket を同時に張り、実際のプロトコルで1試合を回す。ネットワーク経由の性能・安定性を確認する。

### 3.2 ファイル

`cmd/loadtest/main.go`（新規作成）

### 3.3 完全な実装コード

```go
// Command loadtest は99本の WebSocket を同時に張り、実サーバーで1試合を回す負荷テスト。
//
//	go run ./cmd/loadtest -url wss://takoda99-server.onrender.com/ws
//	go run ./cmd/loadtest -url ws://localhost:8080/ws -players 10
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"takoda99/internal/proto"
	"takoda99/internal/transport"
)

func main() {
	serverURL := flag.String("url", "ws://localhost:8080/ws", "WebSocket サーバーURL")
	players := flag.Int("players", 99, "同時接続数")
	connectTimeout := flag.Duration("connect-timeout", 10*time.Second, "全員の接続完了タイムアウト")
	matchTimeout := flag.Duration("match-timeout", 5*time.Minute, "試合完了タイムアウト")
	flag.Parse()

	fmt.Printf("=== loadtest: %d players → %s ===\n", *players, *serverURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── Phase 1: 全員接続 ───────────────────────────────────

	fmt.Println("[Phase 1] 接続開始...")
	connectStart := time.Now()

	type connResult struct {
		index int
		conn  transport.Connection
		err   error
	}

	results := make(chan connResult, *players)
	for i := 0; i < *players; i++ {
		go func(idx int) {
			connCtx, connCancel := context.WithTimeout(ctx, *connectTimeout)
			defer connCancel()
			conn, err := transport.Dial(connCtx, *serverURL)
			results <- connResult{index: idx, conn: conn, err: err}
		}(i)
	}

	conns := make([]transport.Connection, *players)
	var connectErrors int
	for i := 0; i < *players; i++ {
		r := <-results
		if r.err != nil {
			fmt.Printf("  ERROR: player-%d 接続失敗: %v\n", r.index, r.err)
			connectErrors++
			continue
		}
		conns[r.index] = r.conn
	}
	connectElapsed := time.Since(connectStart)
	fmt.Printf("  接続完了: %d/%d 成功 (%.2fs)\n", *players-connectErrors, *players, connectElapsed.Seconds())

	if connectErrors > 0 {
		fmt.Printf("  WARN: %d 接続が失敗\n", connectErrors)
		if connectErrors == *players {
			fmt.Println("ERROR: 全接続失敗。サーバーURL・起動状態を確認")
			os.Exit(1)
		}
	}

	// ── Phase 2: MatchStart 待ち ──────────────────────────────

	fmt.Println("[Phase 2] MatchStart 待ち...")
	matchStartTime := time.Now()

	var matchStartCount atomic.Int32
	var matchEndCount atomic.Int32
	var firstMatchStart atomic.Int64 // UnixMilli
	var lastMatchStart atomic.Int64  // UnixMilli

	var wg sync.WaitGroup

	for i := 0; i < *players; i++ {
		conn := conns[i]
		if conn == nil {
			continue // 接続失敗したスロット
		}
		wg.Add(1)
		go func(idx int, c transport.Connection) {
			defer wg.Done()
			defer func() { _ = c.Close() }()

			for {
				select {
				case env, ok := <-c.Receive():
					if !ok {
						return // 切断
					}
					switch env.Type {
					case proto.TypeMatchStart:
						now := time.Now().UnixMilli()
						matchStartCount.Add(1)
						// CAS で最小値を記録
						for {
							old := firstMatchStart.Load()
							if old != 0 && old <= now {
								break
							}
							if firstMatchStart.CompareAndSwap(old, now) {
								break
							}
						}
						// CAS で最大値を記録
						for {
							old := lastMatchStart.Load()
							if old >= now {
								break
							}
							if lastMatchStart.CompareAndSwap(old, now) {
								break
							}
						}

					case proto.TypeMatchEnd:
						matchEndCount.Add(1)
						return // この接続の役目は終了

					case proto.TypeMatchmakingStatus:
						// マッチング状態の配信（正常）。読み流す
					}

				case <-time.After(*matchTimeout):
					fmt.Printf("  WARN: player-%d タイムアウト\n", idx)
					return
				}
			}
		}(i, conn)
	}

	// MatchStart が全員に届くまで待つ（ポーリング）
	msDeadline := time.After(*connectTimeout)
	for {
		select {
		case <-msDeadline:
			fmt.Printf("  WARN: MatchStart タイムアウト (%d/%d 受信)\n",
				matchStartCount.Load(), *players-connectErrors)
			goto phaseEnd
		case <-time.After(200 * time.Millisecond):
			count := int(matchStartCount.Load())
			expected := *players - connectErrors
			if count >= expected {
				goto phaseEnd
			}
		}
	}
phaseEnd:

	matchStartElapsed := time.Since(matchStartTime)
	msCount := int(matchStartCount.Load())
	fmt.Printf("  MatchStart 受信: %d/%d (%.2fs)\n",
		msCount, *players-connectErrors, matchStartElapsed.Seconds())

	if firstMatchStart.Load() > 0 && lastMatchStart.Load() > 0 {
		spread := lastMatchStart.Load() - firstMatchStart.Load()
		fmt.Printf("  MatchStart 配信スプレッド: %dms (最初→最後)\n", spread)
	}

	// ── Phase 3: 試合完走待ち ──────────────────────────────────

	fmt.Println("[Phase 3] 試合完走待ち...")
	matchWaitStart := time.Now()

	// wg.Wait() で全 goroutine の終了を待つ（MatchEnd 受信 or タイムアウト）
	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		// 全員完了
	case <-time.After(*matchTimeout):
		fmt.Println("  WARN: 試合完走タイムアウト")
	}

	matchElapsed := time.Since(matchWaitStart)
	totalElapsed := time.Since(connectStart)

	// ── 結果出力 ──────────────────────────────────────────────

	fmt.Println()
	fmt.Println("=== 結果 ===")
	fmt.Printf("接続:              %d/%d 成功 (%.2fs)\n",
		*players-connectErrors, *players, connectElapsed.Seconds())
	fmt.Printf("MatchStart:        %d/%d 受信 (%.2fs)\n",
		msCount, *players-connectErrors, matchStartElapsed.Seconds())
	fmt.Printf("MatchEnd:          %d/%d 受信 (%.2fs)\n",
		matchEndCount.Load(), *players-connectErrors, matchElapsed.Seconds())
	fmt.Printf("合計所要時間:      %.2fs\n", totalElapsed.Seconds())
	fmt.Println()

	// 判定
	ok := true
	if connectErrors > 0 {
		fmt.Printf("WARN: %d 接続失敗\n", connectErrors)
		ok = false
	}
	if matchStartElapsed > 3*time.Second {
		fmt.Println("WARN: MatchStart が3秒以内に届かなかった")
		ok = false
	}
	if int(matchEndCount.Load()) < *players-connectErrors {
		fmt.Println("WARN: 全員の MatchEnd を受信できなかった")
		ok = false
	}
	if ok {
		fmt.Println("OK: 全テスト通過")
	}
}
```

### 3.4 注意点

- マッチングの `minPlayers` がデフォルト（20）のままだと、20人接続した時点でカウントダウンが始まり、99人揃う前に試合が開始される可能性がある。テスト前に `minPlayers` を接続数に合わせて設定すること
- 実サーバーが `--mode solo` で動いている場合、接続ごとに即試合が始まってしまう。`--mode match` で動いていることを確認
- Render Free プランはアイドル15分でスリープする。テスト前に `/healthz` を叩いてウォームアップすること

---

## 4. 実行方法

### 4.1 ヘッドレステスト（ローカル）

```bash
# デフォルト（99 Bot, tick=1ms で最速）
go run ./cmd/matchsim

# プレイヤー数・tick 間隔を指定
go run ./cmd/matchsim -players 50 -tick 10

# 本番と同じ tick 間隔で計測（リアルタイム相当）
go run ./cmd/matchsim -players 99 -tick 150
```

### 4.2 リモート負荷テスト

```bash
# ローカルサーバーに対して（先に別ターミナルでサーバー起動）
# ターミナル1:
go run ./cmd/server --mode match --bots 99
# ターミナル2:
go run ./cmd/loadtest -url ws://localhost:8080/ws -players 99

# Render デプロイ済みサーバーに対して
go run ./cmd/loadtest -url wss://takoda99-server.onrender.com/ws -players 99

# 少人数テスト
go run ./cmd/loadtest -url ws://localhost:8080/ws -players 10
```

### 4.3 CI での実行

- **ヘッドレステスト**: `go test` として CI で毎回回すことも可能（`-short` フラグで制御）。ただし `cmd/matchsim` はテストではなく main パッケージなので、CI では `go run ./cmd/matchsim` を直接実行するか、ロジック部分を `_test.go` に切り出す
- **リモート負荷テスト**: CI では回さない（外部サーバー依存）。手動実行のみ

---

## 5. 計測項目

### ヘッドレステスト

| 項目 | 目標 | 測定方法 |
|---|---|---|
| 試合シミュレーション時間 | **10秒以内** (tick=1ms) | `time.Since(start)` |
| HeapAlloc（試合中ピーク） | **100MB 以内** | `runtime.MemStats.HeapAlloc` |
| TotalAlloc（累積 alloc） | 参考値 | `runtime.MemStats.TotalAlloc` |
| GC 回数 | 参考値 | `runtime.MemStats.NumGC` |
| Mallocs（alloc 回数） | 参考値 | `runtime.MemStats.Mallocs` |

### リモート負荷テスト

| 項目 | 目標 | 測定方法 |
|---|---|---|
| 全員接続完了 | **5秒以内** | 最後の Dial 成功まで |
| MatchStart 全員受信 | **3秒以内** | 最後の MatchStart 受信まで |
| MatchStart 配信スプレッド | **500ms 以内** | 最初の MatchStart と最後の差 |
| 試合完走 | タイムアウトなし | 全員の MatchEnd 受信 |
| 接続失敗数 | **0** | Dial エラー数 |

---

## 6. 完了条件

- [ ] `cmd/matchsim/main.go` が作成され、`go run ./cmd/matchsim` で実行できる
- [ ] 99 Bot のヘッドレス1試合が完走する（panic/deadlock なし）
- [ ] 試合シミュレートが10秒以内（tick=1ms）
- [ ] `cmd/loadtest/main.go` が作成され、`go run ./cmd/loadtest` で実行できる
- [ ] 99 WebSocket 接続で MatchStart が3秒以内に全員に届く
- [ ] 99 WebSocket 接続の1試合が完走する（panic/OOM/timeout なし）
- [ ] Render Starter tier（512MB RAM）で OOM しない
- [ ] 結果が stdout にレポートされる（所要時間/メモリ/接続結果）
