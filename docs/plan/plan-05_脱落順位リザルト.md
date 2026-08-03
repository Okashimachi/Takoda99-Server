# Plan-05: 脱落・順位確定・リザルト（tako-I）

> **目的**: 試合終了条件を実装し、脱落順に基づく最終順位の確定と MatchEnd 配信を行う。
> **対応issue**: #9
> **依存**: Plan-04（下位淘汰で脱落が発生する）
> **参照**: 試合進行仕様 §10, 全体仕様 §8.3, 用語集 §9

---

## 0. 前提知識

### 読むべきファイル

| ファイル | 内容 |
|---|---|
| `internal/game/session.go` | Session 構造体・Tick ループ・checkFinish スタブ |
| `internal/game/params.go` | GameParameters / SessionParams |
| `internal/proto/messages.go` | MatchEnd / StoreEliminated / MatchStats の再輸出 |
| `Takoda99-Proto/proto/messages.go` | 正典メッセージ定義 |
| `internal/room/room.go` | dispatch / envelopeOf（Room 層の配信機構） |

### 関連概念

- **脱落順位**: 先に脱落した店ほど大きい rank 値（= 下位）。最後の生存者が rank=1。
- **2つの脱落経路**: SelfCollapse（Plan-02, creditLife=0）と Cull（Plan-04, storm 強制淘汰）。
- **制限時間廃止**: proto v0.3.0 (#33)。storm が決着を保証するので時間切れは不要。

---

## 1. 現状のコード

### checkFinish（session.go:308–316）

```go
func (s *Session) checkFinish(out []Outbound) []Outbound {
	limit := s.params.Session.MatchTimeLimitMs
	timeUp := limit > 0 && s.elapsedMs >= int64(limit)
	lastAlive := len(s.order) > 1 && s.aliveCount <= 1
	if timeUp || lastAlive {
		s.state = Finished
	}
	return out
}
```

現在は `Finished` に遷移するだけで **MatchEnd を配信しない**。

### Proto 型（Takoda99-Proto/proto/messages.go）

```go
type MatchEnd struct {
	FinalRank int        `json:"finalRank"`
	Stats     MatchStats `json:"stats"`
}

type MatchStats struct {
	ServedCount  int     `json:"servedCount"`
	AvgAccuracy  float64 `json:"avgAccuracy"`
	AvgElapsedMs int     `json:"avgElapsedMs"`
}

type StoreEliminated struct {
	StoreId   StoreId           `json:"storeId"`
	Reason    EliminationReason `json:"reason"`
	FinalRank int               `json:"finalRank"`
}
```

### storeState（session.go:59–69）

```go
type storeState struct {
	id             PlayerId
	name           string
	creditLife     int
	evalRaw        float64
	buzzBonus      float64
	evalNormalized float64
	rank           int
	served         servedStats
	alive          bool
}
```

### servedStats（session.go:52–56）

```go
type servedStats struct {
	count       int
	accuracySum float64
	elapsedSum  int64
}
```

### Outbound ヘルパー（session.go:38）

```go
func to(pid PlayerId, msg any) Outbound { return Outbound{To: Recipient{PlayerId: pid}, Msg: msg} }
```

Broadcast 用は `Recipient{Broadcast: true}` を使う。

### envelopeOf（room.go:156–191）

MatchEnd のケースは **既に登録済み**:
```go
case proto.MatchEnd:
	typ = proto.TypeMatchEnd
```

---

## 2. 実装手順

### Step 1: storeState に finalRank フィールドを追加

`internal/game/session.go` の storeState に追加:

```go
type storeState struct {
	id             PlayerId
	name           string
	creditLife     int
	evalRaw        float64
	buzzBonus      float64
	evalNormalized float64
	rank           int
	served         servedStats
	alive          bool
	finalRank      int    // 脱落時に確定（0=未確定=まだ生存中）
	elimination    string // "SelfCollapse" / "Cull" / ""（優勝者）
}
```

### Step 2: 脱落時の finalRank 付与（Plan-02/04 との接続）

Plan-02 の selfCollapse と Plan-04 の stepStorm で脱落処理をするとき、以下を行う:

```go
// selfCollapse 内（Plan-02）
st.alive = false
s.aliveCount--
st.finalRank = s.aliveCount + 1
st.elimination = string(proto.ElimSelfCollapse)

// stepStorm の cull 内（Plan-04）
st.alive = false
s.aliveCount--
st.finalRank = s.aliveCount + 1
st.elimination = string(proto.ElimCull)
```

> **注意**: Plan-02/04 で既にこの処理が入っている場合は、finalRank/elimination フィールドへの書き込みを追加するだけ。

### Step 3: 同時脱落のタイブレーク

同一 tick 内で複数店が脱落する場合、`aliveCount` の減算順で rank が決まる。
より弱い店（creditLife が少ない / evalNormalized が低い）を先に脱落処理することで、弱い店がより下位の rank を取る。

Plan-04 の stepStorm では cull 対象を sort して処理する:

```go
// stepStorm 内のカル処理の前に、対象を弱い順にソート
sort.SliceStable(cullTargets, func(i, j int) bool {
	a, b := cullTargets[i], cullTargets[j]
	if a.creditLife != b.creditLife {
		return a.creditLife < b.creditLife // 信用が少ない = 弱い = 先に処理 = 下位
	}
	return a.evalNormalized < b.evalNormalized
})
```

### Step 4: checkFinish を実装

`internal/game/session.go` の `checkFinish` を以下に置き換え:

```go
func (s *Session) checkFinish(out []Outbound) []Outbound {
	// solo モード（1店だけの試合）は終了させない
	if len(s.order) <= 1 {
		return out
	}

	// 終了条件: 生存が1以下
	if s.aliveCount > 1 {
		return out
	}

	s.state = Finished

	// 最後の生存者に rank=1 を付与
	for _, st := range s.stores {
		if st.alive {
			st.finalRank = 1
			st.alive = false
			break
		}
	}

	// 全員が同時脱落した場合（aliveCount==0 で Finished）
	// → 全員に finalRank が既に付いている（脱落処理で付与済み）

	// 全参加者に MatchEnd を配信
	for _, pid := range s.order {
		st := s.stores[pid]
		stats := s.buildMatchStats(st)
		out = append(out, to(pid, proto.MatchEnd{
			FinalRank: st.finalRank,
			Stats:     stats,
		}))
	}

	return out
}
```

### Step 5: buildMatchStats ヘルパーを追加

```go
// buildMatchStats は1店の servedStats から MatchStats を組み立てる。
func (s *Session) buildMatchStats(st *storeState) proto.MatchStats {
	if st.served.count == 0 {
		return proto.MatchStats{}
	}
	return proto.MatchStats{
		ServedCount:  st.served.count,
		AvgAccuracy:  st.served.accuracySum / float64(st.served.count),
		AvgElapsedMs: int(st.served.elapsedSum / int64(st.served.count)),
	}
}
```

### Step 6: 制限時間の無効化

`SessionParams.MatchTimeLimitMs` を 0 にして制限時間を廃止する。
`DefaultParameters()` で:

```go
Session: SessionParams{
	TickIntervalMs:    150,
	PublishIntervalMs: 250,
	MatchTimeLimitMs:  0, // 制限時間廃止。storm が決着を保証する
},
```

checkFinish から timeUp 判定を削除済み（Step 4 で差し替え）。

### Step 7: Session.Results() を公開（room/app 向け）

試合終了後に room/app 層が結果を取得するための公開メソッド:

```go
// Results は試合終了後の全店結果を返す。Finished 状態でのみ有効。
func (s *Session) Results() []StoreResult {
	results := make([]StoreResult, 0, len(s.order))
	for _, pid := range s.order {
		st := s.stores[pid]
		results = append(results, StoreResult{
			StoreId:     st.id,
			DisplayName: st.name,
			FinalRank:   st.finalRank,
			Elimination: st.elimination,
			CreditLife:  st.creditLife,
			EvalRaw:     st.evalRaw,
			Stats:       s.buildMatchStats(st),
		})
	}
	return results
}

// StoreResult は外部公開用の1店結果。
type StoreResult struct {
	StoreId     PlayerId
	DisplayName string
	FinalRank   int
	Elimination string // "SelfCollapse" / "Cull" / ""
	CreditLife  int
	EvalRaw     float64
	Stats       proto.MatchStats
}
```

---

## 3. 実装するコード（session.go への追加・変更の全体）

### storeState 変更（既存フィールドに追加）

```go
type storeState struct {
	id             PlayerId
	name           string
	creditLife     int
	evalRaw        float64
	buzzBonus      float64
	evalNormalized float64
	rank           int
	served         servedStats
	alive          bool
	finalRank      int    // 脱落時に確定。0=未確定（生存中）
	elimination    string // "SelfCollapse" / "Cull" / ""（優勝者）
}
```

### checkFinish（既存を置換）

```go
func (s *Session) checkFinish(out []Outbound) []Outbound {
	if len(s.order) <= 1 {
		return out
	}
	if s.aliveCount > 1 {
		return out
	}

	s.state = Finished

	for _, st := range s.stores {
		if st.alive {
			st.finalRank = 1
			st.alive = false
			break
		}
	}

	for _, pid := range s.order {
		st := s.stores[pid]
		out = append(out, to(pid, proto.MatchEnd{
			FinalRank: st.finalRank,
			Stats:     s.buildMatchStats(st),
		}))
	}
	return out
}
```

### 新規ヘルパー

```go
func (s *Session) buildMatchStats(st *storeState) proto.MatchStats {
	if st.served.count == 0 {
		return proto.MatchStats{}
	}
	return proto.MatchStats{
		ServedCount:  st.served.count,
		AvgAccuracy:  st.served.accuracySum / float64(st.served.count),
		AvgElapsedMs: int(st.served.elapsedSum / int64(st.served.count)),
	}
}
```

### StoreResult + Results()

```go
type StoreResult struct {
	StoreId     PlayerId
	DisplayName string
	FinalRank   int
	Elimination string
	CreditLife  int
	EvalRaw     float64
	Stats       proto.MatchStats
}

func (s *Session) Results() []StoreResult {
	results := make([]StoreResult, 0, len(s.order))
	for _, pid := range s.order {
		st := s.stores[pid]
		results = append(results, StoreResult{
			StoreId:     st.id,
			DisplayName: st.name,
			FinalRank:   st.finalRank,
			Elimination: st.elimination,
			CreditLife:  st.creditLife,
			EvalRaw:     st.evalRaw,
			Stats:       s.buildMatchStats(st),
		})
	}
	return results
}
```

---

## 4. テスト

ファイル: `internal/game/session_test.go` に追加。

### テストヘルパー（既存または追加）

```go
// newTestSession は指定人数の Session を作り、Start 済みで返す。
func newTestSession(n int) *Session {
	params := DefaultParameters()
	params.Customer.Total = 10 // テスト用に少数
	params.Credit.InitialLife = 3

	inits := make([]PlayerInit, n)
	for i := range inits {
		inits[i] = PlayerInit{
			Id:          PlayerId(fmt.Sprintf("p-%d", i+1)),
			DisplayName: fmt.Sprintf("Store%d", i+1),
		}
	}
	ws := &stubWordSource{}
	sess := NewSession("test-match", params, ws, rand.New(rand.NewSource(42)), inits)
	sess.Start()
	return sess
}

type stubWordSource struct{}

func (s *stubWordSource) Next(level int, rng *rand.Rand) Word {
	return Word{Text: "テスト", KeystrokeCount: 4}
}
```

### TestCheckFinish_LastOneStanding

```go
func TestCheckFinish_LastOneStanding(t *testing.T) {
	sess := newTestSession(2)
	p1 := PlayerId("p-1")
	p2 := PlayerId("p-2")

	// p2 を脱落させる（creditLife を 0 にして selfCollapse 相当）
	st2 := sess.stores[p2]
	st2.alive = false
	st2.finalRank = 2
	st2.elimination = "SelfCollapse"
	sess.aliveCount = 1

	// checkFinish を呼ぶ
	out := sess.checkFinish(nil)

	// Finished に遷移
	if sess.state != Finished {
		t.Fatalf("state=%v, want Finished", sess.state)
	}

	// 2人分の MatchEnd が返る
	if len(out) != 2 {
		t.Fatalf("outbound count=%d, want 2", len(out))
	}

	// 各店に正しい FinalRank が付いている
	for _, o := range out {
		me, ok := o.Msg.(proto.MatchEnd)
		if !ok {
			t.Fatalf("unexpected msg type: %T", o.Msg)
		}
		pid := o.To.PlayerId
		if pid == p1 && me.FinalRank != 1 {
			t.Errorf("p1 rank=%d, want 1", me.FinalRank)
		}
		if pid == p2 && me.FinalRank != 2 {
			t.Errorf("p2 rank=%d, want 2", me.FinalRank)
		}
	}
}
```

### TestCheckFinish_ThreePlayerRankOrder

```go
func TestCheckFinish_ThreePlayerRankOrder(t *testing.T) {
	sess := newTestSession(3)

	// p1 が最初に脱落 → rank=3
	sess.stores[PlayerId("p-1")].alive = false
	sess.stores[PlayerId("p-1")].finalRank = 3
	sess.stores[PlayerId("p-1")].elimination = "SelfCollapse"

	// p3 が次に脱落 → rank=2
	sess.stores[PlayerId("p-3")].alive = false
	sess.stores[PlayerId("p-3")].finalRank = 2
	sess.stores[PlayerId("p-3")].elimination = "Cull"

	sess.aliveCount = 1

	out := sess.checkFinish(nil)

	if sess.state != Finished {
		t.Fatal("should be Finished")
	}

	ranks := map[PlayerId]int{}
	for _, o := range out {
		me := o.Msg.(proto.MatchEnd)
		ranks[o.To.PlayerId] = me.FinalRank
	}
	if ranks[PlayerId("p-1")] != 3 { t.Errorf("p-1 rank=%d want 3", ranks[PlayerId("p-1")]) }
	if ranks[PlayerId("p-2")] != 1 { t.Errorf("p-2 rank=%d want 1", ranks[PlayerId("p-2")]) }
	if ranks[PlayerId("p-3")] != 2 { t.Errorf("p-3 rank=%d want 2", ranks[PlayerId("p-3")]) }
}
```

### TestCheckFinish_StatsCalculation

```go
func TestCheckFinish_StatsCalculation(t *testing.T) {
	sess := newTestSession(2)
	p1 := PlayerId("p-1")

	// p1 に提供実績を設定
	sess.stores[p1].served = servedStats{
		count:       3,
		accuracySum: 0.9 + 0.8 + 0.7, // = 2.4
		elapsedSum:  3000 + 4000 + 5000, // = 12000
	}

	// p2 を脱落させる
	sess.stores[PlayerId("p-2")].alive = false
	sess.stores[PlayerId("p-2")].finalRank = 2
	sess.aliveCount = 1

	out := sess.checkFinish(nil)

	for _, o := range out {
		if o.To.PlayerId != p1 {
			continue
		}
		me := o.Msg.(proto.MatchEnd)
		if me.Stats.ServedCount != 3 {
			t.Errorf("ServedCount=%d want 3", me.Stats.ServedCount)
		}
		wantAcc := 2.4 / 3.0
		if diff := me.Stats.AvgAccuracy - wantAcc; diff > 0.001 || diff < -0.001 {
			t.Errorf("AvgAccuracy=%.4f want %.4f", me.Stats.AvgAccuracy, wantAcc)
		}
		if me.Stats.AvgElapsedMs != 4000 {
			t.Errorf("AvgElapsedMs=%d want 4000", me.Stats.AvgElapsedMs)
		}
	}
}
```

### TestCheckFinish_ZeroServed

```go
func TestCheckFinish_ZeroServed(t *testing.T) {
	sess := newTestSession(2)

	// served は初期値（count=0）のまま
	sess.stores[PlayerId("p-2")].alive = false
	sess.stores[PlayerId("p-2")].finalRank = 2
	sess.aliveCount = 1

	out := sess.checkFinish(nil)
	if len(out) != 2 {
		t.Fatalf("out=%d want 2", len(out))
	}

	for _, o := range out {
		if o.To.PlayerId != PlayerId("p-2") {
			continue
		}
		me := o.Msg.(proto.MatchEnd)
		if me.Stats.ServedCount != 0 || me.Stats.AvgAccuracy != 0 || me.Stats.AvgElapsedMs != 0 {
			t.Errorf("zero-served stats should be all zeros, got %+v", me.Stats)
		}
	}
}
```

### TestCheckFinish_SoloDoesNotEnd

```go
func TestCheckFinish_SoloDoesNotEnd(t *testing.T) {
	sess := newTestSession(1)
	// 1店だけ → checkFinish は何もしない
	out := sess.checkFinish(nil)
	if sess.state == Finished {
		t.Fatal("solo session should not finish")
	}
	if len(out) != 0 {
		t.Fatalf("solo should produce no outbound, got %d", len(out))
	}
}
```

### TestCheckFinish_AllEliminatedSimultaneously

```go
func TestCheckFinish_AllEliminatedSimultaneously(t *testing.T) {
	sess := newTestSession(2)

	// 両方同時に脱落（storm で起こりうる）
	for _, pid := range sess.order {
		st := sess.stores[pid]
		st.alive = false
		st.finalRank = 1 // 同着
	}
	sess.aliveCount = 0

	out := sess.checkFinish(nil)
	if sess.state != Finished {
		t.Fatal("should be Finished when aliveCount=0")
	}
	if len(out) != 2 {
		t.Fatalf("out=%d want 2", len(out))
	}
}
```

---

## 5. ローカル確認

```bash
# ビルド確認
go build ./...

# Plan-05 関連のテストを実行
go test ./internal/game/ -v -run "TestCheckFinish"

# 全テスト
go test ./...

# vet
go vet ./...

# solo モードで起動して MatchEnd が届くか確認（Bot 同士で試合が終わるまで放置）
go run ./cmd/server --mode solo --bots 2
```

---

## 6. 完了条件

- [ ] `storeState` に `finalRank` / `elimination` フィールドがある
- [ ] 脱落時に `finalRank = aliveCount + 1` が付与される（Plan-02/04 側で実施、ここで確認）
- [ ] 生存1で試合が `Finished` に遷移する
- [ ] solo モード（1店）では終了しない
- [ ] 全参加者（生存者＋脱落者）に `MatchEnd` が配信される
- [ ] `MatchEnd.FinalRank` が脱落順で正しく付く（先に脱落=大きい数字=下位）
- [ ] 優勝者は `FinalRank=1`
- [ ] `MatchStats` が `servedStats` から正しく集計される（count/avgAccuracy/avgElapsedMs）
- [ ] count=0（1度も提供せず脱落）の場合は Stats がゼロ値で panic しない
- [ ] `Results()` メソッドが公開され、room/app 層から呼べる
- [ ] 制限時間の条件が削除されている（MatchTimeLimitMs=0 がデフォルト）
- [ ] `go test ./internal/game/ -v -run "TestCheckFinish"` が全件パス
- [ ] `go build ./...` が通る
