# textro99-server アーキテクチャ設計

サーバー（Go）の内部構造。層アーキで「変わるもの／変わらないもの」を分離し、コア（戦闘の権威）を守る。
本書は AGENTS.md の責務・依存ルールを具体化した設計正典。用語は Textro99-Docs（ダケン等）と canonical proto（Textro99-Proto v0.1.0）に準拠する。

> ステータス: 層1〜2（構造・ports）は合意済み。層コア(game.Session)・スパイン(room/transport)は **提案（要確認）** の節を含む。

---

## 1. 設計目標

1. 変わりうる箇所を差し替え可能に（マッチング・人数・作戦/ターゲティング・通信方式・永続化・調整値）
2. 後輩が触る箇所を隔離し、コアに影響させない
3. 追加・変更を容易に。コアは触らない（Open/Closed）
4. SOLID／DIP を Go のイディオム（暗黙 interface）で実現

要は「**変わらないもの（コア）を、変わるもの（部品）から守る**」。

---

## 2. 層構成

```
┌───────────────────────────────────────────────┐
│ 層3: 差し替え部品（機能追加はここ）              │ ← 後輩の主戦場
│   targeting / odai / config / bot / store       │
├───────────────────────────────────────────────┤
│ 層2: 継ぎ目 = interface（game/ports.go, DIP）    │ ← りーせが定義・凍結
│   TargetingStrategy / WordSource / ConfigProvider │
├───────────────────────────────────────────────┤
│ 層1: コア game（戦闘の権威）不可侵               │ ← 誰も編集しない
│   combo/attack/offset/stack/difficulty/session   │
└───────────────────────────────────────────────┘
    スパイン（背骨・りーせ）: room / matchmaking / transport
    契約: proto（canonical Textro99-Proto の薄いラッパ）
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
  game/     [層1コア]  戦闘の権威。純粋・Tick(dt)駆動
    session.go          試合の状態機械（WaitingStart→Running→Finished）
    combo.go attack.go offset.go stack.go difficulty.go
    state.go params.go
    ports.go  [層2]     コアが要求する interface（DIP）
  targeting/ [層3]  作戦0〜9。1作戦1ファイル。game.TargetingStrategy を実装
  odai/      [層3]  お題供給。game.WordSource を実装
  config/    [層3]  調整値取得(GAS)。game.ConfigProvider を実装
  bot/       [層3]  CPUの振る舞い
  store/     [層3]  永続化の口（今はNoop）
  room/      [スパイン]  tickループ駆動。session.Tick を回し入力適用・配信
  matchmaking/ [スパイン]  待機プール・人数・Bot補完
  transport/ [スパイン]  Connection(WS/InMemory) ＋ StatePublisher(間引き)
  proto/     [契約]  canonical Textro99-Proto の薄い再輸出ラッパ
```

- `internal/` はモジュール外から import されないための隔離。
- `match` という語は使わない（試合セッション=game内 / マッチング=matchmaking と紛れるため）。

---

## 4. 依存の向き（最重要）

```
   targeting ─┐
   odai ──────┤  部品は game を import して interface を実装する
   config ────┤
   bot ───────┘
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
- `game` が作戦/お題を使いたい時は **`game/ports.go` に interface を定義し、実装を注入してもらう**（DIP）。game は targeting/odai の実体を知らない。
- 依存は常に **部品 → game の一方向**。組み立ては `main.go` だけ。
- これを `.golangci.yml` の depguard で機械強制し、`go test ./internal/game/...` をCIの門番にする。

---

## 5. 層2：game/ports.go（合意済み・凍結対象）

コアが所有する継ぎ目。決定A〜G（別途メモ）を織り込む。

```go
package game

// ── ターゲティング（作戦0〜9）──
type PlayerView struct {          // 生存者1人分のスナップショット
    PlayerId PlayerId
    ComboValue, DakenStackCount, DakenStackLimit, BadgeCount int
    IncomingWarnings int          // 決定F: この人宛にPending中の予告数（作戦8/9用）
}
type TargetingContext struct {    // 攻撃者本人の文脈 ＋ 全生存者
    SelfId           PlayerId
    Alive            []PlayerView
    PendingAttackers []PlayerId    // 決定F: 自分に予告中（新しい順）→作戦1
    LastImpactorId   *PlayerId     // 決定F: 直近着弾者→作戦5
    Rng              *rand.Rand    // 乱択・タイブレーク（注入＝決定的テスト/sim）
}
type TargetingStrategy interface {
    Id() int
    SelectTargets(ctx TargetingContext) []PlayerId  // 対象集合。空=不発。1〜9は0/1件、0は全員
}

// ── お題供給 ──
type Word struct { Text string; KeystrokeCount int } // 決定C: 正準ローマ字打鍵数
type WordSource interface {
    Next(effectiveLevel int, rng *rand.Rand) Word
    NextTrap(rng *rand.Rand) Word
}

// ── 設定取得 ──
type ConfigProvider interface {
    Load(ctx context.Context) (GameParameters, error) // 失敗時もデフォルトを返す（起動を止めない・決定E）
}
```

**不変条件**：作戦1〜9は0/1件、複数件を返してよいのは作戦0だけ（威力 `floor(power/N)` 分配は session の責務）。ターゲティングは「誰を撃つか」だけで威力に触れない。

---

## 6. 層1：game.Session（提案・要確認）

純粋な状態機械。時間は外部から `dt` で渡され、内部で `elapsedMs` を積算する（**Clock はスパイン room 側**。game は時計を持たない）。

```go
type Session struct {
    // 横断状態: players map / order / warnings / aliveCount / state / elapsedMs
    // 注入された部品: strategies(map[int]TargetingStrategy) / words WordSource / params GameParameters
}

// 出力は proto メッセージを宛先付きで返す（room が Envelope に包んで送る）。game は通信を知らない。
type Outbound struct {
    To        PlayerId // Broadcast=true なら全体
    Broadcast bool
    Msg       any      // proto.<Message> の値
}

// step関数（純粋・room から呼ばれる）
func (s *Session) ApplyDakenClear(from PlayerId, r proto.DakenClearReport) []Outbound
func (s *Session) ApplyAttack(from PlayerId, r proto.AttackRequest) []Outbound
func (s *Session) ApplyStrategy(from PlayerId, r proto.StrategySelect) []Outbound
func (s *Session) Tick(dtMs int) []Outbound
```

- **per-player 状態**は `playerState`（session 内）が集約：`combo`＋`stack{count,trapMilestone}`＋`personalLevel`＋`strategy`＋`issued map[DakenId]issuedDaken`（台帳=整合検証と時間切れ監視の元・決定D... C/E）＋`pendingAgainstMe`＋`lastImpactor`＋`alive/badges/koCount`。
- **ApplyAttack** は決定どおり**同期解決**：全コンボ消費→威力→自分宛pending warningへ相殺充当→余剰は現作戦でターゲット解決→撃ち返しを `maxReboundChain` までその場でループ。作戦0はN対象へ均等分配。
- **Tick** は tick駆動で：各issued dakenの時間切れ→積み残し、warningのexpire→着弾、30秒境界で全体難易度+1、KO走査、`aliveCount==1`でFinished。
- step関数を純粋に保つことで、room の実tickでも、ヘッドレスsim（合成dtを手で流す）でも**同じ戦闘コード**が動く。

---

## 7. スパイン：room / transport / matchmaking（提案・要確認）

**room（駆動・1試合=1goroutine＋1channel）**
```
run():
  for state != Finished:
    select {
      case c := <-inbox:  out := session.Apply*(c);        route(out)
      case dt := <-ticker: out := session.Tick(dt);         route(out); publisher.Publish(session)
    }
```
- `Clock`/`Ticker` を注入（本番=実時間 / sim・テスト=fake）。tick周期は `GameParameters` 由来（`session.tickIntervalMs`、決定4=パラメータ化）。
- 各接続 goroutine は「Envelopeを受けて inbox に渡す／out を送る」だけ。戦闘判断は session goroutine のみ。

**transport**
```go
type Connection interface {
    Send(env proto.Envelope) error
    Receive() <-chan proto.Envelope
    Close() error
}
```
- 実装：coder/websocket（本番）／InMemory（Bot・負荷検証）。**Bot も人間も同じ Connection**として room から区別しない（IsBot はプレイヤー属性）。
- `StatePublisher`：`PlayerListUpdated`/`PlayerListDelta` を間引いて配信（自分＋周辺＋上下）。差し替え可能（フル配信⇔間引き）。まずフル配信、負荷が見えてから間引き。

**matchmaking**：待機プール→編成→session/room を生成し開始。定員不足は Bot 補完。

---

## 8. 合成ルート（main.go）

```
config.Provider で GameParameters をロード（失敗時デフォルト）
  → targeting.Registry（game.TargetingStrategy 実装群）と odai.StaticPool（game.WordSource）を用意
  → game.NewSession(params, strategies, words, players) を作り room に載せる
  → transport で接続を張る（--mode solo=マッチング迂回 / --mode match=matchmaking起動）
```
「どの実装を使うか」を知るのは main.go だけ。差し替えは注入の差し替えで完結し、game は無変更。

---

## 9. 確定事項の反映

- proto は canonical **Textro99-Proto v0.1.0**（DakenClearReport 等）。旧称（寿司/お題/JoinRoom…）は不使用。
- 決定A〜G（相殺余剰の消失／全コンボ消費／ローマ字打鍵数／自滅時KO null／elapsedMs権威／Snapshot予告情報／tick駆動・同期解決）を session/ports に織り込み済み。
- 調整値は全て `GameParameters` 経由（tick周期含む）。ハードコード禁止。

## 10. コア不変条件の機械保証

1. 依存は内向き固定（4章）。game は部品を import しない。
2. `internal/` 隔離。3. 拡張はファイル追加のみ（Open/Closed）。
4. depguard で逆流 import をCIで弾く。5. `go test ./internal/game/...` が落ちる変更はブロック。

後輩が層3で何を書いてもコアは無事。最悪でも「その部品が変になる」で止まり、ゲームは死なない。
