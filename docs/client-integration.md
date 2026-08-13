# クライアント結合仕様 — ワイヤ仕様とタイミング

クライアント実装者向けの **「どんな型が、いつ届くか」** の実務リファレンス。

型は **`Takoda99-Proto` が正典**だが、**タイミングと頻度は proto には書かれていない**。
「`EvaluationUpdate` は毎tick来るのか提供時だけか」「客はどの順で捌けるのか」といった、
**サーバーの実装にしか無かった情報**をここに集める。

## この文書の置き場所について

| リポジトリ | 担当 |
|---|---|
| **ここ（Takoda99-Server `docs/client-integration.md`）** | **ワイヤ仕様とタイミング**。サーバーの実挙動そのもの |
| `Takoda99-Proto` | メッセージの**型**（正典。変更は人間承認が要る） |
| `Takoda99-Client-Docs` | **クライアント側の設計**（アーキテクチャ・状態管理・ディスパッチ層・打鍵判定・画面遷移） |

内容がサーバー実装に完全に従属するため、**実装と同じリポジトリに置いて一緒に更新する**。
別リポジトリに置くと、サーバーを直したときに更新されず必ず腐る。

> **サーバーの挙動を変えたら、同じ PR でこの文書も直すこと。**
> 食い違いを見つけたら、**文書ではなくサーバーの実挙動を正**としてここを直す。

**裏取り時点**: proto **v0.8.0** / plan-h21（スコア制）適用後。参照元のファイル名・関数名を各所に明記してある。

> 🔴 **本戦ルールへの移行途中**（`docs/plan-honsen/plan-h20` の移行マップ）。
> この文書は **h21（スコア制）まで**を反映している。**まだ追随していない**のは:
>
> | 未反映 | 担当 plan | 現在の記述 |
> |---|---|---|
> | 20秒等間隔の時刻足切り（`cullSchedule`）・120秒で全店脱落 | **h22** | §3.8・§4 は**予選の storm 仕様のまま** |
> | `RankingSnapshot` / `RankingDelta` / `StoreEliminatedBatch`・配信順序・間引き | **h23** | §3.10 は `StoreListUpdate` のまま |
>
> 上記2つはサーバー実装もまだ予選仕様なので、**この文書は現在のサーバーの実挙動と一致している**。
> h22 / h23 の PR でそれぞれ書き換えること。

---

## 0. 前提

| | |
|---|---|
| エンドポイント | `wss://takoda99.mooo.com/ws` |
| フレーム | text（JSON） |
| 封筒 | `{"type": "<MessageName>", "payload": { ... }}` |
| 同時接続上限 | **200**（超過は 503。99人＋再接続/観戦の余裕） |
| proto | **v0.3.0**（本番反映済み） |

数値パラメータは**コードに直書きしない**。`MatchStart.params`（`GameParametersPublicSubset`）から取る。
現在値は `GET https://takoda99.mooo.com/api/params` でも見られるが、**表示に使うのは `MatchStart.params` の方**。

---

## 1. 接続からリザルトまでの時系列

```
[接続]
  │
  ├─→ C2S: MatchmakingJoin        ← ★接続したら即送る（3秒以内）
  │
  ├─← S2C: MatchmakingStatus      ← 1秒ごと＋カウントダウン開始時
  │        （waitingCount / minPlayers / countdownMs）
  │
  │  … minPlayers に到達 → カウントダウン → 定員未満は Bot で補完
  │
  ├─← S2C: MatchStart             ← 自分宛て1通。selfStoreId で自店を特定
  │
  │  ┌── 以降 tick ごと（現在 150ms 周期）─────────────────
  │  │
  │  ├─← PhaseChange              ← 移行時のみ・全員
  │  ├─← CustomerArrived          ← 自店に客が来たとき
  │  ├─← EvaluationUpdate         ← 毎tick・生存店それぞれへ（score / rank）
  │  ├─← DifficultyUpdate         ← heatLevel が変化したときのみ・全員
  │  ├─← ForcedEliminationWarning ← storm 予告時に1回・生存店それぞれへ
  │  ├─← StoreEliminated          ← 脱落確定のたび・全員
  │  │
  │  ├─→ C2S: OrderServed         ← 注文を打ち切ったとき
  │  │   └─← EvaluationUpdate     ← 成功時のみ即レス1通
  │  │
  │  └─← StoreListUpdate          ← 250ms ごと・全員（tick とは別系統）
  │
  └─← MatchEnd                    ← 全店へ（脱落済みの店にも届く）
```

> **本戦（h21）で消えたもの**: `CustomerLeft` / `CreditUpdate`。
> **客は逃げなくなり、信用（ライフ）も廃止された**。一度出たお題は必ず打ち切られるので、
> クライアントは「打っている最中に客が消える」割り込みを扱わなくてよい
> （自店が足切りで脱落したときのシーン遷移は別の話）。

---

## 2. C2S — クライアントが送るもの（3種だけ）

**試合中に送るのは実質 `OrderServed` のみ。** 完成報告（1個ごと）・離脱報告・脱落報告は**存在しない**。
すべてサーバーが自律的に確定する。

### 2.1 `MatchmakingJoin`

```json
{ "type": "MatchmakingJoin", "payload": { "displayName": "たこ焼き太郎" } }
```

| フィールド | 型 | 備考 |
|---|---|---|
| `displayName` | `string` | 省略可。**最大6文字**・制御文字は除去される |

**★上限は6文字。** 数え方は **Unicode コードポイント**（Go の `[]rune`）。
UTF-8 バイト数でも UTF-16 コードユニット数でもない。Unity の `TMP_InputField.characterLimit` は
UTF-16 単位で数えるので**完全一致はしない**。**サーバーの数え方が正**で、クライアント側の
入力欄は入力補助として近い値を掛けるだけでよい。

- 超えた分は切り詰められる（エラーにはならない）
- 切り詰めで宙に浮いた結合文字（絵文字の ZWJ・異体字セレクタ・結合マーク）は末尾から落とす。
  完全な書記素クラスタ対応ではないので、絵文字を混ぜると想定より短くなることがある
- **クライアント側で受信した名前を切り詰めないこと。** サーバーが正規化した結果をそのまま表示する

### 名前を送らなかった場合（フォールバック名）

サーバーが**試合内の通し番号**で割り当てる。**必ず6文字以内**。

| | 書式 | 例 |
|---|---|---|
| 名前なしの人間 | `ゲスト{席番号}` | `ゲスト1` 〜 `ゲスト99`（最大5文字） |
| Bot（定員補完） | `CPU{席番号}` | `CPU1` 〜 `CPU99`（最大5文字） |

席番号は `MatchStart.stores[]` の並び順と一致する（1始まり）。
接続ID（`p-42`）は使わない。試合数が増えると桁が伸びて6文字を超えるため。

> **クライアント側でフォールバック名を生成しないこと。** 必ずサーバーが配る。

**★接続したら最初に、すぐ送る。**

サーバーは接続を受けると**最初の1メッセージを最大3秒待つ**（`cmd/server/main.go` の `awaitJoinName`）。

- 送らないと**3秒待たされたうえで表示名が空**になる（フォールバック名が割り当たる）
- **別種のメッセージを最初に送っても同じ扱い**（`env.Type != MatchmakingJoin` なら即座に空名で続行）
- JSON のパースに失敗した場合も空名

> 盤面に名前が出ない不具合の大半はこれ。

### 2.2 `MatchmakingLeave`

```json
{ "type": "MatchmakingLeave", "payload": {} }
```

待機列から抜ける。**切断でも自動的に外れる**ので、明示的に送らなくても幽霊待機者にはならない。

### 2.3 `OrderServed`

```json
{ "type": "OrderServed",
  "payload": { "customerId": "c-42", "elapsedMs": 3200, "missCount": 2, "clientTimestamp": 1754400000000 } }
```

| フィールド | 型 | 意味 |
|---|---|---|
| `customerId` | `string` | 打ち切った客 |
| `elapsedMs` | `int` | **注文N個ぶん**の所要時間（クライアント計測） |
| `missCount` | `int` | **注文N個ぶん**のミス総数（クライアント計測） |
| `clientTimestamp` | `int64` | ⚠ **現在のサーバーは読んでいない**（§5.5） |

**単語ごとではなく、客ごとに1回**送る。`orderCount` 個すべてを打ち切った瞬間。

#### サーバー側の検証（`ApplyOrderServed`）

以下のいずれかに当たると**黙って破棄される。エラーも応答も返らない。**

- 試合が進行中でない
- 自店が脱落済み
- `customerId` が存在しない
- その客が自店に割り当てられていない
- **その客が自店の行列の先頭でない** ← ★最重要

> **★客は到着順（行列の先頭から）にしか提供できない。**
> 2人目・3人目に先に `OrderServed` を送っても無視される。応答が無いので気付きにくい。
> クライアントは `CustomerArrived` の到着順を保持し、先頭から捌くこと。

通った場合は**値が丸められる**。

| | 丸め |
|---|---|
| `elapsedMs` | 下限 = `eval.minMsPerWord × orderCount`（現在 200ms × 注文数）。それ未満は下限値に |
| `missCount` | `[0, サーバーが発行した総打鍵数]` にクランプ |

**速く申告しても得しない**（下限で頭打ち）。**ミスを過大申告しても打鍵数以上にはならない**。

---

## 3. S2C — サーバーから届くもの（10種）

宛先の「自分」＝その店だけに届く、「全員」＝試合参加者全員に同じものが届く。

| メッセージ | 宛先 | いつ | 頻度 |
|---|---|---|---|
| `MatchmakingStatus` | 待機者 | 待機中 | 1秒ごと＋カウントダウン開始時 |
| `MatchStart` | 自分 | 試合開始 | 1回 |
| `CustomerArrived` | 自分 | 客が自店の行列に入った | 不定 |
| `EvaluationUpdate` | 自分 | 毎tick ＋ 提供成功の即レス | **高頻度** |
| `DifficultyUpdate` | 全員 | `heatLevel` が**変化したとき** | 低頻度 |
| `PhaseChange` | 全員 | フェーズ移行 | 試合中2回 |
| `ForcedEliminationWarning` | 生存店 | storm 予告 | 1周期に1回 |
| `StoreEliminated` | 全員 | 脱落確定 | 98回 |
| `StoreListUpdate` | 全員 | 一定間隔 | 250ms ごと |
| `MatchEnd` | 全員 | 試合終了 | 1回 |

> ~~`CustomerLeft`~~ / ~~`CreditUpdate`~~ は**サーバーが送らなくなった**（h21）。
> 型は proto に残っているが、受信ハンドラを書く必要は無い。

### 3.1 `MatchmakingStatus`

```json
{ "waitingCount": 12, "minPlayers": 20, "countdownMs": 15000 }
```

`countdownMs` は**カウントダウン中のみ入る**（待機中は**キーごと無い**）。

配信されるのは次の3つの場合（`internal/matchmaking/matchmaking.go` の `Run`）:

1. **1秒ごとのティッカー**（待機者が1人以上いるとき）
2. カウントダウンの**開始時**
3. カウントダウンの**中断時**（`minPlayers` を割り込んだとき。`countdownMs` が消える）

> ⚠ **`MatchmakingJoin` を送っても、すぐには `MatchmakingStatus` が返らない**（最大1秒待つ）。
> 会場で99人が一斉参加したとき「1 join ごとに全員へ配信」だと O(N²)＝約4,900通のバーストになり、
> 送信キューが溢れて待機者が切断される。それを避けるため join では強制配信していない。
> **接続直後の1秒間は状態が空**として画面を組むこと。

> **本番は match モードなので待機画面が必ず要る。** 接続したら即 `MatchStart` が来るとは限らない。

### 3.2 `MatchStart`

```json
{
  "matchId": "m-3",
  "selfStoreId": "p-42",
  "params": { "maxStores": 99, "cullSchedule": [],
              "scoreWeightTakoyaki": 100, "scoreWeightMiss": 30,
              "finalStageAliveThreshold": 20, "finalRushAliveThreshold": 10,
              "initialLife": 0, "stormThresholdPct": 0,
              "patienceLateMul": 0, "patienceAlertMs": 0 },
  "phase": "Early",
  "stores": [ { "storeId": "p-1", "displayName": "…", "rank": 0, "alive": true,
                "score": 0, "evalNormalized": 0, "creditLife": 0 }, … ]
}
```

> 上の値は**例**。運営UIから変更できるので、**必ず受信値を使う**（§5.7）。

- `selfStoreId` で `stores[]` の中の自分を特定する
- **`stores[]` は表示名の唯一の供給源。** 以降のメッセージは帯域削減のため `storeId` しか送らない。
  ここを辞書としてキャッシュし、`storeId` → 表示名 を自前で引くこと。**再送は期待しない**
- `scoreWeightTakoyaki` / `scoreWeightMiss` — スコアの重み。
  `deltaScore = scoreWeightTakoyaki × たこ焼き数 − scoreWeightMiss × ミス数`。
  **スコア算出はサーバー権威**で、これを配るのは「+100」等の**加点演出のためだけ**
- `finalStageAliveThreshold` / `finalRushAliveThreshold` は**演出の切り替え専用**。ゲーム進行には影響しない

> 🔴 **ゼロ値で届く廃止フィールドを読まないこと。**
> `initialLife` / `stormThresholdPct` / `patienceLateMul` / `patienceAlertMs` は
> 契約（proto v0.8.0・方式B）に定義が残っているだけで、**サーバーは値を入れない**。
> `initialLife` をライフゲージの最大値に使うと 0 除算やゲージ消滅になる。
>
> `cullSchedule` は **h22 で埋まるまで空配列**。タイムラインUIはそれまで組めない。

### 3.3 `CustomerArrived`（= `CustomerView`）

```json
{ "customerId": "c-42", "attribute": "Normal", "orderCount": 2,
  "words": ["たこやき", "おおきに"],
  "patienceMaxMs": 0, "patienceStartedAtServerMs": 0 }
```

| フィールド | 備考 |
|---|---|
| `attribute` | `Normal` / `Bonus` / `Claimer` / `Buzz`。**試合中不変**。初回のみ配られる |
| `orderCount` | = `words` の長さ = **たこ焼きの個数**（スコアの加点対象） |
| `words` | サーバー発行のお題。**ひらがな**（現在の辞書） |
| ~~`patienceMaxMs`~~ / ~~`patienceStartedAtServerMs`~~ | **常に 0**。我慢ゲージは廃止（読まない） |

**★属性はゲームに一切影響しない**（h21）。予選は属性ごとに評価が増減したが、
「同じように打ったのに評価が違う」という運の要素だったため廃止された。
**見た目の出し分け専用**で、キャラクター・アイコン・行列の賑わいには引き続き使う。

**★客は逃げない。** 我慢ゲージも離脱もない。**一度出たお題は必ず打ち切られる**ので、
入力中に客が消える割り込みを扱う必要はない。

### 3.4 `EvaluationUpdate`

```json
{ "score": 12300, "rank": 17, "aliveCount": 64,
  "evalRaw": 0, "normalized": 0, "starRating": 0, "starDelta": 0 }
```

| フィールド | 意味 |
|---|---|
| `score` | **順位を決める累積値**。`W_TAKOYAKI×たこ焼き数 − W_MISS×ミス数` の累計 |
| `rank` | 生存店内の順位（1が最上位） |
| `aliveCount` | 現在の生存店数 |
| ~~`evalRaw`~~ / ~~`normalized`~~ / ~~`starRating`~~ / ~~`starDelta`~~ | **常に 0**。相対評価・星は廃止（読まない） |

**★`score` は負になりうる。** サーバーは 0 でクランプしない（ミスが多ければ実際に負になる）。
`uint` で受けたり `Mathf.Max(0, …)` で潰したりしないこと。順位表がその店だけ嘘になる。

**★自店の順位はこれが権威。** 他店を含む一覧（h23 で入る `RankingSnapshot` / `RankingDelta`）は
表示用で取りこぼしがありうる。自分の順位は必ずこちらを使う。

届くのは2系統:
1. **毎tick**（`stepRank`）— 生存店それぞれへ
2. **`OrderServed` の成功レスポンス** — 提供直後の演出用に即座に1通

### 3.5 `DifficultyUpdate`

```json
{ "heatLevel": 7 }
```

**値が変化したときだけ**届く。毎tick来ると思って組まないこと。
お題の難度段階で、現在の辞書は **0〜17**。

### 3.6 `PhaseChange`

```json
{ "phase": "Mid" }
```

`Early` → `Mid` → `Late`。**戻らない。** 生存数と経過時間のどちらか先に成立した方で移行する。

フェーズが変えるもの: Claimer の解禁（Early は来ない）／火力（お題難度）の加算。

> **本戦では脱落は Phase では起きない**（足切りの時刻スケジュールで起きる・h22）。
> Phase は**演出の切り替えとお題難度の目安**として使う。

### 3.7 `ForcedEliminationWarning`

```json
{ "untilTick": 30, "thresholdPct": 0.1, "selfAtRisk": true }
```

storm（下位淘汰）の予告。**1周期に1回だけ**届く。

> 🔴 **これは予選仕様のまま。h22 で書き換わる。** 本戦では `untilMs` / `stageIndex` /
> `stageTotal` / `cutLineRank` / `cutStoreIds` を持つ**常時配信**になり、`untilTick` /
> `thresholdPct` は廃止される。**今この2つに依存した実装をしないこと。**

- `untilTick` は**実行までの残りtick数**。秒に直すなら `tickIntervalMs` を掛ける
- **`selfAtRisk` は店ごとに違う**（だから全体配信ではなく個別配信）
- **自分が対象かをクライアントで判定しない。** `selfAtRisk` をそのまま使う

### 3.8 `StoreEliminated`

```json
{ "storeId": "p-42", "reason": "Cull", "finalRank": 63 }
```

`reason` は**常に `Cull`**。本戦（h21）で信用制が廃止され、`SelfCollapse`（自滅）の経路が消えたため、
脱落経路は足切りの1本だけになった。`reason` で分岐する意味は無い。

> h22 で複数店が同時に落ちるようになるため、`StoreEliminatedBatch`（1メッセージにまとめる）へ移る。

- **自店なら** → リザルトへ遷移。ただし**接続は切らない**（§5.4）
- **他店なら** → ミニ盤面の更新

### 3.9 `StoreListUpdate`

```json
{ "stores": [ … 99店ぶん … ], "aliveCount": 64 }
```

**tick とは別系統**で、現在 **250ms ごと**に全員へ全店分が届く。

> 🔴 **これは予選仕様のまま。h23 で `RankingSnapshot`（全量・低頻度）と
> `RankingDelta`（差分・高頻度）に置き換わる。**
> `StoreSummary.score` には既に本戦のスコアが入っている（`evalNormalized` / `creditLife` は常に 0）。

`StoreSummary.finalRank` は**脱落済みの店にだけ入る**（生存店では**キーごと出ない**）。

> ⚠ **欠落を 0 として扱わないこと。** 順位0は存在しない。
> C# の `int` で受けると 0 になるので、**nullable（`int?`）で持つ**。

### 3.10 `PersonalResult`

```json
{
  "finalRank": 1,
  "survivedMs": 120000,
  "score": 12300,
  "takoyakiCount": 34,
  "stats": {
    "servedCount": 12, "avgAccuracy": 0.94, "avgElapsedMs": 3100,
    "leftCount": 0, "totalKeystrokes": 1180, "totalMisses": 71,
    "fastestMs": 2100, "slowestMs": 5400,
    "normal":  { "served": 9, "left": 0 },
    "bonus":   { "served": 2, "left": 0 },
    "claimer": { "served": 0, "left": 0 },
    "buzz":    { "served": 1, "left": 0 }
  },
  "creditLeft": 0, "evalRaw": 0, "evalNormalized": 0
}
```

**自店の脱落が確定した瞬間に、本人だけへ届く。全員の試合終了を待たない。**
クライアントはこれを保持しておき、任意のタイミング（「次へ」押下時など）で個人成績画面を出す。
画面遷移とデータ受信を切り離すための設計（予選の「1位が決まる前に遷移すると何も出ない」対策）。

| フィールド | 意味 |
|---|---|
| `finalRank` | 最終順位。1が優勝 |
| `survivedMs` | 生存時間（試合開始から自分が脱落するまでの積算ms）。**試合の総経過時間ではない** |
| `score` | **最終スコア**。順位を決めた値そのもの。負もありうる |
| `takoyakiCount` | 作ったたこ焼きの総数（＝累計 `orderCount`）。`stats.servedCount`（提供した**客**の数）とは別物 |
| ~~`reason`~~ | **サーバーは入れない**（脱落経路が足切りの1本だけになったため）。キーごと出ない |
| ~~`creditLeft`~~ / ~~`evalRaw`~~ / ~~`evalNormalized`~~ | **常に 0**。信用・相対評価は廃止（読まない） |

`stats` は**自店ぶんのみ**。

| フィールド | 意味 |
|---|---|
| `servedCount` | 捌けた**客**の数（たこ焼きの数は `takoyakiCount`） |
| ~~`leftCount`~~ | **常に 0**。客は逃げない。集計欄だけ残っている |
| `avgAccuracy` | **客ごとの精度の平均**（0..1） |
| `totalKeystrokes` / `totalMisses` | 打鍵の生の合計。**「全体で何打鍵中いくつミスしたか」はこちらで出す**（客ごとに打鍵数が違うので `avgAccuracy` からは出せない）。**総ミス数はここが唯一の出どころ** |
| `avgElapsedMs` / `fastestMs` / `slowestMs` | 1客を捌くのに要した平均・最短・最長。提供0なら全て 0 |
| `normal` / `bonus` / `claimer` / `buzz` | 属性別の `{ served, left }`。`left` は常に 0 |

属性別の `served` の合計は `servedCount` と一致する。

> **★最大コンボはサーバーから返らない。** サーバーは**打鍵列を受け取らない**（`OrderServed` は
> 客1人ぶんの `elapsedMs` と `missCount` だけ）ので、連続無ミス数を知る手段が無い。
> 加えて「コンボ」は企画転換で概念ごと廃止されている。
> **リザルトに出すならクライアント側で自前に数えること。**

> **全店の最終順位表は `PersonalResult` に入らない。** `StoreListUpdate` の最後のスナップショットに
> 99店分が `finalRank` 込みで入っているので、それを保持しておけば順位表を描ける
> （h23 で最後の `RankingSnapshot` がこの役目を引き継ぐ）。

### 3.11 `MatchEnd`

```json
{}
```

試合全体の終了を全員へ知らせる締めの合図。**ペイロードは持たない。**
勝者の特別扱いはサーバーが持たないので、クライアントは `PersonalResult.finalRank` に応じて
リザルト演出を分岐する。

## 4. 試合の終了条件

**現在のサーバーは「生存店が1になったときだけ」終わる**（予選仕様のまま）。

> 🔴 **h22 で変わる。** 本戦は `cullSchedule` の最終ステージ（**120秒**）で**全店が同時に脱落**して終わる。
> 実質の制限時間は `cullSchedule` の最終 `atMs`。`matchTimeLimitMs` は契約から削除されたままなので、
> **残り時間は `cullSchedule` から自前で組む**（h22 で `MatchStart.params.cullSchedule` が埋まる）。
> それまでは `cullSchedule` が空配列で届くため、タイムラインUIは組めない。

---

## 5. 落とし穴（実装前に読む）

### 5.1 客は到着順にしか捌けない

行列の先頭以外への `OrderServed` は**黙って捨てられる**。応答が無いのでデバッグしづらい。
`CustomerArrived` の順序を保持し、先頭から処理する。

### 5.2 リジェクトは無応答

サーバーは「不正な `OrderServed` でした」を返さない。
**提供したのに `EvaluationUpdate` が返らなかったら、それがリジェクト**と考えてよい。

### 5.3 客は逃げない・スコアは負になる

我慢ゲージと信用は廃止された（h21）。**一度出たお題は必ず打ち切られる**ので、
「打鍵中に客が消える」割り込み処理は要らない。

`EvaluationUpdate.score` / `PersonalResult.score` は **0 でクランプされていない**。
ミスが多ければ実際に負の値が届く。符号なしで受けたり 0 で潰したりしないこと。

### 5.4 脱落しても接続を切らない

サーバーは脱落した店の接続を保持し、`StoreListUpdate` / `StoreEliminated` / `MatchEnd` を送り続ける
（`internal/room/room.go` は試合終了まで `conns` を閉じない）。**観戦とリザルトのため接続を維持する。**

### 5.5 `clientTimestamp` は現在使われていない

proto のコメントには「同時脱落のタイブレーク等に使用」とあるが、**サーバー実装は読んでいない**。
同順位のタイブレークは **スコア→正確性→速度→storeId** でサーバー側が決めている
（`internal/game/session.go` の `weakerForRank`）。送っても害は無いが、**これに依存した実装をしない**。

### 5.6 `finalRank` は nullable

`StoreSummary.finalRank` は生存店では**キーごと存在しない**。0 として扱わない（§3.10）。

### 5.7 数値をハードコードしない

スコアの重み・演出のしきい値（および h22 以降は `cullSchedule`）は、すべて `MatchStart.params` から取る。
サーバー側は運営UIから試合中でも変更できる（次の試合から反映）。

**ゼロ値で届く廃止フィールドを「設定されていない」と解釈して独自の既定値で補わないこと**
（`initialLife` など）。値が入らないのは仕様で、その機能自体が存在しない。

---

## 6. 疎通のさわり

```bash
websocat wss://takoda99.mooo.com/ws
```

繋いだら即座にこれを1行で送る:

```json
{"type":"MatchmakingJoin","payload":{"displayName":"テスト"}}
```

`MatchmakingStatus` が返れば疎通OK。

**1接続だけで試合を最後まで通したい場合**は、サーバー側で `matching.minPlayers=1` に下げてもらう
（運営UIから数秒で反映・再起動不要）。定員の残りは Bot が埋める。

---

## 7. この文書の更新ルール

- **サーバーの挙動を変える PR は、この文書も同じ PR で直す。** ここが腐ると、クライアント側は
  「動かない理由」を実装から逆算するしかなくなる
- **型が変わったら Proto が先**（人間承認が要る。`AGENTS.md` §1.2）。この文書はそれに追随する
- **タイミングが実装と違っていたら、サーバーの実挙動を正**としてここを直す
- 参照元（ファイル名・関数名）を書く。「サーバーのどこを読んでそう言えるのか」が辿れないと
  次の人が検証できない
- クライアント**側**の設計（どう受けてどう描くか）はここではなく `Takoda99-Client-Docs`
