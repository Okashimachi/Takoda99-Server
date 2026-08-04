# デプロイ（GCP Compute Engine）

takoda99-server を GCP の e2-micro VM に置く手順と、疎通確認の方法。

構成は **VM 1台 + Caddy(TLS終端) + systemd**、永続データは **Supabase Postgres**。

```
ブラウザ / Unity WebGL
      │ wss://<ドメイン>
      ▼
┌─────────────────────────────────┐
│ GCP Compute Engine (e2-micro)   │
│                                 │
│  Caddy :443 ──▶ server :8080    │
│  (Let's Encrypt)   systemd      │
└─────────────────────────────────┘
      │ DATABASE_URL
      ▼
 Supabase Postgres
```

> **未確定**: 実際の VM 名・リージョン・ドメインは Cloud Console で確認して埋めること。
> Textro の時のデプロイ設定はリポジトリに残っていない（手作業だったため）。

## 前提

- サーバーは `$PORT` があればそれを、無ければ `:8080` を listen する。
- バイナリは **静的リンク**（`CGO_ENABLED=0`）。VM 側に Go もライブラリも要らない。
- ヘルスチェック: `GET /healthz` → `200 ok`。
- **e2-micro は RAM 1GB・共有vCPU**。VM 上でビルドすると OOM しかねないので、**手元でクロスコンパイルして転送する**。

## 初回セットアップ

### 1. VM を用意する

- マシンタイプ **e2-micro**、OS は Debian か Ubuntu LTS。
- **外部IPは「静的」に予約する**（エフェメラルだと再起動でIPが変わりDNSが外れる）。
- ファイアウォールで **80/443 を開ける**（80 は Let's Encrypt の取得に要る）。8080 は開けない。

> 無料枠の e2-micro は **us-west1 / us-central1 / us-east1** に限られる。
> 東京リージョンに置くと課金対象になるかわりに RTT が大幅に縮む。料金は最新の GCP 料金表で確認すること。

### 2. ユーザーと配置先を作る

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin takoda99
sudo mkdir -p /opt/takoda99 && sudo chown takoda99:takoda99 /opt/takoda99
```

### 3. 秘密を置く

```bash
sudo tee /etc/takoda99.env >/dev/null <<'EOF'
DATABASE_URL=postgresql://...@aws-0-ap-northeast-1.pooler.supabase.com:5432/postgres
CONFIG_ADMIN_TOKEN=...
CONFIG_FRONT_ORIGIN=https://<config-front>.vercel.app
ALLOWED_ORIGINS=https://<web-front>.vercel.app
EOF
sudo chmod 600 /etc/takoda99.env
sudo chown root:root /etc/takoda99.env
```

**このファイルは git に入れない。** unit ファイルからは `EnvironmentFile=` で読む。

### 4. systemd unit を入れる

```bash
sudo cp deploy/takoda99.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable takoda99
```

### 5. Caddy で TLS を張る

ドメインの A レコードを VM の静的IPに向けてから:

```bash
sudo apt install -y caddy
sudo cp deploy/Caddyfile /etc/caddy/Caddyfile   # ドメインを実際のものへ書き換えてから
sudo systemctl reload caddy
```

Caddy が Let's Encrypt の証明書を自動取得・自動更新する。WebSocket の upgrade はそのまま素通しされる。

## デプロイ（2回目以降もこれ）

手元でクロスコンパイルして転送し、再起動する。

```bash
GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/server ./cmd/server
```

```bash
gcloud compute scp /tmp/server <VM名>:/tmp/server --zone <ゾーン>
```

```bash
gcloud compute ssh <VM名> --zone <ゾーン> --command 'sudo install -o takoda99 -g takoda99 -m 755 /tmp/server /opt/takoda99/server && sudo systemctl restart takoda99'
```

> ⚠ **試合中にデプロイしない**。試合状態は in-memory なので、再起動で進行中の試合が消滅する。
> 試合の合間（MatchEnd 後・次の MatchStart 前）に行うこと。

## 運用コマンド

```bash
sudo systemctl status takoda99
```

```bash
sudo journalctl -u takoda99 -f
```

## 設定の反映タイミング

- **試合系パラメータ**（credit/customer/eval/phase/heat/storm/distribution/patience）: config-front で編集すると**次の試合から**再起動なしで反映。
- **matching 系**（minPlayers/maxPlayers/startCountdownMs）: 起動時スナップショットなので**再起動が要る**。
- **環境変数**: `/etc/takoda99.env` を書き換えて `systemctl restart takoda99`。

## 環境変数一覧

| 変数 | 用途 | 未設定時 |
|---|---|---|
| `DATABASE_URL` | Supabase Postgres 接続文字列 | 内蔵デフォルトで起動。API は 503 |
| `CONFIG_ADMIN_TOKEN` | `POST /api/params` `POST /api/words` の共有トークン（`X-Admin-Token`） | POST は 503 |
| `CONFIG_FRONT_ORIGIN` | `/api/params` `/api/words` の CORS 許可オリジン。カンマ区切り可 | `*`（全許可） |
| `ALLOWED_ORIGINS` | `/ws` の許可オリジン。カンマ区切り、ワイルドカード可 | 全許可 |
| `GOGC` | GC のトリガ間隔（unit で 200） | 100 |
| `PORT` | listen ポート | `8080` |

### 許可オリジン（ブラウザ結合の要）

ブラウザは `Origin` を必ず送り、サーバーは 2 系統でオリジンを見る。**どちらも末尾スラッシュ無し**で指定する。

- **`ALLOWED_ORIGINS`** … `/ws`（ゲームクライアント）用。未設定なら全許可（`/ws` は Cookie 認証等を持たないため実害小）。
- **`CONFIG_FRONT_ORIGIN`** … `/api/*`（config-front）用の CORS 許可オリジン。

> なぜ2つ: `/ws` はゲームクライアント（Web/Unity）、`/api/*` は config-front と**相手が別**なので分けている。

## 疎通確認

```bash
curl https://<ドメイン>/healthz          # => ok
```

```bash
websocat wss://<ドメイン>/ws
```

match モードなら接続後に `MatchmakingStatus` が届く。

## 負荷まわり（Plan-09）

- `/ws` の同時接続は **200 で頭打ち**（超過は 503）。99人＋余裕の設定。
- `GOGC=200` / `GOMAXPROCS=2` を unit で設定済み。
- 99接続の実負荷計測は Plan-11。
