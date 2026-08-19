# 本戦前チェックリスト（残タスクの手順書）

本戦 **2026-08-24**。移行トラック（h20-A〜h26）と h03（注文単位の記録）の実装は完了し、
残っているのは**人が要る作業だけ**。上から順にやれば終わる形にしてある。

> **イベント後の宿題**: h04（プロファイル生成）→ h05（Bot tier制）。
> §2 の実プレイで貯まる `order_attempt` が入力になるので、**本戦前にやることは無い**。

> 当日トラブったときの対処は [runbook.md](runbook.md)。**本書は「本戦前にやること」**。
> デプロイの背景・初回構築は [deploy.md](deploy.md)。

| # | タスク | いつまで | 所要 |
|---|---|---|---|
| 1 | デプロイして実測で確認 | まず最初 | 15分 |
| 2 | 実プレイ確認 と Bot 調整 | 早めに（何度も回す） | 1回30分〜 |
| 3 | カシューへ `PersonalResult` 修正を共有 | 早めに | 5分 |
| 4 | 1つ前のバイナリを保管（runbook §0） | 当日の朝まで | 10分 |

**1 → 3 → 2 →（本戦直前に）4** の順を推奨。3 はカシューの作業を止めている可能性があるので先に流す。

---

## 1. デプロイして実測で確認

### 1.1 なぜ実測が要るか

**コードの既定値は本番に効かない。** `GameParameters` は DB から読むので、
`DefaultParameters()` を変えても本番の値は変わらない。h33 の再測定で決めた
`score.weightMiss = 22` も、h32 で決めた `heat.*`（下表）も、**DB に入って初めて効く**。

> 🔴 **新しく足したキーも自動では入らない。** `backfillDefaults` の補完は**グループ単位**で、
> グループ全体がゼロのときだけ既定値を入れる。`heat` グループは既に DB にあるので、
> h32 で足した **`heat.perElapsedSec` は 0 のまま**になる（＝難度カーブが h32 以前の階段に戻る）。
> **config-front から手で入れること。**

「デプロイしたのに変わらない」の大半はこれ。**必ず curl で実値を見る。**

### 1.2 デプロイ（gcloud があれば1コマンド）

```bash
cd /Users/ryu/kindai/2026/THEHACK/Takoda99-Server && git checkout main && git pull --ff-only origin main
```

```bash
make deploy
```

確認プロンプトが出るので、**試合中でないことを確認**してから `y`。
ビルド → 転送 → 差し替え → 再起動まで一気に走る。

> gcloud のセットアップは [deploy.md](deploy.md) の「gcloud のセットアップ（初回のみ・macOS）」。
> ⚠ **Homebrew 版は失敗する**（`python@3.14` の libexpat 問題）。公式インストーラを使うこと。

### 1.3 gcloud が無い場合（コンソール経由）

```bash
make build
```

`~/takoda99-backup/takoda99-server-<hash>` ができる
（**Finder から見える場所**。`/tmp` は再起動で消えるうえ Finder に出ないので使わない）。

GCPコンソール → VM の **SSH** ボタン → 右上の **⚙ → ファイルをアップロード** で選ぶ
（ホームディレクトリに入る）。SSH 上で:

```bash
sudo install -o takoda99 -g takoda99 -m 755 ~/takoda99-server-<hash> /opt/takoda99/server && sudo systemctl restart takoda99
```

> ⚠ **`deploy/takoda99.service`（unit）は差し替えない。** 過去に本番を止めている。
> ⚠ **試合中に再起動しない**（進行中の試合が消える）。試合の合間に。

### 1.4 ★実測（ここが本番）

```bash
make verify
```

設定の実値と `/healthz` `/admin/` の疎通をまとめて見る（**gcloud 不要**）。中身はこれ:

```bash
curl -s https://takoda99.mooo.com/api/params | jq '.score, .cull, .publish, .sanity, .heat'
```

**期待する値**:

| キー | 期待 |
|---|---|
| `score.weightTakoyaki` / `weightMiss` | `100` / **`22`**（h26:25 → h30:30 → h32後の再測定:**22**） |
| `cull.stages` | 6段階・`atMs` が 20000〜120000・最終の `targetAliveCount` が **0** |
| `publish.*` | 5キーが存在（`rankingDeltaEnabled` は `false`） |
| `sanity.minMsPerWord` | `200`（旧 `eval.minMsPerWord` から改名） |
| `heat.perElapsedSec` | **`0.12`**（h32 で新設。**DB に無いので手で入れる**。0 だと難度が階段状に戻る） |
| `heat.perAliveDrop` | **`0.03`**（h32。旧値 0.05） |
| `heat.phaseMid` / `heat.phaseLate` | **`1` / `2`**（h32。旧値 9 だと終盤突入で +8 跳ねる） |
| `heat.maxLevel` | `17`（お題辞書の上端と一致させる） |

**もし古い値が返ってきたら**（例: `weightMiss` が 25 や 30、`score` が無い）:

DB に予選スキーマが残っていて `backfillDefaults` が既定で埋めた状態。
**config-front から保存し直せば新しい値が入る**（タスク2で触るのでそのついででよい）。

```bash
sudo journalctl -u takoda99 -n 20 --no-pager | grep config
```

`config: 内蔵デフォルトで起動` が出ていたら DB が読めていない。`DATABASE_URL` を疑う（deploy.md）。

### 1.4.1 `order_attempt` が貯まり始めているか（h03）

**h03 で注文単位の記録が入った。** 実プレイ確認（§2）を回すと、そのデータが全部残る。
これが h04/h05（実データから Bot を人間らしくする）の燃料になるので、**動いていることを確認しておく**。

1試合遊んだあとに DB を見る（Supabase のSQLエディタ等）:

```sql
SELECT COUNT(*), MIN(heat_level), MAX(heat_level) FROM order_attempt;
```

- 0件のまま → 保存が効いていない。`journalctl` で `result:` の保存失敗ログを見る
- 件数が増える → OK。**1試合で数百件**が目安

> **既定では人間のぶんだけ保存する**（学習の入力は人間の分布なので）。
> Bot も含めたいときだけ `/etc/takoda99.env` に `SAVE_BOT_ORDERS=1` を足して再起動。
> **本戦当日は不要**（データ量が増えるだけ）。

### 1.5 ダッシュボードと疎通

```bash
curl -s https://takoda99.mooo.com/healthz
```

ブラウザで `https://takoda99.mooo.com/admin/?token=<CONFIG_ADMIN_TOKEN>` が開くこと。

### 1.6 受入スクリプト（任意・確信を持ちたいとき）

```bash
cd /Users/ryu/kindai/2026/THEHACK/Takoda99-Server && BASE=https://takoda99.mooo.com TOKEN=<CONFIG_ADMIN_TOKEN> ./scripts/verify-configapi.sh
```

22項目。**ゼロ埋め（足切り5段階）が 400 で弾かれること**まで見る。
⚠ **値を書き換えて戻すので、試合中は走らせない。**

---

## 2. 実プレイ確認 と Bot 調整（h26 §3・§4）

**いま最も効くタスク。** sim は「速さ型 / 正確型」という2点モデルで測っており、
実際の人間は連続分布なので、**体感は人でしか測れない**。

### 2.1 何を見るか（優先順）

| # | 見るもの | 判定 | 外れていたら |
|---|---|---|---|
| **A** | **決勝20秒が勝負になっているか** | 逆転が起きる／スコア差が開きすぎない | `score.weightMiss` を動かす（**現在22**。正確型が強すぎる→下げる／速さ型が強すぎる→上げる） |
| **B** | **20秒で切られた人の体感** | 「理不尽」でなく「納得」か | `cull.stages[0].targetAliveCount` を上げる（75→80） |
| **C** | **人間が真ん中あたりに来るか** | Bot に上位を独占されていない／全滅していない | `bot.tiers[*].msPerKey` を調整（§2.4） |
| **D** | スコアの伸びの気持ちよさ | 打った実感と数字が一致 | `score.weightTakoyaki` |

**A が最重要。** 22 は sim で「速さ型と正確型の平均順位が拮抗する点」として選んだ値
（差 −1.3位）。**実プレイで確かめるのがここ。**

> 🔴 **拮抗点はゲーム側を変えるたびに動く。** h26(25) → h30(30) → h32後(22) と3回動いており、
> h32 のときは測り直していなかった。**お題・heat・`customer.orderTiers`（注文数）を触ったら
> `go run ./cmd/matchsim --sweep-miss 18,22,25,28,30 --runs 10 --seed 42` を回すこと。**

### 2.2 やり方

1. `https://takoda99.mooo.com/admin/?token=...` を**別ウィンドウで開いておく**
2. 数人（2〜3人でよい）で `/ws` に繋いで1試合遊ぶ。残りは Bot が埋める
3. ダッシュボードで**スコア分布ビュー**を見る:
   - **分離度**が出ているか（上位と下位が団子になっていないか）
   - **カットライン**の位置と、切られた店の Bot/人間の別
   - **足切り履歴**（各ステージで何人切られたか・内訳）
4. 終わったら**遊んだ人に聞く** — 「理不尽だったか」「決勝は熱かったか」

> 💡 人数が少なくても意味がある。**Bot が99人中の大半を占めるのは本番も同じ**なので、
> 「人間がBotの中でどのあたりに来るか」は少人数でも測れる。

> 💡 **ここで遊んだぶんは全部 `order_attempt` に残る**（h03）。
> イベント後に h04/h05（実データから Bot のプロファイルを作る）へ進むときの入力になるので、
> **回せば回すだけ後で効く**。意識してやることは無い（勝手に貯まる）。

### 2.3 値の変え方（ビルド不要・数秒）

config-front（takoda99-config）から変更 → 保存。**次の試合から反映**される。

- サーバーの再起動は要らない
- ⚠ `cull.atMs` は編集不可にしてある（企画で確定した値なので触らせない）
- ⚠ `cull.stages` の**最終段は `targetAliveCount = 0`** を崩さない（Validate が弾く）

**極端な値を入れて壊したら、まず config を戻す**（runbook §2 の①）。

### 2.4 Bot の強さ（h26 §4 / **h31 で tier 制に変わった**）

Bot は **強／中／弱の3階層（tier）を重みで抽選し、そこへ個体差（±20%）を掛けた個体**として
生成される。強さは「1打鍵あたりのms」で持つので、お題が長くなる終盤も人間と同じだけ遅くなる。

| キー | 既定（強/中/弱） | 効き方 |
|---|---|---|
| `bot.tiers[*].msPerKey` | **150 / 200 / 400** | **小さいほど速い＝強い**。主に触るのはこれ。**3つとも同じ向きに動かす** |
| `bot.tiers[*].weight` | 25 / 50 / 25 | 階層の出現比。「強 Bot だけが上位を独占」なら強の重みを下げる |
| `bot.tiers[*].missRate` | 0.02 / 0.05 / 0.10 | 打鍵あたりのミス率。大きいほど弱い |
| `bot.tiers[*].heatPenalty` | 0.01 / 0.02 / 0.04 | 難度追従。大きいほど終盤に崩れる |
| `bot.individualSpread` | 0.20 | 個体差の幅。0 は「未設定」扱いで既定 0.20 に読み替えられる |
| `bot.elapsedJitterMs` | 500 | 1注文ごとの揺らぎ（個体差ではない） |

- 人間が上位を独占する → Bot が弱い → `msPerKey` を3つとも**下げる**
- 人間が20秒で全滅する → Bot が強い → `msPerKey` を3つとも**上げる**

> 📏 目安（実測・`go test -v ./internal/app/ -run ReportHumanPercentile`）:
>
> | 人間の速さ | 順位 |
> |---|---|
> | 150ms/打鍵（速い） | 19位 / 100 |
> | **200ms/打鍵（標準）** | **54位 / 100** ← 狙いどおり真ん中 |
> | 280ms/打鍵（のんびり） | 77位 / 100 |
> | 400ms/打鍵（初心者） | 91位 / 100 |
>
> 🔴 **弱 tier が 280 だった頃は初心者が必ず 100位/100 だった。** Bot の幅が人間の幅より
> 狭かったため。400 にして幅を覆わせてある。**強・中を動かさない限り上位争いは変わらない**ので、
> 「初心者が多い」なら**弱だけをさらに上げる**のが安全（440 なら初心者 87位）。

> ⚠ `bot.baseElapsedMs` / `bot.baseAccuracy` / `bot.accuracyJitter` は **h31 で廃止**。
> DB に残っていても無視される（config-front の UI からも消してある）。

**狙いは「人間が真ん中あたりに来る」**。ダッシュボードで Bot/人間が色分けされているので、
スコア分布のどこに人間がいるかを見る。

### 2.4' 注文数（たこ焼きの個数・h36）

注文数は**属性から切り離され**、独立した3段階の抽選表になっている。
同じ見た目の客に 2個も 8個も出る（キャラから個数は推測できない）。

| キー | 既定 | 効き方 |
|---|---|---|
| `customer.orderTiers[0]` | count 2 / weight 35 | **軽い客**。🔴 **消さないこと**（下記） |
| `customer.orderTiers[1]` | count 4 / weight 35 | 母数 |
| `customer.orderTiers[2]` | count 8 / weight 30 | 重い客。達成すると大きい |

- 「重すぎる／打ち切れない」→ 8個の**重みを下げる**（例 35/45/20）
- 「軽すぎる／物足りない」→ 8個の重みを上げる（例 30/30/40）
- h36 以前（平均3.05個）の体感へ戻すなら **2/3/6・重み 10/85/5**

> 🔴 **最も軽い段（2個）を消さない。** 4/6/8 のように全段を重くすると、遅い人が
> 「ずっと打っているのに0点」になり、実力相関が 0.95 → 0.88 に落ちる（h36 §0.3 の実測）。
> 効いているのは 8個の割合ではなく「**軽い客が存在すること**」で、8個の比率は
> 10%〜40% まで振っても相関は動かなかった。

> ⚠ `customer.*.orderCount` は **h36 で廃止**。DB に残っていても無視される
> （config の UI からも消してある）。

### 2.5 sim で先に当たりをつける

人を集める前に、机上で候補を絞れる。

```bash
cd /Users/ryu/kindai/2026/THEHACK/Takoda99-Server && GOWORK=off go run ./cmd/matchsim --runs 5 --profile normal
```

```bash
GOWORK=off go run ./cmd/matchsim --sweep-miss 18,22,25,28 --runs 5
```

**「人間が真ん中に来るか」は `--profile match` が直接答える**（h33）。
本番と同じ卓（`bot.tiers` から抽選した Bot ＋ 人間数名）を回して、人間の平均順位・
どの足切りで落ちたか・1位になった回数を出す:

```bash
GOWORK=off go run ./cmd/matchsim --profile match --runs 20 --humans 3 --quiet
```

> 既定値での実測（4シード×20試合）は **人間の平均順位 46〜48位/99**・人間が1位 11%。
> ここが 30位より上に寄るなら Bot が弱すぎ、60位より下なら Bot が強すぎ。
> `bot.tiers[*].msPerKey` を3つとも同じ向きに動かして再実行する。

> ⚠ sim の数字は**ダミーの実力分布に依存する**。実プレイの代わりにはならない。
> 「この方向に動かすとこうなる」を確認する道具として使う。

---

## 3. カシューへ `PersonalResult` 修正を共有

**優先度が高い。** カシューの作業を止めている可能性がある。

### 3.1 何が起きていたか

サーバーは `PersonalResult`（個人成績）を**作ってはいたが、送信処理への登録が漏れていて
内部で捨てられていた**。予選から一度も届いていない。

皮肉なことに `PersonalResult` は proto v0.7.0 で
**「個人成績画面に何も出ない」予選バグの対策として追加された**もの。その対策自体が配線漏れで無効化されていた。

### 3.2 送る文面（コピペ可）

```
サーバー側でバグを1つ見つけて直したので共有です🙏

■ リザルト（個人成績）が1通も届いていませんでした
サーバーが PersonalResult を作ってはいたんですが、送信処理への登録が
漏れていて、内部で捨てられていました。予選からずっとです。

Unity 側で「リザルト画面に何も出ない」が起きていたなら、原因はこれの
可能性が高いです。クライアント側は悪くないです。

修正済みなので、次のデプロイ後は脱落した瞬間に PersonalResult が届きます。
（脱落と同時に届くので、画面遷移のタイミングに関係なくデータは揃っています）

■ ついでに配信まわりも本戦仕様になりました
・RankingSnapshot / RankingDelta … 他プレイヤーの順位とスコア
・StoreEliminatedBatch … 足切りの脱落者がまとめて1メッセージで届く
・ForcedEliminationWarning … 秒読み(untilMs)とカットライン

全部 proto v0.8.0 に入っているものです。

■ お願い（v0.8.0 を使うときの注意3つ）
① v0.7.0 のタグは使わないでください
   C#/TS ミラーに PersonalResult が入っていません（同期がタグの後になった）
② 廃止フィールドを読まないでください
   creditLife / patienceMaxMs / evalNormalized などは定義は残ってますが
   値が入りません（0が届く）。「ライフ0＝死亡」みたいな誤表示になります
③ 配列が null で届くことがあります
   cutStoreIds / entries / cullSchedule。C#だと List<T> が null になって
   foreach で落ちるので、空配列扱いにする防御を入れてほしいです
```

### 3.3 決めてもらう必要があること（まだ未合意）

| 項目 | 現状 | 誰が決める |
|---|---|---|
| ~~`cutStoreIds` の件数上限~~ | **合意済み: 24**（2026-08-15）。plan-h35 で `cull.warnMaxIds` として設定値になった（既定 24） | ~~クライアントと合意~~ |
| `PersonalResult` の項目 | `finalRank` / `score` / `takoyakiCount` / `survivedMs` / `stats` | 企画・クライアントと合意 |

---

## 4. 1つ前のバイナリを保管（runbook §0）

### 4.1 なぜ必要か

plan-h20 §3 で、廃止処理（信用・我慢ゲージ・storm）を**フラグで残さず削除**すると決めた。
その代わり「**戻す手段は運用で担保する**」と約束したのがこれ。

**コード上の戻り道は無い。戻す＝1つ前のバイナリに差し替える、が唯一の手段。**

### 4.2 やること

本戦に臨むコミットが確定してから（＝これ以上マージしないと決めてから）。

```bash
cd /Users/ryu/kindai/2026/THEHACK/Takoda99-Server && git checkout main && git pull --ff-only origin main && git log --oneline -3
```

**本戦バイナリ**と、**その1つ前のマージコミット**の2つをビルドする。

```bash
GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o ~/takoda99-backup/takoda99-server-$(git rev-parse --short HEAD) ./cmd/server
```

1つ前に戻ってビルド（`<1つ前のハッシュ>` は上の `git log` で確認）:

```bash
git checkout <1つ前のハッシュ> && GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o ~/takoda99-backup/takoda99-server-$(git rev-parse --short HEAD) ./cmd/server && git checkout main
```

### 4.3 runbook §0 の表を埋める

`docs/runbook.md` の §0 を実際の値で埋める。**当日これを見て復旧する。**

```
| | ファイル名 | コミット | 置き場所 |
|---|---|---|---|
| 本戦バイナリ | takoda99-server-6092a8d | 6092a8d | ~/takoda99-backup/ |
| 1つ前（退避用） | takoda99-server-62c3133 | 62c3133 | ~/takoda99-backup/ |
```

あわせて §0 の他のチェックも埋める:

- [ ] `curl https://takoda99.mooo.com/api/params` が手元から通る
- [ ] `CONFIG_ADMIN_TOKEN` を手元に控える
- [ ] `https://takoda99.mooo.com/admin/?token=...` が開く

### 4.4 使うとき

runbook §2 の**優先順位を守る**。

1. **まず config を戻す**（数秒・ビルド不要）← 調整値が原因ならここで直る
2. **バイナリを1つ前に戻す**（数十秒）← コードが原因のときだけ
3. 再起動だけ試す

---

## 付録：当日の持ち物

- [ ] `CONFIG_ADMIN_TOKEN`
- [ ] GCPコンソールにログインできる状態
- [ ] `~/takoda99-backup/` の2バイナリ（§4）
- [ ] [runbook.md](runbook.md) を開いておく（印刷 or 別タブ）
- [ ] ダッシュボードのURL（トークン付き）
