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
    スパイン（背骨・りーせ）: room / matchmaking / transport / configapi
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
2. stepDistribute … 客分配（restPool→行列）
3. stepPatience   … 我慢ゲージ減算 → 離脱 → 信用減 → 自滅脱落（SelfCollapse）
4. stepEvaluate   … 評価の時間減衰（バズ加点の減衰）
5. stepNormalize  … 生存店内でパーセンタイル化 → rank
6. stepHeat       … 火力（お題難度）更新
7. stepStorm      … 下位淘汰の予告・実行
8. checkFinish    … 終了条件・順位確定・MatchEnd
```

- **per-store 状態**は `storeState` が集約：信用（ライフ）・評価EMA・バズ加点・行列・提供済み客数・順位。
- **Tick** は tick駆動で：客の分配→我慢ゲージ→離脱→評価更新→正規化→火力→storm→終了判定。
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

**matchmaking**：待機プール→編成→session/room を生成し開始。定員不足は Bot 補完。

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
