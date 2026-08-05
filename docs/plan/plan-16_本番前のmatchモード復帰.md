# Plan-16: 本番前に solo → match モードへ戻す（GCP）

> **目的**: 結合テストのために本番を solo に切り替えた場合、イベント前に必ず match へ戻す。
> **対応issue**: #37
> **優先度**: 中（**ただし Plan-15 で方式Bを採るなら不要 — その場合この issue は close する**）
> **依存**: Plan-15（#36）の方式決定
> **参照**: `docs/deploy.md`, `deploy/takoda99.service`

---

## 0. このプランが要るかどうかの判定

**まず Plan-15 でどの方式を採ったかを確認する。**

| Plan-15 の方式 | 本プラン |
|---|---|
| **B: solo 用の2つ目のインスタンス**（推奨・採用予定） | **不要**。本番の起動モードを一度も触らないので戻す作業が発生しない。issue #37 は close |
| A: 本番VMの systemd を直接書き換え | **必要**。以下を実施 |
| C: `minPlayers` を1に下げる | **必要**。ただし戻す対象は systemd ではなく config（§3） |

```bash
# 判定コマンド: solo 用インスタンスが居るか
systemctl list-units --type=service | grep takoda99
```

`takoda99-solo.service` が居れば方式B → 本プランは不要。

---

## 1. なぜ危険か

戻し忘れは「**イベント当日に1人接続しただけで試合が始まってしまう**」という致命的な事故になる。
99人で始めるはずの試合が、最初に繋いだ1人＋Bot5体で勝手に始まり、残り98人は次の試合を待つことになる。

Textro でも「検証のため一時的に solo」という render.yaml のコメントが**そのまま放置されていた前例がある**。
人間の注意力に頼る運用は失敗するので、**構造的に起こり得ない方式B（Plan-15）を推奨**している。
本プランは方式Aを採ってしまった場合の保険。

---

## 2. 方式Aを採った場合の戻し手順

### Step 1: systemd unit を match へ戻す

```bash
sudo sed -i 's|--mode solo --bots 5|--mode match|' /etc/systemd/system/takoda99.service
```

```bash
sudo systemctl daemon-reload && sudo systemctl restart takoda99
```

### Step 2: 実際の ExecStart を確認

```bash
systemctl cat takoda99 | grep ExecStart
```

期待:

```
ExecStart=/opt/takoda99/server --mode match
```

### Step 3: リポジトリと実機の一致を確認

リポジトリの `deploy/takoda99.service` は**既定が `--mode match`**。実機がこれと一致していること。

```bash
diff <(systemctl cat takoda99 | tail -n +2) deploy/takoda99.service
```

差分が出たら、実機を直すのではなく**リポジトリを正としてコピーし直す**:

```bash
sudo cp deploy/takoda99.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl restart takoda99
```

### Step 4: 単独接続で試合が始まらないことを確認

```bash
websocat wss://takoda99.mooo.com/ws
```

期待: `MatchmakingStatus` が届き続け、**`MatchStart` が来ない**。

```json
{"type":"MatchmakingStatus","payload":{"waitingCount":1,"minPlayers":20}}
```

`MatchStart` が届いたら **まだ solo のまま**。Step 1 からやり直す。

---

## 3. 方式Cを採った場合（`minPlayers` を下げていた）

systemd ではなく **config を戻す**。

1. config-front で `matching.minPlayers` を本番想定値へ戻す
2. matching 系は**再起動不要で数秒で反映される**（`docs/deploy.md`）
3. 確認:

```bash
websocat wss://takoda99.mooo.com/ws
```

`MatchmakingStatus.minPlayers` が戻っていること。

> ⚠ **この目的で `systemctl restart` してはいけない**。再起動すると進行中の試合が消える。
> matching 系は再起動なしで反映されるので、config を変えるだけでよい。

---

## 4. 当日の直前チェックへの組み込み

Plan-19（当日オペレーション手順）の事前チェックリストに以下を必ず入れる:

- [ ] `systemctl cat takoda99 | grep ExecStart` が `--mode match`
- [ ] 単独接続で `MatchmakingStatus` 止まりになる
- [ ] `MatchmakingStatus.minPlayers` が当日の想定人数

---

## 5. 完了条件

**方式Bを採った場合:**

- [ ] `takoda99-solo.service` が存在し、本番 `takoda99.service` の ExecStart が `--mode match` のまま一度も変更されていない
- [ ] issue #37 を「方式Bにより不要」として close

**方式A/Cを採った場合:**

- [ ] 実機の ExecStart が `--mode match`
- [ ] リポジトリの `deploy/takoda99.service` と実機が一致
- [ ] 単独接続で試合が始まらず `MatchmakingStatus` を受信し続ける
- [ ] `minPlayers` が当日の想定値
- [ ] Plan-19 の当日チェックリストに確認項目が入っている
