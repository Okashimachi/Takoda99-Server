# Plan-h21: スコア制への移行（game コア）

> **目的**: 順位を決める値を **評価（EMA×パーセンタイル正規化の相対値）→ スコア（累積の絶対値）** に置き換える。あわせて信用・我慢ゲージ・客属性の評価効果を止める。
> **依存**: h20-A（proto v0.8.0 化の3点セット）完了後
> **正典**: [11_差分_サーバー §3.1](../../../Takoda99-Docs/00_本選差分/11_差分_サーバー.md) / [01_本選企画書 3.2・3.8](../../../Takoda99-Docs/00_本選差分/01_本選企画書.md)
> **範囲**: `internal/game/session.go`（評価まわり）・`internal/game/params.go`。**配信は h23、足切りは h22**

---

## ▶ このファイル単体を実装プロンプトにする場合

1. **先に読む**: `AGENTS.md`（憲法）→ `docs/architecture.md` → `docs/plan-honsen/plan-h20`（移行マップ）。
2. **前提チェック**（満たさなければ先に解消）:
   - `grep Proto go.mod` → **v0.8.0**。未達なら **plan-h20 §2（3点セット）を先に実施**。
   - `grep -n "SA1019" .golangci.yml` がヒット（h20-A の抑止済み）。無ければ h20-A から。
   - このトラック最初の game 改修。上記2つ以外の前提は無い。
3. **進め方**: `main` へ直 push しない。`feat/h21-score` 等でブランチ。1 plan = 1 PR。
   **`internal/game/` にログ・DB・時計・スパインを持ち込まない**（純粋コア維持・AGENTS.md §1.4）。
   proto には触らない（v0.8.0 で確定済み）。
4. **完了の定義**: 末尾「§7 完了条件」を全て満たし、`go test -race ./internal/game/...` と `golangci-lint run` が緑。
5. **PR 後**: `gh pr checks <N>` で CI 実走・緑を確認。スタックして発火しなければ close→reopen。
   ⚠ 本 plan 単体では試合が終わらない（storm が旧仕様のまま）。**sim の失敗は許容し PR 本文に明記**（§7 末尾）。

---

## 0. 前提知識

### 読むべきファイル

| ファイル | 内容 |
|---|---|
| `internal/game/session.go` | `ApplyOrderServed`（212-273）／`stepEvaluate`／`stepNormalize`（611-）／`storeState` |
| `internal/game/params.go` | `EvalParams` / `CreditParams` / `PatienceParams` / `Validate` |
| `plan-h20` | 移行の順序と、廃止処理は**削除**する方針（§3） |

### 何が変わるか（1行で）

```
予選: perOrder = w_acc×精度 + w_spd×速度  →  evalRaw = EMA(perOrder) + バズ加点 − クレーマー減点
      順位 = 生存店内で evalRaw をパーセンタイル化

本戦: deltaScore = W_TAKOYAKI × orderCount − W_MISS × missCount
      score += deltaScore
      順位 = score の降順
```

**EMA・正規化・属性の加減点がすべて不要になる。加算とソートだけになる。**

---

## 1. スコアの算出（`ApplyOrderServed`）

現行の評価計算（`session.go:249-256` 付近）を置き換える。**サニティ検証は残す**。

```go
// 検証は現行のまま（行列先頭か・elapsed の下限クランプ・miss の [0,keys] クランプ）
// ↓ 評価EMA の代わりに
delta := s.params.Score.WeightTakoyaki*c.orderCount - s.params.Score.WeightMiss*miss
st.score += delta
```

| 論点 | 決定 |
|---|---|
| 型 | **int**（重みが整数なら誤差なく厳密。float にしない） |
| 下限クランプ | **しない**。負値を許容する（§1.1） |
| 速度の項 | **持たない**。速さは「時間内に何個作れたか」に自然に表れる |
| 属性の加減点 | **無し**。`buzzBonus` / クレーマー減点は削除 |
| `elapsedMs` | スコアに使わないが**サニティ検証には引き続き使う**（下限クランプを残す） |

### 1.1 なぜ 0 でクランプしないか

0 で止めると**下位が全員ぴったり 0 に密集**する。20秒地点で「下位24店を切る」とき、
0 の店が30店あると**どの24店を切るかが恣意的**になり、本来スコアで差がついている店が
人為的に同点にされてタイブレークで決着する。「速く正確に打った人が上に行く」という
本戦の原則に反する。

負値は「ミスが多かった」という正直な情報でもある。`W_TAKOYAKI=100 / W_MISS=30` なら
1語あたり3.3ミス超が必要で、実際に累積が負になるのは相当な事故。

> ⚠ **副作用として「何もしなかった店（0点）」が「挑戦して大量ミスした店（負）」より上に来る。**
> 構造的な問題ではなく重み次第なので、**h26 のシミュレーションで負値の発生率を観測項目に入れる**。

---

## 2. 順位の算出（`stepNormalize` の置き換え）

現行 `stepNormalize`（`session.go:611-`）は「生存店を evalScore で挿入ソート → パーセンタイル化 → rank」。
**正規化を捨て、スコア降順で rank を振るだけ**にする。関数名は `stepRank` 等に改めてよい。

```
1. 生存店を score の降順に並べる
2. rank = 1..n を振る
3. 各生存店へ EvaluationUpdate を返す（頻度は h23 で間引く。本 plan では現行どおり毎tickでよい）
```

- `evalNormalized` は**廃止**。ただし客分配が参照しているので §4 とセットで消す。
- `starRating` / `starDelta` は相対評価前提の表示だったので**送らない**（proto では定義が残る）。

### 2.1 タイブレーク（同スコア時の順序）★決定済み

**スコア昇順 → 正確性 → 速度 → storeId**（下位から切るときの順序）。

```
1. score が低い方が下位
2. 同値 → accuracy = 1 - misses/keystrokes が低い方が下位
3. 同値 → 平均所要 elapsedSum/count が大きい（遅い）方が下位
4. 同値 → storeId（決定性の最終担保）
```

> 🔴 **ゼロ除算に注意。** 20秒地点では「1件も提供していない店」が必ず出る。
> その店は `keystrokes=0` / `count=0` なので、`accuracy` も平均所要も**未定義**。
> **未提供店は accuracy=0 / 平均所要=+∞ とみなす**（＝最下位側）ようガードする。
> ここを忘れると初日に必ず落ちる。

> 🔴 **4段目の storeId を省略しない。** Go の map 反復順はランダムなので、完全同値が残ると
> `cmd/matchsim` がシード固定でも再現しなくなり、バランス調整とテストが信用できなくなる。
> 走査は `s.order`（スライス）を使えば決定的。

---

## 3. 廃止する処理（plan-h20 §3 のとおり削除）

| 対象 | 場所の目安 |
|---|---|
| `stepPatience`（我慢ゲージ減算・離脱・信用減・自滅） | tick ループから丸ごと |
| `processLeave` / `selfCollapse` / `resolveCollapses` | `session.go:528-596` |
| `storeState.creditLife` / `buzzBonus` / `evalRaw` / `evalNormalized` | `storeState` |
| `customer.patienceMaxMs` / `patienceLeftMs` | 客レジストリ |
| `stepEvaluate`（バズ加点の時間減衰） | tick ループから |
| `CustomerLeft` / `CreditUpdate` の Outbound | 送出箇所 |

**tick ループは次の形になる**（順序の意味は AGENTS.md §2 のまま）:

```
1. stepPhase      … 変更なし
2. stepDistribute … §4
3. （stepPatience 削除）
4. stepScore?     … スコアは ApplyOrderServed で加算済みなので tick 側の処理は不要
5. stepRank       … スコア降順で rank（§2）
6. stepHeat       … 変更なし
7. stepCull       … h22
8. checkFinish    … h22
```

> `MatchStats.LeftCount` と `AttributeTally.Left` は**常に 0** になる。
> 集計フィールド自体は残す（リザルトの表示互換のため）。

---

## 4. 客分配を単純化する（★決定済み）

`stepDistribute`（`session.go:364-`）は分配重みに `evalNormalized` を使っているため、
**評価の廃止に伴い必然的に変更が要る**。

**既存コードの `allZero` 分岐（`session.go:418`）を常用する**。

```go
// 評価による重み付けを落とし、行列が短い店から埋める
weights[i] = 1.0 / float64(cd.queueLen+1)
```

| なぜこれでよいか | |
|---|---|
| 「お題が途切れない」保証 | 重みではなく **`QueueRefillThreshold`（既定5）** が担っている。閾値を下回った店だけを候補にして上限まで詰める構造 |
| 実績 | `allZero` 分岐は**試合開始直後（全店 evalNormalized=0）に毎回通っている**既存の経路。新規ロジックではない |
| 客の枯渇 | `Customer.Total` は既に **5000**、99店×閾値5=495 に対し十分な余裕（#101 で手当て済み） |

**評価が高い店ほど客が来る案（③）は採らない。** スコアが累積の絶対値になった今、
「スコアが高い→客が増える→さらに伸びる」の正のフィードバックが二重にかかり、序盤の小差が終盤に発散する。
**決勝20秒の逆転劇**（企画が最も見せたい20秒）が死に、遅れた側は理由も見えない。

> `restPool` / 客レジストリは**残す**。それが行列を埋める機構そのもの。消すのは重み付けだけ。
> Early でクレーマーを配らない現行のゲートは**残してよい**（見た目のペース配分として無害）。

---

## 5. `GameParameters` の増減

**本 plan ではキーの追加・削除と `Validate` の更新まで**を行う（config-front の UI は h24）。

### 5.1 追加

```go
type ScoreParams struct {
    WeightTakoyaki int `json:"weightTakoyaki"` // たこ焼き1個あたりの加点（仮 100）
    WeightMiss     int `json:"weightMiss"`     // ミス1打鍵あたりの減点（仮 30）
}
```

### 5.2 削除

`Credit` / `Patience` グループ全体、`Eval` の `EmaAlpha` / `WeightAccuracy` / `WeightSpeed` /
`SpeedBaselineMs` / `SpeedCap` / `BuzzBonus` / `BuzzDecay` / `BuzzCap`、
`AttributeSpec.PatienceBaseMs`。

> `Eval.MinMsPerWord` は**残す**（サニティ検証の下限クランプに使う）。
> `Eval` グループごと消すなら `Sanity` 等へ移す。

### 5.3 `Validate` の更新

- 削除したキーの検証（`credit.initialLife` / `eval.emaAlpha` / `patienceBaseMs`）を除去
- 追加：`score.weightTakoyaki > 0` / `score.weightMiss >= 0`

> ⚠ **`GameParameters` の全フィールドは `==` 比較可能に保つ**（AGENTS.md §1.3）。
> `ScoreParams` は int 2つなので問題なし。

---

## 6. テスト（バグを注入して落ちることを確認する方式）

- `ApplyOrderServed` を1回呼ぶと `score` が `W_T×orderCount − W_M×miss` ぶん増える。
  **重みを取り違える変異でテストが落ちること**を確認。
- ミス過多で `deltaScore` が負になり、`score` が負まで下がる（クランプされていない）。
- `stepRank` がスコア降順に rank を振る。同スコアで §2.1 のタイブレーク順になる。
- **未提供店（keystrokes=0）を含めてもゼロ除算・panic しない**、かつ決定的な順序になる。
- 分配が `evalNormalized` を参照せず、行列が短い店から埋まる。全店の行列が閾値まで満たされる。
- 我慢ゲージ・信用・離脱が発生しない（`CustomerLeft` / `CreditUpdate` が Outbound に現れない）。
- `game` が `slog`・DB・スパインを import していない（depguard）。

---

## 7. 完了条件

- [ ] `storeState.score`（int）が `ApplyOrderServed` で加算される
- [ ] スコアは**クランプされない**（負値を許容）
- [ ] `stepRank` がスコア降順で rank を振る（パーセンタイル正規化を廃止）
- [ ] タイブレークが score→accuracy→速度→storeId で**決定的**、未提供店でゼロ除算しない
- [ ] 我慢ゲージ・信用・自滅・属性加減点・EMA・バズ減衰の処理が**コードから消えている**
- [ ] 客分配が行列長のみの重みで動き、お題が途切れない
- [ ] `GameParameters` に `Score` が追加され、廃止キーが消え、`Validate` が更新されている
- [ ] `GameParameters` が `==` 比較可能なまま
- [ ] `go build` / `go vet` / `go test -race ./internal/game/...` / `golangci-lint run` が緑

> h22（cullSchedule）が入るまでは storm が旧仕様のまま残る。**本 plan 単独では試合が終わらない可能性がある**ので、
> `internal/sim` の決着保証テストは h22 とセットで緑にする。本 plan の PR では sim の失敗を許容してよいが、
> **その旨を PR 本文に明記する**こと。
