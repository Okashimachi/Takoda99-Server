# Plan-16: 本番前に検証用設定を戻す

> **目的**: 結合テストのために下げた `minPlayers` 等を、イベント前に必ず本番値へ戻す。
> **対応issue**: #37
> **優先度**: 中。**作業自体は数十秒**だが、忘れると致命的。
> **依存**: Plan-15（#36）
> **参照**: `docs/deploy.md`, `deploy/takoda99.service`

---

## 0. 何を戻すのか（Plan-15 の方式Cを前提）

Plan-15 で **config の `minPlayers` を下げる方式**を採ったので、戻す対象は
**systemd ではなく config**。SSH も再起動も要らない。

| キー | 検証中 | 本番 |
|---|---|---|
| `matching.minPlayers` | 1 | 当日の想定人数（例 20） |
| `matching.startCountdownMs` | 2000 | 15000 |

> issue #37 は元々「`--mode solo` から `--mode match` へ戻す」という内容だったが、
> Plan-15 が**起動モードを一切触らない方式**を採ったので、対象が config に変わった。
> 起動モードは常に `--mode match` のまま。

---

## 1. なぜ危険か

戻し忘れると「**イベント当日に1人接続しただけで試合が始まってしまう**」。
99人で始めるはずの試合が、最初に繋いだ1人＋Bot98体で勝手に始まり、
残り98人は次の試合を待つことになる。

Textro でも「検証のため一時的に solo」という設定が**そのまま放置された前例がある**。
人間の注意力に頼る運用は失敗する前提で、**確認手順を機械的にする**。

---

## 2. 戻す手順

### Step 1: config を本番値へ

config-front で:

- `matching.minPlayers` → 当日の想定人数
- `matching.startCountdownMs` → 15000

**再起動不要**。約3秒で反映される（マッチングループ1秒 ＋ ConfigStore の2秒キャッシュ）。

> ⚠ **この目的で `systemctl restart` してはいけない**。進行中の試合が消える。

### Step 2: API で確認

```bash
curl -s https://takoda99.mooo.com/api/params | jq '.matching'
```

```json
{
  "minPlayers": 20,
  "maxPlayers": 99,
  "startCountdownMs": 15000,
  "minFill": 99
}
```

### Step 3: 実際に接続して確認（これが本命）

**API だけでなく、クライアントから見える値を確認する。**
`ConfigStore` に2秒キャッシュがあるので、API の値と実際の挙動が一瞬ズレうる。

```bash
websocat wss://takoda99.mooo.com/ws
```

接続後、`MatchmakingJoin` を送る:

```json
{"type":"MatchmakingJoin","payload":{"displayName":"check"}}
```

期待:

```json
{"type":"MatchmakingStatus","payload":{"waitingCount":1,"minPlayers":20}}
```

- `minPlayers` が当日の想定人数
- **`MatchStart` が来ない**（1人では始まらない）

`MatchStart` が届いたら**まだ戻っていない**。Step 1 からやり直す。

---

## 3. 起動モードの確認（念のため）

Plan-15 の方式Cなら触っていないはずだが、誰かが方式A（systemd 書き換え）を
やってしまった可能性を潰す。

```bash
systemctl cat takoda99 | grep ExecStart
```

期待:

```
ExecStart=/opt/takoda99/server --mode match
```

`--mode solo` になっていたら:

```bash
sudo cp deploy/takoda99.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl restart takoda99
```

リポジトリの `deploy/takoda99.service` は**既定が `--mode match`** なので、
迷ったら実機を直すのではなく**リポジトリからコピーし直す**。

### `--bots` が付いていないことも確認

```bash
systemctl cat takoda99 | grep -c "\-\-bots" || echo "OK: --bots なし"
```

`--bots` が付いていると config の `minFill` が無視される（`botsExplicit`）。
Plan-15 の手が使えなくなるので付けない。

---

## 4. 当日チェックリストへの組み込み

Plan-19（当日オペレーション手順）の前日/当日チェックに以下を入れる。**必須**。

```
[ ] curl -s https://takoda99.mooo.com/api/params | jq '.matching.minPlayers'
    → 当日の想定人数（1 になっていないこと）

[ ] websocat wss://takoda99.mooo.com/ws → MatchmakingJoin を送る
    → MatchmakingStatus が届き、MatchStart が来ないこと

[ ] systemctl cat takoda99 | grep ExecStart
    → --mode match（--bots が付いていない）
```

---

## 5. 完了条件

- [ ] `matching.minPlayers` が当日の想定人数に戻っている
- [ ] `matching.startCountdownMs` が本番値に戻っている
- [ ] **単独接続で試合が始まらず `MatchmakingStatus` を受信し続ける**
- [ ] `systemctl cat takoda99` の ExecStart が `--mode match`（`--bots` なし）
- [ ] リポジトリの `deploy/takoda99.service` と実機が一致
- [ ] Plan-19 のチェックリストに上記の確認が入っている
- [ ] 戻す作業で `systemctl restart` を使っていない（config 変更のみ）
