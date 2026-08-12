# Plan-h04: BOTプロファイル生成（heat別分位 → tier化）

> **目的**: 蓄積した `order_attempt`（人間データ）を分析し、**弱/中/強** 3tier × heatレベル別の BOT プロファイルを生成する。手調整の代わりに実プレイ分布からBotの強さを作る。
> **対応issue**: #15（BOT学習ループ）
> **依存**: h03（`order_attempt` にデータが貯まっていること）
> **参照**: `docs/plan/plan-24_BOT学習ループ.md` §3（本planは Phase3 の本戦版）, `internal/bot/bot.go`

---

## 0. 前提知識

### 読むべきファイル

| ファイル | 内容 |
|---|---|
| `internal/bot/bot.go` | `bot.Config`（`BaseAccuracy`/`BaseElapsedMs`/`AccuracyJitter`/`ElapsedJitterMs`） |
| `docs/plan/plan-24_BOT学習ループ.md` | §3 の集計クエリ・生成物の設計 |
| `plan-h03`（本ディレクトリ） | `order_attempt` のスキーマ |

### Bot の現状

Bot は**固定パラメータ**（`bot.Config`）で、1打鍵ではなく**1注文単位**のモデルで動く。したがって生成物は `msPerKey`/`missRate` のままではなく、**`accuracy` と `elapsedMs` に変換**して出す。

---

## 1. 集計（heatレベル別の分位）

```sql
SELECT
    heat_level,
    COUNT(*) AS n,
    percentile_cont(0.25) WITHIN GROUP (ORDER BY elapsed_ms::float / order_count) AS p25_ms_per_order,
    percentile_cont(0.50) WITHIN GROUP (ORDER BY elapsed_ms::float / order_count) AS p50_ms_per_order,
    percentile_cont(0.75) WITHIN GROUP (ORDER BY elapsed_ms::float / order_count) AS p75_ms_per_order,
    AVG(miss_count::float / NULLIF(keystrokes,0)) AS avg_miss_rate
FROM order_attempt
WHERE is_bot = FALSE
GROUP BY heat_level
ORDER BY heat_level;
```

> **属性の偏りに注意**（plan-24 §3）。`order_count`（語数）は属性で違う（Buzzは多め）。
> `elapsed_ms` をそのまま平均せず、**`order_count` で正規化**してから分位を取る（上のクエリは正規化済み）。ミス率は打鍵数基準。

---

## 2. 生成物 — `bot.Config` に写せる形

```
accuracy  = 1 - miss_count / keystrokes
elapsedMs = 1注文あたりの実測（order_count 正規化後、代表 order_count に戻す）
jitter    = 分位の幅（p75 - p25）から算出
```

> ⚠ **実装前に要確認（実 Bot モデルとの突き合わせ・実装時に確定）**: 現行 Bot（`internal/bot/bot.go` の `act()`）は
> - `elapsedMs` を **注文サイズに依らず固定**（`BaseElapsedMs` をそのまま。order_count/keystrokes で伸びない）
> - `missCount` を **注文ごとに 0 か 1 の二値**（`rng > BaseAccuracy` で miss=1）
>
> で生成する。つまり `BaseAccuracy` は「1注文をノーミスで捌く確率」であり、人間の per-keystroke 精度（`1 - miss/keystrokes`）とは意味が違う。
> また §1 の分位は本 plan では `elapsed_ms / order_count` で正規化しているが、**plan-24 §3 は `elapsed_ms / keystrokes` で正規化**しており基準が違う。
> どちらに合わせるか（＝Bot 側の生成モデルを人間に寄せて作り直すか、既存の粗いモデルに合わせて集計を丸めるか）は h05 の Bot 改修とセットで決める。**本 plan の式は暫定**。

3tier:

| tier | 元にする分位 |
|---|---|
| 弱BOT | p75（遅い・不正確側） |
| 中BOT | p50 |
| 強BOT | p25（速い・正確側） |

出力 `profiles.json`（plan-24 §3 と同形式）:

```json
{
  "generatedAt": "2026-08-13T12:00:00Z",
  "sampleSize": 4213,
  "profiles": {
    "weak":   { "0": {"baseAccuracy":0.78,"baseElapsedMs":4800,"accuracyJitter":0.10,"elapsedJitterMs":900}, "5": {...} },
    "normal": { "0": {"baseAccuracy":0.90,"baseElapsedMs":3200,"accuracyJitter":0.07,"elapsedJitterMs":600}, "5": {...} },
    "strong": { "0": {"baseAccuracy":0.97,"baseElapsedMs":2100,"accuracyJitter":0.03,"elapsedJitterMs":300}, "5": {...} }
  }
}
```

---

## 3. 生成方法（バッチ不要・手動）

データが増えるのはイベント時だけなので、常駐バッチは作らない。

```bash
go run ./cmd/botprofile --dsn "$DATABASE_URL" --out profiles.json
```

- `cmd/botprofile` を新規追加。DBに接続し §1 の集計 → §2 の変換 → JSON出力。
- サンプル数が少ない heat_level は**近傍で補間 or フォールバック値**を使う（データ0で NaN を出さない）。
- 分布が薄いうちは既定 `BotParams` を混ぜて安全側に倒す。

---

## 4. テスト

- 変換ロジック（分位 → accuracy/elapsedMs/jitter）の単体テスト。既知の入力で期待値。
- `order_count` 正規化が効いている（Buzz偏重データを入れても elapsed が偏らない）ことを**故意に偏らせた入力で検証**。
- サンプル0の heat_level で NaN/panic を出さずフォールバックする。
- 出力JSONが h05 が読める schema（tier × heat × bot.Config フィールド）になっている。

---

## 5. 完了条件

- [ ] `cmd/botprofile` が `order_attempt` から heat別分位を出せる
- [ ] 弱/中/強 3tier が生成される
- [ ] 出力が `bot.Config`（accuracy/elapsedMs/jitter）の形（msPerKey のままにしない）
- [ ] `order_count` の属性偏りを正規化してから集計している
- [ ] サンプル希薄な heat_level をフォールバック/補間する（NaN を出さない）
- [ ] `profiles.json` が h05 の投入 schema と一致する
- [ ] `go build` / `go vet` / `golangci-lint run` が通る
