# デプロイ（Render）

takoda99-server を Render にデプロイする手順と、疎通確認の方法。

## 前提
- サーバーは `$PORT`（Render が注入）を listen する。未設定なら `:8080`。
- ビルドは `Dockerfile`（go 1.25 / public な Takoda99-Proto を go.mod で解決）。go.work は使わない。
- ヘルスチェック: `GET /healthz` → `200 ok`。

## デプロイ手順（Render ダッシュボード）
1. Render で **New > Blueprint** を選び、この `Takoda99-Server` リポジトリを指定する（`render.yaml` が読まれる）。
   - または **New > Web Service** で同リポジトリを選び、Runtime を **Docker** にする（`Dockerfile` を自動検出）。
2. プランは検証中は **Free** で可（スリープあり）。本番前に **Starter 以上**でスリープ無効化。
3. 作成するとビルド→デプロイが走り、`https://<service>.onrender.com` が払い出される。

### 設定（任意）
- 起動モード/Bot数を変える: Docker Command を `/server --mode match --bots 5` 等に上書き。
- **結合テスト用（solo）**: `--mode solo` にすると /ws 接続ごとに「人間1＋Bot」で即試合開始し、単独クライアントで `MatchStart` 以降の全メッセージを検証できる。本番（99人・match）前に必ず戻す。
- 調整値をリモート取得する: 環境変数 `CONFIG_URL` に config-front の JSON エンドポイントを設定（未設定なら内蔵デフォルトで起動）。

### DB 設定（Supabase Postgres）
- `DATABASE_URL` を設定すると **Supabase Postgres**（東京リージョン）に接続し、以下が有効になる:
  - `game_config` — `GET/POST /api/params` で GameParameters を Web 編集
  - `words` — `GET/POST/DELETE /api/words` でお題単語を管理
  - `match` + `store_result` — 試合結果を自動保存（best-effort）
- 起動時にテーブルを自動作成（`Migrate`）し、空なら内蔵デフォルトで seed する。
- `DATABASE_URL` 未設定なら内蔵デフォルトで起動し、`/api/params` と `/api/words` は 503。

### 環境変数一覧

| 変数 | 用途 | 未設定時 |
|---|---|---|
| `DATABASE_URL` | Supabase Postgres 接続文字列 | 内蔵デフォルトで起動。API は 503 |
| `CONFIG_ADMIN_TOKEN` | `POST /api/params` および `POST /api/words` の共有トークン（`X-Admin-Token` ヘッダ） | POST は 503 |
| `CONFIG_FRONT_ORIGIN` | `/api/params` と `/api/words` の CORS 許可オリジン。カンマ区切りで複数可 | `*`（全許可） |
| `ALLOWED_ORIGINS` | `/ws`（ゲームクライアント）の許可オリジン。カンマ区切り、ワイルドカード可 | 全許可 |
| `CONFIG_URL` | config-front JSON エンドポイント（DB 未使用時の外部取得用） | 内蔵デフォルト |

### 許可オリジン（ブラウザ結合の要）
ブラウザは `Origin` を必ず送り、サーバーは 2 系統でオリジンを見る。**どちらも末尾スラッシュ無し**で指定する。

- **`ALLOWED_ORIGINS`** … **`/ws`（ゲームクライアント）** の許可オリジン。
  - **未設定なら全許可**（`/ws` は Cookie 認証等を持たないため実害小）。本番で絞るならフロントのオリジンを列挙。
  - 例: `ALLOWED_ORIGINS=http://localhost:5173,http://localhost:4173,https://<web-front>.vercel.app`
- **`CONFIG_FRONT_ORIGIN`** … **`/api/params` と `/api/words`（config-front）** の CORS 許可オリジン。

> なぜ2つ: `/ws` はゲームクライアント（Web/Unity）、`/api/*` は config-front と**相手が別**なので許可リストを分けている。両方に同じ値を入れても害はない。

## 疎通確認
```bash
# 1) ヘルスチェック（HTTP）
curl https://<service>.onrender.com/healthz          # => ok

# 2) WebSocket 疎通（要 websocat 等）
websocat wss://<service>.onrender.com/ws
#   接続に成功すると、サーバーから MatchmakingStatus が届く（match モードの場合）
```

## 注意
- **Free プランはスリープ**する（無アクセスで停止→次アクセスで起動に数秒）。デモ/本番は Starter 以上へ。
