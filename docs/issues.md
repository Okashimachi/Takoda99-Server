# issue 一覧・タスクボード（ドラフト）

全タスクを GitHub issue に落とす前のドラフト。番号・ラベル・依存・担当・着手可否を整理する。
確定後に `gh` で一括作成する。設計正典は [docs/architecture.md](./architecture.md)。proto 正典は Textro99-Proto v0.1.0。

> 元は企画段階の 04_issue一覧。**層アーキ（game/ports/room/matchmaking）・canonical proto（DakenClearReport 等）・完了分**を反映して更新した。
> **指示書＝この issue 本文**（パッケージ内 README は置かない）。後輩issueは単独で着手できる詳細を本文に持たせる。

---

## 1. ラベル

| ラベル | 意味 |
|--------|------|
| `foundation` | 土台。号砲。これが終わらないと他が動けない |
| `core` | 戦闘の権威（`internal/game`） |
| `spine` | ネットワークとコアを繋ぐ背骨（room/matchmaking/transport） |
| `module` | 差し替え可能な独立部品（後輩が触れる層3） |
| `infra` | デプロイ・検証・環境 |
| `junior-ok` | 後輩が単独で着手できる |
| `external` | server 単独で閉じない（Render/GAS/クライアント等・別担当と合同） |
| `blocked` | 依存未完で着手不可 |

担当は `@りーせ` / `@後輩`。

## 2. マイルストーン

```
M0: 土台凍結     … proto と ports を固める（号砲）        … ✅ 完了
M1: 部品 & 骨格   … 後輩=部品、りーせ=コア＆スパインを並行
M2: 99人検証     … InMemory + Bot で CPU99人試合を成立（核のリスク潰し）
M3: クライアント結合 … Web → Unity と繋ぐ
M4: 本番化       … GAS調整・Render課金・負荷実測
```

---

## 3. issue 一覧

### 済: 基盤（記録）

#### #0 層アーキへのリファクタ ✅完了
- **目的**: コアを不可侵に保つ層構造（コア/継ぎ目/部品）を確立し、後輩が何を書いてもコアが壊れない土台を作る。
- ラベル: `foundation` `core` / 担当: `@りーせ`
- 内容: `rules`→`game`、interfaceを`game/ports.go`へDIP移設、`targeting/odai/config`をports実装へ、`match`廃止。AGENTS.md/.golangci.yml/ci.yml/docs更新。
- 完了: build/vet/test/-race game/golangci 全green。

#### #1 proto（メッセージ契約）v0.1.0 凍結 ✅完了
- **目的**: server/web/unity が同じ形で通信するための唯一の契約を確定する。全リポジトリの結合点であり、全ての号砲。
- ラベル: `foundation` / 担当: `@りーせ`（クライアントと同期）
- 内容: Textro99-Proto に Go/TS/C# を対で定義、tag `v0.1.0`。server は `internal/proto` で薄く再輸出。
- 完了条件: 3言語の型が揃い、クライアントと合意。（public化は #1b で分離管理）

#### #1b Textro99-Proto を public 化する 🔴最優先（他の全てに先行）
- **目的**: server の CI を通す。未完だと後輩が「正しいのに CI 赤」で詰まるため、他の全てに先行して閉じる。
- ラベル: `foundation` `infra` `external` / 担当: `@りーせ`
- 内容: Textro99-Proto (private) を GitHub で public にする。**これをしないと server の CI が private モジュール解決で落ちる**（無認証の GitHub Actions が `go get github.com/Okashimachi/Textro99-Proto@v0.1.0` できない）。ローカルは go.work で解決済み。
- ⚠ **最優先・他の全てに先行**: 未完だと**後輩のPRで `go build ./...` が private 解決で失敗し CI が赤くなる**→「自分のコードは合ってるのにCI赤」で後輩が混乱する。**りーせが今日中に閉じる。**
- 完了条件: server の CI（build / arch-rules）が緑。#1 の「完了✅」に埋もれさせず、独立issueとして明示的にクローズする。

#### #2 game/ports.go（層2 interface）凍結 ✅完了
- **目的**: コアと部品の継ぎ目（interface）を固定し、後輩3件を並行着手可能にする。契約が動くと全員の実装が壊れるため先に凍結する。
- ラベル: `foundation` `core` / 担当: `@りーせ`
- 内容: `TargetingStrategy` / `WordSource` / `ConfigProvider` と付随型を `game/ports.go` に定義（決定A〜G反映）。
- 完了条件: 後輩がこの interface を見て実装を書ける状態。→ **#10/#11/#12 が着手可に。**

---

### M1: 部品（後輩・層3）

> #2 完了済みなので**全て着手可**。PR は担当パッケージ内に閉じる。`internal/game/` は読み取りのみ（編集不可）。
> **推奨着手順: #10 → #11 → #12**。#10 config は普通のサーバー仕事（HTTP取得＆パース）で既存経験が活き、Goの文法に慣れる warmup に最適。#12 targeting は8作戦で一番量が多いので最後。
> ⚠ 前提: **#1b（proto public化）が閉じていないと、後輩の正しいPRでも CI が赤くなる**。#1b を先に閉じること。

#### #10 config: RemoteLoader を実装する 🟢着手可
- **目的**: バランス調整値（コンボ係数・予告猶予・スタック上限など）を**コードから切り離し、ビルド・再デプロイなしで外部から差し替えられる**ようにする。当日「猶予Tを0.2秒短く」等の調整をコードを触らず即反映するのが狙い。⚠ **取得元の方式は未定**：GAS/スプレッドシートでも、簡易 Web アプリでも、静的 JSON ファイルでもよい。「**HTTP で GameParameters の JSON を返せれば何でも可**」で、ここでは決め打ちしない。
- ラベル: `module` `junior-ok` / 担当: `@後輩` / 依存: #2(済)
- **ゴール**: `internal/config/remote.go` の `RemoteLoader.Load` を完成させる。interface・DefaultLoader・スタブは用意済み。
- **実装する interface**（コア所有・`game/ports.go`）:
  ```go
  type ConfigProvider interface { Load(ctx context.Context) (game.GameParameters, error) }
  ```
- **入力**: `l.URL`（GameParameters を JSON で返す HTTP エンドポイント。**何がそれを返すかは問わない**＝GAS/Webアプリ/静的ファイル等、方式未定）。JSON形は `game.GameParameters` の json タグそのまま（ネスト構造も同じ。`game/params.go` 参照）。
- **受け入れ条件**:
  | 状況 | 第1返り値 | 第2返り値 |
  |---|---|---|
  | 取得&パース成功 | 取得値 | nil |
  | HTTP失敗(接続不可/200以外/timeout) | `game.DefaultParameters()` | 理由を包んだ err |
  | JSON破損 / 必須異常(例 stack.limit<=0) | `game.DefaultParameters()` | 理由を包んだ err |
  - **鉄則**: 第1返り値は常に有効（err時もデフォルト＝起動を止めない）。`ctx` を尊重（`http.NewRequestWithContext`）。検証は最小限（`stack.limit>0` と `difficulty.maxLevel>0`）でよい。
- **触ってよい/ダメ**: ✅`internal/config/` / 📖`internal/game/params.go`(読取のみ) / ❌他パッケージ・`.golangci.yml`・`go.mod`
- **テスト**: `httptest.NewServer` で ①正常JSON→値 ②500→デフォルト+err ③壊れJSON→デフォルト+err。既存 `TestRemoteLoader_StubFallsBackToDefaults` は実装したら正常系へ差し替え、`ErrNotImplemented` は**削除すること**（未使用の残骸を残さない）。
- **完了条件**: `go test ./internal/config/... && go build ./... && golangci-lint run` 通過。PR は config 内に閉じる。

#### #11 odai: プレースホルダ辞書を用意する 🟢着手可
- **目的**: プレイヤーが実際に打つ「お題（文字列）」を難易度段階別に供給する。テーマ（寿司）が未確定でもゲームを動かせるよう、まずダミーの単語辞書で先行して埋める（後で本物に差し替え）。
- ラベル: `module` `junior-ok` / 担当: `@後輩` / 依存: #2(済)
- **ゴール**: `internal/odai/data.go` の `placeholderWords()`/`placeholderTraps()` を段階0〜10＋トラップまで埋める。供給メカニズム（`StaticPool`＝`game.WordSource`実装）は用意済み。
- **実装する interface**（コア所有）:
  ```go
  type Word struct { Text string; KeystrokeCount int }
  type WordSource interface { Next(effectiveLevel int, rng *rand.Rand) Word; NextTrap(rng *rand.Rand) Word }
  ```
- **やること**: `placeholderWords()` の map を段階0〜10すべて（**各段階 最低5語**、上がるほど長く・濁音/促音/拗音、後半は記号/数字混じり）。`placeholderTraps()` に煽り長文を**最低3個**。各語の `KeystrokeCount` は**正準ローマ字打鍵数**（例 "がっこう"→"gakkou"=6）。
  - **ローマ字の数え方（表記揺れを固定）**: し=si(2) / ち=ti(2) / つ=tu(2) / ふ=hu(2) / じ=zi(2) / しゃ=sya(3)。促音は子音重ね（っこ=kko）、撥音 ん=n(1)。**この最短系で一貫して数える**（迷ったら最短）。
  - **記号は当面使わない**（ひらがな・カタカナ・英数字まで）。もし使う場合は 1文字=1打鍵 で数える。段階8〜10の難化は「長さ・濁音/半濁音/促音/拗音・カタカナ/英数字混じり」で付ける（記号で悩まない）。
  - ⚠ テーマ（寿司）変更予定なので**ダミー文言でOK**。大事なのは難易度の傾斜と打鍵数。本物のローマ字テーブルは後日 proto 共有データから来る（当面は手数え概算）。
- **触ってよい/ダメ**: ✅`internal/odai/`（中心は`data.go`）/ ❌`pool.go`のinterface前提・他パッケージ
- **完了条件**: 段階0〜10網羅（各5語以上）・トラップ3個以上。`go test ./internal/odai/... && go build ./... && golangci-lint run` 通過。**打鍵数の妥当性はりーせがレビューで確認**（機械テスト対象外。目安: 表示文字数×1.5〜2 程度）。

#### #12 targeting: 作戦0,1,2,5,6,7,8,9 を実装する 🟢着手可
- **目的**: 攻撃時に「誰を狙うか」を作戦別に決めるロジック。テトリス99的な駆け引き（狙われたら撃ち返す/瀕死を狩る/強者を叩く/ヘイト分散など）の中身をここで作る。作戦を増やしてもコアは無変更（1作戦1ファイルの追加式）。
- ラベル: `module` `junior-ok` / 担当: `@後輩` / 依存: #2(済)
- **ゴール**: `game.TargetingStrategy` を1作戦1ファイルで実装。**参考実装 作戦4(`random.go`)・作戦3(`badge.go`) が入っている**ので、残り8つを同型で書く。
- **実装する interface**（コア所有）:
  ```go
  type TargetingStrategy interface { Id() int; SelectTargets(ctx TargetingContext) []game.PlayerId }
  ```
  入力 `TargetingContext{ SelfId, Alive []PlayerView, PendingAttackers []PlayerId, LastImpactorId *PlayerId, Rng }`。
  `PlayerView{ PlayerId, ComboValue, DakenStackCount, DakenStackLimit, BadgeCount, IncomingWarnings }`。
  `ctx.Others()`＝自分以外、`PickRandomOther(ctx)`＝乱択1名（フォールバック用）。
- **不変条件**: 空=不発。**1〜9は0/1件**、複数件を返してよいのは作戦0だけ。威力に触れない。
- **10作戦の仕様**:
  | id | 作戦 | アルゴリズム | 不成立/フォールバック |
  |---|---|---|---|
  | 0 | 全体割り | `ctx.Others()` 全員のID | 他にいない→空 |
  | 1 | カウンター | `PendingAttackers[0]`（最新の予告主） | 予告なし→`PickRandomOther` |
  | 2 | とどめ | Others で stack比率(count/limit)最大（同値ランダム。比較はクロス乗算でゼロ除算回避） | Others空→空 |
  | 3 | バッジ狙い | Others で BadgeCount 最大（同値ランダム）※実装済 | Others空→空 |
  | 4 | ランダム | `PickRandomOther`※実装済 | Others空→空 |
  | 5 | リベンジ | `LastImpactorId` がまだ生存なら1名 | nil/脱落済→`PickRandomOther` |
  | 6 | 出る杭 | Others で ComboValue 最大（同値ランダム） | Others空→空 |
  | 7 | 隣狙い | Alive を PlayerId 昇順ソートし自分の次（末尾は先頭へラップ） | 生存2人未満→空（隣＝ラップ対象がいないため。7だけランダムにフォールバックしない） |
  | 8 | 巻き添え | Others で IncomingWarnings 最大（同値ランダム）。最大が0なら該当なし | 全員0→`PickRandomOther` |
  | 9 | 平和主義 | Others のうち IncomingWarnings==0 から乱択 | 該当なし→`PickRandomOther` |
  - ファイル名例: `split.go`(0) `counter.go`(1) `finisher.go`(2) `revenge.go`(5) `tallpoppy.go`(6) `neighbor.go`(7) `pileon.go`(8) `pacifist.go`(9)。型名は用語集の英名。
- **触ってよい/ダメ**: ✅`internal/targeting/`（1作戦1ファイル追加）/ ❌`strategy.go`のRegistry/ヘルパ定義・他パッケージ
- **完了条件**: 8作戦＋各テスト（狙い通り＋フォールバック/不成立）。**作戦0のテストだけは複数件が返ることを確認**（他8つのコピーだと甘くなる）。`go test ./internal/targeting/... && go build ./... && golangci-lint run` 通過。

---

### M1: コア（りーせ・`internal/game`）

#### #20 game.Session 状態機械（Tick(dt)）
- **目的**: 1試合を進める「頭脳」。お題クリア→コンボ確定→次のお題、攻撃解決、脱落、優勝までの生存ループを回す心臓部。すべての戦闘判定がここに集約され、外からは `Tick(dt)` と入力だけで駆動される（純粋＝テスト/シミュレーション可能）。
- ラベル: `core` / 担当: `@りーせ` / 依存: #2(済)
- 内容: `WaitingStart→Running→Finished`。step関数 `ApplyDakenClear/ApplyAttack/ApplyStrategy` と `Tick(dtMs)` が `[]Outbound`（宛先つき proto メッセージ）を返す純粋実装。per-player 状態（combo/stack/personalLevel/strategy/dakenId台帳/pendingAgainstMe/lastImpactor）を保持。Clock は持たない（room が dt を渡す）。
- 完了条件: 状態遷移・step関数の単体テスト（`docs/architecture.md` §6 準拠）。

> combo(#0で実装済)に続く戦闘ロジックを機能ごとに4分割（1 issue = 1完結）。全て決定A〜G・単位フロー(combo→power→count)遵守、数値は GameParameters 経由。
> **#21a / #21c / #21d は並行着手可、#21b は #21a 後**（相殺は威力算出に依存するため）。

#### #21a game: attack（威力算出・威力→ダケン個数変換）
- **目的**: 消費コンボを攻撃の「威力」に変換し、さらに相手に積むダケン個数へ変換する。コンボ(数百)がそのまま個数(上限20)にならない**単位変換の要**で、ここが無いと即KOで破綻する。
- ラベル: `core` / 担当: `@りーせ` / 依存: #2(済)
- 内容: 基礎威力=consumedCombo×comboToPowerRatio、バッジ倍率=1+min(perBadge×badge, cap)、最終威力=floor(基礎×倍率)。威力→個数=floor(残余威力×powerToDakenRate)。
- 完了条件: `go test ./internal/game/...` グリーン。計算例(04パラメータ仕様9章)と一致。

#### #21b game: offset（相殺・撃ち返し連鎖）
- **目的**: 攻撃を「予告→相殺」で受け止める防御と、相殺しきれた余剰を反撃に回す撃ち返しを成立させる（ぷよぷよ通の相殺システム）。防御も攻撃と同じ「正確に打つ」行為に帰着させるのが狙い。
- ラベル: `core` / 担当: `@りーせ` / 依存: #21a
- 内容: 相殺は威力単位(n vs m)、残余威力だけを個数変換。余剰は撃ち返し(現作戦で再ターゲティング)。**maxReboundChain 上限で連鎖を必ず止める**(超過は消失)。相殺余剰の対象不成立時は消失(決定A)。
- 完了条件: 完全/部分相殺・撃ち返し・連鎖上限の単体テスト。無限ループしないこと。

#### #21c game: stack（スタック・トラップ誘発・脱落）
- **目的**: 被弾の蓄積＝ダケンスタック（＝図の「HP」に相当。溜まって上限で脱落）と、劣勢を加速させるトラップダケンを管理する。勝敗＝脱落を確定させる部分で、単体でバグりやすいので独立issue化してテストを厚くする。
- ラベル: `core` / 担当: `@りーせ` / 依存: #2(済)
- 内容: スタック増減、**トラップ誘発=ハイウォーターマーク方式(trapMilestone 整数1個)**、トラップミスペナルティ(+trapMissPenalty)、時間切れ積み残しの合流、上限到達で脱落確定。**単体でバグりやすいのでテストを厚く**。
- 完了条件: ハイウォーターマークの往復ケース(3→6→4→7…で連発しない)を含むテストがグリーン。

#### #21d game: difficulty（全体/個人難易度）
- **目的**: 時間経過（全員共通の圧）と個人のコンボ量に応じて出題難易度を上げる。「逃げ切り」や「無限コンボ」を防ぎ、終盤ほど純粋な実力勝負になる緊張カーブを作る。
- ラベル: `core` / 担当: `@りーせ` / 依存: #2(済)
- 内容: 全体難易度(globalIntervalMs毎+1)、個人コンボ連動(personalDifficultyStep/MaxLevel)、実効=min(全体+個人, maxLevel)。ダケン制限時間=base−perLevel×level(下限min)。
- 完了条件: 合成・クリップ・制限時間算出の単体テスト。

---

### M1: スパイン（りーせ）

#### #30 transport: Connection interface と WebSocket 実装
- **目的**: クライアントが実際にサーバーへ繋がり、メッセージを送受信できる通信の入口。以降の全機能の"線"を通す。
- ラベル: `spine` / 担当: `@りーせ` / 依存: #1(済)
- 内容: `Connection{ Send(proto.Envelope) error; Receive() <-chan proto.Envelope; Close() error }` を定義、coder/websocket で実装。送受信・切断検知。

#### #31 transport: InMemory Connection
- **目的**: 実ソケット無しで Bot や負荷検証を回すための擬似接続。実クライアント完成を待たず 99人検証(#50)を可能にする土台。
- ラベル: `spine` / 担当: `@りーせ` / 依存: #30
- 内容: 実ソケット無しの疑似接続。Bot・負荷検証用。#50a/#50b の土台。

#### #32 transport: StatePublisher（間引き配信）
- **目的**: 99人分の状態を帯域破綻させずに配信する（毎tick全員へ全員分＝O(99×99)を避ける）。99人スケールの最大の技術リスク対策。
- ラベル: `spine` / 担当: `@りーせ` / 依存: #30
- 内容: `PlayerListUpdated`/`PlayerListDelta` を自分＋周辺＋上下で間引き配信。まずフル配信、後で間引きに強化。差し替え可能に。

#### #33 room: tick ループと試合の器
- **目的**: tick で試合を実際に回す駆動役。頭脳(session)に「時間」と「入力」を与える背骨。状態変更を単一goroutineの1箇所に集約し、99人同期をシンプルに保つ。
- ラベル: `spine` / 担当: `@りーせ` / 依存: #20, #30
- 内容: 1試合=1goroutine＋1channel。Clock/Ticker 注入（本番=実時間/sim=fake）。inbox(client cmd)＋tick を直列処理し `session.Apply*`/`session.Tick` を呼び、`Outbound` を接続へ配信。tick周期は `session.tickIntervalMs`。

#### #34 matchmaking: 待機プール・人数・Bot補完
- **目的**: モード選択→人数待ち→カウントダウンで試合を成立させ、足りない枠を Bot で埋める。ハッカソン規模の「人が集まらず試合が始まらない」リスクへの対策。
- ラベル: `spine` / 担当: `@りーせ` / 依存: #33
- 内容: 人数下限＋カウントダウン方式（`matching.*`）。ソロ/バトロワ編成。定員不足を Bot 補完。`MatchmakingStatus` 配信。

#### #35 bot: BotController
- **目的**: CPUが自動で打って攻撃する。人数補完・デモ表示・切断補完・99人負荷検証の"燃料"になる（人間と同じ接続として扱うのが設計の肝）。
- ラベル: `spine` `module` / 担当: `@りーせ`（#50 のクリティカルパス上のため後輩に渡さない）/ 依存: #2, #33
- 内容: tick毎にCPUの行動（DakenClearReport相当・AttackRequest）を生成。疑似打鍵速度・ミス率。**サーバー発行の実在 dakenId に整合**させる（チート検証と衝突させない）。人間と同じ Connection(InMemory) に乗せ session から区別しない。

#### #36 切断フォールバック
- **目的**: 途中で誰かが切断しても試合が虫食い/停止しないよう、枠を Bot に引き継いで継続する（初期は「即脱落」で割り切ってもよい）。
- ラベル: `spine` / 担当: `@りーせ` / 依存: #34, #35
- 内容: 切断検知→枠を Bot 制御へ切替え試合継続（初期は「即脱落」で割り切り可）。

#### #37 store: ResultStore interface と Noop 実装
- **目的**: 将来のランキング等のための永続化の差し込み口だけ先に用意する（今は何もしない Noop。DBが必要になったら実装を差し替える）。
- ラベル: `module` / 担当: `@りーせ` / 依存: なし
- 内容: 永続化の口だけ。今は Noop。

#### #38 合成ルート（cmd/server/main.go）
- **目的**: 全部品を1箇所で配線し、`--mode` で起動できる"実行可能なサーバー"にする組み立て役。差し替え（どの実装を注入するか）を知るのはここだけ。
- ラベル: `spine` / 担当: `@りーせ` / 依存: スパイン一式
- 内容: 全部品を配線。config→params ロード、targeting.Registry/odai.StaticPool を game.Session に注入、room に載せる。`--mode solo`（マッチング迂回）/`--mode match`。

---

### M0/M1: インフラ

#### #40 Render に Go サーバーの空箱をデプロイ
- **目的**: 本番環境(Render)で WebSocket が通ることを最小構成で先に確認し、デプロイ経路のリスクを早期に潰す。
- ラベル: `infra` `external` / 担当: `@りーせ` / 依存: なし
- 内容: 最小 Go サーバーを Render にデプロイ、WebSocket ping/pong 疎通。

#### #41 WebGL ⇄ サーバー 疎通確認（クライアントと合同）
- **目的**: WebGLビルドからサーバーに繋がるか（クライアント側の最重要技術リスク）を早期に潰す。
- ラベル: `infra` `external` / 担当: `@りーせ`＋クライアント / 依存: #40
- 内容: WebGLビルドからの WebSocket 接続最小検証。クライアント側の最重要技術リスク、早期に潰す。

---

### M2: 99人検証

> **段階化**: まず数体で最小成立させ、リスクを早期発見してから99体へスケールする。

#### #50a Bot 数体で試合が最小成立する（入口・早期リスク潰し）
- **目的**: コア＋スパインの結線バグを、数体の小さな試合で早期に発見する（99人に上げる前の安全確認）。
- ラベル: `infra` `core` `spine` / 担当: `@りーせ` / 依存: #20,#21a-d,#31,#33,#35（#11/#12 は暫定ダミーで代替可）
- 内容: InMemory+Bot 2〜10体で1試合が開始→攻防→脱落→順位確定まで走る。**小さく回してコア/スパインの結線バグを早期に発見**。マッチング(#34)・間引き(#32)はまだ不要。
  - **後輩成果物(#11/#12)の完成を待たない**: 最小ダミー（単語数個・作戦は RandomStrategy 1個）で先に結線検証してよい。これでクリティカルパスから後輩依存を外せる。
- 完了条件: Bot 数体の試合が最後まで走り順位が確定する。

#### #50b 99人スケールで成立させる ★最重要
- **目的**: 企画の核「99人が実際に動く」を Bot だけで証明する。ここが通れば企画が成立する最重要マイルストーン。
- ラベル: `infra` `core` `spine` / 担当: `@りーせ` / 依存: #50a,#32,#34
- 内容: #50a を99体へスケール。配信負荷(#32間引き)・マッチング(#34)込みで最後まで。後輩の #11(お題)・#12(作戦) がここで組み込まれる。
- 完了条件: 99人試合が最後まで走り順位確定。Render 本番スペックでも回る。**企画の核（99人が動く）の証明。**

---

### M3: クライアント結合

#### #60 Web フロントと結合
- **目的**: 実クライアント(Web)と繋ぎ、人間が複数人で実際に遊べる状態にする。
- ラベル: `spine` `external` / 担当: `@りーせ`＋クライアント / 依存: #50b
- 内容: 実 WebSocket で Web テストフロントと結合。複数人テスト。

#### #61 Unity と結合
- **目的**: 本番クライアント(Unity)と繋ぐ。Web で確立した接続構造を C# でミラーして流用する。
- ラベル: `spine` `external` / 担当: `@りーせ`＋クライアント / 依存: #60

---

### M4: 本番化

#### #70 StatePublisher を間引き実装に強化
- **目的**: 負荷実測を踏まえて状態配信を間引き、99人でも軽く動くようにする（初期のフル配信からの強化）。
- ラベル: `spine` / 担当: `@りーせ` / 依存: #50b
- 内容: 負荷が見えてからフル配信→間引き。閾値を実測で決める。

#### #71 外部設定でバランス調整
- **目的**: 予告猶予T・火力係数などを実測しながら調整し、1試合5〜10分・面白い攻防に寄せる。**設定ソースの方式は未定**（#10 で採用したリモート設定＝GAS/Webアプリ/静的ファイル等に準ずる）。
- ラベル: `infra` `external` / 担当: `@りーせ`＋プランナー / 依存: #10, #50b
- 内容: 予告猶予T・コンボ係数等を外部設定（#10 のリモート設定ソース）経由で調整。GameParameters を書き換えるだけでビルド不要。

#### #72 Render を Starter 以上に上げてスリープ無効化・負荷実測
- **目的**: 本番前に Render を課金プランにしてスリープを無効化し、本番スペックで99人の負荷を実測する。
- ラベル: `infra` `external` / 担当: `@りーせ` / 依存: #50b

---

## 4. 依存グラフ

```
#1 proto(済) ─┬─→ #30 transport ─→ #31 InMemory ──────┐
#1b public化   │                    #32 Publisher ──────┼──┐
#2 ports(済) ─┼─→ #10 config(後輩・着手可)             │  │
              ├─→ #11 odai(後輩・着手可) ──────────────┤  │
              ├─→ #12 targeting(後輩・着手可) ──────────┤  │
              ├─→ #20 session ─→ #33 room ─→ #35 bot ───┤  │
              └─→ #21a→#21b / #21c / #21d 戦闘ロジック ─┤  │
                                                          ↓  │
                              #50a Bot数体で最小成立 ─────────┤
                                          ↓  #34 matchmaking ─┘
                                    #50b 99人スケール ─→ #60/#61 結合 ─→ #70-72 本番化
```

- #1・#2 は済（号砲は鳴った）。ただし **#1b（proto public化）を閉じないと CI が落ちる**。後輩 #10/#11/#12 は互いに独立で即着手可。
- #50a（数体）で早期にリスクを潰し、#50b（99体）で配信負荷(#32)・マッチング(#34)込みのスケールを証明する。

## 5. 運用ルール

- 1 issue = 1 PR = 1完結タスク。PR は担当パッケージ内に閉じる。
- 後輩PRは `internal/config` `internal/odai` `internal/targeting` のいずれかに閉じる。`internal/game` を触る後輩PRは差し戻し。
- proto（Textro99-Proto）を変えたい時はクライアント含め通知＋りーせ承認。
- `go test ./internal/game/...` が落ちるPRは CI が自動で弾く（コアの安全保証）。
- **詰まったら即りーせに聞く（抱え込まない）。目安: 30分詰まったら相談。** 自走を求めすぎず、早く相談する方が全体が速い。CIが赤い時はまず #1b（proto public化）が閉じているか確認。
