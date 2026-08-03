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

### checkFinish（Plan-01 の骨組み状態）

```go
func (s *Session) checkFinish(out []Outbound) []Outbound {
	if len(s.order) > 1 && s.aliveCount <= 1 {
		s.state = Finished
	}
	return out
}
```

`Finished` に遷移するだけで **MatchEnd を配信しない**。順位も確定しない。

> Plan-01 未適用の現行コードには `MatchTimeLimitMs` による時間切れ判定が残っているが、
> 制限時間は廃止済み（proto v0.3.0 / #33）なので Plan-01 で除去される。

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

### Step 1: storeState のフィールド確認（追加不要）

`finalRank` / `elimination` は **Plan-01 の骨組みで追加済み**。存在を確認するだけ。

```go
type storeState struct {
	// ...既存...
	alive       bool
	finalRank   int    // 最終順位。0=未確定（生存中）。1=優勝
	elimination string // "SelfCollapse" / "Cull" / ""（優勝者・未脱落）
}
```

書き込みは Plan-02（自滅）・Plan-04（下位淘汰）・本プラン（優勝者）の3箇所。

### Step 2: 脱落時の finalRank 付与（Plan-02/04 で実装済み・確認のみ）

脱落時の書き込みは Plan-02 / Plan-04 の実装に含まれている。本プランに入る前に
両方が済んでいることを確認する:

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

**確認方法**: `grep -n "st.finalRank = " internal/game/session.go` で2箇所ヒットすること。
0箇所なら Plan-02/04 が未完了。ここが埋まっていないと `Results()` が全店 rank=0 を返す。

### Step 3: 同時脱落のタイブレーク（Plan-04 に実装済み・確認のみ）

同一 tick 内で複数店が脱落する場合の順位は、Plan-04 の `sortCulledForRank` が決める。
設計文書のタイブレーク規則（**信用ライフ残 → 正規化評価**、試合進行仕様 §10）に従い、
弱い店から順に脱落処理することで弱い店ほど低順位（大きい finalRank）になる。

本プランで追加実装するものはない。`sortCulledForRank` が存在することだけ確認する。

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

	// 最後の生存者に rank=1 を付与。
	// alive は true のまま残す（優勝者は「脱落していない」）。false にすると
	// summaries()/Snapshot() が優勝者を脱落表示にしてしまう。
	// 走査は map ではなく s.order（決定的な順序）で行う。
	for _, pid := range s.order {
		if st := s.stores[pid]; st.alive {
			st.finalRank = 1
			st.elimination = "" // 優勝者は脱落理由なし
			break
		}
	}

	// aliveCount==0（全員同時脱落）の場合は上のループが空振りし、
	// 全店とも脱落処理で finalRank が付与済みなのでそのまま配信に進む。

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

### Step 6: 制限時間の無効化（Plan-01 で対応済み・確認のみ）

`SessionParams.MatchTimeLimitMs` の既定値は Plan-01 で `0` になっており、
checkFinish の時間切れ判定も Plan-01 の骨組みで除去済み。ここでの追加作業はない。

```bash
grep -n "MatchTimeLimitMs" internal/game/params.go   # 既定値が 0 であること
grep -n "MatchTimeLimitMs" internal/game/session.go  # checkFinish で参照していないこと
```

なお `publicParams()` は MatchTimeLimitMs をクライアントへ送り続ける（0 が送られる）。
proto の公開サブセットからフィールドを消すのは Proto 変更（要承認）なのでここでは触らない。

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

あわせて、room/app 層が試合の状態を読むための getter も**ここでまとめて**追加する
（Plan-08 の結果永続化と Plan-12 のログが使う。**各プランで重複定義しないこと**）:

```go
// Id は試合IDを返す。
func (s *Session) Id() proto.MatchId { return s.id }

// AliveCount は現在の生存店数を返す。
func (s *Session) AliveCount() int { return s.aliveCount }

// ElapsedMs は試合経過時間（ms）を返す。
func (s *Session) ElapsedMs() int64 { return s.elapsedMs }
```

---

## 3. 実装するコード（session.go への追加・変更の全体）

### storeState（Plan-01 で定義済み・変更不要）

```go
	finalRank   int    // 最終順位。0=未確定（生存中）。1=優勝
	elimination string // "SelfCollapse" / "Cull" / ""（優勝者・未脱落）
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

	// 優勝者は alive=true のまま（脱落していない）。順序は s.order で決定的に。
	for _, pid := range s.order {
		if st := s.stores[pid]; st.alive {
			st.finalRank = 1
			st.elimination = ""
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

### テストヘルパー（**新規定義しない**）

`internal/game/session_test.go` には既に以下が定義されている。**同名で再定義すると
コンパイルエラー（duplicate declaration）になる**ので、そのまま使うこと。

```go
// 既存（session_test.go:12-16）— WordSource のテスト実装
type fakeWords struct{}
func (fakeWords) Next(int, *rand.Rand) Word { return Word{Text: "たこ", KeystrokeCount: 4} }

// 既存（session_test.go:19-26）— n店の Session を作る
// 注1: 店IDは "s-1", "s-2", ... （"p-N" ではない）
// 注2: Start() は呼ばれない。試合を進める場合はテスト側で s.Start() する
func newTestSession(n int) *Session

// 既存（session_test.go:305-314）— 指定属性の客を行列に直接配置
func placeAssigned(s *Session, cid proto.CustomerId, store PlayerId,
                   attr proto.CustomerAttribute, orderCount, keystrokes int)
```

以下のテストはこの既存ヘルパ前提で書く。`checkFinish` は `s.state` を見ないため
`Start()` を呼ばずに状態を直接組み立ててよい（ただし `state` は明示的に `Running` にする）。

### TestCheckFinish_LastOneStanding

```go
func TestCheckFinish_LastOneStanding(t *testing.T) {
	sess := newTestSession(2)
	p1 := PlayerId("s-1")
	p2 := PlayerId("s-2")

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
			t.Errorf("s-1 rank=%d, want 1", me.FinalRank)
		}
		if pid == p2 && me.FinalRank != 2 {
			t.Errorf("s-2 rank=%d, want 2", me.FinalRank)
		}
	}
}
```

### TestCheckFinish_ThreePlayerRankOrder

```go
func TestCheckFinish_ThreePlayerRankOrder(t *testing.T) {
	sess := newTestSession(3)

	// p1 が最初に脱落 → rank=3
	sess.stores[PlayerId("s-1")].alive = false
	sess.stores[PlayerId("s-1")].finalRank = 3
	sess.stores[PlayerId("s-1")].elimination = "SelfCollapse"

	// p3 が次に脱落 → rank=2
	sess.stores[PlayerId("s-3")].alive = false
	sess.stores[PlayerId("s-3")].finalRank = 2
	sess.stores[PlayerId("s-3")].elimination = "Cull"

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
	if ranks[PlayerId("s-1")] != 3 { t.Errorf("p-1 rank=%d want 3", ranks[PlayerId("s-1")]) }
	if ranks[PlayerId("s-2")] != 1 { t.Errorf("p-2 rank=%d want 1", ranks[PlayerId("s-2")]) }
	if ranks[PlayerId("s-3")] != 2 { t.Errorf("p-3 rank=%d want 2", ranks[PlayerId("s-3")]) }
}
```

### TestCheckFinish_StatsCalculation

```go
func TestCheckFinish_StatsCalculation(t *testing.T) {
	sess := newTestSession(2)
	p1 := PlayerId("s-1")

	// p1 に提供実績を設定
	sess.stores[p1].served = servedStats{
		count:       3,
		accuracySum: 0.9 + 0.8 + 0.7, // = 2.4
		elapsedSum:  3000 + 4000 + 5000, // = 12000
	}

	// p2 を脱落させる
	sess.stores[PlayerId("s-2")].alive = false
	sess.stores[PlayerId("s-2")].finalRank = 2
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
	sess.stores[PlayerId("s-2")].alive = false
	sess.stores[PlayerId("s-2")].finalRank = 2
	sess.aliveCount = 1

	out := sess.checkFinish(nil)
	if len(out) != 2 {
		t.Fatalf("out=%d want 2", len(out))
	}

	for _, o := range out {
		if o.To.PlayerId != PlayerId("s-2") {
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
- [ ] `Results()` / `Id()` / `AliveCount()` / `ElapsedMs()` が公開され、room/app 層から呼べる
- [ ] 制限時間の条件が削除されている（MatchTimeLimitMs=0 がデフォルト）
- [ ] `go test ./internal/game/ -v -run "TestCheckFinish"` が全件パス
- [ ] `go build ./...` が通る
