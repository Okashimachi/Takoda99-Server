# takoda99-server アーキテクチャ設計

サーバー（Go）の内部構造。層アーキで「変わるもの／変わらないもの」を分離し、コア（試合の権威）を守る。
本書は AGENTS.md の責務・依存ルールを具体化した設計正典。用語は Takoda99-Docs と canonical proto（Takoda99-Proto v0.2.0+）に準拠する。

---

## 1. 設計目標

1. 変わりうる箇所を差し替え可能に（マッチング・人数・お題・通信方式・永続化・調整値）
2. 後輩が触る箇所を隔離し、コアに影響させない
3. 追加・変更を容易に。コアは触らない（Open/Closed）
4. SOLID／DIP を Go のイディオム（暗黙 interface）で実現

要は「**変わらないもの（コア）を、変わるもの（部品）から守る**」。

---

## 2. 層構成

```
┌───────────────────────────────────────────────┐
│ 層3: 差し替え部品（機能追加はここ）              │ ← 後輩の主戦場
│   odai / config / bot / store / db             │
├───────────────────────────────────────────────┤
│ 層2: 継ぎ目 = interface（game/ports.go, DIP）    │ ← りーせが定義・凍結
│   WordSource / ConfigProvider                   │
├───────────────────────────────────────────────┤
│ 層1: コア game（試合の権威）不可侵               │ ← 誰も編集しない
│   session / params / ports                      │
└───────────────────────────────────────────────┘
    スパイン（背骨・りーせ）: room / matchmaking / transport / configapi / admin
    契約: proto（canonical Takoda99-Proto の薄いラッパ）
```

- **層1 game**：ゲームのルールそのもの。`Tick(dt)` で進む純粋ロジック。I/O・時間・通信を持たない。
- **層2 ports**：コアが外部に要求する約束。**コアが所有**（DIP）。りーせが凍結。
- **層3 部品**：ports を満たす具体実装。ここをいじってもコアは無事。後輩の担当。
- **スパイン**：コアをネットワークに繋ぎ実際に試合を回す統合部（層に属さない特別枠）。りーせが持つ。

---

## 3. パッケージ構成

```
cmd/server/main.go        合成ルート。全部品を配線（--mode solo/match）

internal/
  game/     [層1コア]  試合の権威。純粋・Tick(dt)駆動
    session.go          1試合の状態機械。客レジストリ/行列/たべたべエリア/店舗状態と tick ループ
    params.go           GameParameters（全調整値）+ ConfigHash
    ports.go  [層2]     コアが要求する interface（DIP）: WordSource / ConfigProvider
  odai/      [層3]  お題供給。game.WordSource を実装（StaticPool / ConfigurablePool）
  config/    [層3]  調整値取得。game.ConfigProvider を実装
  db/        [層3]  Postgres（game_config / words / match / store_result）
  bot/       [層3]  CPUの自動入力（OrderServed を内部生成）
  store/     [層3]  試合結果永続化の口（Noop / db.ResultStore）
  room/      [スパイン]  tickループ駆動。session.Tick を回し入力適用・配信
  matchmaking/ [スパイン]  待機プール・人数下限＋カウントダウン・Bot補完
  transport/ [スパイン]  Connection(WS/InMemory) ＋ StatePublisher(間引き)
  configapi/ [スパイン]  config-front 用 REST API（/api/params, /api/words）
  admin/     [スパイン]  観測配信。AdminHub（読み取り専用ファンアウト）＋ /admin 静的同梱（embed）
  proto/     [契約]  canonical Takoda99-Proto の薄い再輸出ラッパ
```

- `internal/` はモジュール外から import されないための隔離。
- `match` という語は使わない（試合セッション=game内 / マッチング=matchmaking と紛れるため）。

---

## 4. 依存の向き（最重要）

```
   odai ──────┐
   config ────┤  部品は game を import して interface を実装する
   bot ───────┤
   store ─────┘
              ↓
            game   ←── 何も import しない（proto は参照可）。純粋なコア
              ↑
   room ──────┐  スパインは game を駆動する。game はスパインを知らない
   matchmaking┤
   transport ─┘

   proto は全レイヤーが参照する共有契約 / main だけが全部を import して組み立てる
```

**鉄則**
- `game` は他の internal 部品/スパインを import しない（proto は可）。崩れたら設計違反。
- `game` がお題を使いたい時は **`game/ports.go` に interface を定義し、実装を注入してもらう**（DIP）。game は odai の実体を知らない。
- 依存は常に **部品 → game の一方向**。組み立ては `main.go` だけ。
- これを `.golangci.yml` の depguard で機械強制し、`go test ./internal/game/...` をCIの門番にする。

---

## 5. 層2：game/ports.go（合意済み・凍結対象）

コアが所有する継ぎ目。

```go
package game

// ── お題供給 ──
type Word struct { Text string; KeystrokeCount int }
type WordSource interface {
    Next(effectiveLevel int, rng *rand.Rand) Word
}

// ── 設定取得 ──
type ConfigProvider interface {
    Load(ctx context.Context) (GameParameters, error)
}
```

---

## 6. 層1：game.Session

純粋な状態機械。時間は外部から `dt` で渡され、内部で `elapsedMs` を積算する（**Clock はスパイン room 側**。game は時計を持たない）。

```go
type Session struct {
    // 横断状態: stores map / customers レジストリ / restPool / storeQueues
    //           aliveCount / phase / heat / storm / elapsedMs / state
    // 注入された部品: words WordSource / params GameParameters
}

// 出力は proto メッセージを宛先付きで返す（room が Envelope に包んで送る）。game は通信を知らない。
type Outbound struct {
    To        Recipient
    Msg       any      // proto.<Message> の値
}
type Recipient struct {
    PlayerId  PlayerId
    Broadcast bool
}

// step関数（純粋・room から呼ばれる）
func (s *Session) ApplyOrderServed(from PlayerId, r proto.OrderServed) []Outbound
func (s *Session) Tick(dtMs int) []Outbound
```

### tick ループの順序（変えない）

```
1. stepPhase      … フェーズ判定（Early/Mid/Late）
2. stepDistribute … 客分配（restPool→行列）。重みは行列の短さのみ
3. stepRank       … 生存店をスコア降順に並べて rank → EvaluationUpdate
4. stepHeat       … 火力（お題難度）更新
5. stepCull       … 時刻足切りの実行・予告（常時）
6. checkFinish    … 最終ステージ到達（生存0）で終了・MatchEnd
```

順序には意味がある。`stepPhase` が先なのは分配の Claimer 解禁判定に要るため、
`stepCull` が `stepRank` の後なのは足切り対象の選定に**その tick のスコア順位**が要るため。

**消えたステップ**: `stepPatience`（我慢ゲージ→離脱→信用減→自滅脱落）と
`stepEvaluate`（バズ加点の時間減衰）と `stepNormalize`（パーセンタイル化）— plan-h21。
`stepStorm`（tick周期の下位%淘汰）— plan-h22。
スコアは `ApplyOrderServed` で加算されるので、tick 側にスコアの処理は無い。

**決着は `cull.stages` の最終ステージ（120秒）で全店が同時に脱落して起きる。**
「生存1店で終了」はもう無い（残った1店だけが試合に取り残される状態を作らないため）。
勝者の特別扱いはサーバーが持たず、1位も他店と同じ経路で PersonalResult を受け取る。

- **per-store 状態**は `storeState` が集約：**スコア（累積の絶対値）**・行列・提供済み客数・順位。
  信用（ライフ）・評価EMA・パーセンタイル正規化・バズ加点は**廃止済み**（復活させない）。
- **Tick** は tick駆動で：客の分配→順位付け→火力→storm→終了判定。
- step関数を純粋に保つことで、room の実tickでも、ヘッドレスsim（合成dtを手で流す）でも**同じ試合コード**が動く。

---

## 7. スパイン：room / transport / matchmaking

**room（駆動・1試合=1goroutine＋1channel）**
```
run():
  for state != Finished:
    select {
      case c := <-inbox:  out := session.Apply*(c);        route(out)
      case dt := <-ticker: out := session.Tick(dt);         route(out); publisher.Publish(session)
    }
```
- tick周期は `GameParameters` 由来（`session.tickIntervalMs`）。
- 各接続 goroutine は「Envelopeを受けて inbox に渡す／out を送る」だけ。判断は session goroutine のみ。

**transport**
```go
type Connection interface {
    Send(env proto.Envelope) error
    Receive() <-chan proto.Envelope
    Close() error
}
```
- 実装：coder/websocket（本番）／InMemory（Bot・負荷検証）。**Bot も人間も同じ Connection**として room から区別しない（IsBot はプレイヤー属性）。
- `StatePublisher`：`StoreListUpdate` を間引いて配信。差し替え可能（フル配信⇔間引き）。まずフル配信、負荷が見えてから間引き。
- **`Send` は非同期**（送信キュー＋writeLoop）。room は単一 goroutine から全接続へ順に Send するため、
  ここで実 I/O をすると半開接続1つで試合全体が止まる。キューが埋まった接続は切る（slow-consumer eviction）。
  `Close` は残りを吐き切ってから閉じる（最終 `MatchEnd` を落とさないため）。

**matchmaking**：待機プール→編成→session/room を生成し開始。定員不足は Bot 補完。

### 切断時の扱い（#40・自然減衰に任せる）

切断専用の機構は持たない。切断した店は `OrderServed` を送らなくなるので、
**スコアが伸びなくなる → 足切りで下位から落ちる**という既存の順位モデルだけで自然に脱落する。
（予選は「客が我慢ゲージ切れで離脱 → 信用減 → 0 で `SelfCollapse`」だったが、plan-h21 で信用制ごと廃止した。）

- Bot 引き継ぎはしない（「切断すれば Bot が代わりに戦う」となり離脱のペナルティが消え、順位の公平性が崩れるため）。
- 即時脱落もしない（一時的な回線断で即死すると再接続の余地がなくなるため）。
- session は切断を認識しない（コアを純粋に保つ）。切断は transport / room の層で吸収する。

これが成立する前提は「**切断が試合の進行を止めないこと**」。room は tick 駆動で接続に依存せず、
`readConn` は `Receive()` のクローズで抜け、`Send` は非同期でブロックしない。

**待機中の切断**は別扱いで、`mm.Leave` でプールから外す（Plan-09）。外さないと `WaitingCount` が
水増しされ、実体のない人数で試合が始まる。監視は必ず `Join` の後に張る（先に張ると、切断が
`Join` より早い場合に `Leave` が空振りして幽霊が残る）。

### 接続数リミッター（Plan-09）

`/ws` は同時接続を `maxConcurrentConnections`（200 = 99人＋余裕）で制限し、超過は 503 を返す。

- 枠は**接続の生存期間ぶん保持**し、実際の切断で返す（＝真の同時接続数キャップ）。
  upgrade 直後に返す実装だと瞬間的なハンドシェイク数しか見ず、居座る接続に無防備になる。
- 解放の検知には `Connection.Done()` を使う。`Receive()` を監視すると room の `readConn` と
  同じチャネルを2箇所で読むことになり、メッセージを奪い合う。

---

## 8. 合成ルート（main.go）

```
config.Provider で GameParameters をロード（失敗時デフォルト）
  → odai.ConfigurablePool（DB語彙があれば使用、なければ StaticPool フォールバック）を用意
  → game.NewSession(params, words, players) を作り room に載せる
  → transport で接続を張る（--mode solo=マッチング迂回 / --mode match=matchmaking起動）
```
「どの実装を使うか」を知るのは main.go だけ。差し替えは注入の差し替えで完結し、game は無変更。

---

## 9. 永続化

| データ | 保存先 | 理由 |
|---|---|---|
| ライブ試合状態（tick毎） | メモリ（Goの構造体） | レイテンシ。DB/Redis に置かない |
| GameParameters | Postgres `game_config` | config-front から Web 編集 |
| お題単語 | Postgres `words` | DB管理。フォールバックは StaticPool |
| 試合結果 | Postgres `match` + `store_result` | 分析・Bot調整 |

- Postgres は **Supabase**（東京リージョン）。pgx ドライバで接続。
- 起動時にテーブル自動作成（Migrate）＋空なら seed。
- 結果保存は best-effort（失敗はログ出力、試合は止めない）。

---

## 10. コア不変条件の機械保証

1. 依存は内向き固定（4章）。game は部品を import しない。
2. `internal/` 隔離。3. 拡張はファイル追加のみ（Open/Closed）。
4. depguard で逆流 import をCIで弾く。5. `go test ./internal/game/...` が落ちる変更はブロック。

後輩が層3で何を書いてもコアは無事。最悪でも「その部品が変になる」で止まり、ゲームは死なない。
