# Plan-h05: BOT tier制の反映（bot_profile → 挙動）

> **目的**: h04 が生成したプロファイルを **`bot_profile` テーブル経由**でBotに注入し、1試合のBot99体を弱/中/強の分布で編成する。ハードコードせず、DBが無ければ現行 `BotParams` にフォールバックする。
> **対応issue**: #15（BOT学習ループ）
> **依存**: h04（`profiles.json` / 生成ロジック）
> **参照**: `docs/plan/plan-24_BOT学習ループ.md` §4（本planは Phase4 の本戦版）, `internal/bot/bot.go`, `internal/app/app.go`（`NewBotPlayer`）

---

## 0. 前提知識

### 読むべきファイル

| ファイル | 内容 |
|---|---|
| `internal/bot/bot.go` | `bot.Config`、`bot.New`。挙動の実装 |
| `internal/app/app.go` | `NewBotPlayer`（Bot生成・IsBot付与） |
| `internal/matchmaking/matchmaking.go` | Bot補完（定員不足を埋める箇所） |
| `docs/plan/plan-24_BOT学習ループ.md` | §4 の設計（bot_profile / 難易度帯の割り当て） |

---

## 1. 反映方式（ハードコードしない）

AGENTS.md §1.3「調整値をハードコードしない」に従い、生成プロファイルは **config 経由**で注入する。
`GameParameters` に heat別テーブルを足すと肥大化＋`==`比較制約に触れるため、**専用テーブル `bot_profile`** を使う（plan-24 §4 推奨）。

```sql
CREATE TABLE IF NOT EXISTS bot_profile (
    tier              TEXT  NOT NULL,   -- weak/normal/strong
    heat_level        INT   NOT NULL,
    base_accuracy     FLOAT NOT NULL,
    base_elapsed_ms   INT   NOT NULL,
    accuracy_jitter   FLOAT NOT NULL,
    elapsed_jitter_ms INT   NOT NULL,
    PRIMARY KEY (tier, heat_level)
);
```

- カラム名を `bot.Config` のフィールドと**1対1**にして、写す処理を素直にする。
- `cmd/botprofile` に `--apply`（or 別 `cmd/botprofile-apply`）を足し、`profiles.json` を `bot_profile` に upsert する。

---

## 2. 注入（DIP）

`internal/bot` は `BotProfileSource` interface で受け取る（実体は DB / JSON / フォールバック）。

```go
// bot が要求する継ぎ目
type BotProfileSource interface {
    // tier と heatLevel から Config を返す。無ければ ok=false
    Lookup(tier string, heatLevel int) (Config, bool)
}
```

- DB実装（`internal/db`）が `bot_profile` を読む。
- **DBが無ければ現行 `BotParams`（固定）にフォールバック**（オフライン開発・当日DB不通でもBotが動く）。
- Bot は試合中 heat が上がったら、自 tier の該当 heat の Config に切り替える（難度追従）。

> `internal/bot` は層3部品。game コアは無関係（Botは `OrderServed` を内部生成する外部入力に過ぎない）。

---

## 3. tier配分（1試合のBot99体をどう割るか）

人間の分布に寄せる既定（plan-24 §4）:

```
weak 25% / normal 50% / strong 25%
```

- この配分は `GameParameters` で調整可能にする（固定フィールドの struct、`==` 比較を壊さない）。
- 割り当ては `matchmaking` の Bot補完時 or `app.NewBotPlayer` で tier を決めて `Config` を引く。

---

## 4. A/B観測（最終ゴールの検証）

- `match.config_hash` と人間の `final_rank` 分布で、旧Bot（固定）と新Bot（tier制）を比較。
- **新Botの方が「人間が真ん中あたりに来る」**なら成功（弱すぎ/強すぎを是正できている）。
- この観測は h01/h02 のダッシュボード＋ `store_result`（PR #104）で回せる。

---

## 5. テスト

- `Lookup` がDB/JSONの値を返す。無い tier/heat で ok=false → フォールバックする。**フォールバック分岐を故意に壊してテストが落ちること**を確認。
- heat 上昇でBotの Config が切り替わる。
- tier配分が config どおり（99体の内訳が 25/50/25 に近い、乱数シードで再現）。
- DB無しでBotが現行 `BotParams` で動く（既存 solo/sim 非破壊）。
- `GameParameters` に map/slice を足していない（`==` 比較維持）。

---

## 6. 完了条件

- [ ] `bot_profile` テーブルが Migrate で作られ、`profiles.json` を upsert できる
- [ ] `BotProfileSource`（DIP）でBotに注入され、ハードコードしていない
- [ ] DBが無い時は現行 `BotParams` にフォールバックする
- [ ] heat上昇でBotが該当 Config に追従する
- [ ] tier配分（weak/normal/strong）が `GameParameters` で調整でき、`==` 比較を壊さない
- [ ] A/Bで人間の順位分布の変化を観測した
- [ ] `go build` / `go vet` / `golangci-lint run` が通り、game テストがグリーン
