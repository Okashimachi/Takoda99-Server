# Plan-09: マッチングスパイク対策

> **目的**: ハッカソン会場で99人が一斉に接続する瞬間のスパイクに耐えるようにする。
> **対応issue**: 新規（#45）
> **依存**: Plan-01（基盤移行）
> **参照**: マッチング仕様 §1-3

---

## 0. 前提知識

### 読むべきファイル

| ファイル | 内容 |
|---|---|
| `cmd/server/main.go` | WebSocket ハンドラ（/ws）、ヘルスチェック |
| `internal/matchmaking/matchmaking.go` | マッチングプール（channel-based, buffer=256） |
| `internal/transport/websocket.go` | WebSocket の Accept / Connection |
| `internal/game/session.go` | initCustomers（300客初期化）、Start |
| `render.yaml` | Render デプロイ設定 |

### スパイクの特性

ハッカソンの「せーの」で全員がアクセス:
1. **数秒間に99本の WebSocket upgrade** が同時に来る
2. 各接続で `MatchmakingJoin` → 99人揃った瞬間に試合開始
3. 試合開始時に **客300人の初期化 + 99店×初期お題発行** が一気に走る

---

## 1. ボトルネック分析

| 箇所 | 負荷 | 対策 |
|---|---|---|
| WebSocket upgrade | 99 TLS handshake + HTTP upgrade | Go の net/http は並行可。問題になりにくい |
| Matchmaking Join | 99 event を channel 処理 | channel buffer=256 で十分。ボトルネックにならない |
| 試合開始（initCustomers） | 300客の alloc + 属性抽選 | 軽い。問題になりにくい |
| 初期お題発行（admitCustomer×N） | 最大 99店×queueRefillThreshold×orderCount 語 | お題プールが大きいとメモリ・文字列 alloc が多い |
| 全店への MatchStart 配信 | 99本の JSON marshal + WS write | 1回だけなので許容範囲 |

---

## 2. 対策

### 2.1 同時接続数リミッター

`cmd/server/main.go` に接続数制限ミドルウェアを追加する。

#### 実装コード

```go
// connLimiter は同時 WebSocket 接続数を制限する。
type connLimiter struct {
	sem chan struct{}
}

func newConnLimiter(max int) *connLimiter {
	return &connLimiter{sem: make(chan struct{}, max)}
}

// acquire は枠を1つ取る。取れなければ false を返す（ブロックしない）。
func (cl *connLimiter) acquire() bool {
	select {
	case cl.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// release は枠を1つ返す。
func (cl *connLimiter) release() {
	<-cl.sem
}
```

#### /ws ハンドラへの組み込み

```go
func main() {
	// ... 既存の flag.Parse() 等 ...

	limiter := newConnLimiter(200) // 99 + 余裕

	switch *mode {
	case "solo":
		http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			if !limiter.acquire() {
				http.Error(w, "too many connections", http.StatusServiceUnavailable)
				return
			}
			conn, err := transport.Accept(w, r, wsAccept)
			if err != nil {
				limiter.release()
				return
			}
			id := nextID()
			players := []matchmaking.Player{{Id: id, Conn: conn}}
			for i := 0; i < *bots; i++ {
				players = append(players, app.NewBotPlayer(ctx, nextID(), bot.DefaultConfig()))
			}
			log.Printf("solo: 試合開始 human=%s bots=%d", id, *bots)
			go func() {
				defer limiter.release()
				app.RunMatch(ctx, loadDeps(), players)
			}()
		})

	default: // match
		// ... matchmaking setup ...
		http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			if !limiter.acquire() {
				http.Error(w, "too many connections", http.StatusServiceUnavailable)
				return
			}
			conn, err := transport.Accept(w, r, wsAccept)
			if err != nil {
				limiter.release()
				return
			}
			id := nextID()
			log.Printf("match: 参加 %s (active=%d)", id, len(limiter.sem))
			mm.Join(matchmaking.Player{Id: id, Conn: conn})
			// release は試合終了時に行う（Room の接続が切れたら）
			// → transport.Connection の Close 時にコールバックで release
		})
	}
}
```

> **注**: match モードでの release タイミングは設計判断が要る。
> シンプルな実装: goroutine で `conn.Receive()` の close を待ち `limiter.release()` する。
> あるいは limiter は接続受付の瞬間スパイクだけ防ぐ目的なら、acquire→upgrade 成功→即 release でも可。

### 2.2 Render インスタンスサイズ

| プラン | RAM | CPU | 月額 | 判定 |
|---|---|---|---|---|
| Free | 512MB | 0.1 CPU | $0 | スリープありで不可 |
| **Starter** | 512MB | 0.5 CPU | **$7** | 99接続なら十分 |
| Standard | 2GB | 1 CPU | $25 | 確実 |

**推奨**: Starter で試験 → 負荷テスト(Plan-11)で判断 → 必要なら Standard に変更。

#### render.yaml の更新

```yaml
services:
  - type: web
    name: takoda99-server
    runtime: docker
    plan: starter           # ← free から変更
    envVars:
      - key: GOGC
        value: "200"        # ← GC 頻度を下げる
```

### 2.3 GC チューニング

Go の GC は一般に問題にならないが、大量の小 alloc が一瞬で来ると STW が伸びる可能性:

```
GOGC=200
```

Render の環境変数に設定。GC の閾値を2倍にしてトリガ回数を減らす。

### 2.4 MatchStart 配信の最適化（計測後）

99店分の `StoreSummary[]` を含む MatchStart を99回 JSON marshal するのは無駄だが:
- ハッカソン規模では問題にならない可能性が高い
- 計測して問題が出てからの対処で十分
- 最適化する場合: StoreSummary 部分を1回だけ marshal し、per-player 部分と結合

### 2.5 再試合対策

1試合目が終わった後に即座に再マッチングに入る場合:
- 同時に複数試合が走る可能性（1試合目の残り + 2試合目の開始）
- Go の goroutine + channel モデルで自然に並行処理できる
- メモリ: 1試合あたり 300客 + 99店 ≒ 数MB。2試合同時でも問題ない

---

## 3. テスト方法（Plan-11 と連携）

### ローカルで簡易確認

```bash
# 接続リミッターの動作確認
# 1. サーバーを起動
go run ./cmd/server --mode match --addr :8080

# 2. 別ターミナルから wscat で大量接続
for i in $(seq 1 100); do
  wscat -c ws://localhost:8080/ws &
done
# → 最初の200接続は成功、超過分は 503 が返る

# 3. 接続数を確認（/healthz が接続数を返す場合、Plan-12）
curl http://localhost:8080/healthz
```

### 本番相当テスト

Plan-11 の `cmd/loadtest` で99 WebSocket 接続の同時負荷テストを行う。

計測項目:
- 接続 → MatchStart 受信: **3秒以内**
- メモリ使用量: 512MB 以内
- panic/OOM: なし

---

## 4. 完了条件

- [ ] `connLimiter` が実装され、`/ws` ハンドラに組み込まれている
- [ ] `maxConcurrentConnections = 200` で制限がかかる
- [ ] 超過接続に 503 が返る
- [ ] render.yaml で Starter プラン + `GOGC=200` が設定されている
- [ ] 99接続同時で panic/OOM しない（Plan-11 で確認）
- [ ] MatchStart が全員に3秒以内に届く（Plan-11 で計測）
- [ ] `go build ./...` が通る
