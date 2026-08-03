# Plan-03: 客分配と評価正規化（tako-G）

> **目的**: たべたべエリア(restPool)の客を生存全店へ重み付き抽選で分配し、評価をパーセンタイル正規化 → rank を確定する。
> **対応issue**: #7
> **依存**: Plan-02（離脱で客が restPool へ戻る仕組み）, tako-E（ApplyOrderServed で evalRaw が更新される前提）
> **参照**: 試合進行仕様 &sect;6, 全体仕様 &sect;4.2-6, 用語集 &sect;3-5

---

## 0. 前提知識

### 読んでおくファイル

| ファイル | 要点 |
|---|---|
| `internal/game/session.go` | Session 構造体、`stepDistribute`/`stepNormalize` の no-op スタブ（実装対象）、`admitCustomer` (既存)、`evalScore` (既存) |
| `internal/game/params.go` | `GameParameters`, `EvalParams`, `CustomerParams`, `DefaultParameters()` |
| `internal/game/ports.go` | `PlayerId`, `Word`, `WordSource` |
| `internal/proto/messages.go` | `EvaluationUpdate`, `CustomerView`, `Phase`, `CustomerAttribute` の再輸出 |
| `internal/game/session_test.go` | `newTestSession`, `fakeWords`, `placeAssigned` など既存テストヘルパ |

### キーコンセプト

**重み付き分配 (weighted distribution)**
客を店に割り当てる際、均等ではなく各店の「重み」に比例した確率で選ぶ。評価が高い店ほど客が来やすく（正のフィードバック）、行列が長い店ほど来にくい（負のフィードバック = 独走抑制）。初期状態（全店 evalNormalized = 0）では均等分配にフォールバックする。

重みの式は設計文書（試合進行仕様 §6）では `正規化評価 ÷ (行列長 + 1)` だが、**本プランでは
下駄 `WeightFloor` を足した式を使う**（理由は §2.5）:

```
重み = (WeightFloor + evalNormalized) / (queueLen + 1)
```

**パーセンタイル正規化 (percentile normalization)**
生存店の `evalScore` を昇順ソートし、順位位置を `[0, 1]` に線形写像する。最下位 = 0、最上位 = 1。これにより evalRaw の絶対値に依存せず、相対的な立ち位置で分配重みが決まる。


### 2.5 仕様上の穴と対処（WeightFloor）

**問題**: パーセンタイル正規化は最下位店に必ず `evalNormalized = 0.0` を与える
（`i/(n-1)` で i=0 のとき 0）。設計文書どおり重みを `evalNormalized / (queueLen+1)` にすると、
最下位店の重みは常に **0** になる。

その結果:

1. 最下位店には客が1人も来ない
2. 客が来ない → 提供できない → `evalRaw` が更新されない
3. 評価が上がらない → 最下位のまま → 永久に客が来ない
4. storm の下位淘汰で確実に脱落する

つまり**一度最下位になると復帰不能**で、プレイヤーは何もできないまま脱落を待つことになる。
企画書の「終盤まで逆転の余地がある」設計意図に反する。

**対処**: 重みに下駄 `WeightFloor` を足す。

```
重み = (WeightFloor + evalNormalized) / (queueLen + 1)
```

`WeightFloor = 0.25` のとき、最下位店（norm=0）の重みは `0.25`、最上位店（norm=1）は `1.25`。
最下位でも最上位の約20%の来店率を保ち、挽回の余地が残る。相対的な優劣（上位ほど客が多い）は維持される。

`WeightFloor = 0` にすれば設計文書どおりの挙動に戻せる（config-front から変更可能）。
バランス調整で「独走を許容して差をつけたい」場合は小さく、「逆転を起きやすくしたい」場合は大きくする。

> **決定済み（マネージャー承認）**: この下駄を入れる方針で確定。
> Takoda99-Docs の試合進行仕様 §6 の式（`正規化評価 ÷ (行列長+1)`）は**そのままにする**。
> つまりこの1点だけ Docs と実装が意図的に食い違う。**Docs を見て「下駄は仕様外だから消そう」と
> しないこと**。挙動の正典はこのプラン（と `WeightFloor` の config 値）側。

---

## 1. 現状のコード

### no-op スタブ（session.go ~L272, ~L296）

```go
// stepDistribute は restPool の客と補充が要る店へ客を割り当て、CustomerArrived を配る。tako-G(+D)。
func (s *Session) stepDistribute(out []Outbound) []Outbound { return out }

// stepNormalize は生存店内で evalRaw をパーセンタイル化(evalNormalized)+rank 確定し、EvaluationUpdate を配る。tako-G。
func (s *Session) stepNormalize(out []Outbound) []Outbound { return out }
```

### 既存で利用する関数・型

```go
// admitCustomer は客1人を store へ来店させる。行列末尾追加 + お題発行 + CustomerArrived 返却。
func (s *Session) admitCustomer(cid proto.CustomerId, store PlayerId) (Outbound, bool)

// evalScore は正規化/順位付けに使う実効評価 = EMA基準 + JK一時加点。
func (s *Session) evalScore(st *storeState) float64 { return st.evalRaw + st.buzzBonus }

// storeState（関連フィールドのみ抜粋）
type storeState struct {
    evalRaw        float64
    buzzBonus      float64
    evalNormalized float64
    rank           int
    alive          bool
}

// customer（関連フィールドのみ抜粋）
type customer struct {
    attribute     proto.CustomerAttribute
    assignedStore *PlayerId
}
```

### proto 型

```go
// EvaluationUpdate（Takoda99-Proto 由来）
type EvaluationUpdate struct {
    EvalRaw    float64
    Normalized float64
    Rank       int
    AliveCount int
}

// Phase 定数: proto.PhaseEarly, proto.PhaseMid, proto.PhaseLate
// 属性定数: proto.AttrNormal, proto.AttrBonus, proto.AttrClaimer, proto.AttrBuzz
```

### GameParameters に DistributionParams がまだ無い

`params.go` の `GameParameters` には Distribution 関連のフィールドが存在しない。追加が必要。

---

## 2. 実装手順

### Step A: DistributionParams を GameParameters に追加（params.go）

1. `DistributionParams` 型を定義する
2. `GameParameters` にフィールド `Distribution DistributionParams` を追加する
3. `DefaultParameters()` にデフォルト値を設定する

### Step B: stepDistribute を実装（session.go）

1. 分配先候補の収集: 生存(`alive`) かつ行列長が `QueueRefillThreshold` 未満の店を集める
2. 分配候補客の収集: `restPool` から取得。`phase == PhaseEarly` のとき `AttrClaimer` は除外（restPool に残す）
3. 候補店が空 or 候補客が空なら即座に return
4. 候補客を1人ずつ処理:
   - 各候補店の重みを算出: `evalNormalized / (queueLen + 1)`
   - 全店の evalNormalized が 0（初期状態）の場合: `1.0 / (queueLen + 1)` にフォールバック
   - 重み付きランダム選択で店を決定
   - `admitCustomer(cid, store)` を呼び、返った Outbound を out に追加
   - 分配後にその店の行列長が `QueueRefillThreshold` に達したら候補から除外

### Step C: stepNormalize を実装（session.go）

1. 生存店を `evalScore` の昇順でソートする
2. パーセンタイル値を算出: `evalNormalized = i / (n-1)` （i は 0-based 位置、n は生存店数）
3. n <= 1 の場合: `evalNormalized = 1.0`, `rank = 1`
4. rank を算出: `rank = n - i`（1 が最上位）
5. 全生存店へ `EvaluationUpdate` を送信

### Step D: weightedSelect ヘルパを追加（session.go）

重み配列から1つをランダム選択する汎用ヘルパ。`s.rng` を使用。

---

## 3. 実装するコード

### 3-1. DistributionParams 型 + DefaultParameters 更新（params.go）

```go
// DistributionParams: 客分配の調整値（tako-G）。
type DistributionParams struct {
	QueueRefillThreshold int     `json:"queueRefillThreshold"` // 行列がこの数未満の店を分配対象にする
	WeightFloor          float64 `json:"weightFloor"`          // 重みの下駄（最下位店の客ゼロを防ぐ・§2.5 参照）
}
```

GameParameters に追加:

```go
type GameParameters struct {
	// ... 既存フィールド ...
	Distribution DistributionParams `json:"distribution"` // tako-G
}
```

DefaultParameters() に追加:

```go
Distribution: DistributionParams{
	QueueRefillThreshold: 3,
	WeightFloor:          0.25, // 最下位店でも最上位店の 0.25/1.25 = 20% の来店率を確保
},
```

### 3-2. stepDistribute の完全実装（session.go）

```go
// stepDistribute は restPool の客を補充が要る生存店へ重み付き抽選で分配し、
// CustomerArrived を配る。tako-G。
//
// 重み = evalNormalized / (行列長+1)。evalNormalized が全店0の初期状態では
// 均等(1/(行列長+1))にフォールバックする。Claimer は Early 中は分配対象外。
func (s *Session) stepDistribute(out []Outbound) []Outbound {
	threshold := s.params.Distribution.QueueRefillThreshold

	// 1. 分配先候補: 生存 && 行列長 < threshold
	type candidate struct {
		id       PlayerId
		queueLen int
	}
	candidates := make([]candidate, 0, len(s.order))
	for _, sid := range s.order {
		st := s.stores[sid]
		if !st.alive {
			continue
		}
		ql := len(s.storeQueues[sid])
		if ql < threshold {
			candidates = append(candidates, candidate{id: sid, queueLen: ql})
		}
	}
	if len(candidates) == 0 {
		return out
	}

	// 2. 分配候補客: restPool から取得（Early では Claimer を除外）
	distributable := make([]proto.CustomerId, 0, len(s.restPool))
	for _, cid := range s.restPool {
		c := s.customers[cid]
		if c == nil {
			continue
		}
		if s.phase == proto.PhaseEarly && c.attribute == proto.AttrClaimer {
			continue
		}
		distributable = append(distributable, cid)
	}
	if len(distributable) == 0 {
		return out
	}

	// 3. 全候補店の evalNormalized が 0 か判定（初期状態フォールバック）
	allZero := true
	for _, cd := range candidates {
		if s.stores[cd.id].evalNormalized != 0 {
			allZero = false
			break
		}
	}

	// 4. 1人ずつ重み抽選で分配
	for _, cid := range distributable {
		if len(candidates) == 0 {
			break
		}
		// 重み算出
		weights := make([]float64, len(candidates))
		floor := s.params.Distribution.WeightFloor
		for i, cd := range candidates {
			if allZero {
				weights[i] = 1.0 / float64(cd.queueLen+1)
			} else {
				// 下駄(WeightFloor)を足す。パーセンタイル正規化では最下位店が必ず
				// evalNormalized=0 になり、素の式だと重み0＝客が永久に来なくなるため（§2.5）。
				weights[i] = (floor + s.stores[cd.id].evalNormalized) / float64(cd.queueLen+1)
			}
		}
		idx := s.weightedSelect(weights)
		chosen := &candidates[idx]

		ob, ok := s.admitCustomer(cid, chosen.id)
		if ok {
			out = append(out, ob)
		}

		// 行列長を更新し、threshold に達したら候補から除外
		chosen.queueLen++
		if chosen.queueLen >= threshold {
			candidates = append(candidates[:idx], candidates[idx+1:]...)
		}
	}
	return out
}

// weightedSelect は重み配列から1つのインデックスを rng で選ぶ。
// 全重みが 0 以下の場合は均等抽選にフォールバックする。
func (s *Session) weightedSelect(weights []float64) int {
	total := 0.0
	for _, w := range weights {
		if w > 0 {
			total += w
		}
	}
	if total <= 0 {
		return s.rng.Intn(len(weights))
	}
	r := s.rng.Float64() * total
	for i, w := range weights {
		if w > 0 {
			r -= w
			if r <= 0 {
				return i
			}
		}
	}
	return len(weights) - 1
}
```

### 3-3. stepNormalize の完全実装（session.go）

```go
// stepNormalize は生存店内で evalScore をパーセンタイル化(evalNormalized) + rank 確定し、
// EvaluationUpdate を全生存店へ配る。tako-G。
//
// 昇順ソートして i/(n-1) で 0..1 に写像。n<=1 のときは 1.0/rank=1。
func (s *Session) stepNormalize(out []Outbound) []Outbound {
	// 生存店を収集
	type entry struct {
		store *storeState
		score float64
	}
	alive := make([]entry, 0, s.aliveCount)
	for _, sid := range s.order {
		st := s.stores[sid]
		if st.alive {
			alive = append(alive, entry{store: st, score: s.evalScore(st)})
		}
	}
	n := len(alive)
	if n == 0 {
		return out
	}

	// evalScore 昇順ソート（安定ソート: order 内の順序を保存）
	sortEntries(alive)

	// パーセンタイル + rank
	for i, e := range alive {
		if n <= 1 {
			e.store.evalNormalized = 1.0
			e.store.rank = 1
		} else {
			e.store.evalNormalized = float64(i) / float64(n-1)
			e.store.rank = n - i
		}
	}

	// EvaluationUpdate を全生存店へ配信
	for _, e := range alive {
		out = append(out, to(e.store.id, proto.EvaluationUpdate{
			EvalRaw:    s.evalScore(e.store),
			Normalized: e.store.evalNormalized,
			Rank:       e.store.rank,
			AliveCount: s.aliveCount,
		}))
	}
	return out
}

// sortEntries は entry スライスを score 昇順で安定ソートする。
// sort パッケージの import を避けるため挿入ソート（n<=99 で十分高速）。
func sortEntries(entries []entry) {
	for i := 1; i < len(entries); i++ {
		key := entries[i]
		j := i - 1
		for j >= 0 && entries[j].score > key.score {
			entries[j+1] = entries[j]
			j--
		}
		entries[j+1] = key
	}
}
```

> **注**: `entry` 型は `stepNormalize` のローカルで使うが、`sortEntries` を分離するためファイルスコープにする。代わりに `sort.SliceStable` を使う場合は `"sort"` の import を追加。99 店以下なので挿入ソートで問題ないが、`sort.SliceStable` への差し替えも可。

### 3-4. sort パッケージ使用版（代替）

挿入ソートの代わりに標準ライブラリを使う場合:

```go
import "sort"

// stepNormalize 内の sortEntries(alive) を以下に置き換え:
sort.SliceStable(alive, func(i, j int) bool {
    return alive[i].score < alive[j].score
})
```

この場合 `sortEntries` 関数は不要。`"sort"` を session.go の import に追加する。

---

## 4. テスト

以下のテストを `internal/game/session_test.go` に追加する。

### 4-1. TestStepDistribute_EvenInitial

全店 eval=0 の初期状態で、restPool の客が概ね均等に分配されることを確認する。

```go
func TestStepDistribute_EvenInitial(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 5}

	// restPool に客を9人用意（3店 x 3人で均等になりやすい数）
	for i := 0; i < 9; i++ {
		cid := proto.CustomerId(fmt.Sprintf("d-%d", i))
		s.customers[cid] = &customer{
			attribute:     proto.AttrNormal,
			patienceMaxMs: 5000,
			orderCount:    1,
		}
		s.restPool = append(s.restPool, cid)
	}

	// 全店 evalNormalized=0（初期状態）
	for _, sid := range s.order {
		s.stores[sid].evalNormalized = 0
	}

	out := s.stepDistribute(nil)
	// 9人分の CustomerArrived が出る
	if len(out) != 9 {
		t.Fatalf("9件の CustomerArrived のはず: %d", len(out))
	}

	// 各店の行列長が 0 でないこと（均等なら各3）
	for _, sid := range s.order {
		ql := len(s.storeQueues[sid])
		if ql == 0 {
			t.Fatalf("店 %s に1人も分配されていない", sid)
		}
	}

	// restPool が空になっている
	if len(s.restPool) != 0 {
		t.Fatalf("restPool が空のはず: %d", len(s.restPool))
	}
}
```

### 4-2. TestStepDistribute_WeightedByEval

evalNormalized が高い店ほど多く客が来ることを確認する。

```go
func TestStepDistribute_WeightedByEval(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 100}

	// 店A: evalNormalized=1.0（最上位）, 店B: evalNormalized=0.01（最下位寄り）
	storeA := s.order[0]
	storeB := s.order[1]
	s.stores[storeA].evalNormalized = 1.0
	s.stores[storeB].evalNormalized = 0.01

	// restPool に客を100人用意
	for i := 0; i < 100; i++ {
		cid := proto.CustomerId(fmt.Sprintf("w-%d", i))
		s.customers[cid] = &customer{
			attribute:     proto.AttrNormal,
			patienceMaxMs: 5000,
			orderCount:    1,
		}
		s.restPool = append(s.restPool, cid)
	}

	s.stepDistribute(nil)

	qA := len(s.storeQueues[storeA])
	qB := len(s.storeQueues[storeB])

	// eval が圧倒的に高い店A のほうが多く来客するはず
	if qA <= qB {
		t.Fatalf("eval の高い店A(%d) が店B(%d) より多く来客するはず", qA, qB)
	}
}
```

### 4-3. TestStepDistribute_QueueLengthSuppression

行列が既に長い店には来客が抑制されることを確認する。

```go
func TestStepDistribute_QueueLengthSuppression(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 10}

	storeA := s.order[0]
	storeB := s.order[1]
	s.stores[storeA].evalNormalized = 0.5
	s.stores[storeB].evalNormalized = 0.5

	// 店A に既に行列5人を積む（threshold=10 なのでまだ候補だが重みが下がる）
	for i := 0; i < 5; i++ {
		cid := proto.CustomerId(fmt.Sprintf("pre-%d", i))
		s.customers[cid] = &customer{
			attribute:     proto.AttrNormal,
			patienceMaxMs: 5000,
			orderCount:    1,
		}
		s.restPool = append(s.restPool, cid)
		s.assignCustomer(cid, storeA)
	}
	// 店B は行列0

	// restPool に新たに20人
	for i := 0; i < 20; i++ {
		cid := proto.CustomerId(fmt.Sprintf("q-%d", i))
		s.customers[cid] = &customer{
			attribute:     proto.AttrNormal,
			patienceMaxMs: 5000,
			orderCount:    1,
		}
		s.restPool = append(s.restPool, cid)
	}

	s.stepDistribute(nil)

	qA := len(s.storeQueues[storeA]) // 事前5 + 新規分配分
	qB := len(s.storeQueues[storeB])

	// eval は同じだが、行列が長い店A より空の店B のほうが多く来客するはず
	newA := qA - 5 // 新規に分配された数
	if newA >= qB {
		t.Fatalf("行列が短い店B(%d) のほうが多く分配されるはず (店Aの新規=%d)", qB, newA)
	}
}
```

### 4-4. TestStepDistribute_ClaimerBlockedInEarly

Early フェーズでは Claimer 属性の客が分配されないことを確認する。

```go
func TestStepDistribute_ClaimerBlockedInEarly(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.phase = proto.PhaseEarly
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 5}

	// Claimer のみ restPool に入れる
	for i := 0; i < 5; i++ {
		cid := proto.CustomerId(fmt.Sprintf("cl-%d", i))
		s.customers[cid] = &customer{
			attribute:     proto.AttrClaimer,
			patienceMaxMs: 5000,
			orderCount:    1,
		}
		s.restPool = append(s.restPool, cid)
	}

	out := s.stepDistribute(nil)

	// Early では Claimer は分配されない
	if len(out) != 0 {
		t.Fatalf("Early で Claimer は分配されないはず: %d 件出力", len(out))
	}
	if len(s.restPool) != 5 {
		t.Fatalf("restPool に5人残るはず: %d", len(s.restPool))
	}

	// Mid に切り替えると分配される
	s.phase = proto.PhaseMid
	out = s.stepDistribute(nil)
	if len(out) != 5 {
		t.Fatalf("Mid では Claimer が分配されるはず: %d 件出力", len(out))
	}
}
```

### 4-5. TestStepDistribute_EmptyRestPool

restPool が空のとき panic せず何もしないことを確認する。

```go
func TestStepDistribute_EmptyRestPool(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 3}
	// restPool は空

	out := s.stepDistribute(nil)
	if out != nil {
		t.Fatalf("空 restPool で出力なしのはず: %v", out)
	}
}
```

### 4-6. TestStepNormalize_ThreeStores

3店の evalRaw が異なるとき正しくパーセンタイル化 + rank が付くことを確認する。

```go
func TestStepNormalize_ThreeStores(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.aliveCount = 3

	// evalRaw を 1.0, 2.0, 3.0 に設定（buzzBonus=0）
	s.stores[s.order[0]].evalRaw = 1.0
	s.stores[s.order[1]].evalRaw = 2.0
	s.stores[s.order[2]].evalRaw = 3.0

	out := s.stepNormalize(nil)

	// 3店分の EvaluationUpdate が出る
	if len(out) != 3 {
		t.Fatalf("3件の EvaluationUpdate のはず: %d", len(out))
	}

	// 昇順ソート: order[0]=最下位, order[1]=中間, order[2]=最上位
	st0 := s.stores[s.order[0]]
	st1 := s.stores[s.order[1]]
	st2 := s.stores[s.order[2]]

	// evalNormalized: 最下位=0.0, 中間=0.5, 最上位=1.0
	if st0.evalNormalized != 0.0 {
		t.Fatalf("最下位の normalized=0.0 のはず: %v", st0.evalNormalized)
	}
	if st1.evalNormalized != 0.5 {
		t.Fatalf("中間の normalized=0.5 のはず: %v", st1.evalNormalized)
	}
	if st2.evalNormalized != 1.0 {
		t.Fatalf("最上位の normalized=1.0 のはず: %v", st2.evalNormalized)
	}

	// rank: 最下位=3, 中間=2, 最上位=1
	if st0.rank != 3 {
		t.Fatalf("最下位の rank=3 のはず: %d", st0.rank)
	}
	if st1.rank != 2 {
		t.Fatalf("中間の rank=2 のはず: %d", st1.rank)
	}
	if st2.rank != 1 {
		t.Fatalf("最上位の rank=1 のはず: %d", st2.rank)
	}

	// EvaluationUpdate の中身を確認
	for _, o := range out {
		ev, ok := o.Msg.(proto.EvaluationUpdate)
		if !ok {
			t.Fatalf("EvaluationUpdate でない: %T", o.Msg)
		}
		if ev.AliveCount != 3 {
			t.Fatalf("AliveCount=3 のはず: %d", ev.AliveCount)
		}
	}
}
```

### 4-7. TestStepNormalize_SingleStore

生存1店のとき `evalNormalized=1.0`, `rank=1` になることを確認する。

```go
func TestStepNormalize_SingleStore(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	// 1店だけ alive
	s.stores[s.order[0]].alive = true
	s.stores[s.order[0]].evalRaw = 5.0
	s.stores[s.order[1]].alive = false
	s.stores[s.order[2]].alive = false
	s.aliveCount = 1

	out := s.stepNormalize(nil)
	if len(out) != 1 {
		t.Fatalf("1件の EvaluationUpdate のはず: %d", len(out))
	}

	st := s.stores[s.order[0]]
	if st.evalNormalized != 1.0 {
		t.Fatalf("単独店の normalized=1.0 のはず: %v", st.evalNormalized)
	}
	if st.rank != 1 {
		t.Fatalf("単独店の rank=1 のはず: %d", st.rank)
	}
}
```

---

### TestStepDistribute_BottomStoreStillGetsCustomers

最下位店（evalNormalized=0）にも客が来ることを確認する（§2.5 の死のスパイラル回帰テスト）。

```go
func TestStepDistribute_BottomStoreStillGetsCustomers(t *testing.T) {
	s := newTestSession(3)
	s.Start()
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 5, WeightFloor: 0.25}

	bottom := s.order[0]
	s.stores[bottom].evalNormalized = 0.0 // 最下位
	s.stores[s.order[1]].evalNormalized = 0.5
	s.stores[s.order[2]].evalNormalized = 1.0

	// 十分な回数まわして最下位店にも客が来ることを確認
	got := 0
	for i := 0; i < 200 && got == 0; i++ {
		s.stepDistribute(nil)
		got = len(s.storeQueues[bottom])
	}
	if got == 0 {
		t.Fatal("WeightFloor があるので最下位店にも客が来るはず（死のスパイラル回帰）")
	}
}
```

### TestStepDistribute_ZeroFloorReproducesSpec

`WeightFloor = 0` で設計文書どおり（最下位店の重み0）に戻ることを確認する。

```go
func TestStepDistribute_ZeroFloorReproducesSpec(t *testing.T) {
	s := newTestSession(3)
	s.Start()
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 5, WeightFloor: 0}

	bottom := s.order[0]
	s.stores[bottom].evalNormalized = 0.0
	s.stores[s.order[1]].evalNormalized = 0.5
	s.stores[s.order[2]].evalNormalized = 1.0

	for i := 0; i < 100; i++ {
		s.stepDistribute(nil)
	}
	if len(s.storeQueues[bottom]) != 0 {
		t.Fatal("WeightFloor=0 なら最下位店の重みは0で客は来ないはず")
	}
}
```

---

## 5. ローカル確認

```bash
# テスト実行
cd /path/to/Takoda99-Server
go test ./internal/game/ -run "TestStepDistribute|TestStepNormalize" -v

# 全テスト通過確認（既存テストの回帰がないこと）
go test ./internal/game/ -v

# vet + 静的解析
go vet ./internal/game/
```

---

## 6. 完了条件

- [ ] `DistributionParams` が `GameParameters` に追加され、`DefaultParameters()` にデフォルト値がある
- [ ] `stepDistribute` が重み付き抽選で客を分配し `CustomerArrived` を配信する
- [ ] 全店 evalNormalized=0 の初期状態で均等分配にフォールバックする
- [ ] Claimer が Early フェーズでは分配されない（Mid 以降は分配される）
- [ ] 行列長が `QueueRefillThreshold` に達した店は分配対象から除外される
- [ ] `stepNormalize` が生存店の評価をパーセンタイル化して rank を確定する
- [ ] 生存1店のとき `evalNormalized=1.0`, `rank=1` になる
- [ ] `EvaluationUpdate` が全生存店に配信される
- [ ] テスト7件が通る: EvenInitial / WeightedByEval / QueueLengthSuppression / ClaimerBlockedInEarly / EmptyRestPool / ThreeStores / SingleStore
- [ ] 既存テスト（session_test.go）に回帰がない
