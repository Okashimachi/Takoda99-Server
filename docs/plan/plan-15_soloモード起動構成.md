# Plan-15: Webフロント単独結合テスト用の solo モード起動構成（GCP）

> **目的**: `Takoda99-WebFront` の開発者が**1接続だけで MatchStart 以降の全メッセージを検証できる**エンドポイントを用意する。
> **対応issue**: #36
> **優先度**: **高**。WebFront の開発を現在ブロックしている。
> **依存**: なし（サーバー本体は稼働中）
> **参照**: `docs/deploy.md`, `deploy/takoda99.service`, `deploy/Caddyfile`

---

## 0. なぜ要るか

現状の本番は `--mode match` で、`Matching.MinPlayers`（既定20）に達してカウントダウンが済むまで試合が始まらない。
WebFront 開発者が1人で繋いでも `MatchmakingStatus` が届き続けるだけで、**`MatchStart` 以降を一切検証できない**。

`--mode solo` は `/ws` 接続ごとに「人間1＋Bot」で即試合を開始するので、単独で最後まで通せる。

現在の稼働状況:

```
wss://takoda99.mooo.com/ws   … --mode match（本番）
```

---

## 1. 方式の選択 — **方式Bを採用する**

### 方式A: 本番VMの systemd を一時的に solo へ切り替える

```bash
sudo sed -i 's|--mode match|--mode solo --bots 5|' /etc/systemd/system/takoda99.service
sudo systemctl daemon-reload && sudo systemctl restart takoda99
```

- 手軽。だが**本番と同じエンドポイントが solo になる**
- 戻し忘れると「イベント当日に1人接続しただけで試合が始まる」という致命的な事故
- **Textro で実際に「検証のため一時的に solo」のまま放置された前例がある**

### 方式B: 同一VMに solo 用の2つ目のインスタンスを常駐（**採用**）

別ポート（8081）で `--mode solo --bots 5` を常駐させ、Caddy で別サブドメインへ振る。

- 本番の match モードを**一度も触らない** → 戻し忘れが構造的に起こり得ない
- メモリ実測は server 21MB。2プロセスでも 1GB に対して余裕（実測は `docs/deploy.md`）
- unit ファイルと Caddy のブロックを1つずつ増やすだけ

### 方式C: `minPlayers` を 1 に下げる

config-front から変更でき、**matching 系も再起動不要で数秒で反映される**（`docs/deploy.md`）。
ただし**本番の設定そのものを触る**ことになり、戻し忘れのリスクは方式Aと同じ。採らない。

> **結論: 方式B**。これにより Plan-16（#37「本番前に match へ戻す」）は不要になり、close できる。

---

## 2. 実装手順

### Step 1: solo 用サブドメインを取る

FreeDNS (afraid.org) で本番と同じ静的IPに向けたサブドメインを追加する。

1. Subdomains →「Add」
2. サブドメイン名 `takoda99-solo`、公開ドメイン `mooo.com`
3. Destination に **本番と同じ VM の静的外部IP**

```bash
dig +short takoda99-solo.mooo.com    # 本番と同じIPが返ればOK
```

### Step 2: solo 用の systemd unit を作る

`deploy/takoda99-solo.service`（新規・リポジトリに置く）:

```ini
# deploy/takoda99-solo.service — WebFront 単独結合テスト用の solo インスタンス。
#
# 本番（takoda99.service / :8080 / --mode match）とは別プロセス・別ポートで常駐する。
# 本番の起動モードを一切触らないので「solo のまま本番当日を迎える」事故が起きない（#36 方式B）。
#
#   sudo cp deploy/takoda99-solo.service /etc/systemd/system/
#   sudo systemctl daemon-reload && sudo systemctl enable --now takoda99-solo
#
# バイナリは本番と同じ /opt/takoda99/server を共有する（デプロイは1回で両方に反映）。

[Unit]
Description=Takoda99 game server (solo mode for client integration)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=takoda99
Group=takoda99
ExecStart=/opt/takoda99/server --mode solo --bots 5
WorkingDirectory=/opt/takoda99

# 本番と同じ秘密を使う（DB は共有。設定・お題を本番と同条件で検証できる）。
-EnvironmentFile=/etc/takoda99.env

# 本番(8080)と衝突しないポート。EnvironmentFile の後に置いて上書きする。
Environment=PORT=8081
Environment=GOGC=200
Environment=GOMAXPROCS=1

Restart=always
RestartSec=2
KillSignal=SIGTERM
TimeoutStopSec=20

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true

[Install]
WantedBy=multi-user.target
```

> **`Environment=PORT=8081` は `-EnvironmentFile=` より後に書く**。systemd は後勝ちなので、
> env ファイルに PORT があっても 8081 が優先される。順序を逆にすると本番と同じポートを
> 掴もうとして起動に失敗する。

> **`GOMAXPROCS=1`**: solo は検証用で負荷が軽い。本番(2)とCPUを奪い合わないよう絞る。

### Step 3: Caddy に solo 用ブロックを足す

`deploy/Caddyfile` を更新:

```
takoda99.mooo.com {
	reverse_proxy localhost:8080
}

# WebFront 単独結合テスト用の solo インスタンス（#36 方式B）。
# 本番(:8080/match)とは別プロセス。ここが solo でも本番は match のまま。
takoda99-solo.mooo.com {
	reverse_proxy localhost:8081
}
```

### Step 4: VM に反映

```bash
sudo cp deploy/takoda99-solo.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now takoda99-solo
```

```bash
sudo cp deploy/Caddyfile /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

### Step 5: `ALLOWED_ORIGINS` に WebFront を追加

```bash
sudo nano /etc/takoda99.env
# ALLOWED_ORIGINS=http://localhost:5173,https://<webfront>.vercel.app
```

```bash
sudo systemctl restart takoda99 takoda99-solo
```

> 本番も再起動されるので**試合中は避ける**。

---

## 3. 動作確認

```bash
curl https://takoda99-solo.mooo.com/healthz
```

```bash
websocat wss://takoda99-solo.mooo.com/ws
```

期待: **接続直後に `MatchStart` が届く**（`MatchmakingStatus` を待たない）。

本番が影響を受けていないことも必ず確認する:

```bash
websocat wss://takoda99.mooo.com/ws
```

期待: `MatchmakingStatus` が届き続ける（＝match のまま）。

---

## 4. WebFront への周知

伝えること:

| 項目 | 値 |
|---|---|
| 結合テスト用 | `wss://takoda99-solo.mooo.com/ws` … 1接続で即試合開始 |
| 本番 | `wss://takoda99.mooo.com/ws` … 20人揃うまで待機 |
| プロトコル | Takoda99-Proto v0.3.0（`matchTimeLimitMs` は削除済み） |
| 結合ガイド | `docs/client-integration.md` |

**両方のシーケンスに対応して作ってもらう**こと（本番は match なので `MatchmakingStatus` 待機の画面が要る）。

---

## 5. 運用上の注意

- solo インスタンスは**本番と同じ DB を見る**。config-front でパラメータを変えると両方に効く。これは意図的（本番と同条件で検証したいため）
- solo は常駐させたままでよい。当日も動いていて問題ない（本番とポートもドメインも別）
- デプロイ時は両方を再起動する:
  ```bash
  sudo systemctl restart takoda99 takoda99-solo
  ```
  `docs/deploy.md` の「2回目以降のデプロイ」にこれを追記する

---

## 6. 完了条件

- [ ] 方式Bを採用（本番の起動モードを触らない）
- [ ] `takoda99-solo.mooo.com` が VM の静的IPを向いている
- [ ] `deploy/takoda99-solo.service` がリポジトリにあり、VM で有効化されている
- [ ] `deploy/Caddyfile` に solo 用ブロックがあり、TLS が張れている
- [ ] `wss://takoda99-solo.mooo.com/ws` に1接続すると**即 `MatchStart` が届く**
- [ ] `wss://takoda99.mooo.com/ws` は **`MatchmakingStatus` のまま**（本番が無傷）
- [ ] `ALLOWED_ORIGINS` に WebFront の dev/本番オリジンが入っている
- [ ] WebFront 開発者が1接続で `MatchStart → CustomerArrived → OrderServed → MatchEnd` まで到達できる
- [ ] `docs/deploy.md` にデプロイ時の両プロセス再起動が追記されている
- [ ] **Plan-16（#37）を不要として close する**
