# Plan-h02: 管理者スナップショット配信 ＋ ダッシュボード強化

> **目的**: h01 の観測ダッシュボードを、客向け `StoreListUpdate` から独立した専用ストリーム `AdminSnapshot` に載せ替え、**客がどこにいるか・フェーズ・heat・storm 予告**まで俯瞰できるようにする。当日のトラブル切り分けと、算法改良（h06）のための「目」を完成させる。
> **対応issue**: #48（observability）, #81（客向け配信の縮小と独立）
> **依存**: h01（AdminHub / `/admin/ws` / 静的配信の配管）, #104 マージ
> **参照**: **[plan-h00 共有コントラクト](plan-h00_共有コントラクト.md)（配線・ワイヤ契約の正典）**, `docs/architecture.md` §6-7, `internal/game/session.go`, `internal/room/room.go`

> ✅ **実装済み**（Server #107 / DashBoard #2、2026-08-12 マージ）。
>
> ⚠ **本戦版は [plan-h25](plan-h25_観測ダッシュボードの本戦対応.md)。** 本 plan の**配管（AdminHub /
> `/admin/ws` / room 配線 / front の枠組み）はそのまま生きる**が、**表示するフィールドは本戦ルールで
> 差し替わる**（`CreditLife` / `EvalNormalized` / `AdminStorm` → `Score` / `AdminCull`）。
> 観測の目的自体も「当日のトラブル切り分け」から「バランスを詰める計測器」へ移る。

---

## 0. 前提知識

### 読むべきファイル

| ファイル | 内容 |
|---|---|
| `internal/game/session.go` | 横断状態（phase / heat / storm / storeQueues / restPool / customers）。**getterを足す先** |
| `internal/room/room.go` | tickループ。session から盤面を作り publish する箇所 |
| `plan-h01`（本ディレクトリ） | `AdminHub` / `/admin/ws` の配管（本planはこれを流用） |
| `docs/plan/plan-04_フェーズ火力下位淘汰.md` | phase / heat / storm の定義 |

### h01 との差分（何を足すか）

| 項目 | h01（既存StoreListUpdate） | h02（AdminSnapshot） |
|---|---|---|
| 店ごと 体力/評価/順位 | ✅ | ✅（引き継ぎ） |
| 店ごと 行列長 / 提供数 | ❌ | ✅ |
| 客分配（restPool残・客属性の分布） | ❌ | ✅ |
| フェーズ（Early/Mid/Late） | ❌ | ✅ |
| heat レベル（お題難度） | ❌ | ✅ |
| storm 予告（残tick・淘汰閾値・対象圏） | ❌ | ✅ |
| 試合メタ（matchId / elapsedMs） | 部分 | ✅ |

---

## 1. 設計方針

### 1.1 `AdminSnapshot` は proto 契約ではない（＝proto変更なし）

- `AdminSnapshot` は**サーバー内部の管理者向けDTO**。Unity クライアントには送らない。
  → **Takoda99-Proto に足さない**。`internal/admin`（or `internal/transport`）に Go 構造体として置く。
  → **proto の人間承認フローは不要**（1.2 の対象外）。ただしスパイン改修なのでりーせ担当。
- 客向け `StoreListUpdate` は変えない。むしろ #81・MTG方針で**痩せてよい**。観測は自前ストリームなので独立。

### 1.2 game コアは純粋なまま — 読み取りは getter で

`internal/game/session.go` に**副作用のない getter**を足すだけ（plan-12 が `Id()/AliveCount()/ElapsedMs()` を足したのと同じ作法）。game は hub も slog も import しない。既存getterは流用（`Id()` `AliveCount()` `ElapsedMs()` `Snapshot()`）。

必要な getter（**内部フィールドは実在を確認済み**。値の出所を併記）:

```go
func (s *Session) Phase() proto.Phase           // s.phase（既存フィールド。proto.PhaseEarly/Mid/Late）
func (s *Session) HeatLevel() int               // s.heatLevel（wordLevel() が返す値）
func (s *Session) RestPoolCount() int           // len(s.restPool)（session.go:133 の []CustomerId）
func (s *Session) StormState() StormView         // 下記 N5 参照（最後に出した予告を保持して返す）
func (s *Session) StoreBoard() []StoreBoardRow    // 店ごと: queueLen=len(s.storeQueues[id]) / servedCount=st.served.count / evalNormalized
func (s *Session) CustomerMix() CustomerMixView   // 属性別 在場数。restPool+各storeQueues を走査して attribute で集計
```

> これらは **全店横断の状態と時系列の読み出し**であって判定式ではない（AGENTS.md §3 の線引きに合致）。純粋 getter なのでコアの純粋性は保たれる。

**N5 — storm 状態の出所**: storm の予告は `stepStorm` が `proto.ForcedEliminationWarning`（`untilTick` / `thresholdPct` / `selfAtRisk`）として毎tick出す。session に「最後に出した予告」を保持するフィールド（`lastStormWarning`）を持たせ、`StormState()` で返すのが素直。`AtRisk` の店集合は `cullTargets()`（`session.go:763`）が計算するので、これを公開して各店に反映する。
>
> ⚠ getter追加は最小限に留める。1つでも**故意に別値を返す変異でテストが落ちる**ことを確認し、コアの純粋性（hub/slog を import しない）を depguard で担保する。

### 1.3 スナップショット生成は room goroutine で

session を読むのは room の単一 goroutine だけ（データ競合回避）。room が publish 直後に getter を集めて `AdminSnapshot` を組み、`hub.Broadcast` する。

```go
// room の publish()（room.go:96）内、h01 で足した hub.Broadcast の呼び出しを差し替える
if r.hub != nil {
    snap := buildAdminSnapshot(r.session)   // getterを読むだけ（同一 goroutine なので競合しない）
    r.hub.Broadcast(snap.JSON())
}
```

### 1.4 `AdminSnapshot` の形（案）

```go
type AdminSnapshot struct {
    MatchId    string           `json:"matchId"`
    ElapsedMs  int64            `json:"elapsedMs"`
    Phase      string           `json:"phase"`       // Early/Mid/Late
    HeatLevel  int              `json:"heatLevel"`
    AliveCount int              `json:"aliveCount"`
    RestPool   int              `json:"restPool"`    // 未割当客数
    Storm      AdminStorm       `json:"storm"`
    Customers  AdminCustomerMix `json:"customers"`   // 属性別 在場数
    Stores     []AdminStore     `json:"stores"`      // 99店
}
type AdminStorm struct {
    Warning      bool    `json:"warning"`      // 予告中
    UntilTick    int     `json:"untilTick"`
    ThresholdPct float64 `json:"thresholdPct"` // 淘汰される正規化評価下位%
}
type AdminCustomerMix struct {
    Normal, Bonus, Claimer, Buzz int
}
type AdminStore struct {
    StoreId        string   `json:"storeId"`
    DisplayName    string   `json:"displayName"`
    Alive          bool     `json:"alive"`
    Rank           int      `json:"rank"`
    FinalRank      *int     `json:"finalRank,omitempty"`
    CreditLife     int      `json:"creditLife"`
    EvalNormalized float64  `json:"evalNormalized"`
    QueueLen       int      `json:"queueLen"`     // 行列の長さ
    ServedCount    int      `json:"servedCount"`   // 累積提供数
    AtRisk         bool     `json:"atRisk"`        // storm 予告の対象圏
}
```

### 1.5 配信頻度

- h01 同様、既存 publish 間隔（`PublishIntervalMs`, 既定250ms）に相乗り。room の `publish()` 内で h01 の `hub.Broadcast` を、`StoreListUpdate` から `AdminSnapshot` の JSON に差し替える。
- 観測は1〜数接続なので O(99) のシリアライズ×低頻度で帯域は問題にならない（客向けの O(99×99) 問題とは別）。

### 1.6 留意点（h01 から引き継ぐ制約）

- **N6 — 1試合前提**: `AdminHub` はプロセス共有。複数試合が並走するとスナップが混線する。`matchId` フィールドで区別はできるが、現状 1部屋 1試合なので単一で足りる（複数試合対応は将来課題）。
- **N7 — 試合非稼働中は無音**: 試合が走っていない間は room が居ないので `hub.Broadcast` が呼ばれず、`/admin/ws` は無音。フロントは「試合なし（待機中）」状態を明示的に描く（最後のスナップから一定時間更新が無ければ "no active match" 表示）。

---

## 2. ダッシュボード強化（フロント）

h01 の99店グリッドに情報レイヤーを重ねる:

- 上部バー: `Phase` / `HeatLevel` / `AliveCount` / `elapsedMs` / `RestPool`（未割当客）/ 属性別 在場数。
- storm 予告中は画面に**カウントダウンと淘汰閾値**を出し、`AtRisk` の店を赤枠でハイライト（誰が消えそうか一目で）。
- 各セルに **行列長** と **提供数** を追加。行列が詰まっている店（離脱リスク）が見えるようにする。
- （任意）客の流れの簡易可視化: restPool → 各店行列 の総量推移を小さなスパークラインで。

> 目的は「**課題2/3（評価跳ね上がり・評価↔体力の連動・お題ランダム）を目で捉える**」こと。h06 の算法改良はこの画面で挙動を観察しながら詰める。

---

## 3. observability（構造化ログ）との統合

- plan-17（#48）の構造化ログ（`phase_change` / `store_eliminated` / `match_end` 等）と**同じ源**（room の Outbound / getter）から出せる。
- `AdminSnapshot` を毎publish保存する必要はない。**イベント（脱落・フェーズ移行・storm発火）は slog、連続状態はダッシュボード**、と役割分担する。
- 当日は「ダッシュボードで見る＋journald の slog で後追い」の二枚看板にする。

---

## 4. テスト

- getter が session 内部状態と一致（phase/heat/storm/restPool/queueLen/servedCount）。**故意に別の値を返す変異を入れてテストが落ちること**を確認。
- `buildAdminSnapshot` が脱落店に `FinalRank` を入れ、生存店には入れない。
- storm 予告中に `Storm.Warning=true` と `AtRisk` が閾値どおり立つ。
- game コアが hub/slog を import していない（depguard で機械確認）。

---

## 5. 完了条件

- [ ] `AdminSnapshot`（+ 付随型）が `internal/admin`（proto外）に定義され、Unityへは送られない
- [ ] `session` に必要な純粋 getter が揃い、game の純粋性が保たれている（hub/slog を import しない）
- [ ] room が publish 直後に `AdminSnapshot` を組んで `hub.Broadcast` する（h01 の配管を流用）
- [ ] ダッシュボードが phase/heat/storm予告/restPool/属性分布/行列長/提供数 を描画する
- [ ] storm 予告時に対象圏の店がハイライトされる
- [ ] 客向け `StoreListUpdate` は無変更（観測は独立系統）
- [ ] proto を変更していない（AdminSnapshot は内部DTO）
- [ ] `go build` / `go vet` / `golangci-lint run` が通り、game テストがグリーン
