# クライアント結合ガイド（Web / Unity 向け）

Takoda99 サーバーへ**クライアント（Web=TS / Unity=C#）をつなぐためのワイヤ仕様**。

> **正典の所在**
> - メッセージ契約: [Takoda99-Proto `proto/messages.go`](https://github.com/Okashimachi/Takoda99-Proto)
> - ゲーム仕様: Takoda99-Docs `02_共通仕様/02_プロトコル仕様.md`
> - **クライアント側の設計**（アーキテクチャ・状態管理・打鍵判定・画面遷移）: **Takoda99-Client-Docs リポジトリ**
>
> 本書はサーバー側から見た「線の上で何が流れるか」だけを扱う。クライアント内部の作り方は Client-Docs を見ること。

---

## 0. 大原則（これだけは外さない）

1. **打鍵の正誤判定はクライアントの責務**。サーバーは判定しない（キー列も受け取らない）。
2. **試合中にクライアントが送るのは `OrderServed` だけ**。注文N個の単語を打ち切った瞬間に1回送る。
   - 単語1個ごとの完成報告は**送らない**
   - 客の離脱・自店の脱落・順位は**送らない**（すべてサーバーが自律確定する）
3. **サーバーが権威**。客が来た/去った、信用が減った、脱落した、順位はいくつ — 全部サーバーから降ってくる。クライアントはローカルで先に確定させない。
4. **お題単語はサーバーが発行する**。`CustomerArrived.words` に入っている。クライアントが自前で単語を選ばない。
5. 全メッセージは **`{"type": "...", "payload": {...}}` の封筒**。JSON、フィールドは **camelCase**。WS フレームは **text**。

> ⚠ 旧 Textro99（直接攻撃型）の `DakenClearReport` / `AttackRequest` / `StrategySelect` / `KoNotified` /
> `Welcome` などは**すべて廃止**。Takoda99-Proto に存在しない。

---

## 1. 接続

| 項目 | 値 |
|---|---|
| エンドポイント | `wss://<service>.onrender.com/ws` |
| ヘルスチェック | `GET https://<service>.onrender.com/healthz` → `200 ok` |
| サブプロトコル | なし（素の WebSocket） |
| フレーム | text（UTF-8 JSON） |
| 認証 | なし（ハッカソン前提） |

ブラウザは `Origin` を必ず送る。サーバーの `ALLOWED_ORIGINS` に載っていないと拒否される（未設定なら全許可）。

### 起動モード（重要）

サーバーには2モードあり、**接続後の挙動が変わる**:

- **solo**（結合テスト用）: `/ws` に**1接続しただけで即試合開始**（人間1＋Bot）。単独で結合テストできる。
- **match**（本番）: 接続すると待機プールに入り、`MatchmakingStatus` が届く。規定人数＋カウントダウン成立で `MatchStart`。

> **今どちらで動いているかは接続直後の最初のメッセージで分かる**: `MatchmakingStatus` が来たら match、いきなり `MatchStart` なら solo。
> 結合開発は solo で回すのが楽（server 窓口＝りーせに solo 化を依頼できる）。本番は match なので、クライアントは**両方のシーケンスに対応**しておくこと。

---

## 2. メッセージ一覧

### 2.1 クライアント→サーバー（C2S）— 送るのはこれだけ

| type | いつ送る |
|---|---|
| `OrderServed` | 注文N個の単語を打ち切った瞬間（**試合中はこれだけ**） |
| `MatchmakingJoin` | マッチングキューに参加する時 |
| `MatchmakingLeave` | キューから抜ける時 |

### 2.2 サーバー→クライアント（S2C）

| type | 宛先 | 内容 |
|---|---|---|
| `MatchmakingStatus` | 待機者 | 待機人数・カウントダウン |
| `MatchStart` | 各自 | 試合開始。自店ID・公開パラメータ・全店概況 |
| `CustomerArrived` | 自店 | 客の来店（お題単語つき） |
| `CustomerLeft` | 自店 | 客の離脱（我慢ゲージ切れ） |
| `CreditUpdate` | 自店 | 信用（ライフ）の変化 |
| `EvaluationUpdate` | 自店 | 自店の評価・順位 |
| `PhaseChange` | 全員 | フェーズ移行（Early/Mid/Late） |
| `DifficultyUpdate` | 全員 | 火力（お題難度）の更新 |
| `StoreListUpdate` | 全員 | 99店概況の低頻度スナップ（ミニ盤面） |
| `ForcedEliminationWarning` | 全員 | 下位淘汰の予告 |
| `StoreEliminated` | 全員 | 脱落確定（自店なら→リザルト、他店なら→盤面更新） |
| `MatchEnd` | 各自 | 試合終了・最終順位・統計 |

---

## 3. 標準シーケンス

```
[接続]
  → MatchmakingStatus（match モードのみ・人数変化のたび）
  → MatchStart                                   ← ここから試合

[試合中・ループ]
  ← CustomerArrived   客が来た（words を表示して打たせる）
  → OrderServed       打ち切ったら送る
  ← EvaluationUpdate  評価・順位が更新される
  ← CustomerLeft      間に合わなかった客は勝手に去る
  ← CreditUpdate      去られると信用が減る
  ← PhaseChange / DifficultyUpdate / StoreListUpdate / ForcedEliminationWarning
  ← StoreEliminated   誰かが脱落（自分かもしれない）

[終了]
  ← MatchEnd          最終順位と統計
```

**自店が脱落した後も接続は切れない**。観戦者として S2C を受け続け、最後に `MatchEnd` が届く。

---

## 4. メッセージ別・フィールド

### OrderServed（C2S）

```json
{
  "type": "OrderServed",
  "payload": {
    "customerId": "c-42",
    "elapsedMs": 3500,
    "missCount": 2,
    "clientTimestamp": 1754212345678
  }
}
```

| フィールド | 意味 |
|---|---|
| `customerId` | どの客への提供か。**行列先頭（対応中）の客でなければ棄却される** |
| `elapsedMs` | 注文N個を打ち切るのにかかった時間（クライアント計測） |
| `missCount` | その注文でのミス総数（クライアント計測） |
| `clientTimestamp` | 同時脱落のタイブレーク用 |

サーバーは性善説で受けるが**下限クランプ＋範囲クランプ**をかける。極端な値を送ってもスコアは伸びない。
存在しない `customerId` や、行列先頭でない客への報告は**黙って無視される**（エラーは返らない）。

### MatchStart（S2C）

```json
{
  "type": "MatchStart",
  "payload": {
    "matchId": "m-1",
    "selfStoreId": "s-7",
    "params": { "matchTimeLimitMs": 0, "initialLife": 3, "maxStores": 99 },
    "phase": "Early",
    "stores": [
      { "storeId": "s-1", "displayName": "たこ屋1", "evalNormalized": 0, "rank": 0, "creditLife": 3, "alive": true }
    ]
  }
}
```

- `selfStoreId` … `stores[]` の中で自分がどれか。**これで自店を特定する**
- `params.matchTimeLimitMs` … **制限時間は廃止されたので 0 が入る**。残り時間 UI は作らない
- `params.initialLife` … ライフゲージの最大値
- `phase` … `Early` / `Mid` / `Late`

### CustomerArrived（S2C）

```json
{
  "type": "CustomerArrived",
  "payload": {
    "customerId": "c-42",
    "attribute": "Normal",
    "orderCount": 2,
    "words": ["たこやき", "そーす"],
    "patienceMaxMs": 8000
  }
}
```

- `words` … **サーバーが発行したお題**。この配列を順に打たせる。要素数 = `orderCount`
- `patienceMaxMs` … 我慢ゲージの最大値。**クライアントはこれでゲージを描画する**が、離脱の確定はサーバーが行う。ローカルで 0 になっても勝手に離脱させず `CustomerLeft` を待つ
- `attribute` … `Normal` / `Bonus` / `Claimer` / `Buzz`（来店時のみ通知・以降不変）

### CustomerLeft / CreditUpdate（S2C）

```json
{ "type": "CustomerLeft", "payload": { "customerId": "c-42", "reason": "Timeout" } }
{ "type": "CreditUpdate", "payload": { "life": 2, "delta": -1, "reason": "CustomerLeft" } }
```

信用は**離脱でのみ減る。回復手段はない**。`life` が確定値、`delta` が変化量。

### EvaluationUpdate（S2C）

```json
{ "type": "EvaluationUpdate", "payload": { "evalRaw": 0.62, "normalized": 0.78, "rank": 21, "aliveCount": 88 } }
```

- `normalized` … 生存店内のパーセンタイル 0..1。**客の集まりやすさ**に直結する
- `rank` … 生存店内の評価順位（1が最上位）。**最終順位ではない**

### StoreListUpdate / PhaseChange / DifficultyUpdate（S2C）

```json
{ "type": "StoreListUpdate", "payload": { "stores": [ ... ], "aliveCount": 88 } }
{ "type": "PhaseChange",     "payload": { "phase": "Mid" } }
{ "type": "DifficultyUpdate","payload": { "heatLevel": 5 } }
```

`StoreListUpdate` は帯域が重い（O(N²)）ため**低頻度**でしか来ない。毎tick来る前提で作らないこと。

### ForcedEliminationWarning（S2C）

```json
{ "type": "ForcedEliminationWarning", "payload": { "untilTick": 10, "thresholdPct": 0.1 } }
```

「あと `untilTick` ティックで、評価下位 `thresholdPct` が強制脱落する」の予告。緊張演出に使う。

### StoreEliminated / MatchEnd（S2C）

```json
{ "type": "StoreEliminated", "payload": { "storeId": "s-7", "reason": "SelfCollapse", "finalRank": 34 } }
{ "type": "MatchEnd", "payload": { "finalRank": 34, "stats": { "servedCount": 21, "avgAccuracy": 0.93, "avgElapsedMs": 3200 } } }
```

- `reason` … `SelfCollapse`（信用0で自滅）/ `Cull`（下位淘汰で強制脱落）
- **最終順位は脱落順のみで決まる**。評価は使わない。先に脱落するほど大きい数字（下位）、最後の1店が `1`

---

## 5. 最小クライアント実装（擬似コード）

```
ws.onmessage = (env) => {
  switch (env.type) {
    case "MatchStart":
      selfStoreId = env.payload.selfStoreId
      maxLife     = env.payload.params.initialLife
      renderBoard(env.payload.stores)
      break

    case "CustomerArrived":
      queue.push(env.payload)      // 客を行列に積む
      startTypingIfHead()          // 先頭なら打ち始められる
      break

    case "CustomerLeft":
      queue.remove(env.payload.customerId)   // サーバー権威。ローカル判断で消さない
      break

    case "CreditUpdate":     setLife(env.payload.life); break
    case "EvaluationUpdate": setRankUI(env.payload);    break
    case "PhaseChange":      setPhase(env.payload.phase); break
    case "StoreListUpdate":  updateBoard(env.payload);  break

    case "StoreEliminated":
      if (env.payload.storeId === selfStoreId) showEliminated(env.payload.finalRank)
      else updateBoard(env.payload)
      break

    case "MatchEnd": showResult(env.payload); break
  }
}

// 注文を打ち切ったら1回だけ送る
function onOrderFinished(customer, elapsedMs, missCount) {
  ws.send({ type: "OrderServed", payload: {
    customerId: customer.customerId, elapsedMs, missCount,
    clientTimestamp: Date.now()
  }})
  // ここで客を消さない。サーバーの次の配信を待つ
}
```

---

## 6. つまずきどころチェックリスト

- ❌ 単語1個ごとに `OrderServed` を送る → **注文N個を打ち切って1回**
- ❌ 我慢ゲージが 0 になったらローカルで客を消す → **`CustomerLeft` を待つ**
- ❌ `OrderServed` を送った直後にローカルで客を消す → サーバーの確定を待つ（ズレの元）
- ❌ 行列の途中の客に対して `OrderServed` を送る → **先頭のみ受理**される
- ❌ 残り時間 UI を作る → **制限時間は廃止**（`matchTimeLimitMs` は 0）
- ❌ 脱落したら接続を切る → 観戦のため接続は維持。`MatchEnd` まで受け続ける
- ❌ `rank`（評価順位）を最終順位として表示する → 最終順位は `MatchEnd.finalRank`
- ❌ お題単語をクライアントが選ぶ → **`CustomerArrived.words` を使う**
- ❌ `StoreListUpdate` が毎tick来る前提で作る → 低頻度。補間はクライアント側で
- ❌ `Welcome` を待つ → **廃止済み**。最初の S2C は `MatchmakingStatus` か `MatchStart`

---

## 7. 動作確認の手っ取り早い方法

```bash
# サーバーを solo モードで起動（接続すると即試合が始まる）
go run ./cmd/server --mode solo --bots 5

# 生WSで覗く
websocat ws://localhost:8080/ws
```

`--mode solo` なら1接続で `MatchStart` 以降の全メッセージを確認できる。
マッチングの挙動まで見たい場合は `--mode match` で複数クライアントを繋ぐ。

> 本書の JSON サンプルは Takoda99-Proto の定義から起こしたもの。実際に流れるバイト列は
> `internal/proto/wire_golden_test.go` と `internal/app/e2e_wire_test.go` が固定している。
> **乖離したらテストのゴールデンを正とする**（ドキュメントではなく）。
