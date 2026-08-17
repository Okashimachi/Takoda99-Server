package game

import (
	"math/rand"
	"testing"

	"takoda99/internal/proto"
)

// plan-h35 §2 で足した調整ツマミのテスト。
//
// 方針は「バグを注入すると落ちる」こと。特に
//   - 既定値（levelSpread=0 / levelOffset=0）で**現行と完全に同一**であること
//   - cull.warnMaxIds が 0（＝本番DBに無い状態）でも予告が消えないこと
// の2つは、デプロイした瞬間に挙動が変わらないことの保証なので厚めに書く。

// recordingWords は要求された level を記録する WordSource。
// 「実際にどの level の語を要求したか」を game 層だけで観測するために使う
// （game は odai を import できないので、辞書そのものは要らない）。
type recordingWords struct {
	levels []int
}

func (w *recordingWords) Next(effectiveLevel int, _ *rand.Rand) Word {
	w.levels = append(w.levels, effectiveLevel)
	return Word{Text: "たこ", KeystrokeCount: 4}
}

// ── wordLevel（お題の下駄とばらつき）──────────────────────────

// 既定（spread=0 / offset=0）では heatLevel と完全に一致すること。
//
// 🔴 ここが崩れると「デプロイした瞬間に難度が変わる」。plan-h35 §7.3 の中心。
func TestWordLevel_DefaultsMatchHeatLevelExactly(t *testing.T) {
	s := newTestSession(1)
	for heat := 0; heat <= 25; heat++ {
		s.heatLevel = heat
		if got := s.wordLevel(); got != heat {
			t.Fatalf("heatLevel=%d のとき wordLevel()=%d, want %d（既定値は現行と完全一致）", heat, got, heat)
		}
	}
}

// 既定では rng を一切消費しないこと（＝既存のシード再現性が変わらない）。
//
// 消費すると、お題ツマミを足しただけで客の生成・分配の乱数列がズレ、
// 既存テストや sim の実測値が「理由なく」変わる。
func TestWordLevel_DefaultsDoNotConsumeRng(t *testing.T) {
	a := newTestSession(1)
	b := newTestSession(1)

	a.heatLevel = 5
	for i := 0; i < 100; i++ {
		a.wordLevel()
	}

	if got, want := a.rng.Int63(), b.rng.Int63(); got != want {
		t.Fatalf("既定の wordLevel() が rng を消費している: %d != %d", got, want)
	}
}

// levelOffset が下駄として効くこと（+1 で1段上、−1 で1段下）。
func TestWordLevel_OffsetShiftsLevel(t *testing.T) {
	for _, off := range []int{-3, -1, 0, 1, 4} {
		params := DefaultParameters()
		params.Odai.LevelOffset = off
		s := newTestSessionWith(params, 1)
		s.heatLevel = 10
		if got, want := s.wordLevel(), 10+off; got != want {
			t.Fatalf("offset=%d: wordLevel()=%d, want %d", off, got, want)
		}
	}
}

// levelSpread が heatLevel±spread の範囲に収まり、かつ範囲を**使い切る**こと。
//
// 上限/下限の計算を間違える変異（例: rng.Intn(2*sp) や -sp の欠落）で落ちる。
func TestWordLevel_SpreadStaysInRangeAndCoversIt(t *testing.T) {
	const spread = 2
	const heat = 10

	params := DefaultParameters()
	params.Odai.LevelSpread = spread
	s := newTestSessionWith(params, 1)
	s.heatLevel = heat

	seen := map[int]int{}
	for i := 0; i < 5000; i++ {
		l := s.wordLevel()
		if l < heat-spread || l > heat+spread {
			t.Fatalf("wordLevel()=%d が範囲 [%d,%d] の外", l, heat-spread, heat+spread)
		}
		seen[l]++
	}
	for l := heat - spread; l <= heat+spread; l++ {
		if seen[l] == 0 {
			t.Fatalf("level %d が一度も出ていない（範囲を使い切っていない）: %v", l, seen)
		}
	}
}

// 下限クランプ。offset や spread がどれだけ下振れしても負にならないこと。
//
// 負の level は WordSource の下降ループ（for l := level-1; l >= 0）が一度も回らず、
// fallback 語だけが出続ける状態になる。
func TestWordLevel_NeverNegative(t *testing.T) {
	params := DefaultParameters()
	params.Odai.LevelOffset = -99
	params.Odai.LevelSpread = 3
	s := newTestSessionWith(params, 1)

	for heat := 0; heat <= 20; heat++ {
		s.heatLevel = heat
		for i := 0; i < 200; i++ {
			if l := s.wordLevel(); l < 0 {
				t.Fatalf("wordLevel()=%d が負（heatLevel=%d）", l, heat)
			}
		}
	}
}

// spread > 0 でも同じシードなら同じ語列になること（再現性の担保）。
func TestWordLevel_IsDeterministicWithSpread(t *testing.T) {
	run := func() []int {
		params := DefaultParameters()
		params.Odai.LevelSpread = 3
		s := newTestSessionWith(params, 1)
		s.heatLevel = 8
		out := make([]int, 0, 200)
		for i := 0; i < 200; i++ {
			out = append(out, s.wordLevel())
		}
		return out
	}
	a, b := run(), run()
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("同じシードで語のレベル列が再現しない: i=%d %d != %d", i, a[i], b[i])
		}
	}
}

// 実際に客へ配る語（admitCustomer）が wordLevel() の結果を使っていること。
//
// wordLevel() だけを直したつもりで呼び出し側が heatLevel を直接読んでいたら落ちる。
func TestAdmitCustomer_RequestsWordLevel(t *testing.T) {
	params := DefaultParameters()
	params.Odai.LevelOffset = -2
	params.Customer.Total = 0 // 自動生成を止めて手で置く

	rec := &recordingWords{}
	inits := []PlayerInit{{Id: "s-1", DisplayName: "s-1"}}
	params.Matching.ReadyCountdownMs = 0
	s := NewSession("test", params, rec, rand.New(rand.NewSource(1)), inits)
	s.heatLevel = 9

	restCustomer(s, proto.CustomerId("c-1"), proto.AttrNormal, 3)
	if _, ok := s.admitCustomer(proto.CustomerId("c-1"), "s-1"); !ok {
		t.Fatal("admitCustomer が失敗した")
	}

	if len(rec.levels) != 3 {
		t.Fatalf("要求された語数=%d, want 3（orderCount ぶん）", len(rec.levels))
	}
	for _, l := range rec.levels {
		if l != 7 {
			t.Fatalf("要求 level=%d, want 7（heatLevel 9 + offset -2）: %v", l, rec.levels)
		}
	}
}

// ── cull.warnMaxIds（足切り予告の件数上限）────────────────────

// warnMaxIds が予告の件数を実際に制限すること（上限を無視する変異で落ちる）。
func TestCullWarning_WarnMaxIdsLimitsCutStoreIds(t *testing.T) {
	for _, limit := range []int{3, 7, 24} {
		params := DefaultParameters()
		params.Customer.Total = 0
		params.Cull.WarnMaxIds = limit
		params.Cull.Stages[0] = CullStage{AtMs: 20000, TargetAliveCount: 1}

		s := newTestSessionWith(params, 40)
		s.Start(0)
		for i, sid := range s.order {
			s.stores[sid].score = i * 100
		}

		out := s.Tick(1000)
		warns := filterMsg[proto.ForcedEliminationWarning](out)
		if len(warns) == 0 {
			t.Fatalf("limit=%d: 予告が出ていない", limit)
		}
		// 候補は 39 店（40→1）なので、上限がそのまま件数になる。
		if got := len(warns[0].CutStoreIds); got != limit {
			t.Fatalf("limit=%d: CutStoreIds=%d件, want %d", limit, got, limit)
		}
	}
}

// 🔴 **warnMaxIds が 0（＝本番DBに無い状態）でも予告が消えないこと。**
//
// backfillDefaults の補完はグループ単位なので、本番DBに既に cull グループがある以上
// 新設の warnMaxIds は 0 のまま読まれうる。0 を素直に「上限0件」と解釈すると
// 足切り予告のIDが1件も届かず、右パネルが空になる（plan-h35 §7.3）。
func TestCullWarning_ZeroWarnMaxIdsFallsBackToDefault(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	params.Cull.WarnMaxIds = 0 // DB から読んだ想定（キーが無い）
	params.Cull.Stages[0] = CullStage{AtMs: 20000, TargetAliveCount: 1}

	s := newTestSessionWith(params, 40)
	s.Start(0)
	for i, sid := range s.order {
		s.stores[sid].score = i * 100
	}

	out := s.Tick(1000)
	warns := filterMsg[proto.ForcedEliminationWarning](out)
	if len(warns) == 0 {
		t.Fatal("予告が出ていない")
	}
	if got := len(warns[0].CutStoreIds); got != DefaultCullWarnMaxIds {
		t.Fatalf("warnMaxIds=0 のときの CutStoreIds=%d件, want %d（既定に読み替えるはず）",
			got, DefaultCullWarnMaxIds)
	}
}

// EffectiveWarnMaxIds の読み替え表。
func TestEffectiveWarnMaxIds(t *testing.T) {
	cases := []struct{ in, want int }{
		{in: 0, want: DefaultCullWarnMaxIds},
		{in: -5, want: DefaultCullWarnMaxIds},
		{in: 1, want: 1},
		{in: 24, want: 24},
		{in: 99, want: 99},
	}
	for _, c := range cases {
		cp := CullParams{WarnMaxIds: c.in}
		if got := cp.EffectiveWarnMaxIds(); got != c.want {
			t.Errorf("EffectiveWarnMaxIds(%d)=%d, want %d", c.in, got, c.want)
		}
	}
}
