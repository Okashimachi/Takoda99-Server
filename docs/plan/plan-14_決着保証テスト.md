# Plan-14: 制限時間廃止後の決着保証テスト（tako-N）

> **目的**: 「試合が必ず終わる」保証が下位淘汰(storm)だけになったので、それを回帰テストとして固定し、決着時間を実測してパラメータ調整の土台を作る。
> **対応issue**: #34
> **優先度**: **高**。試合が終わらないとイベントが成立しない。
> **依存**: Plan-13（`cmd/matchsim` のシミュレータ基盤）
> **参照**: 全体仕様 §8.1〜8.3, パラメータ仕様 §3・§8

---

## 0. 前提知識

### なぜ独立の課題なのか

Plan-13 のシミュレータは「**バランスが良いか**（2〜3分で決着するか）」を見る道具。
本プランは「**そもそも終わるか**」という**正しさ**を見る。目的が違うのでテストも別に持つ。

制限時間を廃止（proto v0.3.0 / #33）した結果:

| 変更前 | 変更後 |
|---|---|
| 生存店=1 **or 制限時間到達**で終了 | **生存店=1 のみ** |
| 上手い者同士が残っても時間で打ち切られた | 打ち切りが無い。**火力の上昇と下位淘汰だけが決着を作る** |

つまり **storm が止まると試合が永遠に終わらない**。これは当日「試合が終わらず次に進めない」という致命的な事故になる。

### 膠着の穴は2つ

どちらも Plan-04（tako-H）の範囲。

1. **淘汰人数が0に丸まる**
   生存数が少ないと「下位 `ThresholdPct`%」が0店になる。
   生存3店 × 10% = 0.3店 → `int()` で 0 → **誰も減らない**。
   → Plan-04 の実装は `cullCount` の最低1店を保証しているか要確認。

2. **火力が頭打ちになる**
   `heatLevel` が上がってもお題の難度に上限があり、上手い者同士が延々と捌き続ける。
   → 難度の上端に張り付いたまま決着しない状態を**検出して失敗させる**必要がある。

---

## 1. 現状の確認（実装前にやること）

まず穴が塞がっているかをコードで確認する。

```bash
grep -n "cullCount" internal/game/session.go
```

期待する実装（Plan-04）:

```go
cullCount := int(float64(len(alive)) * sp.ThresholdPct)
if cullCount < 1 {
    cullCount = 1          // ← これが無いと穴1に落ちる
}
if cullCount > len(alive)-1 {
    cullCount = len(alive) - 1  // ← 全滅防止
}
```

無ければ**先に直す**（本プランのテストが落ちるはずなので、テストを書いてから直してもよい）。

---

## 2. テストの設計

### 2.1 置き場所

`cmd/matchsim` のシミュレータを再利用するが、テストは **CI で毎回回す**ので
`internal/` 側に置きたい。Plan-13 の `simulate` を切り出す:

```
internal/sim/          ← 新規パッケージ
  sim.go               … dummyStore / profile / Simulate()
  sim_test.go          … 本プランの決着保証テスト
cmd/matchsim/main.go   … internal/sim を呼ぶ薄いCLIになる
```

> `internal/sim` は game を import するが、game は sim を知らない（依存の向きは維持）。

### 2.2 テスト1: 全プロファイルで有限ティック決着

```go
func TestDecisiveness_AllProfiles(t *testing.T) {
	profiles := []sim.Profile{
		sim.ProfileUniform, // ← 最悪ケース。全員同実力で差がつかない
		sim.ProfileNormal,
		sim.ProfileBipolar,
		sim.ProfileWide,
	}
	const maxTicks = 20000 // 150ms × 20000 = 50分ぶん。これを超えたら膠着

	for _, p := range profiles {
		p := p
		t.Run(string(p), func(t *testing.T) {
			for seed := int64(1); seed <= 5; seed++ {
				r := sim.Simulate(sim.Config{
					Params:   game.DefaultParameters(),
					Stores:   99,
					Profile:  p,
					Rng:      rand.New(rand.NewSource(seed)),
					MaxTicks: maxTicks,
				})
				if r.Stalled {
					t.Fatalf("seed=%d で決着せず（%d tick で生存%d店・heatLevel=%d）",
						seed, maxTicks, r.AliveAtEnd, r.HeatLevel)
				}
				if r.Winner == "" {
					t.Fatalf("seed=%d 優勝店が確定していない", seed)
				}
			}
		})
	}
}
```

**`ProfileUniform` が本命**。全員同実力だと評価がほぼ同値になり、パーセンタイル正規化でも差がつきにくい。ここで storm が確実に削れるかが決着保証の核心。

### 2.3 テスト2: 終盤で1店ずつ確実に減る

生存数が少ない領域（穴1）を直接突く。

```go
func TestDecisiveness_EndgameAlwaysShrinks(t *testing.T) {
	// 生存が少ない状態から始めて、storm が必ず減らすことを確認する
	for _, n := range []int{2, 3, 5, 10} {
		n := n
		t.Run(fmt.Sprintf("alive=%d", n), func(t *testing.T) {
			r := sim.Simulate(sim.Config{
				Params:   game.DefaultParameters(),
				Stores:   n,
				Profile:  sim.ProfileUniform, // 差がつかない条件
				Rng:      rand.New(rand.NewSource(1)),
				MaxTicks: 20000,
			})
			if r.Stalled {
				t.Fatalf("%d店で決着せず（下位%%が0に丸まっている可能性）", n)
			}
		})
	}
}
```

### 2.4 テスト3: 火力の頭打ち検出

決着時点の `heatLevel` と、それが難度の上端に張り付いていないかを見る。

```go
func TestDecisiveness_HeatNotSaturated(t *testing.T) {
	r := sim.Simulate(sim.Config{
		Params:   game.DefaultParameters(),
		Stores:   99,
		Profile:  sim.ProfileUniform,
		Rng:      rand.New(rand.NewSource(1)),
		MaxTicks: 20000,
	})
	if r.Stalled {
		t.Fatal("決着せず")
	}
	// 上端に張り付いたまま長時間かかっていたら、火力設計に穴がある
	if r.TicksAtMaxHeat > 3000 {
		t.Errorf("火力が上端に張り付いたまま %d tick 経過している。"+
			"お題難度の上限を上げるか storm を強めること（heatLevel=%d）",
			r.TicksAtMaxHeat, r.HeatLevel)
	}
}
```

`Simulate` の戻り値に `TicksAtMaxHeat`（`wordLevel()` が上限に達している間の tick 数）を持たせる。

### 2.5 テスト4: 決着時間の実測（レポート専用・失敗させない）

パラメータ調整の材料。**CI で失敗させない**（数値は人間が判断する）。

```go
func TestDecisiveness_ReportTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("-short では回さない")
	}
	params := game.DefaultParameters()
	tickMs := params.Session.TickIntervalMs

	for _, p := range []sim.Profile{sim.ProfileUniform, sim.ProfileNormal, sim.ProfileBipolar, sim.ProfileWide} {
		var times []float64
		for seed := int64(1); seed <= 10; seed++ {
			r := sim.Simulate(sim.Config{
				Params: params, Stores: 99, Profile: p,
				Rng: rand.New(rand.NewSource(seed)), MaxTicks: 20000,
			})
			if r.Stalled {
				t.Fatalf("%s seed=%d 決着せず", p, seed)
			}
			times = append(times, float64(r.Ticks*tickMs)/1000)
		}
		mean, min, max := stats(times)
		t.Logf("%-8s 決着 平均%.1fs 最短%.1fs 最長%.1fs （目安120〜180s）", p, mean, min, max)
		if mean < 60 || mean > 300 {
			t.Logf("  ⚠ 目安から外れている。調整候補: storm.intervalTicks / storm.thresholdPct / heat.perAliveDrop")
		}
	}
}
```

`go test -v ./internal/sim/ -run ReportTiming` で表が出る。

---

## 3. Simulate の戻り値に足すもの

Plan-13 の `runResult` を拡張:

```go
type Result struct {
	Ticks          int            // 決着までの tick 数
	Stalled        bool           // MaxTicks 到達（膠着）
	Winner         game.PlayerId  // 優勝店（Stalled なら空）
	AliveAtEnd     int            // 終了時の生存数
	FinalPhase     proto.Phase
	HeatLevel      int            // 決着時の火力
	MaxHeatLevel   int            // 試合中の最大火力
	TicksAtMaxHeat int            // 火力が上限に張り付いていた tick 数  ← 本プランで追加
	PhaseChanges   []PhaseChangeAt
	AliveCurve     []AlivePoint   // 生存数の推移
}
```

---

## 4. 膠着が見つかった時の調整順

テストが落ちたら、この順で疑う。

| 症状 | 原因の候補 | 触るパラメータ |
|---|---|---|
| 終盤(生存2〜5)で止まる | 淘汰人数が0に丸まっている | `cullCount` の最低1保証（**コード修正**） |
| 中盤から減らない | storm の間隔が長い / 閾値が小さい | `storm.intervalTicks` ↓ / `storm.thresholdPct` ↑ |
| 全体的に長い | 火力が上がらず全員捌けてしまう | `heat.perAliveDrop` ↑ / `heat.phaseLate` ↑ |
| 火力が上端で頭打ち | お題難度の段階が足りない | お題の最大レベルを増やす（Plan-07 の語彙） |
| 早く終わりすぎる | 淘汰が強すぎる | `storm.thresholdPct` ↓ |

**パラメータの具体値は本プランで確定しない**。実測を出して人間が決める。

---

## 5. ローカル確認

```bash
go build ./...
go test ./internal/sim/ -run TestDecisiveness -v
go test ./internal/sim/ -run ReportTiming -v      # 決着時間の表を見る
go test -short ./...                              # CI 相当（重いレポートは skip）
```

---

## 6. 完了条件

- [ ] `internal/sim` パッケージにシミュレータが切り出され、`cmd/matchsim` はその薄いCLIになっている
- [ ] `cullCount` の最低1店保証がコードにある（無ければ追加）
- [ ] **全プロファイル（uniform 含む）× 複数シードで有限ティック内に生存店=1 へ到達する**
- [ ] 生存2/3/5/10店から始めても決着する（終盤の丸め落ちがない）
- [ ] 火力が上端に張り付いたまま長時間決着しない場合、テストが**失敗する**
- [ ] 決着ティック数・決着時の `heatLevel`・最終フェーズがレポート出力される
- [ ] 決着時間の実測値（プロファイル別の平均/最短/最長）が `-run ReportTiming` で出る
- [ ] 決着保証テストが CI で回る（`-short` でもタイムアウトしない規模）
