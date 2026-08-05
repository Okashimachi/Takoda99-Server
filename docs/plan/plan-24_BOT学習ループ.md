# Plan-24: プレイヤー統計 → BOTプロファイル生成 → 挙動反映

> **目的**: 蓄積したプレイヤー統計を分析し、BOT の強さ・挙動を人間の分布に寄せてリアルにする。
> **対応issue**: #15
> **優先度**: **低（イベント後）**。りーせの最終的な狙いだが、当日の成立には不要。
> **依存**: 試合結果の永続化（実装済み）＋ 打鍵単位イベントの永続化（**未実装・本プランの Phase 2**）
> **参照**: `internal/store/store.go`, `internal/db/result.go`, `internal/bot/bot.go`

---

## 0. なぜ後回しでよいか

BOT は現在**固定パラメータ**（`BotParams`: `baseAccuracy` / `baseElapsedMs` / jitter）で動いており、
config-front から調整できる。当日はこれを手で調整すれば十分成立する。

本プランは「**手で調整する代わりに、実データから自動で人間らしくする**」という改善であって、
無くても試合は成立する。イベント後の楽しみとして取っておく。

---

## 1. 現状で取れているデータ

`match` / `store_result` テーブル（Plan-08 で実装済み）:

| カラム | 粒度 |
|---|---|
| `served_count` | 1試合で提供した注文数 |
| `avg_accuracy` | 1試合の平均精度 |
| `avg_elapsed_ms` | 1注文あたりの平均所要 |
| `is_bot` | 人間/Bot の区別 |
| `final_rank` | 最終順位 |

**これは「試合単位の平均」**。BOT プロファイルを作るには粒度が粗い。

分かること:
- 人間全体の平均精度・平均速度の分布
- 上位/下位プレイヤーの傾向

分からないこと:
- **お題レベル別**の速度・ミス率（難しい語で急に遅くなるのか）
- 時間経過による疲れ・慣れ
- ミスの偏り（特定の文字で詰まる）

---

## 2. Phase 2: 打鍵単位イベントの永続化（本プランの前提）

BOT を人間らしくするには**レベル別の分布**が要る。そのためのテーブルを足す。

### テーブル設計

```sql
CREATE TABLE IF NOT EXISTS order_attempt (
    id            BIGSERIAL PRIMARY KEY,
    match_id      TEXT NOT NULL REFERENCES match(id),
    store_id      TEXT NOT NULL,
    customer_id   TEXT NOT NULL,
    attribute     TEXT NOT NULL,     -- Normal/Bonus/Claimer/Buzz
    heat_level    INT  NOT NULL,     -- 提供時の火力（お題難度の代理）
    order_count   INT  NOT NULL,     -- 注文の語数
    keystrokes    INT  NOT NULL,     -- 総打鍵数
    elapsed_ms    INT  NOT NULL,
    miss_count    INT  NOT NULL,
    is_bot        BOOLEAN NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX ON order_attempt (heat_level, is_bot);
```

> **旧 Textro の `daken_attempt` に相当**するが、たこ焼き版では「1打鍵」ではなく
> **「1注文（N語）」が最小単位**（`OrderServed` の粒度）。名前も `order_attempt` にする。

### 書き込み方式（重要）

**tick ループの中で INSERT しない**。99店 × 数十注文で DB が詰まる。

セッション内にバッファを持ち、**試合終了時に一括 INSERT** する:

```go
// Session に追加（メモリのみ。DBは知らない）
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

// ApplyOrderServed の末尾で append するだけ
s.attempts = append(s.attempts, orderAttempt{...})
```

`Results()` と同様に `Attempts()` で公開し、`app.saveResults` が一緒に保存する。

**メモリ見積もり**: 1試合で 300客 × 平均2回割当 ≒ 600件。1件 100B として 60KB。問題ない。

---

## 3. Phase 3: プロファイル生成

### 集計クエリ

```sql
-- 人間の heat_level 別の速度・ミス率分布
SELECT
    heat_level,
    COUNT(*)                                        AS n,
    percentile_cont(0.25) WITHIN GROUP (ORDER BY elapsed_ms::float / keystrokes) AS p25_ms_per_key,
    percentile_cont(0.50) WITHIN GROUP (ORDER BY elapsed_ms::float / keystrokes) AS p50_ms_per_key,
    percentile_cont(0.75) WITHIN GROUP (ORDER BY elapsed_ms::float / keystrokes) AS p75_ms_per_key,
    AVG(miss_count::float / keystrokes)             AS avg_miss_rate
FROM order_attempt
WHERE is_bot = FALSE
GROUP BY heat_level
ORDER BY heat_level;
```

### 生成物

3段階の難易度帯を作る:

| 帯 | 元にする分位 |
|---|---|
| 弱BOT | p75（遅い側） |
| 中BOT | p50 |
| 強BOT | p25（速い側） |

```json
{
  "generatedAt": "2026-08-10T12:00:00Z",
  "sampleSize": 4213,
  "profiles": {
    "weak":   { "0": {"msPerKey": 320, "missRate": 0.12}, "5": {"msPerKey": 380, "missRate": 0.18} },
    "normal": { "0": {"msPerKey": 210, "missRate": 0.06}, "5": {"msPerKey": 250, "missRate": 0.09} },
    "strong": { "0": {"msPerKey": 140, "missRate": 0.02}, "5": {"msPerKey": 160, "missRate": 0.03} }
  }
}
```

### 生成方法

**バッチではなく手動でよい**（データが増えるのはイベント時だけ）。

```bash
go run ./cmd/botprofile --dsn "$DATABASE_URL" --out profiles.json
```

---

## 4. Phase 4: BOT への反映

### ハードコードしない

生成したプロファイルは **config 経由**で注入する。`internal/bot` に数値を直書きしない
（AGENTS.md 1.3 の「調整値をハードコードしない」）。

選択肢:

| 方法 | 評価 |
|---|---|
| `BotParams` を heat_level 別テーブルに拡張 | `GameParameters` が肥大化する。`==` 比較の制約もある |
| **専用テーブル `bot_profile` を作る** | **推奨**。config とは別ライフサイクル |
| JSON ファイルを埋め込む | 更新に再デプロイが要る |

```sql
CREATE TABLE IF NOT EXISTS bot_profile (
    tier        TEXT NOT NULL,     -- weak/normal/strong
    heat_level  INT  NOT NULL,
    ms_per_key  INT  NOT NULL,
    miss_rate   FLOAT NOT NULL,
    PRIMARY KEY (tier, heat_level)
);
```

`internal/bot` は `BotProfileSource` interface（DIP）で受け取る。
DB が無ければ現在の `BotParams` にフォールバックする。

### 難易度帯の割り当て

1試合の Bot 99体をどの帯にするか。人間の分布に合わせるなら:

```
weak 25% / normal 50% / strong 25%
```

これも `GameParameters` で調整可能にする。

---

## 5. 受入条件（最終ゴール）

- 実プレイのログが増えるほど BOT の打鍵/ミスが人間らしくなる
- **A/B 観測**: 旧BOT と新BOT で、人間プレイヤーの最終順位分布が変わるか

観測方法: `match.config_hash` と `store_result.is_bot` で試合を分け、
人間の `final_rank` の分布を比較する。新BOTの方が「人間が真ん中あたりに来る」なら成功。

---

## 6. 段階的な完了条件

**Phase 2（データ基盤）**
- [ ] `order_attempt` テーブルが作られる
- [ ] `ApplyOrderServed` で attempt がメモリにバッファされる（**tick中に INSERT しない**）
- [ ] 試合終了時に一括 INSERT される
- [ ] 保存失敗で試合が壊れない（best-effort）
- [ ] `internal/game` が DB を知らないまま（`Attempts()` で公開するだけ）

**Phase 3（分析）**
- [ ] heat_level 別の速度・ミス率の分位が出せる
- [ ] `cmd/botprofile` が profiles.json を生成できる
- [ ] 弱/中/強の3帯が作れる

**Phase 4（反映）**
- [ ] `bot_profile` テーブル経由で BOT の挙動が変わる
- [ ] ハードコードしていない（DIP で注入）
- [ ] DB が無い時は現行の `BotParams` にフォールバックする
- [ ] 難易度帯の配分が config で調整できる
- [ ] A/B で人間の順位分布の変化を観測した
