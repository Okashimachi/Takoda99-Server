# Plan-18: 99接続負荷テストの再現可能化

> **目的**: 99接続の負荷テストを**繰り返し実行できるコマンド**として固定し、変更のたびに性能退行がないことを確認できるようにする。
> **対応issue**: #47
> **優先度**: 中〜高。一度は実測済みだが、再現手段が無い。
> **依存**: Plan-13（`internal/sim`）, Plan-17（メトリクス）
> **前身**: `docs/plan/plan-11_負荷テスト.md`（Render前提の設計）

---

## 0. 現状 — 一度は測ってある

`docs/deploy.md` に **2026-08-05 の実機計測結果**がある（e2-micro / 99体 / TLS経由）:

| 項目 | 実測 | e2-micro の予算 |
|---|---|---|
| CPU 合計 | 0.135 vCPU | 54% |
| メモリ | server 21MB + caddy 49MB | 1GB中 7% |
| 通信量(TX) | **645 MB / 1試合** | 無料枠 月1GB の65% |
| 機能 | 接続99/99・MatchStart/End 99/99・エラー0 | — |

**結論は出ている（e2-micro で回せる）。問題は、これを実行したツールがリポジトリに無いこと。**

```bash
ls cmd/     # → server のみ。matchsim も loadtest も無い
```

つまり:
- コードを変えた後に**同じ計測を再現できない**
- 当日直前の最終確認ができない
- egress 削減（645MB → ?）の効果を測れない

本プランはこれを**コマンド化**する。

---

## 1. 2種類のテスト

| | 何を見るか | 実行環境 | 目標 |
|---|---|---|---|
| **ヘッドレス**（`cmd/matchsim`） | ゲームロジックの速度・決着 | メモリ内 | 1試合を10秒以内でシミュレート |
| **実接続**（`cmd/loadtest`） | 99 WebSocket を捌けるか | 実サーバー | MatchStart 3秒以内・完走・OOMなし |

ヘッドレス側は **Plan-13 で作る `internal/sim`** がそのまま使えるので、本プランの主対象は `cmd/loadtest`。

---

## 2. cmd/loadtest の設計

### 2.1 やること

1. 99本の WebSocket を**並行に**張る
2. **接続直後に全員 `MatchmakingJoin`（`displayName` 付き）を送る**
   サーバーは `awaitJoinName` で最初の1メッセージを**最大3秒待つ**（`joinTimeout = 3 * time.Second`）。
   送らないと**99接続 × 3秒**の待ちが入り、接続→MatchStart の計測が丸ごと壊れる
3. 全員が `MatchStart` を受けるまで待つ（計測ポイント①）
4. Bot として振る舞う（`CustomerArrived` → 待つ → `OrderServed`）
5. 全員が `MatchEnd` を受けるまで待つ（計測ポイント②）
6. 受信バイト数・所要時間・エラーをレポート

### 2.2 Bot ロジックは internal/bot を使わない

`internal/bot` は `transport.Connection`（InMemory Pipe）前提。
loadtest は**実 WebSocket** を張るので、素の `coder/websocket` で書く。
ロジック自体は単純なので重複を許容する。

### 2.3 スケルトン

`cmd/loadtest/main.go`:

```go
package main

func main() {
	url := flag.String("url", "wss://takoda99.mooo.com/ws", "接続先")
	n := flag.Int("clients", 99, "同時接続数")
	serveMs := flag.Int("serve-ms", 3000, "1注文を捌くのにかける時間")
	missRate := flag.Float64("miss-rate", 0.05, "ミス率")
	timeout := flag.Duration("timeout", 10*time.Minute, "全体タイムアウト")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	rep := newReport(*n)

	// ── ① 全接続を並行に張る ──
	connectStart := time.Now()
	var wg sync.WaitGroup
	clients := make([]*client, *n)
	for i := 0; i < *n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := dial(ctx, *url)
			if err != nil {
				rep.connectFail(i, err)
				return
			}
			clients[i] = c
		}(i)
	}
	wg.Wait()
	rep.connectDone(time.Since(connectStart))

	// ── ② 各クライアントを走らせる ──
	for _, c := range clients {
		if c == nil {
			continue
		}
		wg.Add(1)
		go func(c *client) {
			defer wg.Done()
			c.run(ctx, *serveMs, *missRate, rep)
		}(c)
	}
	wg.Wait()

	rep.print()
	if rep.failed() {
		os.Exit(1)
	}
}
```

クライアント1本:

```go
func (c *client) run(ctx context.Context, serveMs int, missRate float64, rep *report) {
	defer c.conn.Close(websocket.StatusNormalClosure, "")

	// 接続直後に必ず送る。サーバーは awaitJoinName でこれを最大3秒待っており、
	// 送らないと1接続あたり3秒の空白が入って計測が無意味になる。
	_ = c.send(proto.TypeMatchmakingJoin, proto.MatchmakingJoin{
		DisplayName: fmt.Sprintf("load-%d", c.idx),
	})

	for {
		typ, payload, n, err := c.read(ctx)
		if err != nil {
			rep.readErr(c.idx, err)
			return
		}
		rep.addBytes(n)   // ← egress 計測。645MB の内訳を掴む

		switch typ {
		case proto.TypeMatchStart:
			rep.matchStart(c.idx)

		case proto.TypeCustomerArrived:
			var v proto.CustomerView
			if json.Unmarshal(payload, &v) != nil {
				continue
			}
			// 実時間で待ってから提供（サーバーの我慢ゲージと噛み合わせる）
			go func() {
				select {
				case <-time.After(time.Duration(serveMs) * time.Millisecond):
				case <-ctx.Done():
					return
				}
				keys := 0
				for _, w := range v.Words {
					keys += utf8.RuneCountInString(w)
				}
				miss := 0
				for i := 0; i < keys; i++ {
					if rand.Float64() < missRate {
						miss++
					}
				}
				_ = c.send(proto.TypeOrderServed, proto.OrderServed{
					CustomerId: v.CustomerId,
					ElapsedMs:  serveMs,
					MissCount:  miss,
				})
			}()

		case proto.TypeMatchEnd:
			rep.matchEnd(c.idx)
			return
		}
	}
}
```

> **`OrderServed` を goroutine から送る**理由: `serveMs` 待つ間も他のメッセージを読み続ける必要がある。
> 読みを止めるとサーバーの送信キューが詰まり、slow-consumer として切られる。

> **並行送信に注意**: `coder/websocket` の `Write` は並行安全ではない。
> `client` に `sync.Mutex` を持たせて `send` を排他する。

### 2.4 レポート

```
=== loadtest: wss://takoda99.mooo.com/ws / 99 clients ===
接続            : 99/99 成功 (2.1s)
MatchStart 受信 : 99/99 (接続完了から 0.8s / 合計 2.9s)   ← 目標3秒以内 ✅
MatchEnd 受信   : 99/99 (試合 163.2s)
エラー          : 0

受信バイト      : 合計 645.3 MB / 1クライアント平均 6.5 MB
  内訳（type別）:
    StoreListUpdate      612.0 MB (94.8%)   ← 削るならここ
    CustomerArrived       21.4 MB ( 3.3%)
    EvaluationUpdate      8.1 MB ( 1.3%)
    その他                3.8 MB ( 0.6%)

判定: PASS
```

**type 別の内訳を出すのが重要**。`docs/deploy.md` は「通信量のほぼ全部が StoreListUpdate」と
書いているが、数字で裏取りできるようにする。これが egress 削減の判断材料になる。

### 2.5 コマンド

```bash
# 本番へ（イベント直前の最終確認）
go run ./cmd/loadtest --url wss://takoda99.mooo.com/ws --clients 99

# ローカルへ（開発中の退行チェック）
go run ./cmd/server --mode match &
go run ./cmd/loadtest --url ws://localhost:8080/ws --clients 99
```

> ⚠ **本番に撃つ時は必ず事前に周知する**。試合が1本走るので、他の人が繋いでいると巻き込む。
>
> ⚠ **`minPlayers` を 99 に合わせてから撃つこと**。既定の 20 のままだと、
> 20本目が繋がった時点でカウントダウンが始まり、**99本揃う前に試合が開始**してしまう。
> 遅れて繋いだクライアントは次の試合待ちになり、「MatchStart 99/99」が永久に揃わない。
> config-front で `minPlayers=99` にしてから実行し、終わったら戻す（Plan-16）。

---

## 3. サーバー側の実測（VM で並走させる）

loadtest はクライアント側の数字しか取れない。CPU/メモリは VM 側で測る。

```bash
# 計測前後で CPU 時間の差分を取る（%cpu は生涯平均なので使えない）
ps -o time= -p $(pgrep -f '/opt/takoda99/server')
```

```bash
# メモリ
ps -o rss= -p $(pgrep -f '/opt/takoda99/server')
```

Plan-17 を入れていれば `/healthz` と `metrics` ログの方が楽:

```bash
curl -s https://takoda99.mooo.com/healthz | jq .
```

```bash
sudo journalctl -u takoda99 -o cat | jq -c 'select(.msg=="metrics")' | tail -20
```

> `docs/deploy.md` の計測の注意（`ps -o %cpu` はプロセスの生涯平均で瞬間値ではない）を守ること。

---

## 4. CI に乗せる範囲

| テスト | CI | 理由 |
|---|---|---|
| ヘッドレス（`internal/sim`） | **回す** | 外部依存なし・高速。Plan-14 の決着保証と同じ枠 |
| 実接続（`cmd/loadtest`） | **回さない** | 外部サーバー依存。手動実行 |

`cmd/loadtest` はビルドだけ CI で確認する（`go build ./...` に含まれる）。

---

## 5. 目標値（判定基準）

| 項目 | 目標 | 2026-08-05 実測 |
|---|---|---|
| 接続成功 | 99/99 | 99/99 ✅ |
| 接続 → MatchStart | **3秒以内** | 未計測（本プランで取る）※`startCountdownMs`(既定15秒)を含まない値で見る |
| MatchEnd 受信 | 99/99 | 99/99 ✅ |
| CPU | e2-micro の 0.25 vCPU 以内 | 0.135 (54%) ✅ |
| メモリ | 1GB 以内 | 70MB (7%) ✅ |
| エラー | 0 | 0 ✅ |
| egress | — | 645MB/試合（無料枠 月1GB の65%）⚠ |

**egress だけが要注意**。1試合で月間無料枠の65%を使う。超過は1GBあたり$0.1前後＝1試合10円強。
削るなら `StoreListUpdate` の配信頻度（`Session.PublishIntervalMs`）を下げるのが直効。
4Hz→2Hz で半減する。**本番当日に何試合やるか次第で判断**する。

---

## 6. 完了条件

- [ ] `cmd/loadtest` が存在し、`go run ./cmd/loadtest --url ... --clients 99` で実行できる
- [ ] 99本の WebSocket を並行に張り、全員の `MatchStart` / `MatchEnd` を待てる
- [ ] 接続直後に `MatchmakingJoin`（`displayName` 付き）を送っている（3秒待ちが入らない）
- [ ] 実行前に `minPlayers=99` にする手順が書かれている（途中で試合が始まらない）
- [ ] 接続 → MatchStart の所要時間が計測・出力される（目標3秒以内）
- [ ] 受信バイト数が**メッセージ type 別の内訳付き**で出る
- [ ] 失敗時に exit code 1 で終わる（手動実行でも判定が明確）
- [ ] `--url` でローカル/本番を切り替えられる
- [ ] `coder/websocket` の Write が排他されている（並行書き込みで壊れない）
- [ ] `internal/sim` のヘッドレステストが CI で回る
- [ ] 実測結果（接続時間・egress 内訳）を `docs/deploy.md` の計測セクションへ追記
- [ ] egress 削減が必要かの判断材料（type別内訳）が揃っている
