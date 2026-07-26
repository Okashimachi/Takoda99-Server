# デプロイ（Render）

textro99-server を Render にデプロイする手順と、疎通確認の方法。

## 前提
- サーバーは `$PORT`（Render が注入）を listen する。未設定なら `:8080`。
- ビルドは `Dockerfile`（go 1.25 / public な Textro99-Proto を go.mod で解決）。go.work は使わない。
- ヘルスチェック: `GET /healthz` → `200 ok`。

## デプロイ手順（Render ダッシュボード）
1. Render で **New > Blueprint** を選び、この `Textro99-Server` リポジトリを指定する（`render.yaml` が読まれる）。
   - または **New > Web Service** で同リポジトリを選び、Runtime を **Docker** にする（`Dockerfile` を自動検出）。
2. プランは検証中は **Free** で可（スリープあり）。本番前に **Starter 以上**でスリープ無効化（#72）。
3. 作成するとビルド→デプロイが走り、`https://<service>.onrender.com` が払い出される。

### 設定（任意）
- 起動モード/Bot数を変える: Docker Command を `/server --mode match --bots 5` 等に上書き。
- **結合テスト用（solo）**: `--mode solo` にすると /ws 接続ごとに「人間1＋Bot」で即試合開始し、単独クライアントで `MatchStart` 以降の全メッセージを検証できる（#56）。本番（99人・match）前に必ず戻す（#57）。現在の `render.yaml` は検証のため一時的に solo。
- 調整値をリモート取得する: 環境変数 `CONFIG_URL` に config-front の JSON エンドポイントを設定（未設定なら内蔵デフォルトで起動）。

### config を DB 化して Web から編集する（#49/#50）
- `DATABASE_URL` を設定すると config を **Postgres**（Neon 想定）から取得し、`GET/POST /api/params` で編集できる（未設定なら内蔵デフォルト、`/api/params` は 503）。
- 起動時に `game_config` テーブルを自動作成＋内蔵デフォルトで seed。config はマッチ生成時に読み直すので、編集は**次の試合から再起動なしで反映**。
- 関連 env:
  - `DATABASE_URL` … Postgres 接続文字列。
  - `CONFIG_ADMIN_TOKEN` … `POST /api/params` の共有トークン（`X-Admin-Token` ヘッダで照合）。未設定だと POST は 503。
  - `CONFIG_FRONT_ORIGIN` … `/api/params` の CORS 許可オリジン（config-front の URL）。**カンマ区切りで複数可**。未設定は `*`。
- config-front（#51）は `GET/POST https://<service>.onrender.com/api/params` を叩く。

### 許可オリジン（ブラウザ結合の要）
ブラウザは `Origin` を必ず送り、サーバーは 2 系統でオリジンを見る。**どちらも末尾スラッシュ無し**で指定する（`https://ex.com` ○ / `https://ex.com/` ✗ = ブラウザの Origin と一致しない。CONFIG 側は自動で除去するが揃えるのが無難）。

- **`ALLOWED_ORIGINS`** … **`/ws`（ゲームクライアント）** の許可オリジン。カンマ区切り（フルURL or `host:port`、`*.vercel.app` 等のワイルドカード可）。
  - **未設定なら全許可**（結合をブロックしないための既定。`/ws` は Cookie 認証等を持たないため実害小）。本番で絞るならフロントのオリジンを列挙。
  - 例: `ALLOWED_ORIGINS=http://localhost:5173,http://localhost:4173,https://<web-front>.vercel.app`
- **`CONFIG_FRONT_ORIGIN`** … **`/api/params`（config-front）** の CORS 許可オリジン（上記）。

> なぜ2つ: `/ws` はゲームクライアント（Web/Unity）、`/api/params` は config-front と**相手が別**なので許可リストを分けている。両方に同じ値を入れても害はない。

## 疎通確認
```bash
# 1) ヘルスチェック（HTTP）
curl https://<service>.onrender.com/healthz          # => ok

# 2) WebSocket 疎通（要 websocat 等）
websocat wss://<service>.onrender.com/ws
#   接続に成功すると、サーバーから Welcome メッセージ（{"type":"Welcome",...}）が届く
```

- WebSocket が upgrade できて Welcome が届けば、通信経路（本番）は成立。
- 実クライアント（Web/Unity）との結合は #60/#61。

## 注意
- **Free プランはスリープ**する（無アクセスで停止→次アクセスで起動に数秒）。デモ/本番は #72 で Starter 以上へ。
- 本番の負荷実測も #72。
