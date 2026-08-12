# Plan-h00: 観測基盤の共有コントラクト（実装セッションはまずこれを読む）

> **目的**: h01 / h02（と h03 の一部）が**跨いで共有する契約・配線・順序**の正典。各 plan に散らばると実装セッションが取り違えるので、ここに1本化する。
> **性格**: これは新規機能の plan ではなく、**h01〜h03 を跨ぐ決定事項の固定**。コールドスタートの実装セッションが最初に読む前提。
> **参照**: `AGENTS.md`（責務・依存）, `docs/architecture.md`（層アーキ）, 各 `plan-h01/h02/h03`

---

## 0. 実装順序（確定）

```
PR #104 マージ  →  h01  →  h02  →  h03
```

- **#104 を最初にマージ**（実装セッション開始前に）。理由: h02/h03 が #104 と同じファイル（`session.go`/`app.go`/`db/result.go`/`store.go`）を触るため、先に main へ入れてからその形状に載せる。
- **h01 は #104 と非依存**（`internal/admin`/room/Deps/main のみ）。#104 と順不同で、ダッシュボードを先に見たいなら h01 を先でも可。
- **h02/h03 は #104 マージ必須**。マージ前に書くとコンフリクトする。
- 1 plan = 1 ブランチ = 1 PR。main へ直 push しない（AGENTS.md §7）。

---

## 1. 新パッケージ `internal/admin`（スパイン・りーせ所有）

観測基盤の型は **新規 `internal/admin`** に置く。理由と制約:

- `AdminHub`（h01）と `AdminSnapshot`（h02）をここに置く。
- **`internal/game`（コア）は `internal/admin` を import しない**。game は純粋のまま、必要な読み出しは getter で返すだけ（AGENTS.md §1.4）。
- 依存方向は **`admin → transport`（`Connection` を使う）** の一方向。`transport` は `admin` を知らない（循環しない）。
- `AdminSnapshot` は **proto 契約ではない**（Unity へ送らない内部DTO）。**Takoda99-Proto に足さない・proto承認フロー不要**。
- ⚠ **TODO（実装時）**: `AGENTS.md` §2 と `docs/architecture.md` §3 のパッケージ表に `admin/` を1行追記する（「スパイン・観測配信・game を import しない」）。追記しないと、次に AGENTS を読む人がこのパッケージの位置づけを見失う。

---

## 2. `AdminHub` インターフェース（確定）

```go
package admin

import "takoda99/internal/transport"

// Hub は観測用の読み取り専用ファンアウト。プロセス共有・単一インスタンス。
// room（試合ごとの goroutine）が Broadcast し、/admin/ws ハンドラが Register/Unregister する。
type Hub struct {
    mu    sync.RWMutex
    conns map[transport.Connection]struct{}
}

func NewHub() *Hub
func (h *Hub) Register(c transport.Connection)     // /admin/ws 接続時
func (h *Hub) Unregister(c transport.Connection)   // conn.Done() 発火時
func (h *Hub) Broadcast(payload proto.Envelope)    // 登録中の全 conn へ Send。詰まり/切断は下記
```

- **並行性**: `Register`/`Unregister`（ハンドラ goroutine）と `Broadcast`（room goroutine）が別 goroutine から来るので `mu` で保護する。
- **詰まり/切断**: `Broadcast` は各 conn に `Send` するだけ。`transport.wsConnection.Send` は**キュー満杯で自動的に接続を切る**（slow-consumer eviction, `connection.go:155`）。`Send` がエラー（`ErrConnClosed`）を返した conn は Broadcast 側で **Unregister する**（放置すると map に死んだ conn が溜まる）。
- **観測の詰まりが試合を止めない**のはこの eviction のおかげ。room の単一 goroutine は Broadcast で実 I/O をしない（Send は非同期キュー）。

---

## 3. 配線とライフサイクル（確定・コールドスタートの地雷）

### 3.1 hub は main で1個・全試合で共有

```go
// cmd/server/main.go
hub := admin.NewHub()
baseDeps := app.DefaultDeps()
baseDeps.Hub = hub            // ★ loadDeps() は baseDeps をコピーするので、全試合が同一 hub を共有する
loadDeps := func() app.Deps { d := baseDeps; /* configリロード */; d.Store = resultStore; return d }
```

- `app.Deps` に **`Hub *admin.Hub` フィールドを追加**。
- `loadDeps()` は**毎試合コピーで呼ばれる**（`main.go:113/131`）が、`Hub` はポインタなので全コピーが同じ実体を指す。**hub を loadDeps 内で作らないこと**（試合ごとに別 hub ができ、`/admin/ws` の登録先とズレる）。

### 3.2 room へは SetAdminHub で注入（署名を変えない）

```go
// internal/app/app.go RunMatch 内
rm := room.New(sess, conns, d.Params.Session.TickIntervalMs, d.Clock, pub)
rm.SetAdminHub(d.Hub)   // nil 安全
rm.Run(ctx)
```

- ⚠ **`room.New` の署名は変えない**。呼び出しが **2箇所**（`app.go:70` と `internal/app/scale_test.go:48`）あり、引数を増やすと**既存テストがコンパイルエラー**になる。`Room` に `hub *admin.Hub` フィールドと `SetAdminHub` を足す方式にする。
- `Room.publish()`（`room.go:96`）末尾で配信:

```go
func (r *Room) publish() {
    if r.publisher == nil { return }
    stores, aliveCount := r.session.Snapshot()
    r.publisher.Publish(r.elapsedMs, stores, aliveCount, r.conns)
    if r.hub != nil {
        r.hub.Broadcast(observePayload(r.session, stores, aliveCount)) // h01/h02 で中身が変わる（§4）
    }
}
```

- `r.hub == nil`（sim/既存テスト）では何もしない＝**非破壊**。

---

## 4. ワイヤ契約（server ⇔ ダッシュボード front・同一リポの唯一の結合点）

`/admin/ws` は `proto.Envelope{Type, Payload}` を流す。front は `env.type` で描画を切り替える。

| plan | `Type` | `Payload` | 出所 |
|---|---|---|---|
| **h01** | `"StoreListUpdate"` | `proto.StoreListUpdate` | `session.Snapshot()` をそのまま |
| **h02** | `"AdminSnapshot"` | `admin.AdminSnapshot`（スキーマは `plan-h02` §1.4） | room の `buildAdminSnapshot` |

- **h01→h02 は「新 type 追加」**。h01 の front（`StoreListUpdate` 描画）はそのまま生き、h02 で `AdminSnapshot` 分岐を足す。h01 を作り直さない。
- Envelope で包むことで**客向け `/ws` と同じ復号処理**が front で使える。

---

## 5. `/admin` HTTP サーフェス（確定）

| ルート | 内容 |
|---|---|
| `GET /admin` | 静的ダッシュボード（`//go:embed`。現状 main.go に embed 前例なし＝新規） |
| `GET /admin/ws?token=…` | 読み取り専用 観測WS。**`mm.Join` しない**（店にならない） |

- **認証**: `CONFIG_ADMIN_TOKEN` を **`?token=` クエリ**で受ける（ブラウザWSはヘッダ不可）。比較は configapi の `tokenEqual`（定数時間）流用。未設定は 503。
- **token の受け渡し**: `/admin?token=X` でページを開き、ページJSが自身のURLの `token` を読んで `/admin/ws?token=X` を張る。
- **Origin**: `/admin/ws` も `transport.Accept` を使うので Origin 検証が要る。`/ws` と同じ `wsAcceptOptions(ALLOWED_ORIGINS)` を共用。
- **ログ露出**: `?token=` は Caddy/サーバーのアクセスログに残る。ハッカソン規模なら許容。避けたいなら「`/admin` 表示時に httpOnly Cookie 発行 → `/admin/ws` は Cookie 認証」に差し替え可（まずはクエリで可）。

---

## 6. 各 plan が個別に持つもの（ここには書かない）

- **h01**: `AdminHub` 実装本体・`/admin` 静的配信・99店グリッド描画・MVP のテスト。
- **h02**: session への純粋 getter 追加（`Phase`/`HeatLevel`/`RestPoolCount`/`StormState`/`StoreBoard`/`CustomerMix`）・`AdminSnapshot` スキーマ・`buildAdminSnapshot`・storm 予告の保持（`lastStormWarning`）。
- **h03**: `order_attempt` テーブル・`ApplyOrderServed` でのバッファ・一括INSERT。**`is_bot` は game が知らない**ので `app.saveResults` で `botIds` から埋める（`store.Result.IsBot` と同じ流儀）。#104 マージ後の形状に合わせる。
