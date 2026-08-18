# 本戦当日 runbook — 戻し手順と調整手順

**本戦 2026-08-24。慌てている人が上から順に読んで復旧できること**だけを目的にした文書。
背景や理屈は書かない（それは `deploy.md` と `plan-honsen/`）。

> **前提**: デプロイは手元クロスコンパイル → GCPコンソールSSHでアップロード → `install` + `restart`。
> 詳細は [deploy.md](deploy.md)。**unit ファイル（`deploy/takoda99.service`）は触らない**（過去に本番を止めている）。
>
> **本戦前にやること**（デプロイ・実プレイ確認・バイナリ保管）は [pre-event-checklist.md](pre-event-checklist.md)。
> 本書は**当日おかしくなったときの対処**に絞っている。

---

## 0. 事前準備（当日の朝までに終わらせる）

- [x] **本戦バイナリの1つ前を保管する**（§2 の退避弁。これが無いと戻せない）
- [x] 保管場所とファイル名をこの表に書く

| | ファイル名 | 中身 | 置き場所 |
|---|---|---|---|
| **稼働中** | `takoda99-server-dee606f` | h30〜h35 ＋ 観戦中の足切り予告（#138）。**本戦はこれで走る** | VM `~/` と `/opt/takoda99/server`／手元 `~/takoda99-backup/` |
| **1つ前（退避用）** | `takoda99-server-8cb7f8f` | h30・h32・h35 まで。**Bot は tier 制の前**（全99体が同じ強さ・1注文あたり固定6000ms） | VM `~/`／手元 `~/takoda99-backup/` |
| 2つ前 | `takoda99-server-a0f92ee` | h30・h32 まで。`odai.*` と `cull.warnMaxIds` が無い | VM `~/`／手元 `~/takoda99-backup/` |

> 🔴 **`8cb7f8f` へ戻すと Bot の挙動が大きく変わる。** tier 制の前なので全 Bot が同じ強さになり、
> かつ「1注文あたり固定6000ms」なので**終盤（heat 17）に人間の約6倍速**になる。
> **Bot が原因でないトラブルなら戻さない**こと。設定で直せるものは §3 の表を先に見る。
>
> ⚠ 戻しても **DB の設定はそのまま**（`odai.*` / `cull.warnMaxIds` / `bot.tiers` は
> 古いバイナリからは無視されるだけで、消えはしない）。再デプロイすれば元に戻る。

**戻し方**（VM に既に置いてあるので転送は不要）:

```bash
gcloud compute ssh takoda99-server --zone us-west1-b --command \
  'sudo install -o takoda99 -g takoda99 -m 755 ~/takoda99-server-8cb7f8f /opt/takoda99/server && sudo systemctl restart takoda99'
```

**稼働中のバイナリを確かめる**:

```bash
gcloud compute ssh takoda99-server --zone us-west1-b --command \
  'ls -l /opt/takoda99/server; ls -lt ~/takoda99-server-* | head -3'
```

同じサイズのファイルが並ぶので、**タイムスタンプで見分ける**（`/opt/takoda99/server` の
mtime と一致するものが稼働中）。

- [ ] 手元から `curl https://takoda99.mooo.com/api/params` が通ることを確認
- [ ] 管理トークン（`CONFIG_ADMIN_TOKEN`）を手元に控える
- [ ] ダッシュボード `https://takoda99.mooo.com/admin/?token=...` が開くことを確認

---

## 1. 「おかしい」と思ったら最初に見る3つ

```bash
# 1. サーバーは生きているか
sudo systemctl status takoda99

# 2. 設定はDBから読めているか
sudo journalctl -u takoda99 -n 20 --no-pager | grep config

# 3. 直近のエラー
sudo journalctl -u takoda99 -n 50 --no-pager
```

`config: 内蔵デフォルトで起動` が出ていたら **DB の値が効いていない**。
→ `DATABASE_URL` が渡っていない（deploy.md のトラブルシューティング）。

---

## 2. 🔴 退避弁：直し方の優先順位

**上から順に試す。下へ行くほど影響が大きい。**

### ① まず config を戻す（ビルド不要・数秒・最優先）

**調整値が原因の可能性が少しでもあるなら、まずこれ。**
`GameParameters` は DB から読むので、**サーバーの再起動もデプロイも要らない**。

- 試合系の値 → **次の試合から**反映
- matching 系 → 数秒で反映（待機ループが毎回読み直す）

```bash
# 今の値を確認
curl -s -H "X-Admin-Token: $TOKEN" https://takoda99.mooo.com/api/params | jq .
```

運営UI（takoda99-config）から戻すのが基本。UIが使えなければ上の API に POST する。

> **極端な値を入れて壊した場合はここで直る。** バイナリを戻す前に必ず試すこと。

### ② バイナリを1つ前に戻す（数十秒）

コードが原因のときだけ。**§0 で保管した1つ前のバイナリ**を使う。

```bash
# 1. GCPコンソール → VM の SSH → 右上 ⚙ → ファイルをアップロード
#    （保管しておいた1つ前のバイナリを選ぶ。ホームディレクトリに入る）

# 2. 差し替えて再起動
sudo install -o takoda99 -g takoda99 -m 755 ~/takoda99-server-<1つ前のコミット> /opt/takoda99/server \
  && sudo systemctl restart takoda99

# 3. 起動確認
sudo journalctl -u takoda99 -n 5 --no-pager | grep config
```

> ⚠ **`deploy/takoda99.service` は差し替えない。** 上書きするとサーバー上で入れた設定が消える。

> ⚠ **本戦ルールの廃止処理（信用・我慢ゲージ・storm）はコードから削除済み**で、
> フラグで戻す道は無い（plan-h20 §3）。**戻す＝1つ前のバイナリに置き換える**、が唯一の手段。

### ②' お題を h30 以前へ戻す（「お題が短すぎる／注文が多すぎる」とき）

plan-h30 で **1語を短くし（level 17 で 85打鍵 → 43打鍵前後）、注文数(`orderCount`)を上げた**
（Normal/Bonus/Claimer/Buzz = 3/3/2/6）。当日これが合わないと感じたら、**上から順に**試す。

1. **`orderCount` を戻す（ビルド不要・数秒・これが一番効く）**

   運営UI（takoda99-config）で `customer.*.orderCount` を **2 / 2 / 1 / 4** へ戻す。
   1語が短いままでも、1客あたりの打鍵量と加点は h30 以前へ戻る。**まずこれで足りるか見る。**

2. **長いお題を戻す（ビルド不要・再起動が要る）**

   h30 で辞書から外した 260 語（level 5〜17 の長文）は `internal/odai/retired.go` に**残してある**。
   環境変数を付けて再起動すると DB へ再 upsert される。

   ```bash
   sudo systemctl edit takoda99     # [Service] に下記を足す
   #   Environment="TAKODA99_RESTORE_RETIRED_WORDS=1"
   sudo systemctl restart takoda99
   sudo journalctl -u takoda99 -n 20 --no-pager | grep 旧お題
   ```

   > ⚠ **新しい語は消えない**ので、新旧が混ざった辞書になる（長い語が戻るぶん体感は h30 以前へ寄る）。
   > 戻した後は環境変数を消して再起動しておくこと（付けたままでも再 upsert されるだけで害は無い）。
   > **完全に h30 以前へ戻したいなら ②（バイナリの巻き戻し）**。

3. **個別に直す（ビルド不要）**

   運営UI の語彙編集で、長すぎる／短すぎる語を1語ずつ足す・消す・直す。時間があるときだけ。

> 🔴 **辞書 seed を伴う起動は約36秒 `healthz` が無応答**（既知・#93）。壊れたと誤認しないこと。
> 逆に、**`CurrentSeedVersion` を下げても再 seed はされない**（`applied >= Current` で判定）。
> 「版数を下げて戻す」は効かないので、上の1〜3かバイナリ巻き戻しを使う。

### ③ 再起動だけ試す（最後）

```bash
sudo systemctl restart takoda99
```

**試合中の再起動は進行中の試合を落とす。** 試合と試合の間にやること。

---

## 3. 当日よく触る調整値

すべて運営UI（takoda99-config）から。**ビルド不要・次の試合から反映**。

| 症状 | 触る値 | 方向 |
|---|---|---|
| 人が集まらず試合が始まらない | `matching.minPlayers` | 下げる（**数秒で反映・再起動不要**） |
| Bot が強すぎて人間が20秒で全滅 | `bot.tiers[*].msPerKey` | **3つとも**上げる（遅くする）。h31 で `baseElapsedMs` は廃止 |
| Bot が弱すぎて人間が上位を独占 | `bot.tiers[*].msPerKey` | **3つとも**下げる（速くする） |
| Bot の強さが揃いすぎ / 散らばりすぎ | `bot.individualSpread` | 上げると個体差が広がる（既定 0.20＝±20%） |
| ミスの罰が重すぎる / 軽すぎる | `score.weightMiss` | 既定 30（`weightTakoyaki` 100 に対し 10:3。h30 で 25→30） |
| お題が短すぎる / 注文が多すぎる | `customer.*.orderCount` | 3/3/2/6 → **2/2/1/4** で h30 以前へ（§2 ②'） |
| 序盤で人が減りすぎ / 減らなすぎ | `cull.stages[1..3].targetAliveCount` | 中間ステージのみ |
| **お題が難しすぎる / やさしすぎる** | `odai.levelOffset` | **±1 する**（既定 0）。`heat.*` と違いカーブの形もクライアント表示も変わらない（h35） |
| お題が単調（全員同じ難度の語） | `odai.levelSpread` | 上げる（既定 0＝ばらつき無し）。2 なら heat±2 から選ぶ |
| 足切り予告の送信量を減らしたい | `cull.warnMaxIds` | 下げる。🔴 **既定 24 はクライアントと合意済み。変えたら必ず共有する** |

### 🔴 触ってはいけない値

企画で確定しており、動かすとゲームが成立しない。

- `cull.stages` の **20秒等間隔**
- **最終ステージ 120000ms**（＝ゲーム時間）
- **第5ステージの targetAliveCount = 10**（＝決勝の人数）
- **第1ステージが 20000ms より早くなること**（「どれだけ弱くても20秒は遊べる」の担保）
- 最終ステージの `targetAliveCount = 0`（**0 以外にすると Validate が弾く**）

> `cull.stages` は**6要素すべて**を送ること。要素が足りない JSON を保存すると
> `encoding/json` が残りをゼロ値で埋め、「0秒時点で生存0＝開始直後に全店即死」になる。
> Validate が弾くので保存は失敗するが、**エラーが出たらまずここを疑う**。

---

## 4. 観測（ダッシュボード）

```
https://takoda99.mooo.com/admin/?token=<CONFIG_ADMIN_TOKEN>
```

| タブ | 当日の使いどころ |
|---|---|
| 店舗盤面 | 誰が止まっているか。並び順「切られる順」で次に落ちる店を見る |
| **スコア分布** | 上位と下位が分離しているか。**団子なら足切りが運になっている** |
| 客フロー | **行列が空の店が無いか**（＝お題が途切れていないか） |

- 「試合が走っていません（待機中）」→ そもそも試合が始まっていない（`minPlayers` を確認）
- スコア分布で **Bot が上位10を占めている** → Bot が強すぎる（`bot.tiers[*].msPerKey` を3つとも上げる）
  - 上位を **strong tier だけ**が占めているなら、`bot.tiers[0].weight`（強の出現比）を下げるほうが効く

---

## 5. 連絡先・持ち物

- [ ] 管理トークン
- [ ] GCPコンソールにログインできる端末
- [ ] 1つ前のバイナリが入った端末（**ネットが無くても渡せる状態で**）
