# Plan-02: 我慢ゲージ・離脱・信用（tako-F）

> **目的**: 客の我慢ゲージをtick毎に減算し、0で離脱 → 信用(ライフ)減少 → 信用0で自滅脱落を実装する。
> **対応issue**: #6, #29
> **依存**: Plan-01（基盤移行完了後）
> **参照**: 試合進行仕様 §5, 全体仕様 §5-6, 用語集 §6-7

---

## 1. 前提知識

### 読むべきリポジトリ・ドキュメント

| リポジトリ | 場所 | 読む理由 |
|---|---|---|
| Takoda99-Server | `internal/game/session.go` | Session 構造体、tick ループ、客移動ヘルパ、`stepPatience` スタブ |
| Takoda99-Server | `internal/game/params.go` | `GameParameters`、`CreditParams`、`CustomerParams`、`DefaultParameters()` |
| Takoda99-Server | `internal/game/ports.go` | `PlayerId` 型定義、DIP の継ぎ目 |
| Takoda99-Server | `internal/game/session_test.go` | テストの書き方の規約（`newTestSession`、`placeAssigned`、fakeWords） |
| Takoda99-Server | `internal/proto/messages.go` | Proto 型の再輸出一覧（`CustomerLeft`, `CreditUpdate`, `StoreEliminated`） |
| Takoda99-Proto | `proto/messages.go` | 正典メッセージ定義（フィールド名・json タグ・列挙値） |
| Takoda99-Docs | `03_サーバー仕様/04_パラメータ仕様.md` | パラメータの設計根拠・初期仮値 |

### キーコンセプト

- **我慢ゲージ (patience)**: 客が来店してから離脱するまでのカウントダウン（ミリ秒）。`patienceMaxMs` が最大値、`patienceLeftMs` が残り。来店時（`assignCustomer`）に満タンリセットされる。**行列先頭の客（=対応中）のみ** 毎tick減算される。
- **信用 (credit / creditLife)**: 店の体力。客の離脱でのみ減少し、回復手段はない。属性ごとに減少量（`LeaveLoss`）が異なる。
- **自滅脱落 (self-collapse)**: `creditLife` が 0 以下になると脱落。`alive=false`、行列の全客を回収、`finalRank = aliveCount + 1`（脱落順で順位確定）、`StoreEliminated` を全店ブロードキャスト。
- **#29 ガード**: `stepPatience` は全客属性（Normal/Bonus/Claimer/Buzz）で同一ロジックで発火する。属性による if 分岐で離脱判定をスキップしてはならない。

---

## 2. 現状のコード

### `stepPatience` スタブ

**ファイル**: `internal/game/session.go` 276行目

```go
func (s *Session) stepPatience(dtMs int, out []Outbound) []Outbound { _ = dtMs; return out }
```

これを実装に差し替える。

### 関連する既存型

**`customer` 構造体** (`internal/game/session.go` 42-49行目):

```go
type customer struct {
    attribute      proto.CustomerAttribute
    patienceMaxMs  int
    patienceLeftMs int
    orderCount     int
    keystrokeTotal int
    assignedStore  *PlayerId
}
```

**`storeState` 構造体** (`internal/game/session.go` 59-69行目):

```go
type storeState struct {
    id             PlayerId
    name           string
    creditLife     int         // 信用(HP)。客の離脱でのみ減少・0で自滅脱落
    evalRaw        float64
    buzzBonus      float64
    evalNormalized float64
    rank           int
    served         servedStats
    alive          bool
}
```

**`CreditParams`** (`internal/game/params.go` 48-51行目):

```go
type CreditParams struct {
    InitialLife int `json:"initialLife"`
}
```

### 既存ヘルパ関数

| 関数 | 場所 | 役割 |
|---|---|---|
| `to(pid, msg)` | session.go:38 | `Outbound{To: Recipient{PlayerId: pid}, Msg: msg}` を返す |
| `releaseToRest(cid)` | session.go:410 | 客を行列から除去し、`assignedStore=nil` にし、`restPool` に戻す |
| `assignCustomer(cid, store)` | session.go:398 | 客を restPool から取り、行列末尾に追加、`patienceLeftMs=patienceMaxMs` |
| `removeCustomer(ids, cid)` | session.go:424 | ID配列から1件除去（順序保持） |

### テストの既存ユーティリティ

| 関数/型 | 場所 | 役割 |
|---|---|---|
| `fakeWords` | session_test.go:12-16 | テスト用 `WordSource`（固定4打鍵の「たこ」を返す） |
| `newTestSession(n)` | session_test.go:19-26 | n店の `Session` を `DefaultParameters()` で作る |
| `placeAssigned(s, cid, store, attr, orderCount, keystrokes)` | session_test.go:305-314 | 指定属性の客を行列に直接配置する |

### Proto メッセージ（変更不要・そのまま使用）

**`CustomerLeft`** (Takoda99-Proto `proto/messages.go` 178-181行目):

```go
type CustomerLeft struct {
    CustomerId CustomerId  `json:"customerId"`
    Reason     LeaveReason `json:"reason"`
}
```

**`CreditUpdate`** (同 184-188行目):

```go
type CreditUpdate struct {
    Life   int          `json:"life"`
    Delta  int          `json:"delta"`
    Reason CreditReason `json:"reason"`
}
```

**`StoreEliminated`** (同 222-226行目):

```go
type StoreEliminated struct {
    StoreId   StoreId           `json:"storeId"`
    Reason    EliminationReason `json:"reason"`
    FinalRank int               `json:"finalRank"`
}
```

列挙値（既に `internal/proto/messages.go` で再輸出済み）:

- `proto.LeaveTimeout` = `"Timeout"`
- `proto.CreditCustomerLeft` = `"CustomerLeft"`
- `proto.ElimSelfCollapse` = `"SelfCollapse"`

---

## 3. 実装手順

### 手順a: `LeaveLoss` を `CreditParams` に追加（`params.go`）

`CreditParams` に属性別の離脱ペナルティを追加する。`GameParameters` の `==` 比較可能性を保つため、
**`map` は使わない**。4属性を固定フィールドの struct で持つ。

```go
type LeaveLoss struct {
    Normal  int `json:"normal"`
    Bonus   int `json:"bonus"`
    Claimer int `json:"claimer"`
    Buzz    int `json:"buzz"`
}

type CreditParams struct {
    InitialLife int      `json:"initialLife"`
    LeaveLoss   LeaveLoss `json:"leaveLoss"`
}
```

`LeaveLoss` にルックアップメソッドを付ける:

```go
func (ll LeaveLoss) For(attr proto.CustomerAttribute) int {
    switch attr {
    case proto.AttrBonus:
        return ll.Bonus
    case proto.AttrClaimer:
        return ll.Claimer
    case proto.AttrBuzz:
        return ll.Buzz
    default:
        return ll.Normal
    }
}
```

### 手順b: `PatienceParams` を `GameParameters` に追加（`params.go`）

```go
type PatienceParams struct {
    LateMul float64 `json:"lateMul"` // 終盤の我慢ゲージ短縮倍率（0<x<1.0 で速く減る）
    AlertMs int     `json:"alertMs"` // 離脱アラート閾値（表示用。S2C には載せない）
}
```

`GameParameters` に `Patience PatienceParams` フィールドを追加:

```go
type GameParameters struct {
    // ... 既存フィールド ...
    Patience PatienceParams `json:"patience"` // tako-F
}
```

### 手順c: `DefaultParameters()` の更新（`params.go`）

```go
Patience: PatienceParams{
    LateMul: 0.6,   // Late で実質1.67倍速
    AlertMs: 2000,  // 残り2秒で警告色
},
Credit: CreditParams{
    InitialLife: 3,
    LeaveLoss: LeaveLoss{
        Normal:  1,
        Bonus:   1,
        Claimer: 1,
        Buzz:    2, // ハイリスク・ハイリターン
    },
},
```

### 手順d: `broadcastMsg` ヘルパの追加（`session.go`）

全店にメッセージを配信する汎用ヘルパ。`s.order` を走査して各店に送る。

```go
func (s *Session) broadcastMsg(msg any, out []Outbound) []Outbound {
    for _, pid := range s.order {
        out = append(out, to(pid, msg))
    }
    return out
}
```

### 手順e: `stepPatience` の実装（`session.go`）

スタブを以下に差し替える:

```go
// stepPatience は各店の行列先頭客の我慢ゲージを dt 減算し、0 で離脱（CustomerLeft＋信用減）させる。
// 属性(Normal/Bonus/Claimer/Buzz)で発火可否を分岐しない（#29 の詰まりガード）。tako-F。
func (s *Session) stepPatience(dtMs int, out []Outbound) []Outbound {
    // Late フェーズでは実効 dt を拡大（ゲージが速く減る）
    effectiveDt := dtMs
    if s.phase == proto.PhaseLate {
        effectiveDt = int(float64(dtMs) / s.params.Patience.LateMul)
    }

    for _, sid := range s.order {
        st := s.stores[sid]
        if !st.alive {
            continue
        }
        q := s.storeQueues[sid]
        if len(q) == 0 {
            continue
        }
        head := s.customers[q[0]]
        head.patienceLeftMs -= effectiveDt
        if head.patienceLeftMs <= 0 {
            out = s.processLeave(sid, q[0], out)
        }
    }
    return out
}
```

### 手順f: `processLeave` の実装（`session.go`）

```go
// processLeave は客1人の離脱を処理する。CustomerLeft → 信用減算 → CreditUpdate → 自滅判定。
func (s *Session) processLeave(store PlayerId, cid proto.CustomerId, out []Outbound) []Outbound {
    c := s.customers[cid]

    // 1. CustomerLeft を該当店へ
    out = append(out, to(store, proto.CustomerLeft{
        CustomerId: cid,
        Reason:     proto.LeaveTimeout,
    }))

    // 2. 客をたべたべエリアへ戻す
    s.releaseToRest(cid)

    // 3. 信用減算
    loss := s.params.Credit.LeaveLoss.For(c.attribute)
    st := s.stores[store]
    st.creditLife -= loss

    // 4. CreditUpdate を該当店へ
    out = append(out, to(store, proto.CreditUpdate{
        Life:   st.creditLife,
        Delta:  -loss,
        Reason: proto.CreditCustomerLeft,
    }))

    // 5. 自滅判定
    if st.creditLife <= 0 {
        out = s.selfCollapse(store, out)
    }

    return out
}
```

### 手順g: `selfCollapse` の実装（`session.go`）

```go
// selfCollapse は信用0による自滅脱落を処理する。
func (s *Session) selfCollapse(store PlayerId, out []Outbound) []Outbound {
    st := s.stores[store]

    // 1. 脱落状態へ
    st.alive = false
    s.aliveCount--

    // 2. 行列の全客をたべたべエリアへ回収
    for len(s.storeQueues[store]) > 0 {
        cid := s.storeQueues[store][0]
        s.releaseToRest(cid)
    }

    // 3. 最終順位（脱落順：現在の生存数+1 = この店の順位）
    finalRank := s.aliveCount + 1

    // 4. StoreEliminated を全店ブロードキャスト
    out = s.broadcastMsg(proto.StoreEliminated{
        StoreId:   store,
        Reason:    proto.ElimSelfCollapse,
        FinalRank: finalRank,
    }, out)

    return out
}
```

### 手順h: 終盤我慢短縮（フェーズ連動）

`stepPatience` 内の `effectiveDt` 計算で実装済み（手順e 参照）。

仕組み: `LateMul = 0.6` のとき `effectiveDt = dtMs / 0.6 = dtMs * 1.67`。
つまり Late フェーズでは我慢ゲージが約1.67倍速で減る。

フェーズ移行の実装自体は Plan-04 が担当するが、`stepPatience` は `s.phase` を参照するだけなので先に書ける。`s.phase` が `PhaseLate` に変わるまでは `effectiveDt = dtMs` のまま（Early/Mid では短縮なし）。

---

## 4. 実装するコード

以下は差分ではなく、**新規追加・差し替えするコードの完全版**。

### 4.1 `internal/game/params.go` への追加

#### `LeaveLoss` 構造体と `For` メソッド（`CreditParams` の直前に追加）

```go
// LeaveLoss は客の離脱時の信用減少量（属性別）。固定4フィールドで持ち
// GameParameters の == 比較可能性を保つ（map[string]int は使わない）。
type LeaveLoss struct {
	Normal  int `json:"normal"`
	Bonus   int `json:"bonus"`
	Claimer int `json:"claimer"`
	Buzz    int `json:"buzz"`
}

// For は属性に対応する離脱ペナルティを返す。
func (ll LeaveLoss) For(attr proto.CustomerAttribute) int {
	switch attr {
	case proto.AttrBonus:
		return ll.Bonus
	case proto.AttrClaimer:
		return ll.Claimer
	case proto.AttrBuzz:
		return ll.Buzz
	default:
		return ll.Normal
	}
}
```

#### `CreditParams` の差し替え（既存の48-51行目を置換）

```go
// CreditParams: 信用（ライフ）。客の離脱でのみ減少・0で自滅脱落。
type CreditParams struct {
	InitialLife int       `json:"initialLife"` // 初期信用（例:3。約3回の離脱で脱落）
	LeaveLoss   LeaveLoss `json:"leaveLoss"`   // 属性別の離脱ペナルティ
}
```

#### `PatienceParams` の追加（`CreditParams` の直後に追加）

```go
// PatienceParams: 我慢ゲージの調整値（tako-F）。
type PatienceParams struct {
	LateMul float64 `json:"lateMul"` // 終盤の我慢ゲージ短縮倍率（0<x<1.0 で速く減る。0.6=約1.67倍速）
	AlertMs int     `json:"alertMs"` // 離脱アラート閾値ms（クライアント表示用）
}
```

#### `GameParameters` にフィールド追加

```go
type GameParameters struct {
	Combo      ComboParams      `json:"combo"`
	Attack     AttackParams     `json:"attack"`
	Stack      StackParams      `json:"stack"`
	Difficulty DifficultyParams `json:"difficulty"`
	Odai       OdaiParams       `json:"odai"`
	Matching   MatchingParams   `json:"matching"`
	Session    SessionParams    `json:"session"`

	// たこ焼き版で追加。旧項目(Combo/Attack/Stack/Difficulty/Odai)は tako-K で
	// 評価/信用/フェーズ/火力の新スキーマへ置換予定。
	Credit   CreditParams   `json:"credit"`   // tako-B
	Customer CustomerParams `json:"customer"` // tako-D
	Eval     EvalParams     `json:"eval"`     // tako-E
	Patience PatienceParams `json:"patience"` // tako-F
}
```

#### `DefaultParameters()` の `Credit` と `Patience` 部分

```go
Credit: CreditParams{
	InitialLife: 3,
	LeaveLoss: LeaveLoss{
		Normal:  1,
		Bonus:   1,
		Claimer: 1,
		Buzz:    2,
	},
},
// ... (Customer, Eval は既存のまま) ...
Patience: PatienceParams{
	LateMul: 0.6,
	AlertMs: 2000,
},
```

### 4.2 `internal/game/session.go` への追加・差し替え

#### `broadcastMsg` ヘルパ（`summaries()` の直前に追加）

```go
// broadcastMsg は全店へ msg を配信する。
func (s *Session) broadcastMsg(msg any, out []Outbound) []Outbound {
	for _, pid := range s.order {
		out = append(out, to(pid, msg))
	}
	return out
}
```

#### `stepPatience` の差し替え（既存276行目のスタブを置換）

```go
// stepPatience は各店の行列先頭客の我慢ゲージを dt 減算し、0 で離脱（CustomerLeft＋信用減）させる。
// 属性(Normal/Bonus/Claimer/Buzz)で発火可否を分岐しない（#29 の詰まりガード）。tako-F。
func (s *Session) stepPatience(dtMs int, out []Outbound) []Outbound {
	// Late フェーズでは実効 dt を拡大（ゲージが速く減る）。
	// LateMul=0.6 → effectiveDt = dtMs/0.6 ≈ dtMs*1.67。
	// Early/Mid では LateMul の効果なし（そのまま dtMs）。
	effectiveDt := dtMs
	if s.phase == proto.PhaseLate && s.params.Patience.LateMul > 0 {
		effectiveDt = int(float64(dtMs) / s.params.Patience.LateMul)
	}

	for _, sid := range s.order {
		st := s.stores[sid]
		if !st.alive {
			continue
		}
		q := s.storeQueues[sid]
		if len(q) == 0 {
			continue
		}
		// 行列先頭（＝対応中）の客のみゲージ減算。
		// 2番目以降は「並んでいる」だけで、対応が回ってくるまで我慢ゲージは動かない。
		head := s.customers[q[0]]
		head.patienceLeftMs -= effectiveDt
		if head.patienceLeftMs <= 0 {
			out = s.processLeave(sid, q[0], out)
		}
	}
	return out
}
```

#### `processLeave` ヘルパ（`stepPatience` の直後に追加）

```go
// processLeave は客1人の離脱を処理する。
// CustomerLeft 配信 → releaseToRest → 信用減算 → CreditUpdate 配信 → 自滅判定。
func (s *Session) processLeave(store PlayerId, cid proto.CustomerId, out []Outbound) []Outbound {
	c := s.customers[cid]

	// 1. CustomerLeft を該当店へ送信
	out = append(out, to(store, proto.CustomerLeft{
		CustomerId: cid,
		Reason:     proto.LeaveTimeout,
	}))

	// 2. 客をたべたべエリアへ戻す
	s.releaseToRest(cid)

	// 3. 信用減算（属性別）
	loss := s.params.Credit.LeaveLoss.For(c.attribute)
	st := s.stores[store]
	st.creditLife -= loss

	// 4. CreditUpdate を該当店へ送信
	out = append(out, to(store, proto.CreditUpdate{
		Life:   st.creditLife,
		Delta:  -loss,
		Reason: proto.CreditCustomerLeft,
	}))

	// 5. 信用0以下 → 自滅脱落
	if st.creditLife <= 0 {
		out = s.selfCollapse(store, out)
	}

	return out
}
```

#### `selfCollapse` ヘルパ（`processLeave` の直後に追加）

```go
// selfCollapse は信用0による自滅脱落を処理する。
// alive=false → 行列全客回収 → 順位確定 → StoreEliminated ブロードキャスト。
func (s *Session) selfCollapse(store PlayerId, out []Outbound) []Outbound {
	st := s.stores[store]

	// 1. 脱落状態へ
	st.alive = false
	s.aliveCount--

	// 2. 行列の全客をたべたべエリアへ回収（先頭から順に release）
	for len(s.storeQueues[store]) > 0 {
		cid := s.storeQueues[store][0]
		s.releaseToRest(cid)
	}

	// 3. 最終順位（脱落順：現在の生存数+1 がこの店の順位）
	finalRank := s.aliveCount + 1

	// 4. StoreEliminated を全店へブロードキャスト
	out = s.broadcastMsg(proto.StoreEliminated{
		StoreId:   store,
		Reason:    proto.ElimSelfCollapse,
		FinalRank: finalRank,
	}, out)

	return out
}
```

### 4.3 完成後のファイル構成（変更対象のみ）

```
internal/game/
  params.go       ← LeaveLoss, PatienceParams 追加、CreditParams 拡張、DefaultParameters() 更新
  session.go      ← broadcastMsg, stepPatience, processLeave, selfCollapse 追加
  session_test.go ← テストケース追加（次セクション）
```

---

## 5. テスト

テストは全て `internal/game/session_test.go` に追加する。既存の `newTestSession`、`placeAssigned`、`fakeWords` をそのまま使う。

### 5.1 `TestStepPatience_BasicLeave`

我慢ゲージ分のtickを回すと離脱 → `CustomerLeft` + `CreditUpdate` が出る。

```go
// 1店1客で我慢ゲージ分のtickを進めると CustomerLeft + CreditUpdate が出る。
func TestStepPatience_BasicLeave(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	store := s.order[0]
	cid := proto.CustomerId("patience-1")
	placeAssigned(s, cid, store, proto.AttrNormal, 1, 5)

	patience := s.customers[cid].patienceMaxMs // 8000ms (Normal)
	dt := 150                                  // tick間隔

	// ゲージが 0 になる直前まで進める
	ticks := (patience / dt) - 1
	for i := 0; i < ticks; i++ {
		out := s.stepPatience(dt, nil)
		if len(out) != 0 {
			t.Fatalf("tick %d: ゲージ残ありで出力があった: %v", i, out)
		}
	}

	// あと1tick で離脱するはず
	remaining := s.customers[cid].patienceLeftMs
	if remaining <= 0 {
		t.Fatalf("まだ離脱していないはず: remaining=%d", remaining)
	}

	// ゲージを使い切る dt を投入
	out := s.stepPatience(remaining+1, nil)
	if len(out) != 2 {
		t.Fatalf("CustomerLeft + CreditUpdate の2件のはず: %d件", len(out))
	}

	// 1件目: CustomerLeft
	cl, ok := out[0].Msg.(proto.CustomerLeft)
	if !ok {
		t.Fatalf("1件目が CustomerLeft でない: %T", out[0].Msg)
	}
	if cl.CustomerId != cid || cl.Reason != proto.LeaveTimeout {
		t.Fatalf("CustomerLeft の内容が不正: %+v", cl)
	}
	if out[0].To.PlayerId != store {
		t.Fatalf("宛先が該当店でない: %s", out[0].To.PlayerId)
	}

	// 2件目: CreditUpdate
	cu, ok := out[1].Msg.(proto.CreditUpdate)
	if !ok {
		t.Fatalf("2件目が CreditUpdate でない: %T", out[1].Msg)
	}
	if cu.Delta != -1 || cu.Reason != proto.CreditCustomerLeft {
		t.Fatalf("CreditUpdate の内容が不正: %+v", cu)
	}
	expectedLife := DefaultParameters().Credit.InitialLife - 1
	if cu.Life != expectedLife {
		t.Fatalf("Life=%d のはず: %d", expectedLife, cu.Life)
	}

	// 客が restPool に戻っている
	if s.customers[cid].assignedStore != nil {
		t.Fatal("離脱した客の assignedStore がクリアされていない")
	}
}
```

### 5.2 `TestStepPatience_SelfCollapse`

initialLife=1 で離脱1回 → 自滅脱落。

```go
// initialLife=1、離脱1回で自滅脱落（StoreEliminated）。
func TestStepPatience_SelfCollapse(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.params.Credit.InitialLife = 1
	s.params.Credit.LeaveLoss = LeaveLoss{Normal: 1, Bonus: 1, Claimer: 1, Buzz: 2}
	store := s.order[0]
	s.stores[store].creditLife = 1 // 初期ライフを1に上書き

	cid := proto.CustomerId("collapse-1")
	placeAssigned(s, cid, store, proto.AttrNormal, 1, 5)

	// ゲージを一気に使い切る
	patience := s.customers[cid].patienceMaxMs
	out := s.stepPatience(patience+1, nil)

	// CustomerLeft(1) + CreditUpdate(1) + StoreEliminated(3店ブロードキャスト) = 5件
	if len(out) != 5 {
		t.Fatalf("出力5件のはず: %d件 (内訳: CustomerLeft+CreditUpdate+StoreEliminated*3店)", len(out))
	}

	// StoreEliminated を探す
	var elimCount int
	for _, o := range out {
		if se, ok := o.Msg.(proto.StoreEliminated); ok {
			elimCount++
			if se.StoreId != store {
				t.Fatalf("StoreEliminated の storeId が不正: %s", se.StoreId)
			}
			if se.Reason != proto.ElimSelfCollapse {
				t.Fatalf("Reason が SelfCollapse でない: %s", se.Reason)
			}
			if se.FinalRank != 3 {
				t.Fatalf("3店中の脱落 → FinalRank=3 のはず: %d", se.FinalRank)
			}
		}
	}
	if elimCount != 3 {
		t.Fatalf("StoreEliminated が3店にブロードキャストされるはず: %d件", elimCount)
	}

	// 状態確認
	if s.stores[store].alive {
		t.Fatal("脱落した店が alive のまま")
	}
	if s.aliveCount != 2 {
		t.Fatalf("aliveCount=2 のはず: %d", s.aliveCount)
	}
	if len(s.storeQueues[store]) != 0 {
		t.Fatalf("脱落店の行列が空でない: %v", s.storeQueues[store])
	}
}
```

### 5.3 `TestStepPatience_AttributeLeaveLoss`

Buzz は leaveLoss=2 で信用が2減る。

```go
// Buzz の離脱は leaveLoss=2 で信用が2減る。
func TestStepPatience_AttributeLeaveLoss(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.params.Credit.LeaveLoss = LeaveLoss{Normal: 1, Bonus: 1, Claimer: 1, Buzz: 2}
	store := s.order[0]
	initLife := s.stores[store].creditLife

	cid := proto.CustomerId("buzz-leave")
	placeAssigned(s, cid, store, proto.AttrBuzz, 4, 20)

	patience := s.customers[cid].patienceMaxMs
	out := s.stepPatience(patience+1, nil)

	// CreditUpdate を探す
	var found bool
	for _, o := range out {
		if cu, ok := o.Msg.(proto.CreditUpdate); ok {
			found = true
			if cu.Delta != -2 {
				t.Fatalf("Buzz の離脱ペナルティは -2 のはず: %d", cu.Delta)
			}
			if cu.Life != initLife-2 {
				t.Fatalf("Life=%d のはず: %d", initLife-2, cu.Life)
			}
		}
	}
	if !found {
		t.Fatal("CreditUpdate が見つからない")
	}
}
```

### 5.4 `TestStepPatience_HeadOnly`

行列に2客いても先頭だけがゲージ減算される。

```go
// 行列に2客いても先頭のみゲージ減算。2番目は不変。
func TestStepPatience_HeadOnly(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	store := s.order[0]
	front := proto.CustomerId("front")
	behind := proto.CustomerId("behind")
	placeAssigned(s, front, store, proto.AttrNormal, 1, 5)
	placeAssigned(s, behind, store, proto.AttrNormal, 1, 5)

	behindBefore := s.customers[behind].patienceLeftMs
	frontBefore := s.customers[front].patienceLeftMs

	s.stepPatience(150, nil)

	if s.customers[front].patienceLeftMs >= frontBefore {
		t.Fatalf("先頭のゲージが減っていない: before=%d after=%d", frontBefore, s.customers[front].patienceLeftMs)
	}
	if s.customers[behind].patienceLeftMs != behindBefore {
		t.Fatalf("2番目のゲージが動いた: before=%d after=%d", behindBefore, s.customers[behind].patienceLeftMs)
	}
}
```

### 5.5 `TestStepPatience_ServePreventLeave`

先頭客を提供完了すると離脱回避。次の客が先頭に昇格し、新しいゲージで開始。

```go
// 先頭客を提供完了→離脱回避。次の客が新しいゲージで先頭に昇格。
func TestStepPatience_ServePreventLeave(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	store := s.order[0]

	first := proto.CustomerId("first")
	second := proto.CustomerId("second")
	placeAssigned(s, first, store, proto.AttrNormal, 1, 5)
	placeAssigned(s, second, store, proto.AttrNormal, 1, 5)

	// ゲージを半分まで減らす
	half := s.customers[first].patienceMaxMs / 2
	s.stepPatience(half, nil)

	// 先頭を提供完了 → 離脱回避
	s.ApplyOrderServed(store, proto.OrderServed{CustomerId: first, ElapsedMs: 3000, MissCount: 0})

	// second が先頭に昇格
	q := s.storeQueues[store]
	if len(q) != 1 || q[0] != second {
		t.Fatalf("second が先頭に昇格するはず: %v", q)
	}
	// second のゲージは満タン（来店時にリセット済み）
	if s.customers[second].patienceLeftMs != s.customers[second].patienceMaxMs {
		t.Fatalf("second のゲージは満タンのはず: left=%d max=%d",
			s.customers[second].patienceLeftMs, s.customers[second].patienceMaxMs)
	}

	// さらに tick を回しても離脱しない（ゲージ満タンからスタート）
	out := s.stepPatience(150, nil)
	if len(out) != 0 {
		t.Fatalf("ゲージ満タンで離脱するはずがない: %v", out)
	}
}
```

### 5.6 `TestStepPatience_AllAttributes`（#29 ガード）

全4属性が離脱を発火する。属性による分岐で漏れがないことを保証。

```go
// 全4属性（Normal/Bonus/Claimer/Buzz）で離脱が発火する（#29 ガード）。
func TestStepPatience_AllAttributes(t *testing.T) {
	attrs := []struct {
		attr  proto.CustomerAttribute
		order int
		keys  int
	}{
		{proto.AttrNormal, 2, 10},
		{proto.AttrBonus, 2, 10},
		{proto.AttrClaimer, 1, 5},
		{proto.AttrBuzz, 4, 20},
	}

	for _, tc := range attrs {
		t.Run(string(tc.attr), func(t *testing.T) {
			s := newTestSession(2)
			s.state = Running
			s.params.Credit.LeaveLoss = LeaveLoss{Normal: 1, Bonus: 1, Claimer: 1, Buzz: 2}
			store := s.order[0]
			cid := proto.CustomerId("attr-" + string(tc.attr))
			placeAssigned(s, cid, store, tc.attr, tc.order, tc.keys)

			patience := s.customers[cid].patienceMaxMs
			out := s.stepPatience(patience+1, nil)

			// CustomerLeft が含まれること
			var foundLeave bool
			for _, o := range out {
				if cl, ok := o.Msg.(proto.CustomerLeft); ok {
					foundLeave = true
					if cl.CustomerId != cid {
						t.Fatalf("CustomerId 不一致: %s", cl.CustomerId)
					}
				}
			}
			if !foundLeave {
				t.Fatalf("属性 %s で CustomerLeft が発火しなかった", tc.attr)
			}
		})
	}
}
```

### 5.7 `TestStepPatience_LatePhaseFaster`（終盤短縮）

Late フェーズでは同じ tick 間隔でもゲージが速く減る。

```go
// Late フェーズでは我慢ゲージが LateMul 分速く減る。
func TestStepPatience_LatePhaseFaster(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.params.Patience.LateMul = 0.5 // 2倍速で減る
	store := s.order[0]

	cid := proto.CustomerId("late-1")
	placeAssigned(s, cid, store, proto.AttrNormal, 1, 5)

	dt := 150

	// Early: 150ms 分だけ減る
	s.phase = proto.PhaseEarly
	s.stepPatience(dt, nil)
	afterEarly := s.customers[cid].patienceLeftMs
	earlyDelta := s.customers[cid].patienceMaxMs - afterEarly
	if earlyDelta != dt {
		t.Fatalf("Early の減算量=%d のはず: %d", dt, earlyDelta)
	}

	// リセット
	s.customers[cid].patienceLeftMs = s.customers[cid].patienceMaxMs

	// Late: 150/0.5=300ms 分減る
	s.phase = proto.PhaseLate
	s.stepPatience(dt, nil)
	afterLate := s.customers[cid].patienceLeftMs
	lateDelta := s.customers[cid].patienceMaxMs - afterLate
	if lateDelta != int(float64(dt)/0.5) {
		t.Fatalf("Late の減算量=%d のはず: %d", int(float64(dt)/0.5), lateDelta)
	}

	if lateDelta <= earlyDelta {
		t.Fatalf("Late の方が速く減るはず: early=%d late=%d", earlyDelta, lateDelta)
	}
}
```

### 5.8 `TestStepPatience_DeadStoreSkipped`

脱落済みの店はスキップされる。

```go
// 脱落済み（alive=false）の店はスキップされる。
func TestStepPatience_DeadStoreSkipped(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	dead := s.order[0]
	alive := s.order[1]

	cidDead := proto.CustomerId("dead-c")
	cidAlive := proto.CustomerId("alive-c")
	placeAssigned(s, cidDead, dead, proto.AttrNormal, 1, 5)
	placeAssigned(s, cidAlive, alive, proto.AttrNormal, 1, 5)

	// dead 店を脱落状態にする
	s.stores[dead].alive = false

	beforeDead := s.customers[cidDead].patienceLeftMs
	s.stepPatience(150, nil)

	// dead 店の客のゲージは動かない
	if s.customers[cidDead].patienceLeftMs != beforeDead {
		t.Fatalf("脱落店の客のゲージが動いた: before=%d after=%d", beforeDead, s.customers[cidDead].patienceLeftMs)
	}
	// alive 店の客のゲージは減っている
	if s.customers[cidAlive].patienceLeftMs >= s.customers[cidAlive].patienceMaxMs {
		t.Fatal("生存店の客のゲージが減っていない")
	}
}
```

### 5.9 `TestBroadcastMsg`

broadcastMsg が全店に配信されることを確認。

```go
// broadcastMsg は全店（order の全員）に配信する。
func TestBroadcastMsg(t *testing.T) {
	s := newTestSession(4)
	msg := proto.StoreEliminated{StoreId: "test", Reason: proto.ElimSelfCollapse, FinalRank: 4}
	out := s.broadcastMsg(msg, nil)
	if len(out) != 4 {
		t.Fatalf("4店にブロードキャストのはず: %d件", len(out))
	}
	sent := make(map[PlayerId]bool)
	for _, o := range out {
		sent[o.To.PlayerId] = true
		if _, ok := o.Msg.(proto.StoreEliminated); !ok {
			t.Fatalf("StoreEliminated でない: %T", o.Msg)
		}
	}
	for _, sid := range s.order {
		if !sent[sid] {
			t.Fatalf("店 %s に送られていない", sid)
		}
	}
}
```

### 5.10 `TestLeaveLoss_For`

`LeaveLoss.For` が全属性で正しい値を返す。

```go
// LeaveLoss.For が全属性で正しい値を返す。
func TestLeaveLoss_For(t *testing.T) {
	ll := LeaveLoss{Normal: 1, Bonus: 2, Claimer: 3, Buzz: 4}
	cases := []struct {
		attr proto.CustomerAttribute
		want int
	}{
		{proto.AttrNormal, 1},
		{proto.AttrBonus, 2},
		{proto.AttrClaimer, 3},
		{proto.AttrBuzz, 4},
	}
	for _, tc := range cases {
		got := ll.For(tc.attr)
		if got != tc.want {
			t.Fatalf("For(%s)=%d のはず: %d", tc.attr, tc.want, got)
		}
	}
}
```

---

## 6. ローカル確認

全コマンドはリポジトリルート (`Takoda99-Server/`) で実行する。

```bash
# ビルド確認（コンパイルエラーがないこと）
go build ./...

# テスト実行（game パッケージ）
go test ./internal/game/ -v -run "TestStepPatience|TestBroadcastMsg|TestLeaveLoss"

# テスト実行（全パッケージ）
go test ./...

# vet（静的解析）
go vet ./...

# 既存テストが壊れていないことの確認
go test ./internal/game/ -v -run "TestNewSession|TestStart|TestCustomerMove|TestTick|TestApplyOrder|TestBuzzBonus|TestAttributeDistribution|TestAdmitCustomer|TestPublicParams"
```

---

## 7. 完了条件

- [ ] `LeaveLoss` 構造体と `For` メソッドが `params.go` に追加されている
- [ ] `CreditParams` に `LeaveLoss` フィールドが追加されている
- [ ] `PatienceParams` が `GameParameters` に追加されている
- [ ] `DefaultParameters()` に `LeaveLoss` と `Patience` の初期仮値が入っている
- [ ] `broadcastMsg` ヘルパが `session.go` に追加されている
- [ ] `stepPatience` が毎tickで行列先頭客の我慢ゲージを減算する
- [ ] ゲージ0で `CustomerLeft` + `CreditUpdate` を該当店へ配信する
- [ ] 属性別 `LeaveLoss.For` で信用が減る（Buzz=2, 他=1）
- [ ] 信用0で自滅脱落（`StoreEliminated` を全店ブロードキャスト、行列全客を restPool へ回収）
- [ ] Late フェーズで `LateMul` による我慢ゲージ短縮が効く
- [ ] 脱落済み店（`alive=false`）はスキップされる
- [ ] 全属性（Normal/Bonus/Claimer/Buzz）で離脱が発火する（#29 ガード）
- [ ] テスト: `TestStepPatience_BasicLeave` が通る
- [ ] テスト: `TestStepPatience_SelfCollapse` が通る
- [ ] テスト: `TestStepPatience_AttributeLeaveLoss` が通る
- [ ] テスト: `TestStepPatience_HeadOnly` が通る
- [ ] テスト: `TestStepPatience_ServePreventLeave` が通る
- [ ] テスト: `TestStepPatience_AllAttributes` が通る（#29）
- [ ] テスト: `TestStepPatience_LatePhaseFaster` が通る
- [ ] テスト: `TestStepPatience_DeadStoreSkipped` が通る
- [ ] テスト: `TestBroadcastMsg` が通る
- [ ] テスト: `TestLeaveLoss_For` が通る
- [ ] `go build ./...` が通る
- [ ] `go test ./...` が通る（既存テスト含む）
- [ ] `go vet ./...` がクリーン
