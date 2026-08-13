# Plan-h22: 時刻足切り（cullSchedule）と決着

> **目的**: 脱落を **storm の tick 周期・下位% → 20秒等間隔の時刻スケジュール・目標生存数** に置き換え、**120秒で全店同時脱落**して試合を終える形にする。
> **依存**: h21（スコアが無いと切る順序が決まらない）
> **正典**: [01_本選企画書 3.6・3.7](../../../Takoda99-Docs/00_本選差分/01_本選企画書.md) / [11_差分_サーバー §3.3・§5](../../../Takoda99-Docs/00_本選差分/11_差分_サーバー.md)
> **範囲**: `internal/game/session.go`（storm・checkFinish）・`internal/game/params.go`・`internal/sim/`

---

## ▶ このファイル単体を実装プロンプトにする場合

1. **先に読む**: `AGENTS.md`（憲法）→ `docs/architecture.md` → `docs/plan-honsen/plan-h20` → **h21**（score が前提）。
2. **前提チェック**（満たさなければ先に解消）:
   - `grep Proto go.mod` → **v0.8.0**。
   - `grep -rn "\.score" internal/game/session.go` がヒット（**h21 が入っている**）。無ければ h21 を先に。
3. **進め方**: `main` へ直 push しない。`feat/h22-cull` 等。`internal/game/` の純粋性を保つ（§1.4）。
   🔴 **§0 の AGENTS.md 更新は憲法の変更**。**実装者が独断で書き換えない。変更案を人間（りーせ）に提示して
   承認を得てから**（AGENTS.md §7）。承認前は実装を止める。
4. **完了の定義**: 末尾「§7 完了条件」を全て満たし、`go test -race ./internal/game/...`・`go test ./internal/sim/`・
   `golangci-lint run` が緑。h21+h22 が揃って初めて **sim が緑になる**（本 plan で決着保証を回復させる）。
5. **PR 後**: `gh pr checks <N>` で CI 実走・緑を確認。スタック時は close→reopen。

---

## 0. 前提知識

| ファイル | 内容 |
|---|---|
| `internal/game/session.go` | `stepStorm`（688-）／`cullCandidates`（737）／`cullTargets`（763）／`executeCull`（772-）／`checkFinish`（855-） |
| `internal/game/params.go` | `StormParams`（削除対象）／`Validate` |
| `internal/sim/` | 決着保証テスト（**本 plan で前提が変わる**） |
| `plan-h21` | スコアとタイブレーク（切る順序に使う） |

### 🔴 憲法（AGENTS.md）との衝突を先に解消する

`AGENTS.md` は現在こう書いている。**本戦仕様と正面から矛盾する**ため、**本 plan の実装前に AGENTS.md を更新する**。

| 現在の記述 | 本戦での実際 |
|---|---|
| 「**制限時間はない**」「決着は storm が保証する」（§1 骨子） | **120秒デッドラインで確定** |
| 「❌ 試合に制限時間を復活させる（**廃止済み**）」（§8 チェックリスト L296） | cullSchedule の最終ステージが実質の制限時間 |
| 骨子の「我慢ゲージ／信用／自滅脱落」 | **全廃**（h21） |
| tick 順序の `stepPatience` / `stepNormalize` / `stepStorm` | 差し替え |

> これは「憲法を破る」のではなく「**企画が変わったので憲法を更新する**」作業。
> 更新せずに実装すると、次に AGENTS.md を読んだ人（＝AI含む）が正しい実装を巻き戻す。

---

## 1. 足切りスケジュール（確定値）

```
cullSchedule（20秒等間隔 × 6ステージ）
   20秒 → 生存 75    (24%カット)   ← 全員が最低20秒は遊べる
   40秒 → 生存 55    (27%)
   60秒 → 生存 35    (36%)
   80秒 → 生存 20    (43%)
  100秒 → 生存 10    (50%)         ← 決勝進出ライン
  120秒 → 生存  0    (100%)        ← 試合終了（全店同時脱落）
```

| 動かしてよい | 動かしてはいけない |
|---|---|
| 中間ステージ #2〜#4 の `targetAliveCount` | 20秒等間隔 |
| 初回の 24% | 120秒（＝ゲーム時間）／ #5 の 10人（＝決勝の人数）／ #1 が20秒より早くならないこと |

**なぜ下位%でなく目標生存数か**: %指定は現在の生存数に依存して結果が揺れる。目標生存数なら
**脱落カーブを直接設計できる**し、Bot の強さのばらつきが脱落人数に波及しない。
調整変数が `targetAliveCount` の1本に絞られ、シミュレーションの試行が速く回る。

---

## 2. `GameParameters` への追加 — `[6]CullStage`

### 2.1 型（`==` 比較可能を維持する）

**Go では配列は要素が comparable なら `==` 可能**（スライスと違う）。AGENTS.md §1.3 の
「map / slice をフィールドに入れない」制約を満たす。

```go
type CullStage struct {
    AtMs             int `json:"atMs"`
    TargetAliveCount int `json:"targetAliveCount"`
}
type CullParams struct {
    Stages [6]CullStage `json:"stages"` // 段階数は企画で確定（20秒等間隔×6）
}
```

> ⚠ ワイヤ（proto `GameParametersPublicSubset.CullSchedule`）は**スライス**。
> `==` の制約は**サーバー内部の `GameParameters` にだけ**かかる。公開時に配列→スライスへ変換する。

### 2.2 🔴 ゼロ埋めの罠を `Validate` で塞ぐ（最重要）

`encoding/json` は**配列に要素数が足りない JSON を渡されると残りをゼロ値で埋める**。
config-front から5要素で保存されると `Stages[5] = {AtMs:0, TargetAliveCount:0}` になり、
これは「**0秒時点で生存0＝開始直後に全店即死**」を意味する。当日これが起きたら試合が成立しない。

`Validate` で必ず弾く:

- 各 `AtMs > 0`
- `AtMs` が**厳密に増加**（＝ゼロ埋めを検出できる）
- `TargetAliveCount` が**単調非増加**
- **最終ステージのみ `TargetAliveCount == 0`**、それ以外は `> 0`

### 2.3 削除

`StormParams`（`IntervalTicks` / `WarnTicks` / `ThresholdPct`）を丸ごと削除。

### 2.4 🔴 `internal/admin` を壊さない（h21 §3.1 の続き）

storm を消すと `session.go` の `StormState()` と `internal/admin/snapshot.go` の `AdminStorm`
（`Warning`/`UntilTick`/`ThresholdPct`）がコンパイルできず `go build ./...` が落ちる。

- `StormState()` を**削除 or `CullState()` に置き換え**（次ステージの `UntilMs`/`StageIndex`/`TargetAlive`/`CutLineRank` を返す）
- `AdminStorm` を **`AdminCull` へ差し替え**（h25 の `AdminCull` を前倒しで最小実装）
- `webdist/app.js` は本 plan では触らなくてよい（JS はビルドを止めない。正式な足切り可視化は h25）

> h21 で `Score` 化、h22 で `Cull` 化まで済ませれば、**h25 は「スコア分布ビュー・`IsBot`・演出」という
> 純粋な機能追加だけ**になり、コンパイル都合の巻き込みが無くなる。

---

## 3. 足切りの実行（`stepStorm` → `stepCull`）

```
経過時間 elapsedMs が次のステージの AtMs に到達したら:
  1. 切る数 = aliveCount - targetAliveCount
  2. aliveCount <= targetAliveCount ならスキップ（切る数が負になる事故の保険）
  3. スコア昇順（h21 §2.1 のタイブレーク）で下位から「切る数」だけ脱落確定
  4. finalRank を振る（同一ステージ内はスコアの昇順で下から積む）
  5. 次のステージ index へ進める
```

- **予告と実行で同じ対象選定関数を使う**（現行 `cullCandidates` が `executeCull` と `cullTargets` で
  共有されているのと同じ思想）。ここがズレると「予告が嘘になる」。
- `matchState` に**次のステージ番号**を持たせる（`session` の横断状態）。

### 3.1 予告の常時配信

予選は `stormWarnTicks` 前だけ配信していたが、**本戦の右パネルは常設UI**なので**常に**
「次の足切りまであと何秒」「誰が切られるか」が届いている必要がある。

配信するデータは `ForcedEliminationWarning`（proto v0.8.0）:

| フィールド | 値 |
|---|---|
| `UntilMs` | 次ステージの `AtMs` − `elapsedMs` |
| `StageIndex` / `StageTotal` | 現在向かっている段階（1始まり）／ 6 |
| `CutLineRank` | `targetAliveCount + 1`（**最終ステージのみ例外 → §3.2**） |
| `CutStoreIds` | 現時点で切られる予定の店（**上限10件**・暫定） |
| `SelfAtRisk` | 自店が対象圏内か（クライアントに比較させない原則） |

> **配信の頻度と経路は h23。** 本 plan は「値を作る」ところまで。

### 3.2 ★最終ステージの `CutLineRank` は **2** を送る

企画書 3.7 は2つのことを同時に要求している。**層を分けて両立させる**。

| 層 | 挙動 |
|---|---|
| **処理層** | `targetAliveCount: 0` のまま。120秒で**1位を含む10店全員を脱落**させる |
| **表示層** | `CutLineRank = 2` を送り、右パネルは「**1位以外が脱落対象**」と見せる（決勝の緊張を最大化） |

`ForcedEliminationWarning` は表示のための情報であり、淘汰処理はこの値を使わない。

> ★**実装で確定（PR #114）**：`CutLineRank` だけでなく **`SelfAtRisk` と `CutStoreIds` からも1位を外す**。
> この3つは「今から誰が切られるか」という**同じ表示概念を別の粒度で表しているだけ**なので、
> `CutLineRank` だけ 2 にすると、同じ画面に「カットラインは2位から」と
> 「あなた（1位）は脱落圏内」が同時に出て自己矛盾する。
>
> **三層を分けること**（これが要点）:
>
> | 層 | 最終ステージで1位は | 実装 |
> |---|---|---|
> | 処理（`executeCull`） | **落ちる** | 生の `cullCandidates` を使い、表示分岐を見ない |
> | プレイヤー向け表示（`ForcedEliminationWarning`） | 対象外に見せる | 候補から最強1件を外す |
> | **観測（`StoreBoard` → ダッシュボード）** | **AtRisk = true（真実）** | `cullTargetIds()` は生の候補を使う |
>
> 🔴 **観測は演出に寄せない。** ダッシュボードは挙動を検証する道具なので、真実を映す必要がある。
> **h25 でもこの三分割を維持すること。**

---

## 4. 決着（`checkFinish` の置き換え）

### 4.1 「生存1店で終了」をやめる

| 項目 | 予選 | 本戦 |
|---|---|---|
| 終了条件 | 生存1店 or 制限時間 or 生存0 | **120秒到達のみ**（＝最終ステージの実行） |
| 1位の決まり方 | 最後まで生き残った店 | **120秒時点のスコア1位** |
| タイブレーク | 信用ライフ残 → 正規化評価 | **スコア**（→ 正確性 → 速度 → storeId） |
| 勝者の特別扱い | サーバーが持つ | **持たない。順位を返すだけ** |

**理由**（企画書 3.7）: 「9店を落として1店を残す」形にすると**残った1店だけが試合に取り残される**状態が
生まれ得る（予選の開発で実際に発生）。全店が同じタイミングで同じ状態に入れば、その特殊ケースが消える。

### 4.2 120秒で起きること

```
最終ステージ（120秒）:
  1. 残り10店を全員脱落させ、スコア降順で finalRank 1..10 を確定
  2. 各店へ PersonalResult（脱落と同時・全員が同じ経路）
  3. 全員へ MatchEnd（空）
```

> **優勝者の識別子は `StoreEliminated`（finalRank=1）が全員へブロードキャストされることで届く。**
> `MatchEnd` は空のままでよい（plan-h10 §1.6）。配信の順序保証は h23。

### 4.3 `PersonalResult` の中身

v0.8.0 で `Score` / `TakoyakiCount` が増えた。`TakoyakiCount` は**累計 orderCount**
（`Stats.ServedCount` は「提供した**客**の数」であって、たこ焼きの数ではない）。
**総ミス数は `Stats.TotalMisses`**（`PersonalResult` に重複させない）。

---

## 5. `internal/sim`（決着保証テスト）の前提更新

**決着保証の意味が変わる。** 予選は「storm が効いて必ず終わるか」を検証していたが、
本戦は**時刻で必ず終わる**ので自明になる。代わりに検証すべきは:

| 観点 | 内容 |
|---|---|
| 決着 | 120秒で**必ず**全店が脱落し、`finalRank` が 1..99 に**重複なく**割り当たる |
| 生存カーブ | 各ステージ後の生存数が `targetAliveCount` と**一致**する |
| 早期脱落 | **20秒より前に誰も脱落しない**（企画の C4「どれだけ弱くても20秒は遊べる」） |
| 決定性 | 同じシードで**同じ結果**（タイブレークの storeId 段が効いているか） |

> 旧「膠着0」テストは**役目を終える**。取り消し線で残して訂正を併記する（AGENTS.md §7 の作法）。

---

## 6. テスト（バグを注入して落ちることを確認する方式）

- ステージ時刻に到達すると、生存数がちょうど `targetAliveCount` になる。**切る数の計算を1ずらす変異で落ちること**を確認。
- スコア下位から切られる（上位が切られない）。同スコアはタイブレーク順。
- `aliveCount <= targetAliveCount` のステージがスキップされる（切る数が負にならない）。
- 120秒で**全店**が脱落し、生存0で終了する。「生存1店」の状態が発生しない。
- `finalRank` が 1..99 で重複しない。同一ステージ内はスコア昇順で下から積まれている。
- 予告の `UntilMs` が次ステージまでの残り時間と一致し、**最終ステージだけ `CutLineRank == 2`**。
- 予告の対象（`CutStoreIds`）と実際に切られた店が**一致する**（予告が嘘にならない）。
- `Validate` が **5要素のゼロ埋め**を弾く（`AtMs=0` のステージ／単調でない `AtMs`／最終以外の target 0）。
- `internal/sim` が新しい観点（§5）で緑。

---

## 7. 完了条件

- [ ] **`AGENTS.md` が本戦仕様へ更新されている**（制限時間の禁止・骨子・tick 順序・チェックリスト）
- [ ] `CullParams.Stages [6]CullStage` が追加され、`GameParameters` が `==` 比較可能なまま
- [ ] `Validate` が**ゼロ埋め・非単調・最終ステージ以外の target 0** を弾く
- [ ] `StormParams` が削除されている
- [ ] `stepCull` が時刻で発火し、目標生存数まで**スコア下位から**切る
- [ ] 予告が**常時**生成され、予告対象と実際の脱落が一致する
- [ ] 最終ステージの `CutLineRank` が **2**（処理は全店脱落のまま）
- [ ] 120秒で全店脱落して終了し、「生存1店」が発生しない
- [ ] `finalRank` が 1..99 で重複しない
- [ ] `internal/sim` が新しい決着保証で緑
- [ ] `go build` / `go vet` / `go test -race ./internal/game/...` / `golangci-lint run` が緑
