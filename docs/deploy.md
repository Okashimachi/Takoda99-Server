# デプロイ（Render）

Takoda99-Server を Render にデプロイする手順と、疎通確認の方法。

> 当日のオペレーション手順・ロールバック・トラブル対応は `docs/plan/plan-10_デプロイ戦略.md` に詳しい。
> 本書は「デプロイの仕組みと環境変数」のリファレンス。

## 前提
- サーバーは `$PORT`（Render が注入）を listen する。未設定なら `:8080`。
- ビルドは `Dockerfile`（go 1.25 / public な Takoda99-Proto を go.mod で解決）。go.work は使わない。
- ヘルスチェック: `GET /healthz` → `200 ok`。

## デプロイ手順（Render ダッシュボード）
1. Render で **New > Blueprint** を選び、この `Takoda99-Server` リポジトリを指定する（`render.yaml` が読まれる）。
   - または **New > Web Service** で同リポジトリを選び、Runtime を **Docker** にする（`Dockerfile` を自動検出）。
2. プランは検証中は **Free** で可（スリープあり）。**本番は Starter 以上**でスリープ無効化（#41）。
3. 作成するとビルド→デプロイが走り、`https://<service>.onrender.com` が払い出される。

### 起動モード
- **本番（99店）**: `--mode match`。マッチングプールが人数下限＋カウントダウンで試合を開始する。
- **結合テスト/デモ（solo）**: `--mode solo` にすると `/ws` 接続ごとに「人間1＋Bot」で即試合開始し、単独クライアントで `MatchStart` 以降の全メッセージを検証できる（#36）。
- Docker Command を `/server --mode match --bots 5` 等に上書きして変える。
- **本番前に match へ戻すのを忘れないこと**（#37）。

### 調整値（config）
- 環境変数 `CONFIG_URL` に JSON エンドポイントを設定するとリモート取得（未設定なら内蔵デフォルト）。
- `DATABASE_URL` を設定すると config を **Postgres** から取得し、`GET/POST /api/params` で編集できる（未設定なら内蔵デフォルト、`/api/params` は 503）。
- 優先順位: `DATABASE_URL` > `CONFIG_URL` > 内蔵デフォルト。**DB 接続やマイグレーションに失敗しても起動は止まらない**（内蔵デフォルトへフォールバック）。
- 起動時に設定テーブルを自動作成＋内蔵デフォルトで seed。

#### 反映タイミング（重要）
| パラメータ | 反映 |
|---|---|
| 試合系（信用・我慢・評価・フェーズ・火力・storm・分配） | **次の試合から**（再起動不要） |
| matching 系（minPlayers / maxPlayers / countdown） | **要再起動**（起動時スナップショットのため） |

config はマッチ生成時に読み直すので、進行中の試合のパラメータは固定される。

### 環境変数一覧
| 変数 | 用途 |
|---|---|
| `DATABASE_URL` | Postgres 接続文字列（設定・お題・試合結果） |
| `CONFIG_URL` | config を HTTP 取得する場合のエンドポイント |
| `CONFIG_ADMIN_TOKEN` | `POST /api/params` の共有トークン（`X-Admin-Token` で照合）。未設定だと POST は 503 |
| `CONFIG_FRONT_ORIGIN` | `/api/params` の CORS 許可オリジン（config-front の URL）。カンマ区切りで複数可。未設定は `*` |
| `ALLOWED_ORIGINS` | `/ws` の許可オリジン。カンマ区切り。未設定なら全許可 |
| `GOGC` | GC 頻度の調整（スパイク対策で `200` 推奨・`docs/plan/plan-09`） |

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
```

- `--mode solo` なら接続直後に試合が始まり、`{"type":"MatchStart",...}` が届く。
- `--mode match` なら `{"type":"MatchmakingStatus",...}` が届き、人数が揃うまで待機する。
- upgrade できてこれらが届けば、通信経路（本番）は成立。

> 旧 Textro99 にあった `Welcome` メッセージは**廃止済み**。Takoda99-Proto に存在しないので、
> 届かなくても異常ではない。接続直後の最初の S2C は上記のいずれか。

## 注意
- **Free プランはスリープ**する（無アクセスで停止→次アクセスで起動に数秒）。デモ/本番は Starter 以上へ（#41）。
- **試合中にデプロイすると進行中の試合は消える**（ライブ状態はメモリのため）。ハッカソン中は試合の合間にデプロイする。
- 本番の負荷実測は `docs/plan/plan-11_負荷テスト.md`。
