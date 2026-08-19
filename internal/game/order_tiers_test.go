package game

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"takoda99/internal/proto"
)

// countsByAttribute は Start 後の客レジストリを「属性 → 個数 → 人数」に集計する。
func countsByAttribute(s *Session) map[proto.CustomerAttribute]map[int]int {
	out := map[proto.CustomerAttribute]map[int]int{}
	for _, c := range s.customers {
		if out[c.attribute] == nil {
			out[c.attribute] = map[int]int{}
		}
		out[c.attribute][c.orderCount]++
	}
	return out
}

// 🔴 **注文数が属性と独立に抽選される**（plan-h36 §7）。
//
// これが本 plan の本体。属性から個数を取る実装（h35 まで）に戻すと、
// 各属性の個数が1種類だけになるのでこのテストが落ちる。
func TestInitCustomers_OrderCountIsIndependentOfAttribute(t *testing.T) {
	s := newTestSession(3)
	s.Start(0)

	byAttr := countsByAttribute(s)
	if len(byAttr) < 2 {
		t.Fatalf("属性が1種類しか出ていない（前提が壊れている）: %v", byAttr)
	}
	want := map[int]bool{2: true, 4: true, 8: true}
	for attr, counts := range byAttr {
		if len(counts) < 2 {
			t.Errorf("属性 %s の注文数が %d 種類しかない: %v。"+
				"個数を属性から取る実装に戻っていないか（plan-h36）", attr, len(counts), counts)
		}
		for c := range counts {
			if !want[c] {
				t.Errorf("属性 %s に既定の段階に無い注文数 %d が出た: %v", attr, c, counts)
			}
		}
	}
	// すべての属性で3段階すべてが観測されること（5000人・最小の Buzz でも約250人なので確実に出る）。
	for attr, counts := range byAttr {
		for c := range want {
			if counts[c] == 0 {
				t.Errorf("属性 %s に注文数 %d の客が1人も出ていない: %v", attr, c, counts)
			}
		}
	}
}

// 抽選が重みどおりに出る（既定 2/4/8 = 35/35/30・平均 4.50個）。
func TestRollOrderCount_MatchesWeights(t *testing.T) {
	s := newTestSession(3)
	s.Start(0)

	total := s.params.Customer.Total
	got := map[int]int{}
	sum := 0
	for _, c := range s.customers {
		got[c.orderCount]++
		sum += c.orderCount
	}

	wantPct := map[int]float64{2: 35, 4: 35, 8: 30}
	for count, pct := range wantPct {
		actual := float64(got[count]) / float64(total) * 100
		if math.Abs(actual-pct) > 3 { // 5000人・±3ポイント（二項分布の3σ ≒ 2.0pt）
			t.Errorf("注文数 %d個 の割合が %.1f%%（期待 %.0f%% ±3）: %v", count, actual, pct, got)
		}
	}
	avg := float64(sum) / float64(total)
	if math.Abs(avg-4.50) > 0.15 {
		t.Errorf("平均注文数が %.2f 個（期待 4.50 ±0.15）", avg)
	}
	t.Logf("注文数の分布: 2個=%d 4個=%d 8個=%d（平均 %.2f個）", got[2], got[4], got[8], avg)
}

// 同じシードなら同じ客列になる（決定性）。
func TestInitCustomers_DeterministicWithSameSeed(t *testing.T) {
	build := func(seed int64) []string {
		params := DefaultParameters()
		params.Customer.Total = 300
		params.Matching.ReadyCountdownMs = 0
		params.Matching.RosterWaitMs = 0
		inits := []PlayerInit{{Id: PlayerId("s-1"), DisplayName: "s-1"}}
		s := NewSession("m", params, fakeWords{}, rand.New(rand.NewSource(seed)), inits)
		s.Start(0)
		out := make([]string, 0, params.Customer.Total)
		for i := 0; i < params.Customer.Total; i++ {
			c := s.customers[proto.CustomerId(fmt.Sprintf("c-%d", i+1))]
			out = append(out, fmt.Sprintf("%s/%d", c.attribute, c.orderCount))
		}
		return out
	}
	a, b, other := build(42), build(42), build(7)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("同じシードで結果が違う: [%d] %s != %s", i, a[i], b[i])
		}
	}
	same := true
	for i := range a {
		if a[i] != other[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("シードを変えても同じ結果になっている（rng を使っていない疑い）")
	}
}

// 🔴 EffectiveOrderTiers がゼロを**要素単位**で吸収する（plan-h36 §3）。
//
// 本番DBには `customer` グループが既にあるため orderTiers はゼロのまま読まれる。
// ここで吸収しないと「たこ焼き0個の客」が5000人生まれる。
func TestEffectiveOrderTiers_AbsorbsZeros(t *testing.T) {
	def := DefaultOrderTiers()

	t.Run("全要素ゼロ（本番DBに orderTiers が無い状態）", func(t *testing.T) {
		cp := CustomerParams{}
		if got := cp.EffectiveOrderTiers(); got != def {
			t.Fatalf("既定へ読み替えられていない: %+v", got)
		}
	})

	t.Run("1要素だけゼロ（config が2要素で保存した配列のゼロ埋め）", func(t *testing.T) {
		cp := CustomerParams{OrderTiers: [OrderTierCount]OrderTier{
			{Count: 1, Weight: 50},
			{Count: 3, Weight: 50},
			{}, // ゼロ埋め
		}}
		got := cp.EffectiveOrderTiers()
		if got[0] != (OrderTier{Count: 1, Weight: 50}) || got[1] != (OrderTier{Count: 3, Weight: 50}) {
			t.Fatalf("運営が入れた値が壊れた: %+v", got)
		}
		if got[2] != def[2] {
			t.Fatalf("ゼロ埋めされた要素が既定へ戻っていない: %+v", got[2])
		}
	})

	t.Run("count だけゼロ（0個の客を作らせない）", func(t *testing.T) {
		cp := CustomerParams{OrderTiers: [OrderTierCount]OrderTier{
			{Count: 0, Weight: 10},
			{Count: 4, Weight: 35},
			{Count: 8, Weight: 30},
		}}
		got := cp.EffectiveOrderTiers()
		if got[0].Count != def[0].Count {
			t.Fatalf("count=0 が既定へ読み替えられていない: %+v", got[0])
		}
		if got[0].Weight != 10 {
			t.Fatalf("運営が入れた重みまで書き換えた: %+v", got[0])
		}
	})

	t.Run("重みの合計が0（抽選できない）", func(t *testing.T) {
		cp := CustomerParams{OrderTiers: [OrderTierCount]OrderTier{
			{Count: 2, Weight: 0},
			{Count: 4, Weight: 0},
			{Count: 8, Weight: 0},
		}}
		if got := cp.EffectiveOrderTiers(); got != def {
			t.Fatalf("重み合計0は既定へ落とすべき: %+v", got)
		}
	})

	t.Run("重み0の段は潰さない（「この段は出さない」は正当な設定）", func(t *testing.T) {
		cp := CustomerParams{OrderTiers: [OrderTierCount]OrderTier{
			{Count: 2, Weight: 50},
			{Count: 4, Weight: 50},
			{Count: 8, Weight: 0}, // 8個を当日オフにした
		}}
		got := cp.EffectiveOrderTiers()
		if got[2].Weight != 0 || got[2].Count != 8 {
			t.Fatalf("重み0の段が既定へ巻き戻された: %+v", got[2])
		}
	})

	t.Run("運営の値はそのまま使う", func(t *testing.T) {
		want := [OrderTierCount]OrderTier{{Count: 1, Weight: 10}, {Count: 5, Weight: 20}, {Count: 9, Weight: 30}}
		cp := CustomerParams{OrderTiers: want}
		if got := cp.EffectiveOrderTiers(); got != want {
			t.Fatalf("運営の値が書き換えられた: %+v", got)
		}
	})
}

// ゼロの orderTiers で試合を始めても「0個の客」が生まれない（EffectiveOrderTiers を
// 通さずに rollOrderCount を呼ぶ実装への回帰を防ぐ）。
func TestInitCustomers_ZeroOrderTiersNeverProducesZeroOrders(t *testing.T) {
	params := DefaultParameters()
	params.Customer.OrderTiers = [OrderTierCount]OrderTier{} // 本番DB相当（補完前）
	s := newTestSessionWith(params, 3)
	s.Start(0)

	for cid, c := range s.customers {
		if c.orderCount <= 0 {
			t.Fatalf("注文数 %d の客が生まれた (%s)。EffectiveOrderTiers を通していない", c.orderCount, cid)
		}
	}
}

// 段階の個数と重みを config から変えると、実際の分布が変わる（＝効くツマミである）。
func TestRollOrderCount_ConfigDrivesDistribution(t *testing.T) {
	params := DefaultParameters()
	params.Customer.OrderTiers = [OrderTierCount]OrderTier{
		{Count: 3, Weight: 1},
		{Count: 6, Weight: 0},
		{Count: 9, Weight: 0},
	}
	s := newTestSessionWith(params, 3)
	s.Start(0)

	for cid, c := range s.customers {
		if c.orderCount != 3 {
			t.Fatalf("重み 1/0/0 なのに注文数 %d の客が出た (%s)", c.orderCount, cid)
		}
	}
}
