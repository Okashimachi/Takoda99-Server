# Plan-h03: 注文単位イベントの永続化（order_attempt）

> **目的**: 試合単位の集計（`store_result`）より細かい**注文単位**の打鍵/精度/速度を heat レベル別に蓄積し、BOT強化（h04/h05）と算法改良（h06）の燃料にする。「プレイヤーのデータを全部残す」の中核。
> **対応issue**: #15（BOT学習ループ）
> **依存**: PR #104（`store_result` 詳細stats＋生存時間・Bot除外）マージ後
> **参照**: **[plan-h00 共有コントラクト](plan-h00_共有コントラクト.md) §0・§6**, `docs/plan/plan-24_BOT学習ループ.md` §2（本planは Phase2 の本戦版）, `docs/plan/plan-08_試合結果永続化.md`, `internal/game/session.go`, `internal/db/result.go`

---

## 0. 前提知識

### 読むべきファイル

| ファイル | 内容 |
|---|---|
| `internal/game/session.go` | `ApplyOrderServed`（`212-273`）／`Results()`。keystrokes は `c.keystrokeTotal`、elapsed/miss はクランプ後（`229-247`） |
| Takoda99-Proto `messages.go:210` | `OrderServed` の実フィールド（keystrokes を**持たない**ことの確認） |
| `internal/store/store.go` | `Result` / `MatchResult` / `ResultStore`（PR #104で更新済み想定） |
| `internal/db/result.go` | `SaveMatch`（トランザクション一括INSERT） |
| `internal/app/app.go` | `saveResults`（試合終了後の保存） |
| `docs/plan/plan-24_BOT学習ループ.md` | §2 の設計（本planはその本戦具体化） |

### PR #104 との関係（レイヤーが違う）

| 粒度 | テーブル | 出所 | 本plan |
|---|---|---|---|
| 試合×店 | `store_result` | PR #104（詳細stats・生存時間・Bot除外） | 触らない |
| **注文** | **`order_attempt`（新規）** | 本plan | **これを足す** |

`store_result` は「1店が1試合でどうだったか」。`order_attempt` は「1注文をどう捌いたか」。BOTを人間らしくするには**heat別の速度・ミス率の分布**が要るので後者が要る。両者は積み上がる（衝突なし）。

> ⚠ **N9 — #104 は未マージ（OPEN）。着手はマージ後**。#104 は `session.go` / `store.go` / `db/result.go` / `app.go` を触る（`store_result` に生存時間・詳細stats追加＋Bot除外）。
> 本plan も同じ `session.go`（`ApplyOrderServed` に append）と `app.saveResults`（一括INSERT）に触るため、**先にマージされた #104 の形状（`Results()` / `st.served` の公開の仕方 / `saveResults` の署名）に合わせて実装する**こと。マージ前に書くと確実に競合する。

---

## 1. テーブル設計

```sql
CREATE TABLE IF NOT EXISTS order_attempt (
    id            BIGSERIAL PRIMARY KEY,
    match_id      TEXT NOT NULL REFERENCES match(id),
    store_id      TEXT NOT NULL,
    customer_id   TEXT NOT NULL,
    attribute     TEXT NOT NULL,     -- Normal/Bonus/Claimer/Buzz
    heat_level    INT  NOT NULL,     -- 提供時の火力（お題難度の代理）
    order_count   INT  NOT NULL,     -- 注文の語数（属性で偏る：Buzzは多め）
    keystrokes    INT  NOT NULL,     -- 総打鍵数
    elapsed_ms    INT  NOT NULL,
    miss_count    INT  NOT NULL,
    is_bot        BOOLEAN NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX ON order_attempt (heat_level, is_bot);
```

- 名前は `order_attempt`（旧Textroの `daken_attempt` 相当だが、たこ焼き版の最小単位は「1注文＝N語」）。
- ⚠ **`keystrokes` の出所（重要）**: `OrderServed` は `CustomerId / ElapsedMs / MissCount / ClientTimestamp` **のみ**で、**打鍵数を持たない**（proto `messages.go:210`）。keystrokes は**サーバーが発行したお題語の `KeystrokeCount` 合計**で、客レジストリの `c.keystrokeTotal`（`session.go:237`、`admitCustomer`（`session.go:965`）で発行時に確定）にある。**クライアント集計ではなくサーバー権威値**。`accuracy = 1 - miss/keystrokes` もこの `keystrokeTotal` 基準。

---

## 2. 書き込み方式（tick中にINSERTしない）

99店 × 数十注文で tick 内 INSERT は DB を詰まらせる。**セッション内にバッファ → 試合終了時に一括INSERT**。

```go
// game/session.go（メモリのみ。DBは知らない）
type orderAttempt struct {
    storeId    PlayerId
    customerId proto.CustomerId
    attribute  proto.CustomerAttribute
    heatLevel  int
    orderCount int
    keystrokes int
    elapsedMs  int
    missCount  int
}
// Results() と同様に公開（game は DB を知らない）
func (s *Session) Attempts() []OrderAttempt { ... }
```

**append 位置と各値の出所（`ApplyOrderServed`, `session.go:212-273`）**。`releaseToRest`（271行）の直前は、必要な値が全てスコープ内にある:

```go
// ApplyOrderServed 内、評価反映の後・releaseToRest の直前
s.attempts = append(s.attempts, orderAttempt{
    storeId:    from,               // 引数
    customerId: r.CustomerId,       // OrderServed
    attribute:  c.attribute,        // 客レジストリ
    heatLevel:  s.heatLevel,        // ★提供時点の火力
    orderCount: c.orderCount,       // 客レジストリ
    keystrokes: keys,               // = c.keystrokeTotal（サーバー発行語の打鍵合計）
    elapsedMs:  elapsed,            // G5 参照（クランプ後）
    missCount:  miss,               // G5 参照（クランプ後）
})
```

- **G5 — raw か クランプ後か**: `ApplyOrderServed` は eval 用に値をサニタイズ済み — `elapsed` は `MinMsPerWord*orderCount` で下駄（`session.go:229-233`）、`miss` は `[0, keys]` にクランプ（`session.go:241-247`）。
  **推奨は「クランプ後（`elapsed`/`miss`）を保存」**。理由: これはサーバーが実際に信頼して評価に使った値であり、クライアント送信の異常値（負・巨大・keys超過のmiss）で BOT プロファイルを汚さない。生値が要るなら別カラム（`raw_elapsed_ms`）を足すが、当面は不要。
- **メモリ見積**: 300客 × 平均2回割当 ≒ 600件/試合。1件100Bで60KB。問題なし。
- `heat_level` は**提供時点の火力**（`s.heatLevel`）を記録（後から現在heatで代用しない。難度別分布が崩れる）。
- `app.saveResults` が `store_result` と同じトランザクションで `order_attempt` を一括INSERTする。

> **N8 — 二重計算にならない**: `st.served`（`session.go:258-269`）が既に per-store 集計（keystrokes/misses/count/elapsedSum/accuracySum/byAttr）を持ち、これが **PR #104 の `store_result` 集計の源**。`order_attempt` は同じ per-order 値（keys/miss/elapsed）を行として残すだけで、集計と同源＝整合する。

### Bot を保存するか

- PR #104 は `store_result` で**Bot行を保存しない**方針。`order_attempt` も既定は**人間のみ**に揃える（学習の入力は人間分布）。
- ただし `is_bot` カラムは残す。BotのA/B検証をしたい時に config フラグで保存をONにできるようにする（既定OFF）。
- ⚠ **`is_bot` の埋め場所**: `internal/game` は Bot/人間を区別しない（AGENTS.md §4.2、game から見て同じ `Connection`）。なので `sess.Attempts()` は `is_bot` を持たず、**`app.saveResults` が `botIds`（storeId→bool）で埋める**（`store.Result.IsBot` と同じ流儀・app.go:87）。人間のみ保存もこの層でフィルタする。game に IsBot を持ち込まない。

---

## 3. game コアの純粋性

- `order_attempt` のバッファは**メモリのみ**。`internal/game` は DB を import しない（`Attempts()` で公開するだけ）。plan-24 §6 と同じ制約。
- 保存は best-effort。失敗しても試合は壊さない（ログのみ）。

---

## 4. テスト（バグ注入で落ちることを確認）

- `ApplyOrderServed` を1回呼ぶと `attempts` に1件、内容（heat/attribute/elapsed/miss/keystrokes）が引数と一致する。**わざとフィールドを取り違える変異でテストが落ちること**を確認。
- 試合を通すと `len(Attempts())` が提供回数と一致。
- `heat_level` が「提供時点」の値（tick を跨いで heat が変わっても提供時の値が残る）。
- 保存失敗（DB切断）で試合が正常終了する（best-effort）。
- `//go:build integration` で `order_attempt` に実INSERTされる件数を検証（plan-08 の result_test.go 流用）。
- game が DB を import していない（depguard）。

---

## 5. 完了条件

- [ ] （前提）PR #104 がマージ済みで、その `session.go`/`saveResults` 形状に合わせている
- [ ] `order_attempt` テーブルが Migrate で作成される
- [ ] `ApplyOrderServed` で attempt がメモリにバッファされる（**tick中INSERTしない**）
- [ ] `keystrokes` は `c.keystrokeTotal`（サーバー権威値）を使う（`OrderServed` 由来ではない）
- [ ] `elapsed`/`miss` はクランプ後の値を保存する（G5・生値でBotプロファイルを汚さない）
- [ ] 試合終了時に `store_result` と同一トランザクションで一括INSERTされる
- [ ] `heat_level` は提供時点の値を記録している
- [ ] 既定は人間のみ保存（Bot保存は config フラグ・既定OFF、`is_bot` カラムは保持）
- [ ] 保存失敗で試合が壊れない（best-effort・ログのみ）
- [ ] `internal/game` が DB を知らないまま（`Attempts()` で公開するだけ）
- [ ] 結合テストで件数・内容が検証される
- [ ] `go build` / `go vet` / `golangci-lint run` が通る

---

## 6. h04 への引き継ぎ

`order_attempt` が貯まれば、h04 の `cmd/botprofile` が heat別分位を出して BOT プロファイルを生成できる。**まず本planでデータの蛇口を開ける**のが先。
