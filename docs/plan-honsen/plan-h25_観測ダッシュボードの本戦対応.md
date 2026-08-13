# Plan-h25: 観測ダッシュボードの本戦対応（AdminSnapshot v2）

> **目的**: h02 で作った観測ダッシュボードを本戦ルールへ更新する。体力・我慢ゲージの監視をやめ、**スコア分布と足切りの観測**に作り替える。
> **依存**: h21〜h23（score / cullSchedule / 配信が揃ってから）
> **前提**: h01（#106）・h02（#107）はマージ済み。**配管（AdminHub / `/admin` / `/admin/ws`）は無傷で流用できる**
> **正典**: [11_差分_サーバー §7](../../../Takoda99-Docs/00_本選差分/11_差分_サーバー.md)（観測項目）／ `plan-h00`（配線）／ `plan-h02`（現行の AdminSnapshot）
> **範囲**: `internal/admin/`（Server）・ **Takoda99-DashBoard**（別リポ・正典）

---

## ▶ このファイル単体を実装プロンプトにする場合

1. **先に読む**: `AGENTS.md` → `docs/architecture.md` → `docs/plan-honsen/plan-h20` → **plan-h00**（AdminHub 配線）→ **plan-h02**（現行の AdminSnapshot）→ **h23**（配信）。DashBoard リポは `../Takoda99-DashBoard/`。
2. **前提チェック**（満たさなければ先に解消）:
   - `grep -rn "\.score" internal/admin/snapshot.go` or `grep -rn "RankingSnapshot" internal/room/` がヒット（**h21〜h23 が入っている**）。無ければ先に。
   - `ls internal/admin/snapshot.go`（h02 実装済み。これを v2 化する）。
3. **進め方**: **2リポ（Takoda99-Server と Takoda99-DashBoard）を触る。** DashBoard が正典、Server の
   `internal/admin/webdist/` は同梱コピー。**「DashBoard を直す→webdist へ再sync」を1作業単位**にし、
   **両 PR を同じタイミングでマージ**（§3。h02 で実際にズレた）。`internal/game/` は無改造（getter で読むだけ）。
4. **完了の定義**: 末尾「§5 完了条件」を全て満たす。`go test`・`golangci-lint run` が緑、
   DashBoard `app.js` に廃止フィールド参照が **0 件**（`grep -c "creditLife\|evalNormalized" app.js`）。
5. **PR 後**: Server の `gh pr checks <N>` で CI 実走・緑を確認（checks 0 件のままマージしない。スタック時は close→reopen）。

---

## 0. なぜ変えるのか — **観測の目的そのものが移る**

| | 予選〜h02 | 本戦 |
|---|---|---|
| 主目的 | 当日のトラブル切り分け | **残り期間でバランスを詰めるための計測器** |
| 見るもの | 体力・我慢ゲージ・行列の詰まり | **スコア分布・足切りの妥当性** |

[11_差分_サーバー §7](../../../Takoda99-Docs/00_本選差分/11_差分_サーバー.md) が挙げる観測項目が、そのまま本 plan の要件になる。

| 保留事項 | ダッシュボードで見たいもの |
|---|---|
| **P1** ボット強度が一定でない | ボットが上位を占めていないか（人間/Bot の別が見えること） |
| **P2** お題の出題ランダム性 | 同実力店の順位ブレ |
| **P3** スコアの跳ね上がり | **序盤のスコア差の開き方**（累積方式で跳ねは構造的に減るはず） |
| **P4** 足切りスケジュール | **各ステージの淘汰人数と生存店の実力相関**、実力上位が早期に切られる事故率 |

> **h26（バランス検証）の道具になる。** 見えないものは調整できない。

---

## 1. `AdminSnapshot` の更新（v2）

現行（h02・`internal/admin/snapshot.go`）から**フィールドを差し替える**。配管は変えない。

> **前倒し済みの前提**: `CreditLife`/`EvalNormalized` → `Score` の差し替えは **h21 §3.1** で、
> `AdminStorm` → `AdminCull` の差し替えは **h22 §2.4** で、**コンパイルを緑に保つために既に済んでいる**。
> 本 plan で新規に行うのは下記のうち **`IsBot` / `TakoyakiCount` / `MissCount` の追加**と、
> **ダッシュボード表示（スコア分布ビュー・Bot色分け・足切り履歴）**。フィールドの差し替え自体は再確認だけでよい。

### 1.1 消えるもの

| フィールド | 理由 |
|---|---|
| `AdminStore.CreditLife` | 信用制の廃止 |
| `AdminStore.EvalNormalized` | 相対評価の廃止 → `Score` |
| `AdminStorm{Warning, UntilTick, ThresholdPct}` | storm の廃止 → `AdminCull` |

### 1.2 入るもの

```go
type AdminStore struct {
    // ... StoreId / DisplayName / Alive / Rank / FinalRank / QueueLen / ServedCount / QueueByAttr は継続
    Score         int  `json:"score"`         // 順位を決める値（負値あり）
    TakoyakiCount int  `json:"takoyakiCount"` // 累計 orderCount
    MissCount     int  `json:"missCount"`     // 累計ミス（スコアの内訳を見るため）
    IsBot         bool `json:"isBot"`         // ★P1 の観測に必須
    AtRisk        bool `json:"atRisk"`        // 次の足切りの対象圏
}

type AdminCull struct {
    StageIndex  int `json:"stageIndex"`  // 現在向かっている段階（1..6）
    StageTotal  int `json:"stageTotal"`  // 6
    UntilMs     int `json:"untilMs"`     // 次の足切りまで
    TargetAlive int `json:"targetAlive"` // このステージ後の目標生存数
    CutLineRank int `json:"cutLineRank"` // 境界順位
}
```

> 🔴 **`IsBot` は新規に必要**。P1（ボットが上位を占めていないか）は Bot と人間が区別できないと観測できない。
> `game` は Bot/人間を区別しない（AGENTS.md §4.2）ので、**`app.RunMatch` が持つ `botIds` を
> AdminSnapshot 生成側へ渡す**必要がある。`store.Result.IsBot` と同じ流儀。

### 1.3 客フロー（h02 で入れた `RestPool` / `RestByAttr` / `QueueByAttr`）の扱い

**残す。ただし意味が変わる**ことを注記する。

- 離脱が無くなったので「行列が詰まって取りこぼす」現象は起きない。
- 分配は h21 §4 で単純化され、**行列長のみの重み**になった。
- したがって行列の観測価値は下がるが、**「お題が途切れていないか」の確認には引き続き有効**
  （企画側の要件は「お題が途切れないこと」と「行列が賑やかに見えること」の2点）。

---

## 2. ダッシュボードの表示

### 2.1 上部バー

`Phase` / `HeatLevel` / `AliveCount` / `elapsedMs` に加えて:

- **`StageIndex/StageTotal` と `UntilMs`**（次の足切りまでの秒読み）
- **`TargetAlive` と `CutLineRank`**（何人まで減るか・どこが境界か）

### 2.2 99店グリッド

- セルの主表示を **`Score` と `Rank`** にする（`CreditLife` の表示を削除）
- **`AtRisk` の店を強調**（次に切られる店が一目で分かる）
- **Bot と人間を視覚的に区別**（P1 の観測。色や枠で十分）

### 2.3 ★スコア分布ビュー（新規・本 plan の主目的）

「上位と下位が分離しているか」を見るための表示。**これが無いと h26 が回らない。**

| 表示 | 見たいこと |
|---|---|
| スコアの分布（ヒストグラム or 縦棒を順位順に並べる） | 上位と下位が**分離しているか**、団子になっていないか |
| **カットラインの位置**を分布上に重ねる | 誰が切られるかが分布のどこか |
| Bot と人間の色分け | **P1**: ボットが上位を占めていないか |

> 凝ったグラフは不要。**順位順に並べた縦棒＋カットラインの横線**で十分に読める。

### 2.4 足切りの履歴

各ステージで「何人切られたか」「切られた店の人間/Bot 内訳」を残す。**P4** の観測に使う。

---

## 3. 🔴 リポ間の同期（h02 で実際にズレた）

ダッシュボードの実体は **Takoda99-DashBoard が正典**、Server は `internal/admin/webdist/` に**同梱コピー**を持つ。

```
Takoda99-DashBoard（ここで編集）→ コピー → Takoda99-Server/internal/admin/webdist/
```

**h02 では DashBoard が先にマージされ、Server 側だけ未マージという非対称が起きた**（PR #107 が
CI 未実行のまま滞留）。

**本 plan では次を守る**:

1. DashBoard を直す → **同じ作業単位で Server の webdist へ再sync**
2. **両方の PR を同じタイミングでマージする**
3. Server 側の PR は**必ず CI を通してからマージ**
   （スタックした PR は base が main でないと CI が発火しない。close→reopen で再発火できる）

---

## 4. テスト

**Server**
- `AdminSnapshot` が `Score` / `IsBot` / `AdminCull` を含み、廃止フィールドを含まない
- `IsBot` が `botIds` と一致する（Bot を人間として出さない）
- `AtRisk` が `cutLineRank` と整合（**h22 の予告対象と同じ集合**であること）
- `AdminCull.UntilMs` が次ステージまでの残り時間と一致
- `game` が hub / slog を import していない（depguard）

**DashBoard**
- スナップショットを流し込んでスコア分布とカットラインが描ける
- 廃止フィールド（`creditLife` / `evalNormalized`）を参照していない（grep で 0 件）
- 試合が走っていないときに「試合なし」を表示する（h02 の N7）

---

## 5. 完了条件

- [ ] `AdminSnapshot` から `CreditLife` / `EvalNormalized` / `AdminStorm` が消えている
- [ ] `Score` / `TakoyakiCount` / `MissCount` / `IsBot` / `AdminCull` が入っている
- [ ] ダッシュボードのセルがスコアと順位を主表示にし、`AtRisk` を強調する
- [ ] **スコア分布ビュー**があり、カットラインと Bot/人間の別が読める
- [ ] 足切りの履歴（各ステージの淘汰人数と内訳）が見える
- [ ] DashBoard `app.js` に `creditLife` / `evalNormalized` の参照が**0件**
- [ ] **DashBoard と Server webdist が同期し、両PRが同じタイミングでマージされている**
- [ ] Server の PR で **CI が実際に走って緑**（checks が0件のままマージしない）
- [ ] `go build` / `go vet` / `go test` / `golangci-lint run` が緑
