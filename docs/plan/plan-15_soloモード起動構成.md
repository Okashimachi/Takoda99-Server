# Plan-15: クライアント単独結合テスト用の起動構成

> **目的**: クライアント開発者が**1接続だけで MatchStart 以降の全メッセージを検証できる**ようにする。
> **対応issue**: #36
> **優先度**: **高**。クライアント開発を現在ブロックしている。
> **依存**: なし（サーバー本体は稼働中）
> **参照**: `internal/matchmaking/matchmaking.go`, `cmd/server/main.go`, `docs/deploy.md`

> ⚠ **訂正（2026-08-05）**: 本文中の `Takoda99-WebFront` は**廃止・凍結**された。
> Web先行戦略をやめ **Unity 単独**開発に変わったので、「WebFront」はすべて
> **`Takoda99-Unity`** と読み替えること。`minPlayers` を下げる手順そのものは変わらない。

---

## 0. 結論 — **config で minPlayers を下げるだけでよい**

インフラ作業は要らない。**サーバーの再起動もSSHも不要**。

```
matching.minPlayers      = 1
matching.startCountdownMs = 2000   （待ち時間を短く。0でも可）
```

config-front から保存すると**数秒で反映**され、1接続で試合が始まる。
検証が終わったら値を戻すだけ。

当初は「別ポートに2つ目の solo インスタンスを常駐させる」案を検討したが、
**matching 系パラメータが動的リロードになった**ことで不要になった。以下その根拠。

---

## 1. なぜ config だけで足りるのか（コードの裏取り）

### 1.1 matching パラメータは毎回読み直される

`cmd/server/main.go` の match モード:

```go
mm := matchmaking.New(matchmaking.Config{
    GetParams: func() game.MatchingParams {
        p, _ := provider.Load(ctx)     // ← 呼ばれるたびに設定を取り直す
        m := p.Matching
        if botsExplicit {
            m.MinFill = *bots
        }
        if m.MinFill == 0 {
            m.MinFill = game.DefaultParameters().Matching.MinFill
        }
        return m
    },
    ...
})
```

`GetParams` は `Matchmaker.Run` の中で:

- **1秒ごとの ticker**（`update(true)`）
- join / leave のたび（`update(false)`）
- 試合開始時（`startMatch`）

から呼ばれる。つまり **`minPlayers` / `maxPlayers` / `startCountdownMs` / `minFill` は起動時スナップショットではない**。

反映の遅れは「マッチングループ周期(1秒) ＋ `ConfigStore` の2秒キャッシュ」で**おおむね3秒以内**。

### 1.2 Bot 補完数も config 由来

```go
func (m *Matchmaker) startMatch(pool []Player, params game.MatchingParams) {
    players := append([]Player(nil), pool...)
    for len(players) < params.MinFill && m.cfg.NewBot != nil {
        players = append(players, m.cfg.NewBot())
    }
    ...
}
```

`MinFill` の既定は **99**。`minPlayers=1` にすれば、**1人＋Bot98体**で試合が始まる。
これは `--mode solo --bots 5` より**本番に近い条件**で検証できるという利点もある。

> ⚠ `botsExplicit`: systemd の `ExecStart` に `--bots` を書くと config の `MinFill` を上書きする。
> 現在の `deploy/takoda99.service` は `--mode match` のみで `--bots` を渡していないので、
> **config 側が効く**。`--bots` を足すとこの手が使えなくなるので足さないこと。

---

## 2. 方式の比較（なぜ他を採らないか）

| | 作業 | 反映 | 戻し方 | 検証のしやすさ |
|---|---|---|---|---|
| **C: config で minPlayers=1**（採用） | config-front で2値変更 | 約3秒 | 値を戻す（UI） | **どのクライアントからも見える** |
| A: systemd を solo に書き換え | SSH + sed + restart | 再起動 | SSH + sed + restart | SSH が要る |
| B: 別ポートに2つ目のインスタンス | ドメイン取得・unit追加・Caddy追加 | — | 不要 | — |

### 方式A を採らない理由

`systemctl restart` が要る＝**進行中の試合が消える**。SSH できる人しか操作できず、
戻し忘れの確認にも SSH が要る。方式Cは全ての面で上位互換。

### 方式B を採らない理由

「本番を触らない」点は魅力だが、コストが釣り合わない:

- サブドメイン取得・DNS 設定
- 2つ目の systemd unit
- Caddy のブロック追加
- **DB は結局共有**（config を変えれば両方に効くので「本番を触らない」は部分的にしか成立しない）
- デプロイのたびに2プロセス再起動

方式Cなら**これら全部が不要**で、しかも戻し忘れの検知は方式Bより簡単（§4）。

---

## 3. 手順

### Step 1: config を変更

config-front で:

| キー | 値 | 元の値（戻す時用に控える） |
|---|---|---|
| `matching.minPlayers` | `1` | 20（当日の想定人数） |
| `matching.startCountdownMs` | `2000` | 15000 |

`minFill` は**触らない**（99 のまま。Bot が98体入る）。
Bot を減らして軽くしたいなら `minFill` を 10 等にする。

### Step 2: 反映を確認

```bash
curl -s https://takoda99.mooo.com/api/params | jq '.matching'
```

```bash
websocat wss://takoda99.mooo.com/ws
```

`MatchmakingStatus` の `minPlayers` が 1 になっていること。**接続しただけで見える**のが重要（§4）。

### Step 3: Unity クライアントに接続してもらう

接続先は**本番と同じ**:

```
wss://takoda99.mooo.com/ws
```

クライアント側は接続後に `MatchmakingJoin` を送る（§5）。

### Step 4: `ALLOWED_ORIGINS` にクライアントのオリジンを追加

これだけは環境変数なので再起動が要る。**試合の合間に行う**。

```bash
sudo nano /etc/takoda99.env
# ALLOWED_ORIGINS=http://localhost:5173,https://<unity-host>
```

```bash
sudo systemctl restart takoda99
```

> 未設定なら**全許可**（既定）。開発中は未設定のままでもよく、その場合この手順は不要。
> 本番前に絞る。

### Step 5: 検証が終わったら戻す

config-front で `minPlayers` / `startCountdownMs` を元の値へ。**再起動不要**。

---

## 4. 戻し忘れ対策（方式Cで一番大事なところ）

方式Cの唯一の弱点は「戻し忘れると当日1人で試合が始まる」こと。
ただし**方式A より検知がずっと簡単**。

### 検知が容易な理由

`minPlayers` は `MatchmakingStatus` で**全待機者にブロードキャストされる**。
つまり**接続すれば誰でも見える**。systemd の `ExecStart`（SSH が要る）とは根本的に違う。

### 当日の確認手順（Plan-19 のチェックリストに入れる）

```bash
websocat wss://takoda99.mooo.com/ws
```

```json
{"type":"MatchmakingStatus","payload":{"waitingCount":1,"minPlayers":20}}
```

`minPlayers` が当日の想定人数なら OK。**1 になっていたら戻し忘れ**。

API からも確認できる:

```bash
curl -s https://takoda99.mooo.com/api/params | jq '.matching.minPlayers'
```

### 運用ルール

- **検証が終わったらその場で戻す**（次の作業に移る前に）
- 当日の朝、Plan-19 の前日/当日チェックで必ず確認する
- config-front の画面上でも `minPlayers` は一目で見える

---

## 5. クライアント側の注意点

### 接続直後に `MatchmakingJoin` を送る必要がある

サーバーは接続を受けると `awaitJoinName(conn, joinTimeout)` で
**最初の1メッセージを最大3秒待つ**（`cmd/server/main.go`）。

```go
const joinTimeout = 3 * time.Second
```

期待するのは `MatchmakingJoin`:

```json
{ "type": "MatchmakingJoin", "payload": { "displayName": "たこ焼き太郎" } }
```

- **送らないと3秒待たされ**、表示名は空になる（フォールバック名が割り当てられる）
- 別種のメッセージを最初に送っても同じ（空名で続行）
- 表示名は最大24文字・制御文字は除去される（`SanitizeDisplayName`）

**接続したらすぐ `MatchmakingJoin` を送る**のが正しい実装。

### 伝えること

| 項目 | 値 |
|---|---|
| 接続先 | `wss://takoda99.mooo.com/ws`（本番と同じ） |
| 最初に送る | `MatchmakingJoin`（`displayName` 付き）— **接続直後すぐ** |
| 検証中の設定 | `minPlayers=1` なので1接続で試合が始まる |
| 本番の設定 | `minPlayers` は当日人数。`MatchmakingStatus` 待機の画面が要る |
| プロトコル | Takoda99-Proto v0.3.0 |
| クライアント設計 | `Takoda99-Client-Docs` リポジトリ |

**本番は match なので、待機画面（`MatchmakingStatus` の表示）も必ず作ってもらう。**

---

## 6. 完了条件

- [ ] `matching.minPlayers=1` / `startCountdownMs` を config-front から変更できる
- [ ] 変更が**再起動なしで数秒以内に反映**されることを実測で確認
- [ ] 1接続で `MatchStart` → `CustomerArrived` → `OrderServed` → `MatchEnd` まで到達できる
- [ ] `ALLOWED_ORIGINS` にクライアントの dev/本番オリジンが入っている（または未設定＝全許可）
- [ ] クライアント担当に「接続直後に `MatchmakingJoin` を送る」ことを伝えた
- [ ] クライアントが `MatchmakingStatus` の待機画面も実装している（本番は match のため）
- [ ] 検証後に `minPlayers` / `startCountdownMs` を元の値へ戻した
- [ ] Plan-19 の当日チェックリストに `minPlayers` の確認が入っている
- [ ] **`deploy/takoda99.service` に `--bots` を足していない**（config の `MinFill` が効かなくなるため）
- [ ] 方式B（2つ目のインスタンス）は採らない判断を記録した
