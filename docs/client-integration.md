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

**裏取り時点**: `4ee201c`（2026-08-05）/ proto **v0.3.0**。参照元のファイル名・関数名を各所に明記してある。

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
  │  ├─← CustomerLeft → CreditUpdate  ← 我慢切れ（必ずこの順・同tick）
  │  ├─← EvaluationUpdate         ← 毎tick・生存店それぞれへ
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

## 3. S2C — サーバーから届くもの（12種）

宛先の「自分」＝その店だけに届く、「全員」＝試合参加者全員に同じものが届く。

| メッセージ | 宛先 | いつ | 頻度 |
|---|---|---|---|
| `MatchmakingStatus` | 待機者 | 待機中 | 1秒ごと＋カウントダウン開始時 |
| `MatchStart` | 自分 | 試合開始 | 1回 |
| `CustomerArrived` | 自分 | 客が自店の行列に入った | 不定 |
| `CustomerLeft` | 自分 | 我慢切れで帰った | 不定 |
| `CreditUpdate` | 自分 | 信用が減った | `CustomerLeft` の直後 |
| `EvaluationUpdate` | 自分 | 毎tick ＋ 提供成功の即レス | **高頻度** |
| `DifficultyUpdate` | 全員 | `heatLevel` が**変化したとき** | 低頻度 |
| `PhaseChange` | 全員 | フェーズ移行 | 試合中2回 |
| `ForcedEliminationWarning` | 生存店 | storm 予告 | 1周期に1回 |
| `StoreEliminated` | 全員 | 脱落確定 | 98回 |
| `StoreListUpdate` | 全員 | 一定間隔 | 250ms ごと |
| `MatchEnd` | 全員 | 試合終了 | 1回 |

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
  "params": { "initialLife": 3, "maxStores": 99, "stormThresholdPct": 0.1,
              "finalStageAliveThreshold": 20, "finalRushAliveThreshold": 10 },
  "phase": "Early",
  "stores": [ { "storeId": "p-1", "displayName": "…", "evalNormalized": 0,
                "rank": 0, "creditLife": 20, "alive": true }, … ]
}
```

> 上の値は**例**。運営UIから変更できるので、**必ず受信値を使う**（§5.7）。

- `selfStoreId` で `stores[]` の中の自分を特定する
- **`params` の値を表示に使う。** 信用ゲージの最大値は `initialLife`、順位バーの淘汰圏は `stormThresholdPct`
- `finalStageAliveThreshold` / `finalRushAliveThreshold` は**演出の切り替え専用**。ゲーム進行には影響しない
- **`patienceLateMul`**（既定 0.6）— **Late 以降は我慢ゲージが `1/patienceLateMul` 倍速で減る**。
  `patienceMaxMs` は書き換わらず**減る速度だけ**が変わり、行列内の来店済みの客にも即座に効く。
  **これを使わないと Late 突入以降ゲージが約1.67倍ズレる**（§3.3 も参照）
- `patienceAlertMs`（既定 2000）— 「もうすぐ帰る」警告へ切り替える残り時間。**表示専用**でサーバーは判定に使わない

### 3.3 `CustomerArrived`（= `CustomerView`）

```json
{ "customerId": "c-42", "attribute": "Normal", "orderCount": 2,
  "words": ["たこやき", "おおきに"], "patienceMaxMs": 16000,
  "patienceStartedAtServerMs": 12750 }
```

| フィールド | 備考 |
|---|---|
| `attribute` | `Normal` / `Bonus` / `Claimer` / `Buzz`。**試合中不変**。初回のみ配られる |
| `orderCount` | = `words` の長さ |
| `words` | サーバー発行のお題。**ひらがな**（現在の辞書） |
| `patienceMaxMs` | 我慢ゲージの最大値 |
| `patienceStartedAtServerMs` | **我慢が減り始めたサーバー時刻**（試合開始からの経過ms） |

**★我慢ゲージは `patienceStartedAtServerMs` を起点に描く。** 受信時刻を起点にすると、受信遅延ぶん
そのままズレる。サーバーの経過時刻は tick の積算なので、`MatchStart` 受信時を 0 として自前で進めた
時計と突き合わせればよい。

**★行列に入った瞬間から減る。** 対応中（先頭）の客だけでなく、**待っている客も同時に減っている**。
これが「行列を溜めること自体のコスト」。

**★Late フェーズでは減る速度が変わる。** `dt / patienceLateMul`（既定 0.6 → 約1.67倍速）で減る。
`patienceMaxMs` は書き換わらないので、**線形にカウントダウンすると Late 以降ズレ続ける**。
`PhaseChange` で Late を知ったら、そこから先は倍率を掛けて進めること。

### 3.4 `CustomerLeft` → `CreditUpdate`

```json
{ "customerId": "c-42", "reason": "Timeout" }
{ "life": 18, "delta": -2, "reason": "CustomerLeft" }
```

**必ずこの順で、同じ tick に届く。** 信用は離脱でのみ減り、**回復はしない**。
`delta` は属性ごとに違う（現在 Normal/Bonus/Claimer が -1、Buzz が -2）。

### 3.5 `EvaluationUpdate`

```json
{ "evalRaw": 0.72, "normalized": 0.83, "rank": 17, "aliveCount": 64,
  "starRating": 4.18, "starDelta": 0.21 }
```

| フィールド | 意味 |
|---|---|
| `normalized` | **生存店内**のパーセンタイル 0..1。分配重み・下位淘汰はこれを使う |
| `rank` | 生存店内の順位（1が最上位） |
| `starRating` | **99店全体**を母集団にした表示専用の星 0..5 |
| `starDelta` | 前回配信からの増減。「★+0.2」の演出用 |

**`normalized` と `starRating` は別物。** 星は表示専用で、ゲーム進行には使われない。
**星をクライアントで計算しない**（プレイヤーごとに違う星が見えてしまう）。

届くのは2系統:
1. **毎tick**（`stepNormalize`）— 生存店それぞれへ
2. **`OrderServed` の成功レスポンス** — 提供直後の演出用に即座に1通

### 3.6 `DifficultyUpdate`

```json
{ "heatLevel": 7 }
```

**値が変化したときだけ**届く。毎tick来ると思って組まないこと。
お題の難度段階で、現在の辞書は **0〜17**。

### 3.7 `PhaseChange`

```json
{ "phase": "Mid" }
```

`Early` → `Mid` → `Late`。**戻らない。** 生存数と経過時間のどちらか先に成立した方で移行する。

フェーズが変えるもの: Claimer の解禁（Early は来ない）／火力の加算／**Late は我慢ゲージが速く減る**。

### 3.8 `ForcedEliminationWarning`

```json
{ "untilTick": 30, "thresholdPct": 0.1, "selfAtRisk": true }
```

storm（下位淘汰）の予告。**1周期に1回だけ**届く。

- `untilTick` は**実行までの残りtick数**。秒に直すなら `tickIntervalMs` を掛ける
- **`selfAtRisk` は店ごとに違う**（だから全体配信ではなく個別配信）
- **自分が対象かをクライアントで判定しない。** `selfAtRisk` をそのまま使う

### 3.9 `StoreEliminated`

```json
{ "storeId": "p-42", "reason": "Cull", "finalRank": 63 }
```

`reason` は `SelfCollapse`（信用0の自滅）か `Cull`（下位淘汰）。

- **自店なら** → リザルトへ遷移。ただし**接続は切らない**（§5.4）
- **他店なら** → ミニ盤面の更新

### 3.10 `StoreListUpdate`

```json
{ "stores": [ … 99店ぶん … ], "aliveCount": 64 }
```

**tick とは別系統**で、現在 **250ms ごと**に全員へ全店分が届く。

`StoreSummary.finalRank` は**脱落済みの店にだけ入る**（生存店では**キーごと出ない**）。

> ⚠ **欠落を 0 として扱わないこと。** 順位0は存在しない。
> C# の `int` で受けると 0 になるので、**nullable（`int?`）で持つ**。

### 3.11 `MatchEnd`

```json
{
  "finalRank": 1,
  "reason": "",
  "matchElapsedMs": 145000,
  "creditLeft": 8,
  "evalRaw": 0.72,
  "evalNormalized": 1,
  "stats": {
    "servedCount": 34, "avgAccuracy": 0.94, "avgElapsedMs": 3100,
    "leftCount": 6, "totalKeystrokes": 1180, "totalMisses": 71,
    "fastestMs": 2100, "slowestMs": 5400,
    "normal":  { "served": 24, "left": 4 },
    "bonus":   { "served": 6,  "left": 1 },
    "claimer": { "served": 2,  "left": 1 },
    "buzz":    { "served": 2,  "left": 0 }
  }
}
```

**脱落済みの店にも届く。** 最終順位は**脱落順のみ**で決まる（評価は使わない）。

| フィールド | 意味 |
|---|---|
| `finalRank` | 最終順位。1が優勝 |
| `reason` | `SelfCollapse`（信用0）/ `Cull`（下位淘汰）。**優勝ならキーごと出ない** |
| `matchElapsedMs` | 試合の総経過時間。**途中で脱落しても試合が終わるまでの時間**が入る |
| `creditLeft` | 終了時点の残り信用。自滅なら 0 |
| `evalRaw` / `evalNormalized` | 最終評価。**順位計算には使われない**表示用の値 |

`stats` は**自店ぶんのみ**。

| フィールド | 意味 |
|---|---|
| `servedCount` / `leftCount` | 捌けた客 / 我慢切れで帰られた客 |
| `avgAccuracy` | **客ごとの精度の平均**（0..1） |
| `totalKeystrokes` / `totalMisses` | 打鍵の生の合計。**「全体で何打鍵中いくつミスしたか」はこちらで出す**（客ごとに打鍵数が違うので `avgAccuracy` からは出せない） |
| `avgElapsedMs` / `fastestMs` / `slowestMs` | 1客を捌くのに要した平均・最短・最長。提供0なら全て 0 |
| `normal` / `bonus` / `claimer` / `buzz` | 属性別の `{ served, left }` |

属性別の合計は全体と一致する（`normal.served + bonus.served + … == servedCount`）。

> **★最大コンボはサーバーから返らない。** サーバーは**打鍵列を受け取らない**（`OrderServed` は
> 客1人ぶんの `elapsedMs` と `missCount` だけ）ので、連続無ミス数を知る手段が無い。
> 加えて「コンボ」は企画転換で概念ごと廃止されている。
> **リザルトに出すならクライアント側で自前に数えること。**

> **全店の最終順位表は `MatchEnd` に入らない。** `StoreListUpdate` の最後のスナップショットに
> 99店分が `finalRank` 込みで入っているので、それを保持しておけば順位表を描ける。

---

## 4. 試合の終了条件

**生存店が1になったときだけ。制限時間は無い**（proto v0.3.0 で `matchTimeLimitMs` は削除された）。

**残り時間のUIを作らないこと。** 決着は storm（下位淘汰）が保証する。

---

## 5. 落とし穴（実装前に読む）

### 5.1 客は到着順にしか捌けない

行列の先頭以外への `OrderServed` は**黙って捨てられる**。応答が無いのでデバッグしづらい。
`CustomerArrived` の順序を保持し、先頭から処理する。

### 5.2 リジェクトは無応答

サーバーは「不正な `OrderServed` でした」を返さない。
**提供したのに `EvaluationUpdate` が返らなかったら、それがリジェクト**と考えてよい。

### 5.3 待機中の客も我慢が減っている

先頭だけではない。行列に3人いれば3人とも減る。UI もそう描くこと。

### 5.4 脱落しても接続を切らない

サーバーは脱落した店の接続を保持し、`StoreListUpdate` / `StoreEliminated` / `MatchEnd` を送り続ける
（`internal/room/room.go` は試合終了まで `conns` を閉じない）。**観戦とリザルトのため接続を維持する。**

### 5.5 `clientTimestamp` は現在使われていない

proto のコメントには「同時脱落のタイブレーク等に使用」とあるが、**サーバー実装は読んでいない**。
同時脱落のタイブレークは残信用→評価→提供数→精度→id の総合判定でサーバー側が決めている。
送っても害は無いが、**これに依存した実装をしない**。

### 5.6 `finalRank` は nullable

`StoreSummary.finalRank` は生存店では**キーごと存在しない**。0 として扱わない（§3.10）。

### 5.7 数値をハードコードしない

信用の最大値・淘汰圏の割合・演出のしきい値は、すべて `MatchStart.params` から取る。
サーバー側は運営UIから試合中でも変更できる（次の試合から反映）。

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
