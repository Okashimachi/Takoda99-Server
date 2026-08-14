# デプロイ（GCP Compute Engine / e2-micro）

> 🔴 **本戦当日に慌てて読むなら [runbook.md](runbook.md)。** 戻し手順（退避弁）と
> 当日触ってよい調整値だけを、上から順に読める形でまとめてある。この文書は背景と初回構築。

takoda99-server を GCP の e2-micro VM に置く手順。永続データは Supabase Postgres。

```
ブラウザ / Unity WebGL
      │ wss://takoda99.mooo.com
      ▼
┌─────────────────────────────────────┐
│ GCP Compute Engine e2-micro         │
│ us-west1 / Ubuntu 24.04 LTS         │
│                                     │
│  Caddy :443 ──▶ takoda99 :8080      │
│  (Let's Encrypt)   systemd 常駐     │
└─────────────────────────────────────┘
      │ DATABASE_URL
      ▼
 Supabase Postgres（東京）
```

## Textro99 の時との違い

Textro99 は **VM 上で `docker build` → `docker run`** で運用していた。Takoda99 は
**手元でクロスコンパイルしたバイナリを systemd で常駐**させる。理由は2つ。

1. **e2-micro は RAM 1GB**。Docker デーモンが常駐で 50〜100MB 食う。バイナリは静的リンク
   （`CGO_ENABLED=0`）なので、そもそもコンテナで包む理由（依存の同梱）がない。
2. **デプロイが速い**。0.25 vCPU の共有CPUで `docker build` すると数分かかる（前回の手順書にも
   OOM の注意書きがある）。手元ビルドなら約10秒＋15MB の転送で済む。試合の合間にホットフィックス
   を当てられる差は大きい。

Dockerfile は CI・ローカル検証用に残してあるので、消さないこと。

## ⚠ 無料枠は e2-micro 1台まで

GCP の Always Free は **1アカウントにつき e2-micro 1台**。Textro99 の VM を動かしたまま
Takoda99 用に新規作成すると、**2台目は課金対象**（月$6〜7）になる。

無料枠に収めるなら、Takoda99 の VM を作った後に **Textro99 の VM を停止または削除**する。
1台に相乗りさせるのは不可（99人の試合で 1GB / 0.25vCPU を奪い合う）。

条件は Textro99 の時と同じ:
- リージョンは **us-west1 / us-central1 / us-east1** のいずれか（他は課金）
- ディスクは **30GB** まで
- 静的IPは VM に紐づいている限り無料

---

## 1. VM を作る

Compute Engine →「VM インスタンス」→「インスタンスを作成」。

| 項目 | 設定値 |
|---|---|
| 名前 | `takoda99-server` |
| リージョン | `us-west1` (Oregon) |
| ゾーン | `us-west1-b` |
| マシンタイプ | `e2-micro` |
| ブートディスク | Ubuntu 24.04 LTS / 30GB 標準永続ディスク |
| ファイアウォール | 「HTTPトラフィックを許可」「HTTPSトラフィックを許可」に**両方チェック** |

作成後、**外部IPを静的にする**（エフェメラルのままだと再起動でIPが変わりDNSが外れる）。
「VPC ネットワーク」→「IP アドレス」→ 該当IPの種類を「静的」に変更。名前は `takoda99-ip` など。

## 2. ドメインを用意する

FreeDNS (afraid.org) で **Textro99 とは別のサブドメイン**を取る。

1. Subdomains →「Add」
2. サブドメイン名に `takoda99`、公開ドメインから `mooo.com` を選ぶ
3. Destination に **VM の静的外部IP** を入れて Save

反映確認:

```bash
dig +short takoda99.mooo.com
```

## 3. ユーザーと配置先を作る

VM に SSH（GCPコンソールの「SSH」ボタン）して:

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin takoda99
sudo mkdir -p /opt/takoda99 && sudo chown takoda99:takoda99 /opt/takoda99
```

## 4. 秘密を置く

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

**このファイルは git に入れない。** unit からは `EnvironmentFile=` で読む。

## 5. バイナリを送る

**手元（Mac）**でクロスコンパイル:

```bash
GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/server ./cmd/server
```

転送（`gcloud` CLI が無ければ、GCPコンソールのSSH画面右上「歯車 → ファイルをアップロード」でも可）:

```bash
gcloud compute scp /tmp/server takoda99-server:/tmp/server --zone us-west1-b
```

配置:

```bash
sudo install -o takoda99 -g takoda99 -m 755 /tmp/server /opt/takoda99/server
```

## 6. systemd で常駐させる

リポジトリの `deploy/takoda99.service` を置く:

```bash
sudo cp deploy/takoda99.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now takoda99
```

確認:

```bash
curl http://localhost:8080/healthz
```

## 7. Caddy で TLS を張る

DNS が反映されてから:

GCE の Ubuntu イメージは最小構成で **`gnupg` が入っていない**。先に入れること
（無いと鍵を変換できず、リポジトリが未署名扱いになって apt が拒否する）。
`debian-keyring` / `debian-archive-keyring` は Debian 用なので Ubuntu では不要。

```bash
sudo apt-get update && sudo apt-get install -y gnupg curl
```

```bash
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt-get update && sudo apt-get install -y caddy
```

```bash
sudo cp deploy/Caddyfile /etc/caddy/Caddyfile
sudo systemctl restart caddy
```

確認（証明書の取得に数十秒かかることがある）:

```bash
curl https://takoda99.mooo.com/healthz
```

## 8. クライアントの接続先を変える

`Takoda99-Unity` の接続先を `wss://takoda99.mooo.com/ws` に向ける。
WebGL ビルドの場合はブラウザのオリジンが `ALLOWED_ORIGINS` に入っている必要がある（→ Plan-21）。

> ⚠ 旧記述は `Takoda99-WebFront`（Vercel の環境変数）だったが、**同リポジトリは廃止・凍結**され、
> クライアントは Unity のみ。

---

## 2回目以降のデプロイ

手元でビルドして送り、再起動するだけ。

> **`gcloud` CLI はこの環境に入っていない。**実際に使うのは下の「コンソール経由」の手順。
> `gcloud` を入れてある場合のみ、末尾の gcloud 版が使える。

### 手元でビルド

```bash
GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /tmp/server ./cmd/server
```

`-ldflags="-s -w"` はシンボルを落として 16MB → 11MB にするため（コンソールのアップローダを通すので効く）。

ビルドしたものが「どのコミットか」は後から確認できる。デプロイ後に「今動いているのはいつのビルドか」
分からなくなったら、バイナリを手元に持ってきてこれを見る:

```bash
go version -m /tmp/server | head -5
```

`mod takoda99 v0.0.0-<日時>-<コミット>` と `dep github.com/Okashimachi/Takoda99-Proto v0.x.y` が出る。

### 転送（GCPコンソール経由）

GCPコンソール → VM の **SSH** ボタン → 右上の **⚙ → ファイルをアップロード** で
バイナリ（または tgz）を選ぶ。ホームディレクトリに入る。

### 差し替えて再起動

```bash
sudo install -o takoda99 -g takoda99 -m 755 ~/server /opt/takoda99/server && sudo systemctl restart takoda99
```

### 反映確認

```bash
sudo journalctl -u takoda99 -n 5 --no-pager | grep config
```

**`config: Postgres から取得（DATABASE_URL）`** が出ていること。
`config: 内蔵デフォルトで起動` なら **`DATABASE_URL` が渡っていない**（DB接続失敗ではない）。
→ 下のトラブルシューティング参照。

### unit ファイルは毎回コピーしなくてよい

`deploy/takoda99.service` に変更が無いなら**触らない**。差し替えると、サーバー上で手を入れた
設定を上書きする。変更した時だけ:

```bash
sudo cp deploy/takoda99.service /etc/systemd/system/ && sudo systemctl daemon-reload
```

### gcloud CLI がある場合

```bash
gcloud compute scp /tmp/server takoda99-server:/tmp/server --zone us-west1-b
gcloud compute ssh takoda99-server --zone us-west1-b --command 'sudo install -o takoda99 -g takoda99 -m 755 /tmp/server /opt/takoda99/server && sudo systemctl restart takoda99'
```

> ⚠ **試合中にデプロイしない**。試合状態は in-memory なので再起動で進行中の試合が消える。
> 試合の合間（MatchEnd 後・次の MatchStart 前）に行うこと。

## 運用コマンド

```bash
sudo systemctl status takoda99
```

```bash
sudo journalctl -u takoda99 -f
```

## 設定の反映タイミング

- **試合系パラメータ**（credit/customer/eval/phase/heat/storm/distribution/patience）:
  config-front で編集すると**次の試合から**再起動なしで反映。
- **matching 系**（minPlayers/maxPlayers/startCountdownMs/minFill）: **再起動不要。数秒で反映される**。
  matchmaking の `GetParams` が待機ループのたびに `provider.Load` を呼ぶ（`cmd/server/main.go`）。
  反映は「マッチングループ周期(1秒) ＋ ConfigStore の2秒キャッシュ」でおおむね3秒以内。
  ⚠ 当日「人が集まらないので `minPlayers` を下げる」は**サーバーを触らずに config から実施できる**。
  再起動すると進行中の試合が消えるので、**この目的で再起動してはいけない**。
- **環境変数**: `/etc/takoda99.env` を書き換えて `sudo systemctl restart takoda99`。

## 環境変数一覧

| 変数 | 用途 | 未設定時 |
|---|---|---|
| `DATABASE_URL` | Supabase Postgres 接続文字列 | 内蔵デフォルトで起動。API は 503 |
| `CONFIG_ADMIN_TOKEN` | `POST /api/params` `POST /api/words` の共有トークン（`X-Admin-Token`） | POST は 503 |
| `CONFIG_FRONT_ORIGIN` | `/api/params` `/api/words` の CORS 許可オリジン。カンマ区切り可 | `*`（全許可） |
| `ALLOWED_ORIGINS` | `/ws` の許可オリジン。カンマ区切り、ワイルドカード可 | 全許可 |
| `GOGC` | GC のトリガ間隔（unit で 200） | 100 |
| `PORT` | listen ポート | `8080` |

### 許可オリジンが2系統ある理由

`/ws` はゲームクライアント（Web/Unity）、`/api/*` は config-front と**相手が別**なので分けている。
どちらも**末尾スラッシュ無し**で指定する。`ALLOWED_ORIGINS` は未設定なら全許可
（`/ws` は Cookie 認証等を持たないため実害小）。

## 疎通確認

```bash
curl https://takoda99.mooo.com/healthz
```

```bash
websocat wss://takoda99.mooo.com/ws
```

match モードなら接続後に `MatchmakingStatus` が届く。

## 負荷まわり（Plan-09）

- `/ws` の同時接続は **200 で頭打ち**（超過は 503）。99人＋余裕の設定。
- `GOGC=200` / `GOMAXPROCS=2` を unit で設定済み。

### 実機計測の結果（2026-08-05 / e2-micro us-west1 / 99体・TLS経由）

99クライアントを実WebSocketで接続し、1試合（163秒）を完走させた時の消費。

| 項目 | 実測 | e2-micro の予算に対して |
|---|---|---|
| CPU: server | 16 CPU秒 → 0.098 vCPU | ベースライン0.25の **39%** |
| CPU: Caddy(TLS) | 6 CPU秒 → 0.037 vCPU | 同 **15%** |
| **CPU 合計** | **0.135 vCPU** | **54%（約2倍の余裕）** |
| メモリ | server 21MB + caddy 49MB | 1GB中 **7%** |
| 通信量(TX) | **645 MB / 1試合** | 下記 |
| 機能 | 接続99/99・MatchStart/End 99/99・エラー0 | — |

**結論: e2-micro で本番を回せる。** 格上げ（e2-small）も東京への移設も現時点では不要。

**唯一の注意は egress**。無料枠は月1GBなので**1試合で65%を消費**する。超過分は1GBあたり$0.1前後
（最新の料金表で要確認）＝1試合10円強。通信量のほぼ全部が `StoreListUpdate` のフル配信なので、
削るなら Plan-11 の間引き（4Hz→2Hz で半減、差分配信なら桁で減る）が効く。

> 計測の注意: `ps -o %cpu` は**プロセスの生涯平均**で瞬間値ではない。長時間起動したプロセスに
> 短時間の負荷をかけても薄まって見える。CPU時間の差分（`/proc/<pid>/stat` の utime+stime、
> または `ps -o time=`）で測ること。

## トラブルシューティング

**`curl: (7) Failed to connect`** — 80/443 が開いていない。VM 作成時のチェックを入れ忘れた場合は
「VPC ネットワーク → ファイアウォール」で `default-allow-http` / `default-allow-https` を確認。

**Caddy が証明書を取得できない** — `sudo journalctl -u caddy --no-pager -n 50` を見る。
DNS 未反映（A レコードが VM の IP を指していない）が大半。`dig +short takoda99.mooo.com` で確認。

**サーバーが起動しない** — `sudo journalctl -u takoda99 -n 50`。
`/etc/takoda99.env` の権限（600）と `/opt/takoda99/server` の実行権限（755）を確認。

**`config: 内蔵デフォルトで起動` が出て `/api/params` が JSON を返さない** — DB接続の失敗ではなく
**`DATABASE_URL` が渡っていない**。このログは `chooseProvider` が env も `CONFIG_URL` も空だった時にだけ出る
（接続失敗なら `config: DB接続失敗のため設定は内蔵デフォルト: ...` と理由付きで出る）。
また ConfigStore が無いと `/api/params` のハンドラ自体が登録されず 404 本文が返るので、
`json.load` が「Expecting value」で落ちる。

まず unit が env ファイルを読めているか見る:

```bash
sudo journalctl -u takoda99 --no-pager | grep -i 'unknown key\|environmentfile'
sudo systemctl show takoda99 -p EnvironmentFiles
```

`Unknown key '-EnvironmentFile'` が出ていたら unit の構文ミス。**`-` はキーではなく値に付ける**:

```
EnvironmentFile=-/etc/takoda99.env     ← 正
-EnvironmentFile=/etc/takoda99.env     ← 誤（systemd が行ごと黙って無視する）
```

systemd は知らないキーをエラーにせず無視するので、**起動は成功したように見える**。
2026-08-05 に本番で実際に踏んだ（#83）。CI に検査を入れてある。

**WSS は繋がるが試合が始まらない** — `--mode match` は minPlayers 到達＋カウントダウンが要る。
1人で確認したいなら `--mode solo`（unit の `ExecStart` を一時的に変更）。
