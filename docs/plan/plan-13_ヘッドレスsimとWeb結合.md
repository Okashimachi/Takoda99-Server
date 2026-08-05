# Plan-13: ヘッドレスtickシミュレータ ~~＋Web結合~~（tako-L）

> **目的**: ビルド無しでバランス調整できるヘッドレスシミュレータを作る。
> ~~Web フロントと結合して「1試合を接続〜リザルトまで遊べる」状態にする。~~
> **対応issue**: #12（**完了・クローズ済み**）
> **優先度**: **最高**。これが終わるまで「ゲームが遊べる」状態にならない。
> **依存**: コア実装（Plan-02〜05）完了済み
> **参照**: 試合進行仕様 §4, パラメータ仕様 §12

---

> ## ⚠ 訂正（2026-08-05）
>
> **§1（ヘッドレスsim）は完了**。`cmd/matchsim` として PR #73 でマージ済み。
> 実行方法は `AGENTS.md` §6。計測の結果、決着が28〜35秒（目安120〜180秒の1/4〜1/5）だったので、
> **バランス調整は #74 に切り出した**（`DefaultParameters` は未変更）。
>
> **§2（Web結合）は前提が覆ったので無効**。`Takoda99-WebFront` は**廃止・凍結**され、
> 「Web先行 → Unity流用」戦略をやめて**最初から Unity 単独**で開発する方針に変わっている。
> §2 の手順は廃止リポジトリとの結合手順であり、**実行しないこと**。
> 実際のクライアント結合は **Plan-21（#38 WebGL疎通）／ Plan-22（#39 Unity結合）**が持つ。
>
> 誤りを消さずに残しているのは、同じ前提が別の文書へ再び写されるのを防ぐため（`AGENTS.md` §7）。

---

## 0. 前提知識

### なぜこれが最優先か

コアの実装は終わっているが、**通しで1試合が回ることを誰も確認していない**。
単体テストは各 step を個別に検証しているだけで、「99店で始めて1店になるまで」を回した実績がない。
ここが通らないと当日そもそも遊べないので、他のどの作業よりも先に潰す。

シミュレータには2つの役割がある。混同しないこと。

| | 目的 | 実行環境 |
|---|---|---|
| **ヘッドレスsim**（本プラン） | **バランス調整**。決着が2〜3分に収まるか、数値を変えて反復 | メモリ内・大きい dt で高速 |
| 負荷テスト（Plan-18） | **性能検証**。99接続を捌けるか | 実 WebSocket・実時間 |

### 読むべきファイル

| ファイル | 何を見るか |
|---|---|
| `internal/game/session.go` | `NewSession` / `Start` / `Tick` / `Results` のシグネチャ |
| `internal/app/app.go` | `Deps` / `RunMatch` / `NewBotPlayer` の組み立て方 |
| `internal/bot/bot.go` | `bot.Config`（`BaseAccuracy` / `BaseElapsedMs` / 各 Jitter）。**1打鍵単位ではなく1注文単位**のモデル |
| `internal/transport/inmemory.go` | `Pipe()` |
| `internal/odai/pool.go` | `NewStaticPool()`（DB 無しで動くお題） |

---

## 1. ヘッドレスシミュレータ（cmd/matchsim）

### 1.1 方式の選択

**session を直接叩く**。room / transport / bot を通さない。

理由: room は実時計（ticker）で回るので「1試合を数秒で」が成立しない。
`session.Tick(dtMs)` を直接ループで呼べば、1試合ぶんの tick を一瞬で消化できる。

Bot の代わりに **ダミー店**を sim 内に持ち、`ApplyOrderServed` を直接呼ぶ。

```
for tick := 0; ; tick++ {
    out := sess.Tick(tickMs)
    ダミー店の処理: out から CustomerArrived を拾い、
                    打鍵時間が経過したら ApplyOrderServed を呼ぶ
    if sess.State() == game.Finished { break }
}
```

### 1.2 ダミー店のモデル

```go
// dummyStore はシミュレータ内の仮想プレイヤー。
// 実力を (打鍵速度, ミス率) の2値で表し、人間の分布を模す。
type dummyStore struct {
	id          game.PlayerId
	msPerKey    int     // 1打鍵あたりのms（小さいほど速い）
	missRate    float64 // 打鍵ごとのミス確率

	// 対応中の客（CustomerArrived で受け取る）
	current     *pendingOrder
}

type pendingOrder struct {
	customerId  proto.CustomerId
	keystrokes  int   // 全語の打鍵数合計
	remainingMs int   // 打ち終わるまでの残り時間
	missCount   int
}
```

来店を受けたら所要時間を決める:

```go
func (d *dummyStore) accept(v proto.CustomerView, rng *rand.Rand) {
	keys := 0
	for _, w := range v.Words {
		keys += utf8.RuneCountInString(w) // 概算。正確な打鍵数は odai 側にしかない
	}
	miss := 0
	for i := 0; i < keys; i++ {
		if rng.Float64() < d.missRate {
			miss++
		}
	}
	d.current = &pendingOrder{
		customerId:  v.CustomerId,
		keystrokes:  keys,
		remainingMs: keys*d.msPerKey + miss*d.msPerKey, // ミスは打ち直し1回ぶん
		missCount:   miss,
	}
}
```

> **注意**: 打鍵数の算出は本来 **`reading`（読み）** から行う（`odai.Keystrokes(reading string) int`・公開済み）。
> しかし `CustomerView.Words` は**表示テキストしか含まず `reading` が無い**ので、sim では正確な値を出せない。
> ルーン数による概算で十分（バランスの傾向を見るのが目的）。
> 厳密にしたいなら proto に reading を載せる必要があり、それは**契約変更＝要承認**なのでここではやらない。

毎tick進めて、0 になったら報告:

```go
func (d *dummyStore) step(dtMs int) (proto.OrderServed, bool) {
	if d.current == nil {
		return proto.OrderServed{}, false
	}
	d.current.remainingMs -= dtMs
	if d.current.remainingMs > 0 {
		return proto.OrderServed{}, false
	}
	o := proto.OrderServed{
		CustomerId: d.current.customerId,
		ElapsedMs:  d.current.keystrokes * d.msPerKey,
		MissCount:  d.current.missCount,
	}
	d.current = nil
	return o, true
}
```

### 1.3 実力分布のプリセット

#34 が要求する「実力分布を変えても決着する」を満たすため、分布をフラグで選べるようにする。

```go
// profile は99店の実力分布のプリセット。
type profile string

const (
	profileUniform profile = "uniform" // 全員同じ実力（膠着の最悪ケース）
	profileNormal  profile = "normal"  // 正規分布（現実に近い）
	profileBipolar profile = "bipolar" // 二極化（上手い/下手がはっきり）
	profileWide    profile = "wide"    // 実力差が非常に大きい
)

func buildStores(n int, p profile, rng *rand.Rand) []*dummyStore {
	stores := make([]*dummyStore, n)
	for i := range stores {
		var msPerKey int
		var missRate float64
		switch p {
		case profileUniform:
			msPerKey, missRate = 200, 0.05
		case profileNormal:
			msPerKey = 200 + int(rng.NormFloat64()*50)
			missRate = clamp(0.05+rng.NormFloat64()*0.02, 0, 0.5)
		case profileBipolar:
			if i%2 == 0 {
				msPerKey, missRate = 130, 0.02
			} else {
				msPerKey, missRate = 300, 0.10
			}
		case profileWide:
			msPerKey = 100 + rng.Intn(400)
			missRate = rng.Float64() * 0.2
		}
		if msPerKey < 50 {
			msPerKey = 50
		}
		stores[i] = &dummyStore{
			id: game.PlayerId(fmt.Sprintf("s-%d", i+1)),
			msPerKey: msPerKey, missRate: missRate,
		}
	}
	return stores
}
```

### 1.4 コマンドライン

```bash
go run ./cmd/matchsim                          # 既定: 99店・normal 分布
go run ./cmd/matchsim --stores 99 --profile uniform
go run ./cmd/matchsim --runs 20 --profile normal --quiet   # 20回まわして統計
go run ./cmd/matchsim --seed 42                # 再現性
```

| フラグ | 既定 | 意味 |
|---|---|---|
| `--stores` | 99 | 店舗数 |
| `--profile` | normal | 実力分布（uniform/normal/bipolar/wide） |
| `--runs` | 1 | 試行回数。複数なら統計サマリを出す |
| `--seed` | 時刻 | 乱数シード（再現性） |
| `--max-ticks` | 20000 | 保険。超えたら膠着とみなして異常終了 |
| `--quiet` | false | 途中経過を出さない |

### 1.5 出力

1回ぶん:

```
=== match 1 (profile=normal seed=42) ===
決着       : 1043 tick / 156.5 秒（tickIntervalMs=150）
最終フェーズ : Late
最終 heatLevel: 12
優勝       : s-37 (msPerKey=142 missRate=0.031)

フェーズ推移:
  Early →Mid   : tick 210 (31.5s) alive=70
  Mid   →Late  : tick 604 (90.6s) alive=30

生存数の推移（10%刻み）:
  tick    0  alive 99
  tick  180  alive 89
  ...
```

複数回:

```
=== 20 runs / profile=normal ===
決着時間   : 平均 152.3s / 最短 131.0s / 最長 178.5s
決着 tick  : 平均 1015 / 最短 873 / 最長 1190
最終heatLevel: 平均 11.8 / 最大 14
膠着(max-ticks到達): 0 / 20

目安2〜3分(120〜180s)に収まった: 20/20 ✅
```

### 1.6 実装スケルトン

`cmd/matchsim/main.go`:

```go
package main

func main() {
	stores := flag.Int("stores", 99, "店舗数")
	prof := flag.String("profile", "normal", "uniform|normal|bipolar|wide")
	runs := flag.Int("runs", 1, "試行回数")
	seed := flag.Int64("seed", time.Now().UnixNano(), "乱数シード")
	maxTicks := flag.Int("max-ticks", 20000, "膠着とみなす上限")
	quiet := flag.Bool("quiet", false, "途中経過を出さない")
	flag.Parse()

	params := game.DefaultParameters()
	results := make([]runResult, 0, *runs)
	for i := 0; i < *runs; i++ {
		rng := rand.New(rand.NewSource(*seed + int64(i)))
		r := simulate(params, *stores, profile(*prof), rng, *maxTicks, *quiet)
		results = append(results, r)
	}
	report(results, *runs)
}

func simulate(params game.GameParameters, n int, p profile, rng *rand.Rand,
	maxTicks int, quiet bool) runResult {

	dummies := buildStores(n, p, rng)
	inits := make([]game.PlayerInit, n)
	byId := make(map[game.PlayerId]*dummyStore, n)
	for i, d := range dummies {
		inits[i] = game.PlayerInit{Id: d.id, DisplayName: string(d.id)}
		byId[d.id] = d
	}

	sess := game.NewSession("sim", params, odai.NewStaticPool(), rng, inits)
	tickMs := params.Session.TickIntervalMs

	handle := func(out []game.Outbound) {
		for _, o := range out {
			if v, ok := o.Msg.(proto.CustomerView); ok {
				if d := byId[o.To.PlayerId]; d != nil {
					d.accept(v, rng)
				}
			}
			// PhaseChange / StoreEliminated 等はレポート用に記録
		}
	}

	handle(sess.Start())

	for tick := 1; tick <= maxTicks; tick++ {
		handle(sess.Tick(tickMs))
		// ダミー店の打鍵を進め、打ち終わったら報告
		for _, d := range dummies {
			if o, done := d.step(tickMs); done {
				handle(sess.ApplyOrderServed(d.id, o))
			}
		}
		if sess.State() == game.Finished {
			return finishedResult(sess, tick)
		}
	}
	return stalledResult(sess, maxTicks) // 膠着
}
```

> **重要**: `sess.Tick` と `sess.ApplyOrderServed` の戻り値を**両方**処理すること。
> `ApplyOrderServed` も Outbound を返すので、捨てると次の来店を取りこぼす。

---

## ~~2. Web 結合~~（無効。冒頭の訂正を参照）

> **この節は実行しない。** `Takoda99-WebFront` は廃止・凍結され、クライアントは `Takoda99-Unity` のみ。
> クライアント結合は Plan-21（#38）／ Plan-22（#39）が持つ。
> ただし **§2.5「接続直後のハンドシェイク」は Unity でも同じく有効**（サーバー側の挙動の話なので）。

### 2.1 前提

`Takoda99-WebFront` が接続できる solo モードのサーバーが要る → **Plan-15（#36）が先**。

### 2.2 やること

1. **接続先の共有**: WebFront に solo エンドポイントを伝える
2. **`ALLOWED_ORIGINS` に WebFront のオリジンを追加**
   ```
   ALLOWED_ORIGINS=http://localhost:5173,https://<webfront>.vercel.app
   ```
   `/etc/takoda99.env` を編集 → `sudo systemctl restart takoda99`
3. **1試合の目視確認**: `MatchStart → CustomerArrived → OrderServed → EvaluationUpdate → ... → MatchEnd`
4. **proto バージョンと接続手順を Discord で再周知**（v0.3.0 で `matchTimeLimitMs` が消えている）

### 2.3 確認チェックリスト

WebFront 側で以下が確認できること:

- [ ] 接続すると `MatchStart` が届き、`selfStoreId` で自店が特定できる
- [ ] `CustomerArrived` の `words` が表示され、打てる
- [ ] 打ち切ると `OrderServed` を送れ、`EvaluationUpdate` が返る
- [ ] 放置すると `CustomerLeft` → `CreditUpdate` でライフが減る
- [ ] ライフ0で `StoreEliminated`(SelfCollapse) が届く
- [ ] 最後に `MatchEnd` が届き、`finalRank` が表示できる
- [ ] `StoreListUpdate` でミニ盤面が更新される

### 2.4 クライアント側ドキュメントの確認

ワイヤ仕様・クライアント設計は **`Takoda99-Client-Docs` リポジトリ**が正典
（server 側の `docs/client-integration.md` は重複解消のため削除済み）。
結合時に食い違いが見つかったら**ドキュメントではなくサーバーの実挙動を正**として
Client-Docs を直す。

### 2.5 接続直後のハンドシェイク（見落としやすい）

サーバーは接続を受けると `awaitJoinName(conn, joinTimeout)` で
**最初の1メッセージを最大3秒待つ**（`cmd/server/main.go` / `joinTimeout = 3 * time.Second`）。

```json
{ "type": "MatchmakingJoin", "payload": { "displayName": "たこ焼き太郎" } }
```

- **送らないと3秒待たされ**、表示名が空になる（フォールバック名が割り当てられる）
- 別種のメッセージを最初に送っても同じ扱い
- 表示名は最大24文字・制御文字は除去される（`matchmaking.SanitizeDisplayName`）

**接続したらすぐ `MatchmakingJoin` を送る**のが正しい実装。
盤面に名前が出ない不具合の大半はこれ。

---

## 3. テスト

### CI に乗せる（ヘッドレスなので高速）

`cmd/matchsim` のロジックを `internal/` 側でもテストできるよう、
`simulate` は `main` パッケージに置きつつ、**決着保証のテストは Plan-14（#34）が担当**する。

本プランでは最低限:

```go
// cmd/matchsim/main_test.go
func TestSimulate_Finishes(t *testing.T) {
	params := game.DefaultParameters()
	rng := rand.New(rand.NewSource(1))
	r := simulate(params, 20, profileNormal, rng, 20000, true)
	if r.stalled {
		t.Fatal("20店で決着しない")
	}
}
```

---

## 4. ローカル確認

```bash
go build ./...
go run ./cmd/matchsim --stores 99 --profile normal
go run ./cmd/matchsim --runs 10 --profile uniform --quiet
go test ./cmd/matchsim/
```

---

## 5. 完了条件

- [x] `cmd/matchsim` が動き、99店の1試合をヘッドレスで完走できる
- [x] `--profile` で4種の実力分布を切り替えられる
- [x] `--runs N` で統計サマリ（平均/最短/最長の決着時間）が出る
- [x] `--seed` で結果が再現する
- [x] 決着時間・最終フェーズ・最終 heatLevel がレポートされる
- [x] 膠着時（max-ticks 到達）に異常終了して分かる
- [x] **シミュレータで目安2〜3分の決着が安定して出る**（出ない場合はパラメータ調整の材料を出す）
      → 出なかった（28〜35秒）。膠着は0/80。材料を出して **#74** に切り出し済み
- ~~[ ] Web フロントで1試合を接続〜リザルトまで遊べる~~
- ~~[ ] `ALLOWED_ORIGINS` に WebFront のオリジンが入っている（または未設定＝全許可）~~
- ~~[ ] WebFront が接続直後に `MatchmakingJoin`（`displayName` 付き）を送っている~~
- ~~[ ] 盤面に表示名が正しく出る（空名フォールバックになっていない）~~
- ~~[ ] proto バージョンと接続手順をクライアント担当へ周知済み~~

> 取り消し線の5項目は WebFront 廃止により無効。同等の確認は Plan-21（#38）／ Plan-22（#39）が持つ。
