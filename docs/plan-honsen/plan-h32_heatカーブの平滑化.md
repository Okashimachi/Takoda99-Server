# Plan-h32: heat カーブの平滑化（段差と +8 ジャンプの解消）

> **目的**: 難度 `heatLevel` が**階段状**に上がり、しかも Late 突入で**一気に +8 跳ぶ**。
> さらに本番値では**上限17に一度も届かない**（最上位の語彙が死んでいる）。カーブを連続にし、上端まで使い切る。
> **依存**: なし（h30・h31 と独立・並行可）
> **正典**: `plan-h26` §1.2（同じ問題を Late=9 で一度直している）・issue #75
> **範囲**: `internal/game/session.go`（`stepHeat`）・`internal/game/params.go`（`HeatParams`）・config-front の値

---

## ▶ このファイル単体を実装プロンプトにする場合

1. **先に読む**: `AGENTS.md` → `docs/plan-honsen/plan-h20` → `plan-h26` §1.2 → 本書。
2. **前提チェック**: `make check` が緑。`make verify` で**本番の実値**が取れる。
3. **🔴 最重要**: `heat` は**本番DBの値がコードの既定値に勝つ**。コードだけ直しても本番は変わらない。§4 を必ずやる。
4. **完了の定義**: 末尾「§6 完了条件」。
5. **PR 後**: `gh pr checks <N>` で CI 実走・緑を確認。

---

## 0. 現状の測定（本番の実値で計算した）

### 0.1 計算式

```go
newHeat = hp.Base + int(hp.PerAliveDrop * float64(maxStores-aliveCount))
switch phase {
case Early: newHeat += hp.PhaseEarly
case Mid:   newHeat += hp.PhaseMid
case Late:  newHeat += hp.PhaseLate
}
clamp(0, hp.MaxLevel)
```

### 0.2 本番の実値（`/api/params` から取得）

```
base 0 / perAliveDrop 0.05 / phaseEarly 0 / phaseMid 1 / phaseLate 9 / maxLevel 17
phase: midTimeMs 40000 / lateTimeMs 90000
cull:  20s→75 / 40s→55 / 60s→35 / 80s→20 / 100s→10 / 120s→0
```

### 0.3 実際に描かれるカーブ

| 時刻 | 生存 | phase | 生存項 | phase項 | **heat** |
|---|---|---|---|---|---|
| 0–20s | 99 | Early | 0 | 0 | **0** |
| 20–40s | 75 | Early | 1 | 0 | **1** |
| 40–60s | 55 | Mid | 2 | +1 | **3** |
| 60–80s | 35 | Mid | 3 | +1 | **4** |
| 80–90s | 20 | Mid | 3 | +1 | **4** |
| 90–100s | 20 | **Late** | 3 | **+9** | **12** ← 🔴 **+8 の跳ね** |
| 100–120s | 10 | Late | 4 | +9 | **13** |

**問題が3つある。**

#### ① 90秒で難度が +8 跳ぶ

`4 → 12`。プレイヤー体験としては**別のゲームに切り替わる**レベルの変化。
「じわじわ難しくなる」ではなく「突然殴られる」になっている。

#### ② 🔴 上限17に一度も届かない（最上位の語彙が死んでいる）

最大が **13**。`maxLevel = 17` は**また「効かないツマミ」**に戻っている。

> これは plan-h26 §1.2 で一度直した問題（当時 Late を 8→9 にした）。
> ただしその判断は **`perAliveDrop = 0.1`（コード既定値）** を前提にしていた:
> `int(0.1×(99−10)) + 9 = 8 + 9 = 17` ✅
> **本番DBは `perAliveDrop = 0.05` / `phaseMid = 1`** なので:
> `int(0.05×(99−10)) + 9 = 4 + 9 = 13` ❌
>
> **DB値がコード既定値に勝つ**ため、コード側のコメントに書かれた計算が本番では成立していない。
> h20 の「もう一つのミラー問題」がここでも出ている。

#### ③ 生存項が `int()` 切り捨てで階段になる

`perAliveDrop = 0.05` だと生存が20店減ってやっと1上がる。
**足切りの瞬間だけ段差ができ**、間はまったく動かない。

---

## 1. 方針

```
現状: 生存項（粗い階段） + phase項（巨大な段差）
本案: 経過時間に対して連続に上がる。phase項は「補正」に格下げする
```

### 1.1 なぜ phase 項を主役から降ろすか

phase は**離散イベント**（Early/Mid/Late の3値）なので、そこに大きな数を載せると必ず段差になる。
一方 `heatLevel` は**連続に上がってほしい量**。**担当を取り違えている。**

- **難度の主軸** → 経過時間（連続）
- **phase** → 演出上の区切り（UI のフェーズ表示・客の属性分布）。難度への寄与は小さく

### 1.2 案の比較

| 案 | 内容 | 評価 |
|---|---|---|
| **① 時間比例項を足す**（本案） | `PerElapsedSec` を新設し、経過秒に比例させる。phase項は小さく | **推奨**。連続・単調・上端に必ず届く。既存フィールドを壊さない |
| ② 生存項の係数を上げる | `perAliveDrop` を 0.05→0.15 に | 階段のまま。足切り間隔（20秒）でしか動かない |
| ③ cullStage ごとに heat を指定 | 段階ごとに heat を直接持つ | 明示的だが、**config の配列がまた増える**（h22 のゼロ埋め罠が再発しやすい） |

**①で進める。**

---

## 2. 変更内容

### 2.1 `HeatParams` に時間比例項を足す

```go
type HeatParams struct {
    Base         int     `json:"base"`
    PerAliveDrop float64 `json:"perAliveDrop"`
    // PerElapsedSec は経過1秒あたりの heat 上昇（plan-h32）。
    // 難度の主軸。これが連続性を作る。
    PerElapsedSec float64 `json:"perElapsedSec"`
    PhaseEarly   int     `json:"phaseEarly"`
    PhaseMid     int     `json:"phaseMid"`
    PhaseLate    int     `json:"phaseLate"`
    MaxLevel     int     `json:"maxLevel"`
}
```

```go
newHeat := hp.Base +
    int(hp.PerAliveDrop*float64(maxStores-s.aliveCount)) +
    int(hp.PerElapsedSec*float64(s.elapsedMs)/1000.0)
```

> ⚠ `GameParameters` は `==` 比較可能を保つこと（AGENTS.md §1.3）。`float64` 追加は問題ない。
> ⚠ `backfillDefaults` は**ゼロ値のフィールドに既定値を入れる**（#124）。
> `perElapsedSec` は新規なので**既存DBでは 0 → 既定値が入る**。意図どおり。

### 2.2 新しい既定値

**120秒で 0→17 に届く**ように置く。

```
base:          0
perElapsedSec: 0.11      // 120s × 0.11 = 13.2
perAliveDrop:  0.03      // 89店減 × 0.03 = 2.6
phaseEarly:    0
phaseMid:      1
phaseLate:     2         // ★ 9 → 2 に下げる（跳ねを消す）
maxLevel:      17
```

**描かれるカーブ:**

| 時刻 | 生存 | phase | 時間項 | 生存項 | phase項 | **heat** |
|---|---|---|---|---|---|---|
| 0s | 99 | Early | 0 | 0 | 0 | **0** |
| 20s | 99→75 | Early | 2 | 0 | 0 | **2** |
| 40s | 75→55 | Mid | 4 | 0 | +1 | **5** |
| 60s | 55→35 | Mid | 6 | 1 | +1 | **8** |
| 80s | 35→20 | Mid | 8 | 1 | +1 | **10** |
| 90s | 20 | Late | 9 | 2 | +2 | **13** |
| 100s | 20→10 | Late | 11 | 2 | +2 | **15** |
| 120s | 10 | Late | 13 | 2 | +2 | **17** ✅ |

- **最大の段差が +3**（現状 +8）
- **上端17に到達**する（最上位の語彙が生きる）
- 単調増加

> 🔴 **`phaseLate` を 9 → 2 に下げる**のが本 plan の要。ここを下げずに時間項を足すと、
> 単に全体が持ち上がって上限で頭打ちになるだけ（跳ねは消えない）。

### 2.3 h30 との関係

h30 で辞書の1語を短くしても、**レベル数（18段階）は変えない**方針。
したがって `maxLevel = 17` は据え置きで、本 plan と競合しない。

> h30 でレベル数を減らす判断になった場合は、`maxLevel` と `odai.MaxWordLevel` を**両方**揃えること
> （`game` は `odai` を import できないので数値で揃える運用・params.go のコメント参照）。

---

## 3. 上端到達を**テストで守る**（同じ事故を3度目にしない）

この問題は #75 → plan-h26 §1.2 → 本 plan と**3回目**。値を直すだけでは再発する。
**「上端に届くか」をテストにする。**

```
テスト: 既定パラメータで試合を最後まで回し、heatLevel の最大値が maxLevel に到達すること
```

- **到達しない値へ変異させると落ちる**こと（`phaseLate` を下げる等）
- 逆に**早すぎる到達**も検出する（例: 60秒時点で上端に達していないこと）

> これで config-front から値をいじって上端に届かなくなった場合も、
> **CI ではなく sim で気づける**（§5）。

---

## 4. 🔴 本番DBの値を更新する（コードだけでは効かない）

**`perAliveDrop = 0.05` / `phaseMid = 1` は本番DBに保存された値**であり、コードの既定値には戻らない。
`backfillDefaults` は**ゼロ値のときだけ**既定を入れるので、非ゼロの既存値は上書きされない。

**config-front で以下を手で更新する:**

| キー | 現在 | 変更後 |
|---|---|---|
| `heat.perAliveDrop` | 0.05 | **0.03** |
| `heat.perElapsedSec` | （無し→0） | **0.11** |
| `heat.phaseMid` | 1 | 1（据え置き） |
| `heat.phaseLate` | **9** | **2** |

更新後に必ず確認:

```bash
make verify
```

> ⚠ `perElapsedSec` は新規キーなので、**config-front 側に入力欄が出るか**を確認すること
> （h24 の「もう一つのミラー問題」。フィールドを足したらフロントにも出す必要がある）。

---

## 5. 検証

```bash
make sim
```

| 観測 | 期待 |
|---|---|
| heat の最大値 | **17 に到達** |
| heat の最大段差 | **+3 以下** |
| 決着時間 | 120.0s（変わらない。時刻足切りなので） |
| 分離度 | 現状（約4500）と同程度以上 |

> heat が上がると**全店が等しく遅くなる**ので、順位への影響は小さいはず。
> 大きく動いたら h31（Bot の難度追従）との相互作用を疑う。

---

## 6. 完了条件

- [ ] `HeatParams` に `PerElapsedSec` が入り、`==` 比較可能を保っている
- [ ] `stepHeat` が時間比例項を含む
- [ ] `phaseLate` の既定が **2** に下がっている
- [ ] **既定値で heat が maxLevel(17) に到達する**テストがある（変異で落ちること確認済み）
- [ ] **最大段差が +3 以下**であるテストがある
- [ ] **本番DBの `heat.*` を config-front で更新**し、`make verify` で確認した
- [ ] config-front に `perElapsedSec` の入力欄がある
- [ ] `make sim` で決着120秒・分離度が現状以上
- [ ] `make check` が緑
