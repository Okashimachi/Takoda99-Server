# Plan-04: フェーズ・火力・下位淘汰（tako-H）

> **目的**: 試合の緊張カーブを実装する。フェーズ移行（序盤→中盤→終盤）、火力（難易度）の加算上昇、下位淘汰(storm)による強制脱落。
> **対応issue**: #8
> **依存**: Plan-03（正規化評価＝下位淘汰の判定基準）
> **参照**: 試合進行仕様 §9, 全体仕様 §7-8, 用語集 §8-9

---

## 1. 前提知識

### 試合の緊張設計

たこ焼き経営バトロワでは、試合を3フェーズ（Early/Mid/Late）で区切り、後半ほど厳しくする。
フェーズが進むと以下が変わる:

| フェーズ | Claimer分配 | 我慢ゲージ | 火力（お題難度） | 下位淘汰 |
|---|---|---|---|---|
| Early | 除外 | 通常 | base のみ | なし（猶予期間） |
| Mid | 解禁 | 通常 | 緩やか上昇 | 開始 |
| Late | 解禁 | 短縮(Plan-02 LateMul) | 急上昇 | 加速 |

- **フェーズ移行**: 生存数 OR 経過時間、どちらか先に到達した方で一方向移行（Early→Mid→Late）
- **火力**: 店が減るほど＋フェーズが進むほど上がる加算式。`wordLevel()` 経由でお題難度に反映
- **下位淘汰(storm)**: 一定tick間隔で正規化評価の下位%を強制脱落。予告→実行の2段階

### Tick順序での位置

`Tick()` 内の呼び出し順序:

```
1. stepPhase      ← 今回（フェーズ判定）
2. stepDistribute
3. stepPatience
4. stepEvaluate
5. stepNormalize
6. stepHeat       ← 今回（火力更新）
7. stepStorm      ← 今回（下位淘汰）
8. checkFinish
```

stepPhase が最初、stepHeat/stepStorm が stepNormalize の後にある点が重要:
- stepPhase → 当tickのフェーズを確定 → stepDistribute が Claimer 除外判定に使う
- stepNormalize → evalNormalized を確定 → stepStorm が淘汰対象の判定に使う

---

## 2. 現状のコード

### Session 構造体（session.go）

```go
type Session struct {
    // ...既存フィールド...
    state      SessionState
    phase      proto.Phase
    elapsedMs  int64
    tick       int
    aliveCount int
    // ※ heatLevel フィールドは未追加（wordLevel() は return 0 のまま）
    // ※ storm 用のカウンタ/フラグも未追加
}
```

### スタブ関数（session.go）

```go
func (s *Session) stepPhase(out []Outbound) []Outbound { return out }
func (s *Session) stepHeat(out []Outbound) []Outbound  { return out }
func (s *Session) stepStorm(out []Outbound) []Outbound { return out }
func (s *Session) wordLevel() int                      { return 0 }
```

### Proto型（proto/messages.go）

```go
type PhaseChange struct {
    Phase Phase `json:"phase"`
}

type DifficultyUpdate struct {
    HeatLevel int `json:"heatLevel"`
}

type ForcedEliminationWarning struct {
    UntilTick    int     `json:"untilTick"`
    ThresholdPct float64 `json:"thresholdPct"`
}

type StoreEliminated struct {
    StoreId   StoreId           `json:"storeId"`
    Reason    EliminationReason `json:"reason"`
    FinalRank int               `json:"finalRank"`
}
// ElimCull EliminationReason = "Cull"
```

### broadcastMsg ヘルパ

session.go には `to(pid, msg)` で1店宛の Outbound を作るヘルパがあるが、全店ブロードキャスト用のヘルパがない。`Recipient` には `Broadcast bool` フィールドがあるが、明示的に全 order をループするヘルパを用意する（room 側の Broadcast 対応と独立して動くため安全）。

### GameParameters（params.go）

```go
type GameParameters struct {
    // ...既存...
    // PhaseParams, HeatParams, StormParams は未追加
}
```

### releaseToRest（session.go）

```go
func (s *Session) releaseToRest(cid proto.CustomerId) { ... }
```
客を割り当て先の行列から除去し、restPool へ戻す。脱落時の行列全客回収に使用する。

---

## 3. 実装手順

### 3-a. Session 構造体にフィールドを追加（session.go）

```go
type Session struct {
    // ...既存フィールド...
    state      SessionState
    phase      proto.Phase
    elapsedMs  int64
    tick       int
    aliveCount int

    // tako-H: フェーズ・火力・storm
    heatLevel        int  // 現在の火力レベル（stepHeat で更新）
    stormTickCounter int  // storm 間隔カウンタ（毎tick++、IntervalTicks でリセット）
    stormWarnSent    bool // 今サイクルの警告済みフラグ（リセットは実行後）
}
```

NewSession 内での初期化は不要（ゼロ値で正しい: heatLevel=0, counter=0, warnSent=false）。

### 3-b. PhaseParams を追加（params.go）

```go
// PhaseParams: フェーズ移行の閾値（tako-H）。
// 生存数と経過時間の両軸、どちらか先で一方向移行。
type PhaseParams struct {
    MidAliveThreshold  int `json:"midAliveThreshold"`  // Early→Mid の生存数閾値（以下で移行）
    LateAliveThreshold int `json:"lateAliveThreshold"` // Mid→Late の生存数閾値
    MidTimeMs          int `json:"midTimeMs"`           // Early→Mid の経過時間閾値（ms）
    LateTimeMs         int `json:"lateTimeMs"`          // Mid→Late の経過時間閾値（ms）
}
```

GameParameters に追加:
```go
type GameParameters struct {
    // ...既存...
    Phase PhaseParams `json:"phase"` // tako-H
}
```

DefaultParameters に初期仮値を追加:
```go
Phase: PhaseParams{
    MidAliveThreshold:  70,    // 99人→残り70人以下で中盤
    LateAliveThreshold: 30,    // 残り30人以下で終盤
    MidTimeMs:          30000, // 30秒経過でも中盤
    LateTimeMs:         90000, // 90秒経過でも終盤
},
```

### 3-c. HeatParams を追加（params.go）

```go
// HeatParams: 火力（全体難易度）の加算式パラメータ（tako-H）。
// heatLevel = Base + int(PerAliveDrop * float64(maxStores - aliveCount)) + PerPhase[phase]
// PerPhase は map だと GameParameters の == 比較ができなくなるため、フェーズごとに個別フィールドで持つ。
type HeatParams struct {
    Base         int     `json:"base"`         // 火力基礎値
    PerAliveDrop float64 `json:"perAliveDrop"` // 生存1人減るごとの火力加算量
    PhaseEarly   int     `json:"phaseEarly"`   // Early フェーズの火力加算
    PhaseMid     int     `json:"phaseMid"`     // Mid フェーズの火力加算
    PhaseLate    int     `json:"phaseLate"`     // Late フェーズの火力加算
}
```

**注意**: 既存の params.go のコメントに「すべて全項目 comparable に保つ（== 比較維持）」とある。`map[string]int` を使うと `GameParameters` 全体の `==` 比較が壊れる。そのため `PerPhase` は map ではなく個別フィールド（`PhaseEarly`/`PhaseMid`/`PhaseLate`）にする。

GameParameters に追加:
```go
Heat HeatParams `json:"heat"` // tako-H
```

DefaultParameters に初期仮値:
```go
Heat: HeatParams{
    Base:         0,
    PerAliveDrop: 0.1,  // 生存が1人減るごとに 0.1 加算
    PhaseEarly:   0,
    PhaseMid:     3,
    PhaseLate:    8,
},
```

### 3-d. StormParams を追加（params.go）

```go
// StormParams: 下位淘汰(storm)のタイミングと閾値（tako-H）。
type StormParams struct {
    IntervalTicks int     `json:"intervalTicks"` // 実行間隔（tick数、例:40≒6秒）
    WarnTicks     int     `json:"warnTicks"`     // 実行の何tick前に予告するか
    ThresholdPct  float64 `json:"thresholdPct"`  // 下位何%を強制脱落（0.0〜1.0）
}
```

GameParameters に追加:
```go
Storm StormParams `json:"storm"` // tako-H
```

DefaultParameters に初期仮値:
```go
Storm: StormParams{
    IntervalTicks: 40,   // 40tick≒6秒（TickIntervalMs=150 のとき）
    WarnTicks:     10,   // 10tick≒1.5秒前に予告
    ThresholdPct:  0.10, // 下位10%を強制脱落
},
```

### 3-e. broadcastMsg ヘルパを追加（session.go）

```go
// broadcastMsg は全参加者（生存＋脱落の観戦者）へ同じメッセージを配る。
// out(accumulator) へ append して返す。
func (s *Session) broadcastMsg(out []Outbound, msg any) []Outbound {
    for _, sid := range s.order {
        out = append(out, to(sid, msg))
    }
    return out
}
```

全 `s.order` を回す（alive に関わらず）。脱落した店も観戦者として受信する。

### 3-f. stepPhase の実装（session.go）

```go
// stepPhase は elapsedMs と aliveCount からフェーズ（Early/Mid/Late）を判定し、
// 変化時に PhaseChange を全店ブロードキャストする。一方向（Early→Mid→Late）。tako-H。
func (s *Session) stepPhase(out []Outbound) []Outbound {
    pp := s.params.Phase

    switch s.phase {
    case proto.PhaseEarly:
        if s.aliveCount <= pp.MidAliveThreshold || s.elapsedMs >= int64(pp.MidTimeMs) {
            s.phase = proto.PhaseMid
            out = s.broadcastMsg(out, proto.PhaseChange{Phase: proto.PhaseMid})
        }
    case proto.PhaseMid:
        if s.aliveCount <= pp.LateAliveThreshold || s.elapsedMs >= int64(pp.LateTimeMs) {
            s.phase = proto.PhaseLate
            out = s.broadcastMsg(out, proto.PhaseChange{Phase: proto.PhaseLate})
        }
    }
    // PhaseLate は最終フェーズ — 遷移なし。

    return out
}
```

**ポイント**:
- `switch` で現在フェーズを見て、次のフェーズへの条件だけを判定（一方向保証）
- 閾値は「以下」（`<=`）で判定。「生存70人以下」= aliveCount <= 70
- 時間閾値は `>=` で判定。elapsedMs は Tick 先頭で加算済み
- Early→Late の飛び越え: 例えば aliveCount が一気に 30 以下になった場合、1tick 目で Early→Mid、同 tick 内で Mid→Late とは**ならない**（switch で分岐しているため1tick1遷移）。次の tick で Mid→Late が発火する。これは意図的（クライアントに PhaseChange が2連続で来ると UX が壊れる）

### 3-g. stepHeat の実装（session.go）

```go
// stepHeat は全体火力(heatLevel)を更新し、変化時に DifficultyUpdate を全店ブロードキャストする。tako-H。
func (s *Session) stepHeat(out []Outbound) []Outbound {
    hp := s.params.Heat
    maxStores := len(s.order)

    // 加算式: base + perAliveDrop * (maxStores - aliveCount) + perPhase[phase]
    newHeat := hp.Base + int(hp.PerAliveDrop*float64(maxStores-s.aliveCount))
    switch s.phase {
    case proto.PhaseEarly:
        newHeat += hp.PhaseEarly
    case proto.PhaseMid:
        newHeat += hp.PhaseMid
    case proto.PhaseLate:
        newHeat += hp.PhaseLate
    }

    if newHeat != s.heatLevel {
        s.heatLevel = newHeat
        out = s.broadcastMsg(out, proto.DifficultyUpdate{HeatLevel: s.heatLevel})
    }
    return out
}
```

**ポイント**:
- 変化がなければ DifficultyUpdate を送らない（帯域節約）
- heatLevel は int（WordSource.Next の effectiveLevel 引数が int）
- 火力は単調増加ではない（理論上は aliveCount が増えれば下がるが、BR では aliveCount は減る一方なので実質単調増加）

### 3-h. wordLevel() の実装（session.go）

```go
// wordLevel はお題難度の実効レベル。火力(heatLevel) を返す。
func (s *Session) wordLevel() int { return s.heatLevel }
```

既存のスタブ `return 0` を置き換える。admitCustomer 内の `s.words.Next(s.wordLevel(), s.rng)` で使われ、火力が上がるほど難しい単語が出題される。

### 3-i. stepStorm の実装（session.go）

```go
// stepStorm は下位淘汰(storm)の予告・確定を行う。tako-H。
// stormTickCounter を毎tick進め、IntervalTicks に達したら生存店の下位 ThresholdPct% を強制脱落する。
// 実行の WarnTicks 前に ForcedEliminationWarning を全店へ配る。
func (s *Session) stepStorm(out []Outbound) []Outbound {
    sp := s.params.Storm
    if sp.IntervalTicks <= 0 {
        return out // storm 無効（solo/dev 用）
    }

    s.stormTickCounter++

    // ── 予告 ──
    // 実行まで残り WarnTicks のタイミング（＝ IntervalTicks - WarnTicks に達した瞬間）で1回だけ警告。
    warnAt := sp.IntervalTicks - sp.WarnTicks
    if warnAt < 1 {
        warnAt = 1 // WarnTicks >= IntervalTicks の異常設定でも最低1tickで警告
    }
    if s.stormTickCounter == warnAt && !s.stormWarnSent {
        s.stormWarnSent = true
        remaining := sp.IntervalTicks - s.stormTickCounter
        out = s.broadcastMsg(out, proto.ForcedEliminationWarning{
            UntilTick:    remaining,
            ThresholdPct: sp.ThresholdPct,
        })
    }

    // ── 実行 ──
    if s.stormTickCounter < sp.IntervalTicks {
        return out
    }

    // カウンタリセット（次サイクルへ）
    s.stormTickCounter = 0
    s.stormWarnSent = false

    // 生存2店以上でないと淘汰しない（最後の1店を storm で殺すと勝者不在）
    if s.aliveCount <= 1 {
        return out
    }

    out = s.executeCull(out)
    return out
}

// executeCull は下位淘汰の実行処理。生存店を evalNormalized でソートし、
// 下位 ThresholdPct% を強制脱落させる。
func (s *Session) executeCull(out []Outbound) []Outbound {
    sp := s.params.Storm

    // 生存店を収集
    alive := make([]*storeState, 0, s.aliveCount)
    for _, sid := range s.order {
        if s.stores[sid].alive {
            alive = append(alive, s.stores[sid])
        }
    }
    if len(alive) <= 1 {
        return out
    }

    // 淘汰対象数: 下位 ThresholdPct% を切り上げ（最低1店）
    cullCount := int(float64(len(alive))*sp.ThresholdPct + 0.999999)
    if cullCount < 1 {
        cullCount = 1
    }
    // 生存が全滅しないよう、最低1店は残す
    if cullCount >= len(alive) {
        cullCount = len(alive) - 1
    }

    // evalNormalized 昇順ソート（下位が先頭）。
    // 同値のタイブレーク: creditLife 昇順 → evalNormalized 昇順（弱い方が先＝先に脱落＝低順位）。
    sortStoresByWeakest(alive)

    // 先頭から cullCount 店を脱落させる。
    // 同時脱落の順位: 弱い方から順に脱落処理し、脱落時点の aliveCount+1 を finalRank とする。
    // ただし完全同値（creditLife も evalNormalized も一致）は同着扱い: 同じ finalRank を付ける。
    for i := 0; i < cullCount; i++ {
        st := alive[i]
        st.alive = false
        s.aliveCount--

        // 行列の全客を restPool へ回収
        for _, cid := range s.storeQueues[st.id] {
            s.releaseQueuedToRest(cid, st.id)
        }
        s.storeQueues[st.id] = nil

        finalRank := s.aliveCount + 1

        // 同着判定: 直前の脱落店と creditLife, evalNormalized が完全一致なら同じ rank
        if i > 0 {
            prev := alive[i-1]
            if st.creditLife == prev.creditLife && st.evalNormalized == prev.evalNormalized {
                // prev は直前のループで rank を受けている。ただし prev の rank は
                // ブロードキャスト済みの FinalRank から逆算する必要がある。
                // ここでは同着分だけ rank を繰り上げる（aliveCount+1 ではなく prev と同じ値）。
                finalRank = s.aliveCount + 2 // prev 脱落時の aliveCount+1 と同値
            }
        }

        out = s.broadcastMsg(out, proto.StoreEliminated{
            StoreId:   st.id,
            Reason:    proto.ElimCull,
            FinalRank: finalRank,
        })
    }

    return out
}

// releaseQueuedToRest は行列内の客1人を restPool へ戻す。
// releaseToRest と異なり、行列からの除去は呼び出し側が一括で行う（storeQueues[sid] = nil）ため
// ここでは assignedStore のクリアと restPool への追加のみ。
func (s *Session) releaseQueuedToRest(cid proto.CustomerId, store PlayerId) {
    c := s.customers[cid]
    if c == nil {
        return
    }
    c.assignedStore = nil
    s.restPool = append(s.restPool, cid)
}
```

**ポイント**:

- `stormTickCounter` は毎 tick +1。`IntervalTicks` に達したら実行＋リセット
- 予告は `IntervalTicks - WarnTicks` の瞬間に1回だけ送る（`stormWarnSent` で重複防止）
- 実行後にカウンタ＝0、warnSent=false でリセット → 次サイクルへ
- 淘汰対象数は切り上げで最低1店。ただし全滅防止で `len(alive)-1` が上限
- ソートは弱い順（evalNormalized 昇順→ creditLife 昇順）。先頭から脱落させると「弱い方が先に脱落＝より大きい finalRank（低順位）」になる
- 行列客の回収は `releaseQueuedToRest` で assignedStore クリア + restPool 追加のみ行い、storeQueues は一括 nil 化（releaseToRest だと removeCustomer が O(N) の線形探索を行列長回やるため非効率）

### 3-j. sortStoresByWeakest（session.go）

```go
import "sort"

// sortStoresByWeakest は storeState スライスを「弱い順」にソートする。
// 弱い = evalNormalized が低い。同値なら creditLife が少ない方が弱い。
// storm の淘汰対象選定とタイブレーク順位で使用。
func sortStoresByWeakest(stores []*storeState) {
    sort.SliceStable(stores, func(i, j int) bool {
        a, b := stores[i], stores[j]
        if a.evalNormalized != b.evalNormalized {
            return a.evalNormalized < b.evalNormalized // 低い方が先（弱い）
        }
        return a.creditLife < b.creditLife // 信用が少ない方が先（弱い）
    })
}
```

`sort.SliceStable` を使い、完全同値の場合は元の順序（s.order 由来）を保持する。

### 3-k. Validate に検証を追加（params.go）

```go
func (gp GameParameters) Validate() error {
    // ...既存の検証...
    if gp.Storm.IntervalTicks < 0 {
        return fmt.Errorf("storm.intervalTicks は非負である必要 (got %d)", gp.Storm.IntervalTicks)
    }
    if gp.Storm.ThresholdPct < 0 || gp.Storm.ThresholdPct > 1 {
        return fmt.Errorf("storm.thresholdPct は 0..1 の範囲である必要 (got %f)", gp.Storm.ThresholdPct)
    }
    if gp.Phase.MidAliveThreshold < 0 {
        return fmt.Errorf("phase.midAliveThreshold は非負である必要 (got %d)", gp.Phase.MidAliveThreshold)
    }
    return nil
}
```

---

## 4. 完全な実装コード

以下のコードを `session.go` と `params.go` に適用する。

### params.go に追加する型と初期値

```go
// ── tako-H: フェーズ・火力・storm ──────────────────────────────

// PhaseParams: フェーズ移行の閾値（tako-H）。
// 生存数と経過時間の両軸、どちらか先で一方向移行（Early→Mid→Late）。
type PhaseParams struct {
    MidAliveThreshold  int `json:"midAliveThreshold"`  // Early→Mid: 生存数がこれ以下で移行
    LateAliveThreshold int `json:"lateAliveThreshold"` // Mid→Late: 生存数がこれ以下で移行
    MidTimeMs          int `json:"midTimeMs"`           // Early→Mid: 経過時間がこれ以上(ms)で移行
    LateTimeMs         int `json:"lateTimeMs"`          // Mid→Late: 経過時間がこれ以上(ms)で移行
}

// HeatParams: 火力（全体難易度）の加算式パラメータ（tako-H）。
// heatLevel = Base + int(PerAliveDrop * (maxStores - aliveCount)) + phaseBonus
// フェーズ加算は comparable 維持のため個別フィールド（map 不可）。
type HeatParams struct {
    Base         int     `json:"base"`         // 火力基礎値
    PerAliveDrop float64 `json:"perAliveDrop"` // 生存1人減あたりの火力加算
    PhaseEarly   int     `json:"phaseEarly"`   // Early フェーズの追加火力
    PhaseMid     int     `json:"phaseMid"`     // Mid フェーズの追加火力
    PhaseLate    int     `json:"phaseLate"`     // Late フェーズの追加火力
}

// StormParams: 下位淘汰(storm)のタイミングと閾値（tako-H）。
// IntervalTicks=0 で storm 無効（solo/dev 用）。
type StormParams struct {
    IntervalTicks int     `json:"intervalTicks"` // 実行間隔（tick数）
    WarnTicks     int     `json:"warnTicks"`     // 実行の何tick前に予告するか
    ThresholdPct  float64 `json:"thresholdPct"`  // 下位何%を強制脱落（0.0〜1.0）
}
```

GameParameters 構造体に3フィールドを追加:

```go
type GameParameters struct {
    // ...既存フィールド...
    Credit   CreditParams   `json:"credit"`
    Customer CustomerParams `json:"customer"`
    Eval     EvalParams     `json:"eval"`

    // tako-H: フェーズ・火力・storm
    Phase PhaseParams `json:"phase"`
    Heat  HeatParams  `json:"heat"`
    Storm StormParams `json:"storm"`
}
```

DefaultParameters に追加:

```go
Phase: PhaseParams{
    MidAliveThreshold:  70,
    LateAliveThreshold: 30,
    MidTimeMs:          30000,  // 30秒
    LateTimeMs:         90000,  // 90秒
},
Heat: HeatParams{
    Base:         0,
    PerAliveDrop: 0.1,
    PhaseEarly:   0,
    PhaseMid:     3,
    PhaseLate:    8,
},
Storm: StormParams{
    IntervalTicks: 40,   // 40tick ≒ 6秒（TickIntervalMs=150ms のとき）
    WarnTicks:     10,   // 10tick ≒ 1.5秒前に予告
    ThresholdPct:  0.10, // 下位10%
},
```

### session.go の変更箇所

**1. import に `"sort"` を追加**

```go
import (
    "fmt"
    "math/rand"
    "sort"

    "textro99/internal/proto"
)
```

**2. Session 構造体にフィールド追加**

```go
type Session struct {
    id     proto.MatchId
    params GameParameters
    words  WordSource
    rng    *rand.Rand

    customers   map[proto.CustomerId]*customer
    storeQueues map[PlayerId][]proto.CustomerId
    restPool    []proto.CustomerId

    stores map[PlayerId]*storeState
    order  []PlayerId

    state      SessionState
    phase      proto.Phase
    elapsedMs  int64
    tick       int
    aliveCount int

    // tako-H: フェーズ・火力・storm
    heatLevel        int  // 現在の火力レベル（stepHeat で更新、wordLevel() が返す）
    stormTickCounter int  // storm 間隔カウンタ（毎tick++）
    stormWarnSent    bool // 今サイクルの警告済みフラグ
}
```

**3. broadcastMsg ヘルパ追加**

```go
// broadcastMsg は全参加者（生存＋脱落観戦者）へ msg を配る。
func (s *Session) broadcastMsg(out []Outbound, msg any) []Outbound {
    for _, sid := range s.order {
        out = append(out, to(sid, msg))
    }
    return out
}
```

**4. stepPhase 実装（スタブを置換）**

```go
// stepPhase は生存数/経過時間からフェーズを判定し、変化時に PhaseChange を配る。tako-H。
func (s *Session) stepPhase(out []Outbound) []Outbound {
    pp := s.params.Phase
    switch s.phase {
    case proto.PhaseEarly:
        if s.aliveCount <= pp.MidAliveThreshold || s.elapsedMs >= int64(pp.MidTimeMs) {
            s.phase = proto.PhaseMid
            out = s.broadcastMsg(out, proto.PhaseChange{Phase: proto.PhaseMid})
        }
    case proto.PhaseMid:
        if s.aliveCount <= pp.LateAliveThreshold || s.elapsedMs >= int64(pp.LateTimeMs) {
            s.phase = proto.PhaseLate
            out = s.broadcastMsg(out, proto.PhaseChange{Phase: proto.PhaseLate})
        }
    }
    return out
}
```

**5. stepHeat 実装（スタブを置換）**

```go
// stepHeat は火力を更新し、変化時に DifficultyUpdate を配る。tako-H。
func (s *Session) stepHeat(out []Outbound) []Outbound {
    hp := s.params.Heat
    maxStores := len(s.order)

    newHeat := hp.Base + int(hp.PerAliveDrop*float64(maxStores-s.aliveCount))
    switch s.phase {
    case proto.PhaseEarly:
        newHeat += hp.PhaseEarly
    case proto.PhaseMid:
        newHeat += hp.PhaseMid
    case proto.PhaseLate:
        newHeat += hp.PhaseLate
    }

    if newHeat != s.heatLevel {
        s.heatLevel = newHeat
        out = s.broadcastMsg(out, proto.DifficultyUpdate{HeatLevel: s.heatLevel})
    }
    return out
}
```

**6. wordLevel 実装（スタブを置換）**

```go
// wordLevel はお題難度の実効レベル。火力(heatLevel) を返す。tako-H。
func (s *Session) wordLevel() int { return s.heatLevel }
```

**7. stepStorm 実装（スタブを置換）**

```go
// stepStorm は下位淘汰(storm)の予告・確定を行う。tako-H。
func (s *Session) stepStorm(out []Outbound) []Outbound {
    sp := s.params.Storm
    if sp.IntervalTicks <= 0 {
        return out // storm 無効
    }
    s.stormTickCounter++

    // 予告: IntervalTicks - WarnTicks に達した瞬間に1回
    warnAt := sp.IntervalTicks - sp.WarnTicks
    if warnAt < 1 {
        warnAt = 1
    }
    if s.stormTickCounter == warnAt && !s.stormWarnSent {
        s.stormWarnSent = true
        remaining := sp.IntervalTicks - s.stormTickCounter
        out = s.broadcastMsg(out, proto.ForcedEliminationWarning{
            UntilTick:    remaining,
            ThresholdPct: sp.ThresholdPct,
        })
    }

    // 実行タイミングでなければ終了
    if s.stormTickCounter < sp.IntervalTicks {
        return out
    }

    // リセット
    s.stormTickCounter = 0
    s.stormWarnSent = false

    // 生存2店以上でないと淘汰しない
    if s.aliveCount <= 1 {
        return out
    }

    out = s.executeCull(out)
    return out
}

// executeCull は下位淘汰の実行。生存店を弱い順にソートし、下位 ThresholdPct% を脱落させる。
func (s *Session) executeCull(out []Outbound) []Outbound {
    sp := s.params.Storm

    // 生存店を収集
    alive := make([]*storeState, 0, s.aliveCount)
    for _, sid := range s.order {
        if s.stores[sid].alive {
            alive = append(alive, s.stores[sid])
        }
    }
    if len(alive) <= 1 {
        return out
    }

    // 淘汰数: 切り上げ（最低1店）、ただし全滅防止で len-1 が上限
    cullCount := int(float64(len(alive))*sp.ThresholdPct + 0.999999)
    if cullCount < 1 {
        cullCount = 1
    }
    if cullCount >= len(alive) {
        cullCount = len(alive) - 1
    }

    // 弱い順にソート
    sortStoresByWeakest(alive)

    // 先頭から cullCount 店を脱落
    for i := 0; i < cullCount; i++ {
        st := alive[i]
        st.alive = false
        s.aliveCount--

        // 行列の全客を restPool へ回収
        for _, cid := range s.storeQueues[st.id] {
            c := s.customers[cid]
            if c != nil {
                c.assignedStore = nil
                s.restPool = append(s.restPool, cid)
            }
        }
        s.storeQueues[st.id] = nil

        finalRank := s.aliveCount + 1

        // 同着判定: 直前の脱落店と完全同値なら同じ rank
        if i > 0 {
            prev := alive[i-1]
            if st.creditLife == prev.creditLife && st.evalNormalized == prev.evalNormalized {
                finalRank = s.aliveCount + 2 // prev と同じ rank 値
            }
        }

        out = s.broadcastMsg(out, proto.StoreEliminated{
            StoreId:   st.id,
            Reason:    proto.ElimCull,
            FinalRank: finalRank,
        })
    }

    return out
}

// sortStoresByWeakest は弱い順（下位淘汰の対象順）にソートする。
// evalNormalized 昇順 → creditLife 昇順。完全同値は元の順序を保持。
func sortStoresByWeakest(stores []*storeState) {
    sort.SliceStable(stores, func(i, j int) bool {
        a, b := stores[i], stores[j]
        if a.evalNormalized != b.evalNormalized {
            return a.evalNormalized < b.evalNormalized
        }
        return a.creditLife < b.creditLife
    })
}
```

---

## 5. テストケース（session_test.go に追加）

### テスト 1: フェーズ移行（生存数）

```go
func TestStepPhase_AliveThreshold(t *testing.T) {
    s := newTestSession(99)
    s.Start()
    // storm 無効にして stepStorm の副作用を排除
    s.params.Storm.IntervalTicks = 0

    if s.phase != proto.PhaseEarly {
        t.Fatalf("初期は Early のはず: %v", s.phase)
    }

    // 生存を Mid 閾値ぴったりに設定して Tick
    s.aliveCount = s.params.Phase.MidAliveThreshold
    out := s.Tick(150)
    if s.phase != proto.PhaseMid {
        t.Fatalf("aliveCount=%d で Mid に移行するはず: %v", s.params.Phase.MidAliveThreshold, s.phase)
    }
    // PhaseChange(Mid) が全店に配信されている
    phaseChanges := filterMsg[proto.PhaseChange](out)
    if len(phaseChanges) != len(s.order) {
        t.Fatalf("PhaseChange が全店(%d)に配信されるはず: %d", len(s.order), len(phaseChanges))
    }
    if phaseChanges[0].Phase != proto.PhaseMid {
        t.Fatalf("PhaseChange.Phase=Mid のはず: %v", phaseChanges[0].Phase)
    }

    // Late 移行
    s.aliveCount = s.params.Phase.LateAliveThreshold
    out = s.Tick(150)
    if s.phase != proto.PhaseLate {
        t.Fatalf("aliveCount=%d で Late に移行するはず: %v", s.params.Phase.LateAliveThreshold, s.phase)
    }
}

// filterMsg はメッセージ型でフィルタするテストヘルパ。
func filterMsg[T any](out []Outbound) []T {
    var result []T
    for _, o := range out {
        if msg, ok := o.Msg.(T); ok {
            result = append(result, msg)
        }
    }
    return result
}
```

### テスト 2: フェーズ移行（経過時間）

```go
func TestStepPhase_TimeThreshold(t *testing.T) {
    s := newTestSession(99)
    s.Start()
    s.params.Storm.IntervalTicks = 0

    // 生存数は十分だが、経過時間で Mid 移行
    midMs := s.params.Phase.MidTimeMs
    s.Tick(midMs)
    if s.phase != proto.PhaseMid {
        t.Fatalf("elapsedMs=%d で Mid に移行するはず: %v", midMs, s.phase)
    }

    // さらに時間経過で Late 移行
    lateMs := s.params.Phase.LateTimeMs - midMs
    s.Tick(lateMs)
    if s.phase != proto.PhaseLate {
        t.Fatalf("elapsedMs=%d で Late に移行するはず: %v", s.params.Phase.LateTimeMs, s.phase)
    }
}
```

### テスト 3: 火力計算

```go
func TestStepHeat_Calculation(t *testing.T) {
    s := newTestSession(99)
    s.Start()
    s.params.Storm.IntervalTicks = 0
    hp := s.params.Heat

    // Early, 全員生存: heatLevel = Base + 0 + PhaseEarly
    s.Tick(150)
    wantEarly := hp.Base + hp.PhaseEarly
    if s.heatLevel != wantEarly {
        t.Fatalf("Early全員生存の fire=%d のはず: %d", wantEarly, s.heatLevel)
    }

    // 50人脱落した Mid フェーズ
    s.aliveCount = 49
    s.phase = proto.PhaseMid
    s.Tick(150)
    wantMid := hp.Base + int(hp.PerAliveDrop*float64(99-49)) + hp.PhaseMid
    if s.heatLevel != wantMid {
        t.Fatalf("Mid, alive=49 の fire=%d のはず: %d", wantMid, s.heatLevel)
    }
}
```

### テスト 4: 下位淘汰の実行

```go
func TestStepStorm_Cull(t *testing.T) {
    n := 10
    s := newTestSession(n)
    s.Start()
    s.params.Storm = StormParams{IntervalTicks: 5, WarnTicks: 2, ThresholdPct: 0.20}
    // Phase を Mid にして stepPhase の影響を排除
    s.phase = proto.PhaseMid
    s.params.Phase.LateAliveThreshold = 0

    // evalNormalized を手動設定（0.0〜0.9）
    for i, sid := range s.order {
        s.stores[sid].evalNormalized = float64(i) / float64(n-1)
    }

    // 5tick 回す（IntervalTicks=5 で storm 発動）
    var lastOut []Outbound
    for i := 0; i < 5; i++ {
        lastOut = s.Tick(150)
    }

    // 10人の20% = 2人が淘汰される
    culled := filterMsg[proto.StoreEliminated](lastOut)
    if len(culled) == 0 {
        t.Fatal("storm で StoreEliminated が出るはず")
    }
    // 最低2人(10*0.2 切り上げ)
    if s.aliveCount != n-2 {
        t.Fatalf("10人中2人が淘汰されて8人残るはず: %d", s.aliveCount)
    }
    // 淘汰された店は evalNormalized 下位（order[0], order[1]）
    for _, c := range culled {
        if c.Reason != proto.ElimCull {
            t.Fatalf("Reason=Cull のはず: %v", c.Reason)
        }
    }
}
```

### テスト 5: 予告タイミング

```go
func TestStepStorm_Warning(t *testing.T) {
    s := newTestSession(10)
    s.Start()
    s.params.Storm = StormParams{IntervalTicks: 10, WarnTicks: 3, ThresholdPct: 0.10}
    s.phase = proto.PhaseMid
    s.params.Phase.LateAliveThreshold = 0

    // tick 1〜6 は警告なし
    for i := 0; i < 6; i++ {
        out := s.Tick(150)
        warns := filterMsg[proto.ForcedEliminationWarning](out)
        if len(warns) > 0 {
            t.Fatalf("tick %d で警告が出るのは早すぎる", i+1)
        }
    }

    // tick 7 (= IntervalTicks - WarnTicks = 10-3) で警告
    out := s.Tick(150)
    warns := filterMsg[proto.ForcedEliminationWarning](out)
    if len(warns) == 0 {
        t.Fatal("tick 7 で ForcedEliminationWarning が出るはず")
    }
    if warns[0].UntilTick != 3 {
        t.Fatalf("UntilTick=3 のはず: %d", warns[0].UntilTick)
    }

    // tick 8〜9 は重複警告なし
    for i := 8; i <= 9; i++ {
        out := s.Tick(150)
        warns := filterMsg[proto.ForcedEliminationWarning](out)
        if len(warns) > 0 {
            t.Fatalf("tick %d で警告が重複している", i)
        }
    }
}
```

### テスト 6: 同時脱落タイブレーク

```go
func TestStepStorm_Tiebreak(t *testing.T) {
    // 5店で下位40%(=2店)を淘汰。2店の evalNormalized が同値だが creditLife が異なる。
    s := newTestSession(5)
    s.Start()
    s.params.Storm = StormParams{IntervalTicks: 1, WarnTicks: 0, ThresholdPct: 0.40}
    s.phase = proto.PhaseMid
    s.params.Phase.LateAliveThreshold = 0

    // evalNormalized: 全員 0（同値）。creditLife で差をつける。
    for _, sid := range s.order {
        s.stores[sid].evalNormalized = 0
    }
    s.stores[s.order[0]].creditLife = 1 // 最弱
    s.stores[s.order[1]].creditLife = 2 // 2番目に弱い
    s.stores[s.order[2]].creditLife = 3
    s.stores[s.order[3]].creditLife = 4
    s.stores[s.order[4]].creditLife = 5

    out := s.Tick(150)
    culled := filterMsg[proto.StoreEliminated](out)

    if len(culled) < 2 {
        t.Fatalf("2店が淘汰されるはず: %d", len(culled))
    }

    // creditLife=1 の店が先に脱落（より低い順位）
    if culled[0].StoreId != s.order[0] {
        t.Fatalf("creditLife=1 の店が最初に脱落するはず: %s", culled[0].StoreId)
    }
    // 先に脱落した方が FinalRank が大きい（低順位）
    if culled[0].FinalRank <= culled[1].FinalRank {
        t.Fatalf("先に脱落した方が FinalRank が大きいはず: %d vs %d",
            culled[0].FinalRank, culled[1].FinalRank)
    }
}
```

---

## 6. ローカル確認

```bash
cd /Users/ryu/kindai/2026/THEHACK/Takoda99-Server

# コンパイル確認
go build ./...

# テスト実行
go test ./internal/game/ -v -run "Phase|Heat|Storm|Tiebreak"

# 全テスト（既存が壊れていないか）
go test ./...
```

---

## 7. 完了条件

- [ ] Session に `heatLevel`, `stormTickCounter`, `stormWarnSent` フィールドが追加されている
- [ ] params.go に `PhaseParams`, `HeatParams`, `StormParams` が追加され、DefaultParameters に初期値がある
- [ ] `broadcastMsg` ヘルパが実装されている
- [ ] `stepPhase` がフェーズを判定し `PhaseChange` を全店配信する
- [ ] `stepHeat` が火力を計算し、変化時に `DifficultyUpdate` を全店配信する
- [ ] `wordLevel()` が `s.heatLevel` を返す
- [ ] `stepStorm` が IntervalTicks 間隔で下位淘汰を実行し `StoreEliminated(Cull)` を全店配信する
- [ ] storm 実行の WarnTicks 前に `ForcedEliminationWarning` が1回だけ配信される
- [ ] 同時脱落のタイブレーク（creditLife → evalNormalized）が機能する
- [ ] `go test ./internal/game/ -v` が全件パスする
- [ ] `go build ./...` が通る
