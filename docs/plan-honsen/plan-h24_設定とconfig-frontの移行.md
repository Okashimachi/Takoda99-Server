# Plan-h24: `GameParameters` と config-front の移行

> **目的**: 本戦で必要な調整値（`score.*` / `cullSchedule`）を**運営がビルドなしで触れる**状態にし、廃止キーを UI から消す。サーバー・DB・config-front の3点を**同時に**揃える。
> **依存**: h21（`ScoreParams` 追加）・h22（`CullParams` 追加）でキーが確定してから
> **正典**: [11_差分_サーバー §6](../../../Takoda99-Docs/00_本選差分/11_差分_サーバー.md) / [10_差分_プロト §4](../../../Takoda99-Docs/00_本選差分/10_差分_プロト.md)
> **範囲**: `internal/game/params.go`（Validate）・`internal/db/config.go` ・ **takoda99-config**（別リポ）

---

## ▶ このファイル単体を実装プロンプトにする場合

1. **先に読む**: `AGENTS.md` → `docs/architecture.md` → `docs/plan-honsen/plan-h20` → **h21・h22**（`ScoreParams`/`CullParams` が前提）。config リポは `../takoda99-config/`（追加作業ディレクトリ）。
2. **前提チェック**（満たさなければ先に解消）:
   - `grep -rn "ScoreParams\|CullParams" internal/game/params.go` がヒット（**h21・h22 が入っている**）。無ければ先に。
3. **進め方**: **2リポ（Takoda99-Server と takoda99-config）を触る。** 各リポでブランチを切り、
   **両 PR を同じタイミングでマージ**する（§1・§5。片方だけ出すと config-front が使えなくなる）。
   config は Next.js（`npm run build` / `npm run lint` で検証）。
4. **完了の定義**: 末尾「§7 完了条件」を全て満たす。サーバー側は `go test`・`golangci-lint run` が緑、
   `Validate(DefaultParameters())` が nil。config 側は `verify-configapi.sh` が通る。
5. **PR 後**: Server の `gh pr checks <N>` で CI 実走・緑を確認。本番反映後 `curl /api/params | jq '.score, .cull'` で実測。

---

## 0. なぜやるのか（変更動機）

本戦で**触るべき数値が変わった**。予選で4番目だった「速さと正確さの力関係」が**1番目**に上がる。

| 予選の調整優先度 | 本戦 |
|---|---|
| 1. フェーズ配分 | 1. **`score.weightTakoyaki` と `score.weightMiss` の比率** — 本作の面白さの中心 |
| 2. 下位淘汰の周期・閾値 | 2. **中間ステージの `targetAliveCount`** — 脱落カーブの体感 |
| 3. 我慢ゲージ・信用 | 3. 火力（お題難度の上がり方） |
| 4. 評価係数 | 4. 分配 |

**触る係数が実質2〜3種類に絞られる**ので、残り期間で調整サイクルを速く回せる。

> 🔴 **廃止キーを UI に残さない。** 当日「効かない値」をいじって時間を溶かす事故が起きる。
> `initialLife` を DB で変えたのにコードの既定値を見ていた、という予選の混乱の再来を避ける。

---

## 1. ⚠ これは「もう一つのミラー問題」

`GameParameters` のスキーマは**2箇所に手書きで存在する**。proto の3言語ミラーと同じ構造の罠。

```
Takoda99-Server/internal/game/params.go   ← 正典（Go struct）
        │ 手で合わせる
        ▼
takoda99-config/lib/params.ts             ← 編集UIの型・既定値・UIスキーマ
```

**片方だけ変えると壊れる**:

| 症状 | 原因 |
|---|---|
| 保存すると 400 / Validate エラー | config が知らないキーを送る／必須キーを送らない |
| 触ったのに反映されない | config にキーが無く、サーバーが既定値を使い続ける |
| UI に出ているのに効かない | サーバー側で既に廃止されたキー |

> **本 plan は「Server と config を同じタイミングでマージする」ことを前提に書く。**
> どちらか片方だけ先に出すと、その間 config-front が使えなくなる。

---

## 2. サーバー側（`internal/game/params.go`）

h21・h22 でキー自体は追加済み。**本 plan では `Validate` と既定値の最終確認**を行う。

### 2.1 追加されるキー

```go
Score ScoreParams  // weightTakoyaki: 100 / weightMiss: 30（仮）
Cull  CullParams   // Stages [6]CullStage
```

既定値（企画で確定）:

| # | atMs | targetAliveCount |
|---|---|---|
| 1 | 20000 | 75 |
| 2 | 40000 | 55 |
| 3 | 60000 | 35 |
| 4 | 80000 | 20 |
| 5 | 100000 | 10 |
| 6 | 120000 | **0** |

### 2.2 削除されるキー

`Credit`（`initialLife` / `leaveLoss`）／ `Patience`（`lateMul` / `alertMs`）／
`AttributeSpec.PatienceBaseMs` ／ `Eval` の `emaAlpha` / `weightAccuracy` / `weightSpeed` /
`speedBaselineMs` / `speedCap` / `buzzBonus` / `buzzDecay` / `buzzCap` ／
`Storm`（`intervalTicks` / `warnTicks` / `thresholdPct`）

> ★**h21 で確定済み（PR #112）**：`Eval` グループは廃止し、`MinMsPerWord` は
> **`Sanity.MinMsPerWord`（JSON: `sanity.minMsPerWord`）** へ移した。
> 「評価」概念が消えた後に `eval` という箱が残ると誤解を招くため。
> **本番の `eval.minMsPerWord` は 200 = コード既定と同値**であることを実測で確認済みなので、
> このリネームで失われる調整値は無い。
>
> あわせて **`Distribution.WeightFloor` も h21 で削除済み**（分配の単純化で参照ゼロ＝「効かないツマミ」になったため）。

### 2.3 🔴 `Validate` で `[6]CullStage` のゼロ埋めを弾く（最重要・h22 §2.2 再掲）

`encoding/json` は配列に足りない要素をゼロ値で埋める。config から5要素で保存されると
`Stages[5] = {AtMs:0, TargetAliveCount:0}` ＝「**0秒で全店即死**」になる。

- 各 `AtMs > 0` / `AtMs` が厳密増加 / `TargetAliveCount` が単調非増加 / **最終のみ 0**

---

## 3. DB（`internal/db/config.go`）

既存の仕組みで概ね吸収できる。**新しいマイグレーション機構は作らない。**

| 挙動 | 効果 |
|---|---|
| `backfillDefaults`（`config.go:135`） | **新キー（`score` / `cull`）は内蔵デフォルトから自動で埋まる**。DB を手で触る必要なし |
| `Load` が struct へ unmarshal | **廃止キーが DB に残っていても無視される**（実害なし） |
| `Migrate` は空のときだけ seed | 運用中の値を壊さない |

> つまり **DB 側の作業は原則ゼロ**。ただし **`Load` の直後に `Validate` が走る**（`config.go:95`）ので、
> **本番 DB の既存レコードが新しい `Validate` を通るか**は要確認。
> 特に `cull.stages` が backfill で入った直後に Validate されるため、**既定値が Validate を通ることを
> 単体テストで固定**しておく（`Validate(DefaultParameters())` が nil）。

### 3.1 本番反映の確認手順

コードの既定値は本番に効かない（**DB 値が優先**）。デプロイ後に必ず実測する。

```
curl -s https://takoda99.mooo.com/api/params | jq '.score, .cull'
```

---

## 4. config-front（takoda99-config）

### 4.1 触るファイル

| ファイル | 内容 |
|---|---|
| `lib/params.ts` | `GameParameters` 型 / `defaultParameters` / **`schema: GroupSpec[]`（UI定義）** |
| `lib/validate.ts` | クライアント側の入力検証 |
| `app/page.tsx` | 画面。グループを並べて描画 |
| `components/NumberField.tsx` | 数値入力。**単一の数値しか扱えない** |

### 4.2 グループの増減

| グループ | 対応 |
|---|---|
| **`score`（新規）** | `weightTakoyaki` / `weightMiss` の2つ。**最優先で目立つ位置に置く**（調整の主役） |
| **`cull`（新規）** | 6ステージの表。§4.3 |
| `credit` / `patience` | **削除** |
| `storm` | **削除**（`cull` が置き換え） |
| **`sanity`（新規・旧 `eval` から改名）** | ★**h21 で確定**：`eval` グループは廃止し、残る `minMsPerWord` を **`sanity.minMsPerWord`** へ移した。JSON キーが変わるので UI も追従する |
| `distribution.weightFloor` | ★**h21 で削除**（分配の単純化で参照ゼロになったため）。`queueRefillThreshold` は残る |
| `customer` の `patienceBaseMs` 列 | **削除**（属性テーブルの列） |
| `matching` / `phase` / `heat` / `distribution` / `bot` / `session` | 変更なし |

### 4.3 `cullSchedule` の UI — ★時刻は編集させない

`NumberField` は単一値用なので、**6行×2列のテーブル UI が要る**（属性テーブルの実装が参考になる。
`GroupSpec.table` の仕組みが既にある）。

> 🔴 **`atMs` は読み取り専用にする。** 企画で「20秒等間隔・120秒・決勝10人」は**動かしてはいけない**と
> 確定している。編集可能にすると当日いじって試合が壊れる。**編集できるのは `targetAliveCount` だけ**。

| 列 | 編集 | 備考 |
|---|---|---|
| `atMs` | **不可**（表示のみ） | 20秒等間隔・固定 |
| `targetAliveCount` | 可（#2〜#4） | #1=75 / #5=10 / #6=0 は原則固定と注記 |

### 4.4 `validate.ts`

サーバーの `Validate` と**同じ条件**を実装する（保存前に弾けば往復が減る）。
特に **`targetAliveCount` の単調非増加**と**最終ステージ 0**。

> サーバーが最終防衛線。config 側は UX のための先出し検証であって、**サーバーの検証を省く理由にはしない**。

### 4.5 既存の運用資産

`verify-configapi.sh`（受入20項目）が予選で作られている。**廃止キーを参照している項目は更新が要る**。

---

## 5. 移行の順序（片方だけ出さない）

```
1. Server: params.go の増減 + Validate 更新（h21/h22 で実装済み）→ PR
2. config: lib/params.ts + validate.ts + UI → PR
3. 両方を同じタイミングでマージ
4. 本番デプロイ後、curl /api/params で score / cull が返ることを実測
```

> **2 を先に出さない。** サーバーが知らないキーを送ると Validate で弾かれる。

---

## 6. テスト

**Server**
- `Validate(DefaultParameters())` が nil（既定値が自分の検証を通る）
- `Validate` が **5要素のゼロ埋め**・非単調 `atMs`・最終以外の `target 0` を弾く。**条件を1つ緩める変異で落ちること**を確認
- `backfillDefaults` が `score` / `cull` の欠けたレコードを既定値で埋める
- 廃止キーが残った JSON を `Load` しても壊れない（無視される）
- `GameParameters` が `==` 比較可能なまま（`ConfigHash` の差分検出が壊れない）

**config-front**
- `defaultParameters` がサーバーの既定値と一致する
- `cull` の `atMs` が編集不可になっている
- `targetAliveCount` を単調でない値にすると保存前に弾かれる
- `verify-configapi.sh` が更新後のスキーマで通る

---

## 7. 完了条件

- [ ] サーバーの `Validate` が新キーを検証し、**ゼロ埋めを弾く**
- [ ] `Validate(DefaultParameters())` が nil
- [ ] 廃止キーがサーバーの `GameParameters` から消えている
- [ ] config-front に `score` グループがあり、**目立つ位置**にある
- [ ] config-front に `cull` の6ステージ表があり、**`atMs` が編集不可**
- [ ] config-front から廃止グループ（`credit` / `patience` / `storm`）が消えている
- [ ] `validate.ts` がサーバーと同じ条件で先出し検証する
- [ ] **Server と config の PR が同じタイミングでマージされている**
- [ ] 本番へ反映後、`curl /api/params | jq '.score, .cull'` で実値を確認した
- [ ] `verify-configapi.sh` が通る
