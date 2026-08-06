# Plan-27: 設定の一元化とハードコードの棚卸し

> **目的**: 「コードを直したのに本番が変わらない」構造をなくす。
> **対応issue**: #85（お題辞書の seed）/ #86（ハードコード棚卸し）／関連 #82（config の seed）
> **優先度**: **§1 は高**（当日の難易度に直結）。§2・§3 は中
> **調査日**: 2026-08-05（`0f4eb16` 時点）

---

## 0. なぜこの plan があるか

同じ形の問題が**3回続けて**出た。

1. **#82** — `game_config` が seed-once。`DefaultParameters()` を変えても本番が変わらない
2. **#85（本 plan §1）** — `words` も seed-once。辞書に足した level 5〜17 が本番に入っていない
3. **§2** — 既定値がコードの2箇所に書かれていて、片方を変えるともう片方と食い違う

いずれも **「値の出どころが1つに決まっていない」** ことが原因。個別に潰すのではなく、
**設定の反映経路を1本にする**のが本 plan の狙い。

---

## 1. DB の seed が「初回だけ」しか走らない

### 1.1 症状（実測 2026-08-05）

| | level 0〜4 | level 5〜17 | 合計 |
|---|---|---|---|
| **本番DB**（`GET /api/words`） | 100語 | **0語** | **100語** |
| **コード**（`internal/odai/data.go`） | 100語 | 260語 | **360語** |

### 1.2 原因

`internal/db/words.go` の `Migrate`:

```go
var count int
if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM words`).Scan(&count); err != nil { … }
if count > 0 {
    return nil          // ← 1件でもあれば何もしない
}
return s.seedFallback(ctx)
```

`internal/db/config.go` の `Migrate` も同じ形（`ON CONFLICT (id) DO NOTHING`）。
**どちらも「運用中の値を壊さない」ための設計**で、意図自体は正しい。
問題は**「コード側に増えたぶんを後から入れる経路が無い」**こと。

### 1.3 影響 — #75 の修正が本番では効いていない

本番は DB 由来の `ConfigurablePool` を使う（`cmd/server/main.go` の `chooseProvider`）。
`Next` は該当 level が無ければ下へ降りるので:

- **`heatLevel` が 5 以上になっても、出るお題は level 4 のまま**
- つまり **#75（火力を上げてもお題が変わらない）は本番では未解決**
- 終盤の難度が上がらず、決着が下位淘汰頼みになる

> ⚠ ヘッドレスsim（`internal/sim`）は `StaticPool`（コード側の辞書）を使うので、
> **この問題は sim では見えない**。「sim では難度が上がるのに本番では上がらない」状態。

### 1.4 修正案（要判断）

`SaveAll` には既に **`upsert` モード**がある（`(text, level)` で冪等）。使う前提で4案。

| 案 | 内容 | 利点 | 欠点 |
|---|---|---|---|
| **A** | 起動時に**常に** fallback を upsert | 実装が最小。確実に揃う | **運営が config-front で消した語が毎回復活する** |
| **B** | 辞書に**バージョン**を持たせ、DB 記録より新しい時だけ upsert | 削除が次の bump まで保持される。意図が明確 | `word_seed_version` テーブル等が要る |
| **C** | seed は現状のまま。**不足を検知して起動ログに警告** | 挙動を変えない。安全 | 人が気付いて手で入れる必要がある |
| **D** | 管理エンドポイント（`POST /api/words/seed`）を足して手動で流す | 明示的。運用の意思で実行できる | エンドポイントが増える |

**推奨は B。** 「コード側の辞書を更新したら、次の起動で反映される」が自然で、
かつ運営の編集を毎回踏み潰さない。

#### 当日までの即応策（コード変更なし）

`POST /api/words` は `mode: "upsert"` を受け付ける。**level 5〜17 の260語を1回流し込めば直る。**

```bash
curl -X POST https://takoda99.mooo.com/api/words \
  -H "X-Admin-Token: $CONFIG_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d @words-5-17.json     # {"mode":"upsert","words":[...]}
```

`words-5-17.json` は `odai.BuildFallbackEntries()` から生成できる。

> ⚠ **`mode: "replace"` を使わないこと。** `DELETE FROM words` が走り、
> 送っていない語（level 0〜4）が全部消える。

### 1.5 `game_config` 側（#82）との関係

同じ構造だが**判断は別**。config は「運営が意図的に設定した値」なので、
起動のたびにコード側の既定で上書きするのは明確に間違い。
words は「素材の追加」なので追記してよい、という非対称がある。

**§1 の修正で config 側の挙動を変えないこと。**

---

## 2. ハードコードの棚卸し（AGENTS §1.3）

### 2.1 結論 — ゲームバランス値の違反は無い

`internal/game/` を全走査した結果、**信用・我慢・評価の重み・フェーズ閾値・火力係数・
storm の周期と閾値・分配の重み・マッチング人数は、すべて `GameParameters` 経由**だった。
session.go に出てくる裸の数値は `elapsed = 1` `keys = 1` `cullCount = 1` のような
**ゼロ除算/丸め落ちのガード**で、調整値ではない。

**AGENTS §1.3 の本体は守られている。** 以下は「別種の問題」として扱う。

### 2.2 既定値がコードの2箇所にある（実害あり）

`GameParameters` の既定値と、それを使う側のフォールバック値が**別々に書かれている**。
片方を変えるともう片方と静かに食い違う。

| 箇所 | 現在値 | 二重になっている相手 |
|---|---|---|
| `internal/room/room.go:58` | `tickMs = 150` | `DefaultParameters().Session.TickIntervalMs` |
| `internal/transport/publisher.go:24` | `intervalMs = 250` | `DefaultParameters().Session.PublishIntervalMs` |
| `internal/bot/bot.go:48` | `iv = 3000ms` | `DefaultParameters().Bot.BaseElapsedMs` |

**修正内容**: フォールバック値を `game.DefaultParameters()` から引く。

```go
// room.go
if tickMs <= 0 {
    tickMs = game.DefaultParameters().Session.TickIntervalMs
}
```

> `room` / `transport` / `bot` はいずれも game を import してよい層（依存は 部品/スパイン → game の
> 一方向）なので、depguard には触れない。

`bot.go` のフォールバックは `Validate()` が `BaseElapsedMs <= 0` を弾くので**到達しない**
（消してもよい）。残すなら既定値から引く。

### 2.3 `GameParameters` に無い運用値

当日いじりたくなるのに config から触れない値。**全部を移す必要は無い**が、
どれを可変にするかは判断が要る。

| 箇所 | 値 | 用途 | 可変にする価値 |
|---|---|---|---|
| `cmd/server/main.go:286` | `maxConcurrentConnections = 200` | 同時接続上限（超過は503） | **中**。当日の想定人数で変わる |
| `cmd/server/main.go:259` | `joinTimeout = 3s` | `MatchmakingJoin` を待つ上限 | 低。クライアントが即送れば無関係 |
| `internal/db/config.go:27` | `cacheTTL = 2s` | config の反映遅延 | **中**。当日「変えたのに効かない」の体感に直結 |
| `internal/transport/connection.go:22` | `writeTimeout = 10s` | 1送信の上限 | 低 |
| `internal/transport/connection.go:57` | `sendBuffer = 64` | 遅いクライアントを切る閾値 | **中**。会場の回線次第で切断が増える |
| `internal/transport/connection.go:53` | `recvBuffer = 16` | 受信バッファ | 低 |
| `internal/matchmaking/matchmaking.go:31` | `MaxDisplayNameLen = 24` | 表示名の上限 | 低。ただし**クライアントに伝わっていない**（下記） |
| `internal/configapi/handler.go:24` | `maxBodyBytes = 64KiB` | リクエスト上限 | 低（安全弁） |

**修正内容の方針**: `SessionParams` を増やすのではなく、**`OpsParams`（運用値）を新設**して
`GameParameters` に足す。ゲームバランスと運用値は寿命も触る人も違うので混ぜない。

> ⚠ `GameParameters` に足すフィールドは **`==` 比較可能を保つ**（AGENTS §1.3）。

**`MaxDisplayNameLen` はクライアントにも要る値**。現在 proto の公開サブセットに無いため、
Unity 側は24文字という制約を知らない。**proto 変更＝要承認**なので別途判断。

### 2.4 `odai.MaxWordLevel` と `heat.maxLevel` の二重管理

```go
// internal/odai/data.go
const MaxWordLevel = 17
// internal/game/params.go — DefaultParameters()
MaxLevel: 17,   // ← 手で揃える運用
```

`game` は `odai` を import できない（依存が逆流するため）ので、**数値で揃えるしかない**のが現状。
コメントには書いてあるが、**機械的な保証が無い**。

**修正内容の候補**:
- 合成ルート（`cmd/server/main.go`）で `pool.MaxLevel()` を読み、`params.Heat.MaxLevel` を上書きする
- または起動時に不一致を**警告ログ**に出す
- テストで `odai.MaxWordLevel == DefaultParameters().Heat.MaxLevel` を固定する（最小）

**最小案（テストで固定）を推奨。** 依存を増やさずに退行だけ止められる。

---

## 3. URL のベタ書き

### 3.1 サーバー本体 — 違反なし

`internal/` `cmd/` を全走査したが、**コード中に URL・ホスト名の直書きは無い**。
接続先はすべて環境変数（`DATABASE_URL` / `CONFIG_FRONT_ORIGIN` / `ALLOWED_ORIGINS` / `PORT`）。

### 3.2 `deploy/Caddyfile` — ドメイン直書き

```
takoda99.mooo.com {
	reverse_proxy localhost:8080
}
```

**修正内容**: Caddy の環境変数展開を使う。

```
{$TAKODA_DOMAIN:takoda99.mooo.com} {
	reverse_proxy localhost:{$TAKODA_PORT:8080}
}
```

**優先度は低い。** ドメインは1つしか無く、変えるのはインフラ作業時だけ。
ただし**ステージング環境を作るなら先に必要**になる。

### 3.3 `takoda99-config`（運営UI・別リポ） — 既定値の直書き

```ts
// lib/api.ts:7
process.env.NEXT_PUBLIC_GAME_SERVER_URL ?? "https://takoda99.mooo.com"
```

環境変数で上書きできるので**動作上の問題は無い**が、
**env を設定し忘れても本番に繋がってしまう**のは事故のもと
（ローカル開発のつもりで本番の config を書き換えられる）。

**修正内容**: 既定値を消して、未設定なら**起動時に落とす**か `http://localhost:8080` にする。
本番は Vercel の環境変数で明示的に設定する。

> これは別リポジトリの話なので、`takoda99-config` 側に issue を立てる。

---

## 4. 優先度と着手順

```
§1 お題辞書の seed        ← 当日の難易度に直結。最優先
  └─ 即応策（upsert を1回流す）だけなら今日できる
§2.2 既定値の二重管理      ← 小さい。ついでに直せる
§2.4 MaxWordLevel の固定   ← テスト1本。ついでに直せる
§2.3 OpsParams の新設      ← 設計判断が要る。当日運用の要否で決める
§3.3 config-front の既定URL ← 別リポ。事故防止として早めに
§3.2 Caddyfile            ← ステージングを作るまで不要
```

## 5. 完了条件

- [ ] 本番DBの `words` が level 0〜17 で埋まっている（`GET /api/words` で360語）
- [ ] コード側の辞書を更新したとき、本番へ反映する経路が決まっている（§1.4 のどれか）
- [ ] 既定値がコードの2箇所に書かれている状態が解消されている（§2.2）
- [ ] `odai.MaxWordLevel` と `heat.maxLevel` の不一致がテストで止まる（§2.4）
- [ ] `GameParameters` に無い運用値のうち、当日いじる可能性があるものが可変になっている（§2.3・要判断）
- [ ] `takoda99-config` が env 未設定で本番へ繋がらない（§3.3）
