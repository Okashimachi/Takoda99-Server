# Plan-19: 当日オペレーション手順（ランブック）

> **目的**: イベント当日に「誰が見ても同じ手順で回せる」運用手順を確定する。
> **対応issue**: #46
> **優先度**: 中〜高。当日の事故を防ぐ。
> **依存**: Plan-16（検証用設定の復帰）, Plan-17（ログ）
> **前身**: `docs/plan/plan-10_デプロイ戦略.md`（Render前提）。**デプロイ手順自体は `docs/deploy.md` が正典**

---

## 0. 何が済んでいて、何が残っているか

`docs/deploy.md` は GCP 移行に合わせて全面的に書き直され、**構成・デプロイ手順・環境変数・実測値は確定済み**。

| 項目 | 状態 |
|---|---|
| 本番構成（GCP e2-micro + Caddy + Supabase） | ✅ `docs/deploy.md` |
| デプロイ手順（クロスコンパイル → scp → systemd） | ✅ 同上 |
| 環境変数一覧 | ✅ 同上 |
| 設定の反映タイミング | ✅ 同上 |
| 99人実機計測 | ✅ 同上 |
| **当日の進行手順（ランブック）** | ❌ **本プラン** |
| **トラブル時の対応表** | ❌ **本プラン** |
| **事前チェックリスト** | ❌ **本プラン** |

つまり残っているのは「**手順書**」であって、インフラ作業ではない。
成果物は `docs/runbook.md`（新規）。

---

## 1. 成果物: docs/runbook.md

以下の構成で書く。**当日は画面に出しっぱなしにして、上から順に潰す**もの。

---

### 1.1 前日チェック

- [ ] `git log origin/main -1` と本番のバイナリが一致（最新がデプロイ済み）
- [ ] `curl https://takoda99.mooo.com/healthz` → 200
- [ ] `systemctl cat takoda99 | grep ExecStart` が `--mode match`（`--bots` が付いていない）
- [ ] config-front から `matching.minPlayers` を**当日の想定人数**に設定
- [ ] `matching.startCountdownMs` が本番値（検証用に短くしたまま放置していないか）
- [ ] **単独接続で試合が始まらない**ことを実際に確認（＝`minPlayers` の戻し忘れ検知）
      ```bash
      websocat wss://takoda99.mooo.com/ws
      # MatchmakingJoin を送る → MatchmakingStatus が届き、MatchStart は来ないこと
      ```
- [ ] お題単語が入っている（`GET /api/words` が空でない）
- [ ] `ALLOWED_ORIGINS` にクライアントの本番オリジンが入っている
- [ ] WebFront / Unity の接続先が `wss://takoda99.mooo.com/ws` を向いている
- [ ] `cmd/loadtest` で99接続を1回通す（Plan-18）
- [ ] **egress の残量を確認**（1試合645MB・無料枠月1GB。何試合やるか決める）

### 1.2 当日・開始前

- [ ] VM が起きている（`gcloud compute instances list`）
- [ ] `journalctl -u takoda99 -f` を1画面で開いておく
- [ ] `watch -n5 'curl -s https://takoda99.mooo.com/healthz | jq .'` を1画面で開いておく
- [ ] 参加者に接続URLを共有
- [ ] **試合中のデプロイは絶対にしない**ことをチーム内で確認

### 1.3 試合の進行

```
1. 参加者が接続 → /healthz の activeConnections が増える
2. minPlayers 到達 → カウントダウン開始
3. MatchStart → journalctl に match_start が出る
4. 進行中 → phase_change / store_eliminated が流れる
5. match_end が出たら終了
```

**人数が足りない時**: config-front で `minPlayers` を下げる。
**再起動は不要**（matching 系は待機ループごとに config を読み直すので数秒で反映される）。

> ⚠ この目的で `systemctl restart` してはいけない。進行中の試合が消える。

### 1.4 トラブル対応表

| 症状 | 確認 | 対処 |
|---|---|---|
| 接続できない | `curl /healthz` | 200 が返らなければ `sudo systemctl restart takoda99` |
| ブラウザから繋がらない（curlはOK） | ブラウザのコンソール | `ALLOWED_ORIGINS` にオリジンを追加 → restart |
| 試合が始まらない | `/healthz` の activeConnections | `minPlayers` を下げる（再起動不要） |
| 1人で試合が始まってしまう | `curl .../api/params \| jq .matching.minPlayers` | 検証用の `minPlayers=1` が残っている → config で戻す（**再起動不要**・Plan-16） |
| 試合が終わらない | `journalctl` で `store_eliminated` が出ているか | 出ていなければ storm が動いていない。**Plan-14 の決着保証テストで事前に潰しておく** |
| サーバーが落ちた | `systemctl status takoda99` | `Restart=always` で自動復帰。参加者に再接続を案内 |
| 動作が重い | `/healthz` の goroutines / `metrics` の heapMB | goroutine が単調増加ならリーク。試合の合間に restart |
| パラメータがおかしい | config-front | 修正 → **次の試合から**反映（試合系は再起動不要） |
| DB が落ちた | `journalctl` に `config_load_failed` | 内蔵デフォルトで動き続ける（試合は止まらない）。結果保存だけ失われる |

### 1.5 緊急時のコマンド集

```bash
sudo systemctl status takoda99
```

```bash
sudo systemctl restart takoda99
```

```bash
sudo journalctl -u takoda99 -f -o cat | jq -c 'select(.level=="ERROR")'
```

```bash
curl -s https://takoda99.mooo.com/healthz | jq .
```

### 1.6 ロールバック

デプロイ後に問題が出た場合。**バイナリを差し替えるだけ**（コンテナではないので単純）。

事前に前バージョンを VM 上に残しておく:

```bash
# デプロイ前に現行を退避
sudo cp /opt/takoda99/server /opt/takoda99/server.prev
```

戻す:

```bash
sudo cp /opt/takoda99/server.prev /opt/takoda99/server && sudo systemctl restart takoda99
```

> **DB スキーマの破壊的変更をしない**限りこれで戻る。マイグレーションは**カラム追加のみ**にし、
> 削除・リネームはイベントが終わるまでやらない。

### 1.7 デモ用構成（審査員向け）

少人数で即試合を見せたい場合は **config で `matching.minPlayers` を人数に合わせる**。

```
matching.minPlayers      = 2       （審査員2人で即開始）
matching.startCountdownMs = 3000
```

再起動不要で約3秒で反映される。**デモが終わったら必ず戻す**（Plan-16）。
Bot は `minFill`（既定99）まで自動で埋まるので、少人数でも盤面は賑やかになる。

---

## 2. plan-10 の扱い

`docs/plan/plan-10_デプロイ戦略.md` は Render 前提の記述が残っている（render.yaml / Starter プラン等）。
本プランで `docs/runbook.md` を作ったら、**plan-10 の冒頭に「デプロイ手順は `docs/deploy.md`、
当日運用は `docs/runbook.md` が正典。本書は Render 時代の設計記録」と追記**して混乱を防ぐ。

削除はしない（判断の経緯が残っているため）。

---

## 3. 完了条件

- [ ] `docs/runbook.md` が存在する
- [ ] 前日チェックリストがある（デプロイ済み確認・mode確認・minPlayers・お題・オリジン・負荷テスト・egress）
- [ ] 当日の進行手順がある
- [ ] トラブル対応表がある（症状→確認→対処の3列）
- [ ] 緊急時コマンド集がある（コピペで動く）
- [ ] ロールバック手順があり、**1回はテスト済み**
- [ ] デモ用構成（config で `minPlayers` を下げる手順）が書かれている
- [ ] 「試合中にデプロイしない」「minPlayers 変更に再起動は不要」が明記されている
- [ ] `plan-10` に正典の所在が追記されている
- [ ] チーム内で読み合わせ済み
