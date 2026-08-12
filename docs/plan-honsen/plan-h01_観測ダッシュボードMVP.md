# Plan-h01: ライブ観測ダッシュボード MVP

> **目的**: 試合中の99店の状態（体力・評価・順位）を、運営/開発者が1画面で俯瞰できるダッシュボードを最小構成で立ち上げる。まず「見える」を取り、以降の観測強化（h02）・算法改良（h06）の土台にする。
> **対応issue**: #48（observability）
> **依存**: なし（既存 `StoreListUpdate` に相乗り）
> **参照**: **[plan-h00 共有コントラクト](plan-h00_共有コントラクト.md)（AdminHub I/F・配線・`/admin`サーフェスの正典。まず読む）**, `docs/architecture.md` §7, `internal/transport/publisher.go`, `internal/configapi/handler.go`（トークン方式）

---

## 0. 前提知識

### 読むべきファイル

| ファイル | 内容 |
|---|---|
| `internal/transport/publisher.go` | `StatePublisher` / `FullPublisher`。毎tickの盤面配信 |
| `internal/transport/connection.go` | `Connection` interface（`Send`/`Receive`/**`Done`**/`Close`）。`Send` はキュー満杯で自動的に接続を切る（slow-consumer eviction, `connection.go:155`） |
| `internal/room/room.go` | tickループ。`publish()`（`room.go:96`）が `session.Snapshot()` から盤面を作り配信する |
| `internal/game/session.go` | `Snapshot()`（`session.go:181`）が `([]StoreSummary, aliveCount)` を返す。**盤面の出所はここ** |
| `internal/app/app.go` | `RunMatch` が `room.New(...)` を組み立てる。**hub はここ経由で room へ渡す** |
| `cmd/server/main.go` | http ハンドラ配線。`/ws` は接続後 `awaitJoinName → mm.Join` で**プレイヤー化**する（`main.go:148`）、`CONFIG_ADMIN_TOKEN` |
| `internal/configapi/handler.go` | 共有トークン認証（`X-Admin-Token` / 定数時間比較 `tokenEqual`） |

> **注意（改造規模）**: 本plan は「サーバー改造ゼロ」ではない。AdminHub＋`/admin/ws`＋embed静的配信＋room への hub 注入を伴う**最小改修**である。
> 完全に無改修で見たいだけなら、solo開発時に観測PCを1プレイヤーとして `/ws` に繋げば `StoreListUpdate` は見えるが、**スロットを1つ消費し試合に混ざる**ので暫定手段に留める。

### いま流れているデータ（そのまま使える）

`StoreListUpdate`（約250ms間隔・全 `Connection` へフル配信）が既に持っている:

```go
type StoreSummary struct {
    StoreId        StoreId // 店ID
    DisplayName    string  // 表示名
    EvalNormalized float64 // 評価 0..1
    Rank           int     // 順位
    CreditLife     int     // 体力（信用）
    Alive          bool
    FinalRank      *int    // 脱落済みのみ
}
type StoreListUpdate struct {
    Stores     []StoreSummary
    AliveCount int
}
```

**「各店の体力・評価・順位」はこの時点で全部そろっている。** MVP はこれを描くだけ。
客分配・フェーズ・heat・storm は乗っていない → **h02 で追加**する。

### スコープの線引き

- **やる**: 静的ダッシュボードページの Go 同梱配信＋読み取り専用の観測用WS＋99店の描画。
- **やらない**: 客分配・フェーズ・storm の可視化（h02）、DB履歴の描画（h03以降）、proto変更。

---

## 1. 設計

### 1.1 なぜ「観測者は店になってはいけない」か

`/ws` に接続すると、ハンドラが `awaitJoinName` で `MatchmakingJoin` を待ち、`mm.Join(...)` で **1店（プレイヤー）としてマッチングに参加**させる（`main.go:148-150`）。スロットを消費し、`OrderServed` を期待される。観測ダッシュボードは試合に影響してはならない。
→ **読み取り専用の観測エンドポイント `/admin/ws` を別に用意**し、`mm.Join` は呼ばず盤面スナップを配るだけにする（受信は捨てる）。

> この「読み取り専用ファンアウト」の配管が、h02 の管理者ストリームの種になる。h01 では payload = 既存 `StoreListUpdate`、h02 で `AdminSnapshot` に差し替える。

### 1.2 AdminHub（プロセス共有の観測ファンアウト）

```
[room（試合ごと）]  ── publish()（room.go:96）直後 ──▶ hub.Broadcast(snapshotJSON)
        │ 盤面は session.Snapshot()（session.go:181）が返す (stores, aliveCount) を再利用
        ▼
[AdminHub（プロセス共有・1個）]
   ├ Register(conn) / Unregister(conn)
   └ Broadcast(payload []byte): 登録中の観測conn全部へ Send
        ▲
[/admin/ws ハンドラ（main.go）]
   └ トークン検証 → transport.Accept で conn 取得 → hub.Register → conn.Done() で Unregister
```

- `AdminHub` は **新規 `internal/admin`**（または `internal/transport`）に置く。**game コアは触らない**（純粋性維持）。
- **配線経路（初見が詰まる所・[h00 §3](plan-h00_共有コントラクト.md) に完全版）**: Hub はプロセス共有だが Room は試合ごとに `app.RunMatch → room.New(...)` で生成される。
  hub は **`main.go` で1個作り → `baseDeps.Hub` に載せ（`loadDeps()` がコピーするので全試合で同一実体）→ `RunMatch` で `rm.SetAdminHub(d.Hub)`**。
  ⚠ **`room.New` の署名は変えない**（呼び出しが `app.go:70` と `internal/app/scale_test.go:48` の2箇所あり、引数追加で既存テストが壊れる）。`Room.SetAdminHub` を足す。
  `main.go` の `/admin/ws` ハンドラも同じ hub インスタンスを参照する（登録先と配信元が同一）。
- room は `publish()` 内で既に得ている `stores, aliveCount := r.session.Snapshot()` を JSON 化して `hub.Broadcast` する（**新たにスナップを作らない**・二重計算回避）。
- **hub は複数試合が並走すると混線する**。現状 1部屋 1試合なので単一で足りる（複数試合対応は将来課題）。
- 観測 conn は既存 `wsConnection` を流用。`Send` がキュー満杯で自動的に接続を切る（slow-consumer eviction, `connection.go:155`）ので、**観測の詰まりが試合を止めない**。`conn.Done()` を監視して hub から Unregister する（`Receive()` は監視しない — room の readConn とチャネルの奪い合いになる）。

### 1.3 認証（config と同じトークン方式）

- 既存 `CONFIG_ADMIN_TOKEN` を再利用（別値にしたければ `ADMIN_TOKEN` を足すが、ハッカソン規模なら共用で可）。
- **注意**: ブラウザの WebSocket / EventSource は**カスタムヘッダを付けられない**。
  `/admin/ws` は `X-Admin-Token` ヘッダの代わりに **`?token=` クエリ** で受ける。
  比較は configapi の `tokenEqual`（定数時間）を流用。未設定なら 503（無認証で覗けない）。
- **Origin 制御**: `/admin/ws` も `transport.Accept` を使う以上、Origin 検証が要る。`/ws` と同じ `ALLOWED_ORIGINS`（`wsAcceptOptions`）を共用する。ローカル観測なら AllowAll でも実害は小さいが、本番公開時は許可オリジンを絞る。
- **ログ露出の注意**: `?token=` は Caddy / サーバーのアクセスログに残る。ハッカソン規模なら許容だが、気にするなら「`/admin` を開く時だけトークン検証して httpOnly Cookie を発行 → `/admin/ws` は Cookie で認証」に切り替えるとログに出ない。まずはクエリで可、注意点として記録。
- 静的ページ `/admin` 自体もトークンで軽くガードするか、ページを開いた後にトークンを入力してWSを張る形にする。

### 1.4 ダッシュボードのフロント（静的・同梱）

- `cmd/server` から `/admin` で静的 HTML/JS を配信（`//go:embed` で単一バイナリに同梱、追加デプロイ物なし）。
  **現状 main.go に静的配信・embed の前例は無い**ので新規に足す（`embed.FS` ＋ `http.FileServer` or 直書き）。
- WSで受けた `StoreListUpdate` を描画:
  - 99マスのグリッド（店ごとにセル）。セルに **表示名 / 順位 / 体力 / 評価バー**。
  - `Alive=false` はグレーアウトし `FinalRank` を表示。
  - `AliveCount` を上部に大きく表示。
  - 体力が減った/脱落した瞬間に色変化（見ていて状態遷移が分かる）。
- 依存ライブラリなし（素の JS）で十分。凝った描画は h02 以降。

---

## 2. 実装手順（概略）

1. `internal/admin`（or `internal/transport`）に `AdminHub` を追加（Register/Unregister/Broadcast、内部は mutex + conn set）。
2. `room.New` に `hub *admin.Hub` 引数を足す（or `Room.SetAdminHub`）。`publish()`（`room.go:96`）末尾で `if r.hub != nil { r.hub.Broadcast(marshal(StoreListUpdate{stores, aliveCount})) }`。
   - hub 未注入（nil）なら何もしない（sim/既存テスト非破壊）。
3. `app.Deps` に `AdminHub` を足し、`RunMatch` から `room.New` へ渡す。
4. `main.go` で hub を1個生成 → `app.Deps` に載せる。
5. `main.go` に `/admin/ws` ハンドラを追加（`?token=` 検証 → `transport.Accept` → `hub.Register` → `conn.Done()` で Unregister、受信は捨てる）。
6. `main.go` に `//go:embed` で `/admin` 静的配信を追加。
7. ダッシュボードHTML/JS を作成（99店グリッド描画）。
8. solo モードで Bot を並べて目視確認。

---

## 3. テスト（バグを注入して落ちることを確認する方式）

- `AdminHub.Broadcast` が登録中の全 conn に届く／Unregister後は届かない（InMemory Connection で検証）。
- slow-consumer（Send が詰まる conn）が1つあっても Broadcast が他へ届く／詰まった conn は切られる。
- `/admin/ws` がトークン無し・誤トークンで拒否される（configapi のテストパターン流用）。
- room が hub=nil でも panic せず動く（既存の sim/テスト非破壊）。

---

## 4. 完了条件

- [ ] `/admin` で静的ダッシュボードが配信される（バイナリ同梱・追加デプロイ物なし）
- [ ] `/admin/ws` が読み取り専用の観測WSを提供する（`mm.Join` を呼ばず、店として試合に参加しない）
- [ ] `/admin/ws` が `CONFIG_ADMIN_TOKEN`（`?token=`）で認証され、未設定/誤りは拒否される
- [ ] `/admin/ws` の Origin 検証が `/ws` と同じ方針（`ALLOWED_ORIGINS`）で効く
- [ ] `AdminHub` が Register/Unregister/Broadcast を提供し、room の `publish()` に相乗りする（`session.Snapshot()` を再利用）
- [ ] 観測conn が `conn.Done()` で hub から Unregister される
- [ ] ダッシュボードが99店の **表示名/順位/体力/評価/生存** と `AliveCount` を描画する
- [ ] 脱落・体力変化が画面上で見て分かる
- [ ] 観測conn の詰まり/切断が試合進行を止めない（slow-consumer eviction）
- [ ] room が hub=nil でも動く（sim/既存テスト非破壊）
- [ ] `game` コアは無改造（純粋性維持・`slog`/hub を import しない）
- [ ] `go build ./...` / `go vet ./...` / `GOWORK=off golangci-lint run` が通る

---

## 5. h02 への引き継ぎ

- 本planで作った `AdminHub` / `/admin/ws` / 静的配信の配管を**そのまま使い回す**。
- h02 は Broadcast する payload を `StoreListUpdate` → `AdminSnapshot`（客分配・フェーズ・heat・storm 込み）へ差し替え、フロントの描画を拡張する。
- 客向け `StoreListUpdate` が #81・MTG方針で痩せても、観測は自前ストリームなので影響を受けない。
