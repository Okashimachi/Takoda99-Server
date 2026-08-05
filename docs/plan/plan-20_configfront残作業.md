# Plan-20: config-front / DB 基盤の残作業

> **目的**: 運営用の管理UI（`takoda99-config`）とサーバーのスキーマを一致させ、当日パラメータを調整できる状態にする。
> **対応issue**: #43
> **優先度**: 中。当日のバランス調整に要るが、サーバー側は概ね完了済み。
> **依存**: なし
> **前身**: `docs/plan/plan-06_config基盤とDB.md`（設計）

---

## 0. 現状 — サーバー側はほぼ完了している

実装を確認した結果:

| 項目 | 状態 |
|---|---|
| Postgres（Supabase 東京） | ✅ `docs/deploy.md` |
| `GameParameters` たこ焼き版フルスキーマ | ✅ 12セクション・旧項目0件 |
| `GET/POST /api/params` | ✅ `internal/configapi/handler.go` |
| `GET/POST/DELETE /api/words` | ✅ `internal/configapi/words.go` |
| `backfillDefaults`（新セクション追加時のゼロ値対策） | ✅ `internal/db/config.go:135` |
| 設定の反映（試合系=次試合 / matching=数秒） | ✅ `docs/deploy.md` |
| 管理UIリポジトリ `takoda99-config` | ✅ 存在する |

現在のスキーマ（12セクション）:

```go
Session / Matching / Credit / Customer / Eval / Phase /
Heat / Storm / Distribution / Patience / Presentation / Bot
```

**残っているのは主に config-front 側（別リポジトリ）と、その疎通確認。**

---

## 1. 残作業

### Step 1: スキーマの一致確認（最優先）

`takoda99-config` の TS 型がサーバーの 12 セクションと**キー名まで一致**しているか確認する。

サーバー側の正確なキー名を出す:

```bash
go run ./cmd/server --mode solo --bots 0 &
curl -s http://localhost:8080/api/params | jq 'keys'
```

DB 未設定だと 503 なので、実際は本番から取る:

```bash
curl -s https://takoda99.mooo.com/api/params | jq 'keys'
```

期待:

```json
["bot","credit","customer","distribution","eval","heat","matching",
 "patience","phase","presentation","session","storm"]
```

各セクションの中まで突き合わせる:

```bash
curl -s https://takoda99.mooo.com/api/params | jq '.credit, .patience, .distribution, .presentation'
```

`takoda99-config` の `lib/params.ts`（または相当のスキーマ定義）と1対1で照合し、
**ズレていたら TS 側を直す**（サーバーが正典）。

> **ズレると何が起きるか**: POST 時に未知のキーは無視され、欠けたキーはゼロ値になる。
> `backfillDefaults` はセクション丸ごとゼロの時しか救済しないので、
> **セクション内の一部フィールドだけ欠けると 0 が保存されて試合が壊れる**（例: `patience.lateMul=0` で0除算）。
> ここが本プランで一番危ない箇所。

### Step 2: `Presentation` セクションの UI

`Presentation`（演出しきい値・#64 で追加）が config-front に無い可能性が高い。
追加されているか確認し、無ければ足す。

```bash
curl -s https://takoda99.mooo.com/api/params | jq '.presentation'
```

### Step 3: デプロイ確認

- [ ] `takoda99-config` が Vercel にデプロイされている（**Textro の `config-front-self.vercel.app` とは別URL**）
- [ ] 環境変数
  - `NEXT_PUBLIC_API_URL` = `https://takoda99.mooo.com`
  - 管理トークン = サーバーの `CONFIG_ADMIN_TOKEN` と同じ値
- [ ] サーバーの `CONFIG_FRONT_ORIGIN` に config-front のオリジンが入っている

```bash
sudo grep CONFIG_FRONT_ORIGIN /etc/takoda99.env
```

### Step 4: 往復テスト

実際に値を変えて反映されるか確認する。**当日ぶっつけで試さない**。

1. config-front で `matching.minPlayers` を 20 → 5 に変更して保存
2. `curl -s https://takoda99.mooo.com/api/params | jq .matching.minPlayers` → 5
3. 新規接続して `MatchmakingStatus.minPlayers` が 5 になる（**数秒で反映・再起動不要**）
4. 元に戻す

同様に試合系も:

1. `credit.initialLife` を 3 → 5 に変更
2. **次の試合の** `MatchStart.params.initialLife` が 5 になる（進行中の試合は変わらない）
3. 元に戻す

### Step 5: 全セクションの保存確認

12セクション全てを config-front から編集 → 保存 → `GET` で戻ることを確認。
1つでも保存できないセクションがあると、当日そこを触れない。

```bash
curl -s https://takoda99.mooo.com/api/params | jq . > /tmp/before.json
# config-front で各セクションを1項目ずつ変えて保存
curl -s https://takoda99.mooo.com/api/params | jq . > /tmp/after.json
diff /tmp/before.json /tmp/after.json
```

---

## 2. 注意点

### Validate に弾かれる値を入れない

サーバーは `GameParameters.Validate()` で破綻値を弾く。弾かれると **POST が 400 で失敗**する。
config-front 側にも同じ範囲のバリデーションを入れておくと、運営が保存ボタンを押してから
気づく事態を避けられる。

```bash
grep -n -A 30 "func (gp GameParameters) Validate" internal/game/params.go
```

### 2秒キャッシュ

`ConfigStore` には 2秒のキャッシュがある（`internal/db/config.go:60`）。
**保存直後の `GET` が古い値を返しうる**。config-front は保存レスポンスをそのまま採用し、
`GET` で確認し直さない設計にする（#68 で `configHash` を返すようにすれば照合手段ができる）。

### 本番と solo で DB を共有している

Plan-15 の solo インスタンスは本番と同じ DB を見る。
config を変えると**両方に効く**。これは意図的（本番と同条件で検証するため）。

---

## 3. 完了条件

- [ ] `takoda99-config` の TS スキーマがサーバーの12セクションと**キー名まで一致**している
- [ ] `Presentation` セクションが config-front で編集できる
- [ ] `takoda99-config` が Takoda 専用URLで Vercel にデプロイされている
- [ ] `NEXT_PUBLIC_API_URL` が `https://takoda99.mooo.com` を向いている
- [ ] サーバーの `CONFIG_FRONT_ORIGIN` に config-front のオリジンが入っている
- [ ] 12セクション全てを編集 → 保存 → GET で反映される
- [ ] `matching.minPlayers` の変更が**再起動なしで数秒で反映**されることを実測で確認
- [ ] 試合系パラメータの変更が**次の試合から**反映されることを実測で確認
- [ ] config-front 側にサーバーの `Validate()` と同じ範囲チェックがある
- [ ] お題単語（`/api/words`）の一覧・追加・削除が UI から動く
