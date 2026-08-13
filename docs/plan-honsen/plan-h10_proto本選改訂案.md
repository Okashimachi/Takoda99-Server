# Plan-h10: proto 本選改訂（v0.7.0 → v0.8.0）★承認済み

> **目的**: 本選ルール（スコア制・時刻足切り・120秒全店脱落）を契約へ反映する。**依存順で全体のボトルネック**であり、これが確定しないとサーバーの配信もクライアントの秒読みUIも書けない。
> **状態**: ✅ **承認済み**（りーせ／2026-08-13）。Takoda99-Proto `AGENTS.md` §1.1 の承認要件を満たした。
> **正典**: `Takoda99-Docs/00_本選差分/`（[10_差分_プロト](../../../Takoda99-Docs/00_本選差分/10_差分_プロト.md) / [30_通信シーケンス](../../../Takoda99-Docs/00_本選差分/30_通信シーケンス.md)）
> **現行**: proto v0.7.0（server の go.mod は v0.6.0 参照・origin/main が先行）

---

## 0. 決定事項

| # | 論点 | 決定 |
|---|---|---|
| 1 | 不要定義の扱い | **方式B：残して使わない**。フィールド削除をせず **minor（v0.8.0）** で上げる |
| 2 | 客の分配 | ②単純化（`allZero` 分岐を常用）。**proto への影響なし** |
| 3 | ランキング配信 | **契約は差分＋全量を今回定義**。サーバー実装は当面**全量のみ**、差分は後から有効化 |
| 4 | 同時脱落の順位 | スコア昇順 → 正確性 → 速度 → storeId。**proto への影響なし**（`finalRank` に畳まれる） |
| 5 | スコア下限 | クランプしない（負値を許容）。**符号付き整数** |
| 6 | `cullSchedule` | サーバー内部は `[6]CullStage`（`==` 可能）。**公開サブセットに含める** |
| A | `score` の型 | **int**（float にしない。丸め差で順位が揺れるのを防ぐ） |
| B | `RankingDelta` の rank | **含めない**（クライアントがソートで復元） |
| C | `StoreEliminated.reason` | **`Cull` を入れ続ける**（本選では全脱落が足切り＝意味的に正確。旧クライアントも壊れない） |
| D | `StoreListUpdate` | **定期配信を停止**し ranking 系へ置換（issue #81 の回答） |
| E | `cutStoreIds` 件数上限 | 暫定 **10**（クライアントと要合意） |
| F | `PersonalResult` の項目 | §1.4（企画・クライアントと要合意） |
| G | 公開サブセット | `cullSchedule` ＋ スコア重みを含める |
| H | `MatchEnd` の拡張 | **取り下げ。空のまま**（§1.6 の理由） |
| I | `StoreEliminatedBatch` | **追加する**（4-B 準拠＋送信キュー溢れ対策） |
| J | 決勝の `cutLineRank` | **案A**：表示は `2`、処理は全員脱落（§1.2） |
| K | `PersonalResult.MissCount` | **持たない**。`Stats.TotalMisses` に一本化 |

### なぜ方式B（minor）か

- **サーバーとクライアントが別々のタイミングで移行できる。** リーセの稼働制約（金曜東京・土曜夜ハワイ出発）下で「片方だけ先に上げても壊れない」状態を作れる。
- major（削除）だと同期移行が必要で、残り期間にそのコストを払う余裕がない。
- 予選挙動へ戻す退避弁が残る。

> ⚠ **方式Bは「移行しなくてよい」ではない。** サーバーが `creditLife` を送らなくなれば JSON 上は `0` になり、旧クライアントが読むと「ライフ0＝死亡」に見える。**分離できるのは移行の"時期"であって、"要否"ではない。** 廃止フィールドを読んでいる箇所はクライアント側でも必ず外す。

---

## 1. 追加する型・フィールド（すべて後方互換）

### 1.1 スコア（順位を決める唯一の値）

`score` は **符号付き整数**。`W_TAKOYAKI × orderCount − W_MISS × missCount` の累積で、重みが整数なら誤差なく厳密。負値を許容する（決定5）。

```go
// StoreSummary に追加（evalNormalized / creditLife は残すが送らない）
Score int `json:"score"`

// EvaluationUpdate に追加（evalRaw / normalized / starRating / starDelta は残すが送らない）
Score int `json:"score"`
```

### 1.2 `ForcedEliminationWarning` — 秒読みとカットライン

予選の `{untilTick, thresholdPct}` では右パネル（常設UI）が描けない。**追加**し、旧2フィールドは残す（送らない）。

```go
type ForcedEliminationWarning struct {
    // ── 本選で使う ──
    UntilMs     int       `json:"untilMs"`     // 次の足切りまでの残りms（クライアントはローカル補間・3-B）
    StageIndex  int       `json:"stageIndex"`  // 現在向かっている段階（1..6）
    StageTotal  int       `json:"stageTotal"`  // 総段階数（6）
    CutLineRank int       `json:"cutLineRank"` // この順位より下が切られる
    CutStoreIds []StoreId `json:"cutStoreIds"` // 切られる予定の店（上限 10・決定E）
    SelfAtRisk  bool      `json:"selfAtRisk"`  // 継続使用。自店が対象圏か

    // ── 予選の名残（v0.8.0 以降は送らない） ──
    UntilTick    int     `json:"untilTick"`
    ThresholdPct float64 `json:"thresholdPct"`
}
```

- `SelfAtRisk` は既存フィールドを継続使用。`cutLineRank` と自分の rank の比較を**クライアントにさせない原則**（v0.7.0 のコメントに明記）を維持する。

#### ★決定J：最終ステージ（100〜120秒）の `cutLineRank` は `2` を送る

企画書 3.7 は2つのことを同時に要求している。**層を分けることで両立する。**

| 層 | 挙動 |
|---|---|
| **処理層** | `targetAliveCount: 0` のまま。120秒で**1位を含む10店全員を脱落**させる（D9） |
| **表示層** | `cutLineRank = 2` を送り、右パネルは「**1位以外が脱落対象**」と見せる（緊張の最大化） |

`ForcedEliminationWarning` はもともと表示のための情報であり、淘汰処理とは責務が別。内部を曲げずに演出意図を満たせる。

> 🔴 **クライアントへの必須注意**：1位のプレイヤーにも `StoreEliminated` が届く。ここで素朴に「あなたは脱落しました」を出すと**優勝者に敗北演出が流れる**。`finalRank` による分岐が必須（5-C）。表示上「1位は脱落対象外」と出した直後に本人へ脱落が飛ぶ点に注意。

### 1.3 ランキング配信 — 新規2メッセージ

**`StoreListUpdate`（全員へ全店フル・毎250ms）を置き換える。** issue #81（egress が無料枠を割る）への回答でもある。

```go
const (
    TypeRankingSnapshot = "RankingSnapshot"
    TypeRankingDelta    = "RankingDelta"
)

// RankingSnapshot は全店の順位を届ける（整合性の回復用・低頻度）。
type RankingSnapshot struct {
    Entries []RankingEntry `json:"entries"`
}
type RankingEntry struct {
    StoreId StoreId `json:"storeId"`
    Rank    int     `json:"rank"`
    Score   int     `json:"score"`
    Alive   bool    `json:"alive"`
}

// RankingDelta は前回から変化した店のみ（高頻度・取りこぼし可）。
type RankingDelta struct {
    Entries []RankingChange `json:"entries"`
}
type RankingChange struct {
    StoreId StoreId `json:"storeId"`
    Score   int     `json:"score"`
    Alive   bool    `json:"alive"`
}
```

- **`rank` の意味を定義する（統合時の曖昧さ潰し）**：**生存店は現在順位、脱落店は確定順位（以後不変）**。これにより観戦時（6-E）に全99店を1本の `rank` で並べられる。
- **`RankingDelta` は `rank` を持たない**（決定B）。rank は相対値のため1店の変化で全体がずれ、「変化した店だけ送る」の意味が消える。クライアントは `score` でソートして表示順を復元し、**自店の権威 rank は `EvaluationUpdate` から取る**。
- `displayName` は送らない。`MatchStart.stores[]` で配布済み（約束 2-D）。
- 差分の判定基準は **epsilon 不要**。`score` は整数アキュムレータで `OrderServed` の瞬間しか動かないため、`ApplyOrderServed` で dirty フラグを立てれば厳密かつ安価。

**帯域の実測見積り**（`RankingEntry` ≒ 60B × 99店 ≒ 6KB）

| 方式 | 1クライアント | 99台合計 | 1試合(120秒) |
|---|---|---|---|
| 予選 `StoreListUpdate`（問題視された値） | 57 KB/s | 45 Mbps | 約 675 MB |
| **全量のみ 1Hz（初期実装）** | 6 KB/s | 4.8 Mbps | **約 71 MB** |
| 全量4秒＋差分2Hz（後から有効化） | 2.4 KB/s | 1.9 Mbps | 約 28 MB |

### 1.4 `PersonalResult` — 既存メッセージに追加

⚠ **`PersonalResult` は v0.7.0 に既に存在し、「脱落した瞬間に配信」も予選で実装済み。** 差分ドキュメント §2.6 は「新規」としているが、**サーバー側の対策は完了している**。

```go
type PersonalResult struct {
    FinalRank  int        `json:"finalRank"`   // 継続
    Stats      MatchStats `json:"stats"`       // 継続（ミス総数は Stats.TotalMisses）
    SurvivedMs int64      `json:"survivedMs"`  // 継続

    // ── 本選で追加 ──
    Score         int `json:"score"`         // 最終スコア
    TakoyakiCount int `json:"takoyakiCount"` // 作ったたこ焼きの総数（= 累計 orderCount）

    // ── 予選の名残（送らない） ──
    Reason         EliminationReason `json:"reason,omitempty"`
    CreditLeft     int               `json:"creditLeft"`
    EvalRaw        float64           `json:"evalRaw"`
    EvalNormalized float64           `json:"evalNormalized"`
}
```

- **`missCount` は持たない（決定K）**。既存の `Stats.TotalMisses` と重複し、真実の源が2つになるため。
- **`takoyakiCount` は必要**。`Stats.ServedCount` は「提供した**客**の数」であって「たこ焼きの数（＝Σ orderCount）」ではない。

### 1.5 `StoreEliminatedBatch` — 新規（決定I）

30_通信シーケンス **4-B「`StoreEliminated` はまとめて配信する。1件ずつバラバラに送らない」** を型で満たす。

```go
const TypeStoreEliminatedBatch = "StoreEliminatedBatch"

// StoreEliminatedBatch は1回の足切りで脱落した店をまとめて全員へ配信する。
type StoreEliminatedBatch struct {
    StageIndex int               `json:"stageIndex"` // 第何段階の足切りか（1..6）
    Entries    []StoreEliminated `json:"entries"`
}
```

**なぜ型が要るか（実害）**：`StoreEliminated` は1店1メッセージのため、24店脱落＝24 Envelope になる。送信キューは `sendBuffer = 64`（`internal/transport/connection.go:57`）で、**溢れた接続は即座に切断される**（slow-consumer eviction, 同 `:155`）。足切りの瞬間に脱落通知＋`EvaluationUpdate`＋`RankingSnapshot`＋`ForcedEliminationWarning` が1tickで殺到すると、**軽く詰まっただけの健全なクライアントが最も盛り上がる瞬間に切断され得る**。1メッセージに畳めばこのリスクが消え、クライアントの演出集約（4-C）も素直になる。

### 1.6 `MatchEnd` は空のまま（決定H・拡張しない）

当初「優勝者が誰か届かない」と判断したが、**誤りだったため取り下げる**。

`StoreEliminated` は **`broadcastMsg`＝全接続へ配信**され（`internal/game/session.go:800`）、`finalRank` を持つ。脱落者も接続を維持したまま受信し続ける（6-A）ため——

```
120秒 → 残り10店を脱落 → StoreEliminatedBatch（finalRank 1..10 を含む）が全員へ
       → 観戦中のプレイヤーも finalRank==1 の storeId を得る
       → displayName は MatchStart.stores[] にキャッシュ済み（2-D）
```

**1位も他と同じように脱落させるだけで、優勝者の識別子は全員に届く。** 特別扱いは不要で、5-C「サーバーは順位を返すだけで、勝者の特別扱いは持たない」とも整合する。

副産物として、**クライアントは脱落ストリームだけで最終順位表を全部再構成できる**（各ステージのバッチに finalRank が入るため 11〜99位も蓄積済み）。

**残る穴と対処**：`StoreEliminated` は `score` を持たないため、「優勝 たこ太 12,400点」のようなスコア表示ができない。**契約変更ではなく配信順序で解決する**（§3）。

### 1.7 `GameParametersPublicSubset` — cullSchedule と スコア重み

```go
type CullStageView struct {
    AtMs             int `json:"atMs"`
    TargetAliveCount int `json:"targetAliveCount"`
}
// 追加
CullSchedule        []CullStageView `json:"cullSchedule"`        // 6段階。クライアントがタイムラインUIを描ける
ScoreWeightTakoyaki int             `json:"scoreWeightTakoyaki"`
ScoreWeightMiss     int             `json:"scoreWeightMiss"`
```

- **ワイヤ上はスライスで問題ない。** `==` 比較の制約（Server `AGENTS.md` §1.3）は**サーバー内部の `GameParameters` 構造体のみ**にかかる話で、proto の DTO には及ばない。内部は `[6]CullStage`（配列は要素が comparable なら `==` 可能）、公開時にスライスへ変換する。
- 重みの公開は「提供時に +100 を出す」等の演出のため。スコア計算は引き続きサーバー権威。
- ⚠ 既存の `InitialLife` / `StormThresholdPct` / `PatienceLateMul` / `PatienceAlertMs` は**無意味化する**。方式Bで残すが、`initialLife` が 0 で届くとクライアントのライフゲージが「最大0」になるため、`creditLife` と同じ注意喚起が要る。

---

## 2. 残すが送らない（方式B・deprecated）

**定義は消さない。サーバーが値を入れず、クライアントが読まない。**

| 対象 | 扱い |
|---|---|
| `CreditUpdate` メッセージ | 送信しない |
| `CustomerLeft` メッセージ | 送信しない（離脱が発生しない） |
| `StoreSummary.creditLife` / `evalNormalized` | 送らない（`score` を見る） |
| `CustomerView.patienceMaxMs` | 送らない |
| `EvaluationUpdate.evalRaw` / `normalized` / `starRating` / `starDelta` | 送らない（`score` / `rank` を見る） |
| `EliminationReason` 型 | 型は残す。`StoreEliminated.reason` には **`Cull` を入れ続ける**（決定C） |
| `StoreListUpdate` | **定期配信をやめ** ranking 系に置換。`MatchStart.stores[]` の `StoreSummary` は継続使用 |
| 公開サブセットの信用・我慢・storm 系 | 送らない（§1.7） |

---

## 3. 配信順序の約束（契約の一部）

型だけでは表現できないが、**守らないとフローが再現できない**順序。

### 3.1 足切りステージ（20/40/60/80/100秒）— 4-A の具体化

```
1. StoreEliminatedBatch      … 誰が落ちたかを先に配る
2. PersonalResult            … 脱落した店にだけ
3. EvaluationUpdate          … 生存店の新しい順位
4. RankingSnapshot           … 全量で整合をとる（4-E）
5. ForcedEliminationWarning  … 次ステージの秒読み
```

**順位を配る前に脱落を配る。** 逆順だと脱落者を含んだ順位が一瞬表示される。

### 3.2 試合終了（120秒）— 決定H の穴埋め

```
1. StoreEliminatedBatch   … 残り10店（finalRank 1..10）を全員へ
2. PersonalResult         … 10店それぞれへ
3. RankingSnapshot        … ★最終スコアを全員へ（優勝者のスコア表示はこれで賄う）
4. MatchEnd               … 空。全体の締め
```

**3 を省略しない。** これが `MatchEnd` を拡張せずに済ませる条件。

---

## 4. バージョンと互換表

| 項目 | 内容 |
|---|---|
| バージョン | **v0.8.0**（minor・後方互換な追加のみ） |
| 3言語ミラー | Go（正典・`proto/messages.go`）/ TS（`ts/types.ts`）/ C#（`csharp/Takoda99.Proto/Messages.cs`）を**同一PRで**揃える（Proto `AGENTS.md` §2） |
| ゴールデン | `proto/wire_test.go` に新メッセージの JSON 表現を固定 |
| 互換表 | `README.md` に v0.8.0 行を追加 |
| 通知 | タグ後、カシュー（Unity）へ通知。**廃止フィールドの読み出しを外す必要がある**ことを併せて伝える |

---

## 5. 実施手順

1. Takoda99-Proto でブランチを切り、**Go / TS / C# を1PRで**変更
2. `wire_test.go` のゴールデンを更新
3. `README.md` 互換表に v0.8.0 を追記 → マージ → **`v0.8.0` タグ**
4. Server の `go.mod` を **v0.6.0 → v0.8.0** へ更新（origin/main が先行している点に注意）
5. カシューへ通知（§4）

> **付随して直すべき doc rot**（別PR可）: Takoda99-Proto の `AGENTS.md` §3「主要メッセージ」が旧テキストロ99のまま（`DakenClearReport` / `AttackRequest` / `StrategySelect` / `KoNotified` 等）。Proto リポで作業する人が最初に読む憲法なので、誤実装の元になる。

---

## 6. サーバー実装への申し送り（h11 以降）

proto ではないが、本改訂と対で必要になるもの。

| 項目 | 内容 |
|---|---|
| `EvaluationUpdate` の頻度 | 現在 `stepNormalize` が**毎tick全生存店へ**配信している（`session.go:653`）。仕様は 2〜4Hz なので**間引く**方向の調整 |
| タイブレークのゼロ除算 | 20秒地点では「1件も提供していない店」が必ず出る。`accuracy = 1 - misses/keystrokes` が **0除算**、平均所要も未定義。**未提供店は accuracy=0 / 平均所要=+∞ とみなし、最終的に storeId で決定化**する |
| 負値スコアの副作用 | 大量ミスで負になった店は「一度も打たなかった店（0点）」より下に沈む。`W_TAKOYAKI=100 / W_MISS=30` なら1語あたり3.3ミス超が必要で実際にはほぼ起きないが、**シミュレーションで負値の発生率を観測項目に入れる** |
| 自店脱落時の打鍵中断 | 20_廃止リストは「入力中断処理は不要」とするが、**足切りの瞬間に数十人が打鍵中に脱落する**。シーン遷移としての中断は必要（クライアント申し送り） |
