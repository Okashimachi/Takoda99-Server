# Takoda99-Server アーキテクチャ設計

サーバー（Go）の内部構造。層アーキで「変わるもの／変わらないもの」を分離し、コア（試合の権威）を守る。
本書は `AGENTS.md` の責務・依存ルールを具体化した設計正典。用語は Takoda99-Docs（`01_企画/00_用語集.md`）と canonical proto（Takoda99-Proto）に準拠する。

> ゲームの骨子（99店 / 300客 / 直接攻撃なし / 我慢ゲージ / 信用 / 下位淘汰）は `AGENTS.md` 0章を参照。
> 旧 Textro99（直接攻撃型タイピング対戦）のコンボ・威力・相殺・ダケンスタック・KO・ターゲティング（作戦）は**全て廃止済み**。

---

## 1. 設計目標

1. 変わりうる箇所を差し替え可能に（マッチング・人数・お題・通信方式・永続化・調整値）
2. 後輩が触る箇所を隔離し、コアに影響させない
3. 追加・変更を容易に。コアは触らない（Open/Closed）
4. SOLID／DIP を Go のイディオム（暗黙 interface）で実現
5. コアを純粋に保ち、ヘッドレスで高速シミュレートできるようにする（バランス調整）

要は「**変わらないもの（コア）を、変わるもの（部品）から守る**」。

---

## 2. 層構成

```
┌───────────────────────────────────────────────┐
│ 層3: 差し替え部品（機能追加はここ）              │ ← 後輩の主戦場
│   odai / config / db / bot / store              │
├───────────────────────────────────────────────┤
│ 層2: 継ぎ目 = interface（game/ports.go, DIP）    │ ← りーせが定義・凍結
│   WordSource / ConfigProvider                   │
├───────────────────────────────────────────────┤
│ 層1: コア game（試合の権威）不可侵               │ ← 誰も勝手に編集しない
│   session（客/行列/評価/信用/フェーズ/淘汰）      │
└───────────────────────────────────────────────┘
    スパイン（背骨・りーせ）: room / matchmaking / transport / configapi
    契約: proto（canonical Takoda99-Proto の薄いラッパ）
```

- **層1 game**：ゲームのルールそのもの。`Tick(dt)` で進む純粋ロジック。I/O・時計・通信・ログを持たない。
- **層2 ports**：コアが外部に要求する約束。**コアが所有**（DIP）。りーせが凍結。
- **層3 部品**：ports を満たす具体実装。ここをいじってもコアは無事。後輩の担当。
- **スパイン**：コアをネットワークに繋ぎ実際に試合を回す統合部（層に属さない特別枠）。りーせが持つ。

---

## 3. パッケージ構成

```
cmd/server/main.go        合成ルート。全部品を配線（--mode solo/match）

internal/
  game/                   【層1】試合の権威。純粋・Tick(dt)駆動
    session.go              1試合の状態機械（客レジストリ/行列/店舗状態/tickループ）
    params.go               GameParameters（全調整値）
    ports.go              【層2】WordSource / ConfigProvider

  odai/                   【層3】お題単語供給 → game.WordSource
  config/                 【層3】GameParameters 取得 → game.ConfigProvider
  db/                     【層3】Postgres（設定・お題・試合結果）
  bot/                    【層3】CPU 自動入力（OrderServed を内部生成）
  store/                  【層3】試合結果永続化の口 → store.ResultStore

  room/                   【スパイン】tick ループ駆動・入力適用・配信
  matchmaking/            【スパイン】待機プール・人数下限＋カウントダウン・Bot 補完
  transport/              【スパイン】WebSocket・InMemory Pipe・状態配信の間引き
  configapi/              【スパイン】config-front 用 REST API（/api/params）
  proto/                  【契約】Takoda99-Proto の薄い再輸出ラッパ
```

> 旧 `internal/targeting/`（作戦0〜9）は**ディレクトリごと廃止**。たこ焼き版に他店を狙う概念はない。

---

## 4. 依存の向き（最重要）

```
                    ┌──────────────┐
                    │   proto      │ ← 契約（全員が参照してよい）
                    └──────▲───────┘
                           │
  部品(odai/config/db/bot/store) ──► game ◄── スパイン(room/matchmaking/transport)
                                      ▲
                                 ports.go（層2）
```

- **game は他の internal パッケージを一切 import しない**（`proto` のみ可）
- 部品は `game/ports.go` の interface を実装する（依存は 部品 → game）
- スパインは game を駆動する（依存は スパイン → game）
- **逆流は禁止**。`.golangci.yml` の depguard で機械強制。CI で弾かれる

この向きにより、game は単体でテスト・シミュレートでき、通信方式や永続化先を変えても影響を受けない。

---

## 5. 層2：game/ports.go（凍結対象）

コアが外部に要求する interface。**コア自身が所有する**（DIP）。

```go
// PlayerId はコア内の店舗識別子。契約(proto)の StoreId と同一。
type PlayerId = proto.StoreId

// Word は1つの出題語。KeystrokeCount は正準ローマ字打鍵数。
type Word struct {
	Text           string
	KeystrokeCount int
}

// WordSource はお題単語供給の口。実効難易度（火力）に応じた語を返す。
type WordSource interface {
	Next(effectiveLevel int, rng *rand.Rand) Word
}

// ConfigProvider は GameParameters を取得する。
// Load は使用可能な GameParameters を必ず返す（失敗時も内蔵デフォルト＋err）。
type ConfigProvider interface {
	Load(ctx context.Context) (GameParameters, error)
}
```

> 旧 `TargetingStrategy` / `TargetingContext` / `PlayerView`、および `WordSource.NextTrap`
> （トラップダケン用）は**廃止済み**。たこ焼き版に作戦もトラップもない。

試合結果の永続化は `store.ResultStore` が担うが、これは game の port ではなく**合成ルートとスパインが扱う**
（game は試合終了後の結果を `Results()` で返すだけで、保存先を知らない）。

---

## 6. 層1：game.Session

1試合の状態機械。`Tick(dtMs)` で進む。**時計を持たない**（dt は room が渡す）ので、大きな dt を渡せばヘッドレスで高速シミュレートできる＝バランス調整を再ビルドなしで回せる。

### 6.1 権威データ

```go
type Session struct {
	id     proto.MatchId
	params GameParameters
	words  WordSource
	rng    *rand.Rand

	// 客の権威データ（単一情報源）
	customers   map[proto.CustomerId]*customer  // 客レジストリ
	storeQueues map[PlayerId][]proto.CustomerId // 各店の行列（先頭=対応中）
	restPool    []proto.CustomerId              // たべたべエリア（未割当）

	stores map[PlayerId]*storeState
	order  []PlayerId // 安定順（map 走査の非決定性を避ける）

	state      SessionState
	phase      proto.Phase
	elapsedMs  int64
	tick       int
	aliveCount int
	heatLevel  int
}
```

**客の移動は ID 配列の増減のみ**で表す。実体（`*customer`）は複製・破棄しない。
`customers` / `storeQueues` / `restPool` / `customer.assignedStore` の4つが食い違うと客が消える・増えるので、移動は必ず `assignCustomer` / `releaseToRest` ヘルパ経由で行い、直接書き換えない。

**全店走査は `s.order` を使う**（`s.stores` の map 走査は順序が非決定で、リプレイ性とテストの安定性を壊す）。

### 6.2 tick ループ（順序に意味がある）

```
1. stepPhase      … フェーズ判定（Early/Mid/Late）
2. stepDistribute … 客分配（restPool→行列・CustomerArrived）
3. stepPatience   … 我慢ゲージ減算 → 離脱 → 信用減 → 自滅脱落
4. stepEvaluate   … 評価の時間減衰（バズ加点の減衰）
5. stepNormalize  … 生存店内でパーセンタイル化 → rank
6. stepHeat       … 火力（お題難度）更新
7. stepStorm      … 下位淘汰の予告・実行
8. checkFinish    … 終了条件・順位確定・MatchEnd
```

- `stepPhase` が先 → `stepDistribute` の Claimer 解禁判定に当tickのフェーズが要る
- `stepStorm` が `stepNormalize` の後 → 淘汰判定に正規化評価が要る

### 6.3 出力の形（Outbound）

game は通信を知らない。結果を**宛先つきメッセージ**で返し、room が Envelope 化して配信する。

```go
type Recipient struct {
	PlayerId  PlayerId
	Broadcast bool // true で全員
}

type Outbound struct {
	To  Recipient
	Msg any // proto.<Message> の値
}

func to(pid PlayerId, msg any) Outbound { return Outbound{To: Recipient{PlayerId: pid}, Msg: msg} }
func broadcastMsg(msg any) Outbound     { return Outbound{To: Recipient{Broadcast: true}, Msg: msg} }
```

**ブロードキャストは Outbound を1つ返す**。game 側で `order` をループして店舗数ぶんに展開しない
（`room.dispatch` が `Broadcast` を見て全接続へ配るため二重配信になる。Envelope の marshal も1回で済む）。

### 6.4 イベント処理（tick 外）

`ApplyOrderServed(from, r)` … クライアントの提供報告。サニティ検証 → 提供スコア算出 → 評価EMA反映 → 客を満足させて restPool へ戻す。

サニティ検証は「その客が実在し、その店に割り当てられ、**行列先頭（対応中）である**」ことを要求する。途中の客を飛ばして捌く逸脱を弾く（先頭のみ我慢ゲージを減算する `stepPatience` と整合させるため）。

`elapsedMs` / `missCount` は性善説で受けるが、**下限クランプ＋範囲クランプ**をかけてスコア膨張を防ぐ。

---

## 7. スパイン：room / transport / matchmaking

### room

1試合 = 1 goroutine。接続からの入力（inbox）と tick を**単一ループで直列処理**する。

```go
for r.session.State() != game.Finished {
	select {
	case <-ctx.Done():    return
	case in := <-r.inbox: r.dispatch(r.handle(in))                    // C2S 適用
	case <-ticker.C():    r.dispatch(r.session.Tick(r.tickMs)); r.publish()
	}
}
```

直列処理なので session にロックが要らない（コアが純粋である恩恵）。
tick 周期は `GameParameters.Session.TickIntervalMs`。時計は `Clock` で注入（本番=実時間 / テスト=手動）。

`dispatch` は `Outbound` を Envelope へ変換し、`Broadcast` なら全接続、そうでなければ宛先1件へ送る。

### transport

- `Connection` interface … 実 WebSocket と InMemory を切り替える
- `Pipe()` … InMemory 接続ペア。Bot と負荷試験で使う
- `StatePublisher` … 盤面スナップ（`StoreListUpdate`）の定期配信。**間引き必須**（99×99 で帯域が破綻するため）

### matchmaking

待機プール。人数下限 → カウントダウン → Bot 補完 → 試合開始。
マッチング系パラメータは**起動時スナップショット**なので、変更には再起動が要る（試合系パラメータは次の試合から反映される）。この非対称性は当日運用で効いてくるので忘れないこと。

---

## 8. 合成ルート（cmd/server/main.go）

全部品をここで配線する。ここだけが「どの実装を使うか」を知っている。

```go
provider := chooseProvider(ctx, configURL) // DATABASE_URL > CONFIG_URL > 内蔵デフォルト

loadDeps := func() app.Deps {
	d := baseDeps
	p, _ := provider.Load(ctx) // 失敗時も有効なデフォルトが返る
	d.Params = p
	return d
}
```

`loadDeps` が**マッチ生成のたびに最新 config を読む**ことで、config-front の編集が「次の試合から」再起動なしで反映される（進行中の試合はパラメータ固定）。

`chooseProvider` は DB 接続・マイグレーションに失敗しても**必ず有効な ConfigProvider を返す**（起動を止めない）。

---

## 9. 設定と永続化

| 対象 | 置き場所 | 理由 |
|---|---|---|
| **ライブ試合状態**（客・行列・評価・信用） | **メモリ** | tick 毎に更新されるため |
| 調整パラメータ（GameParameters） | Postgres + config-front | 再ビルドなしで当日調整するため |
| お題単語 | Postgres（フォールバックは内蔵） | config-front から管理するため |
| 試合結果（match / store_result） | Postgres | バランス分析・BOT 調整のため |

「NO-DB」ではない。**ライブ試合状態だけがメモリ**で、それ以外は Postgres をゲームサーバーが所有する。
スケールアウト（複数インスタンス）は現状不要。99店1部屋は1台で捌ける前提。

---

## 10. コア不変条件の機械保証

| 不変条件 | 保証方法 |
|---|---|
| game が部品/スパインを import しない | `.golangci.yml` の depguard（CI で自動検査） |
| game が純粋（時計・IO・ログなし） | depguard ＋ レビュー |
| `GameParameters` が `==` 比較可能 | map/slice を入れるとコンパイル/比較が壊れる（config の差分検出・backfill が依存） |
| コアの回帰 | `go test -race ./internal/game/...` が常時グリーン |
| ワイヤ形式の固定 | `internal/proto/wire_golden_test.go`（JSON のゴールデン比較） |

---

## 11. 実装状況と計画

実装計画は `docs/plan/plan-01`〜`plan-12`。各 plan が担当する範囲:

| plan | 範囲 |
|---|---|
| 01 | 基盤移行（Textro99-Server → Takoda99-Server）・旧実装消去・骨組み配置 |
| 02 | 我慢ゲージ・離脱・信用・自滅脱落（`stepPatience`） |
| 03 | 客分配・評価正規化（`stepDistribute` / `stepNormalize`） |
| 04 | フェーズ・火力・下位淘汰（`stepPhase` / `stepHeat` / `stepStorm`） |
| 05 | 終了条件・順位確定・MatchEnd（`checkFinish`） |
| 06 | config 基盤（Postgres + config-front 別URL） |
| 07 | お題単語データ（関西弁語彙・config 管理） |
| 08 | 試合結果永続化（match / store_result） |
| 09 | マッチングスパイク対策 |
| 10 | デプロイ戦略・当日オペレーション |
| 11 | 負荷テスト（99接続） |
| 12 | Observability（構造化ログ・メトリクス） |

plan 同士で型・関数シグネチャが噛み合うよう調整済み。**勝手に別案へ差し替えない**。

### 設計文書と実装が意図的に食い違う箇所

| 箇所 | Takoda99-Docs | 実装 | 理由 |
|---|---|---|---|
| 客分配の重み | `正規化評価 ÷ (行列長+1)` | `(WeightFloor + 正規化評価) ÷ (行列長+1)` | パーセンタイル正規化では最下位店が必ず 0 になり客が永久に来ない（復帰不能）。`docs/plan/plan-03` §2.5 |
