package game

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"

	"takoda99/internal/proto"
)

// ── テストユーティリティ ──────────────────────────────────────

type fakeWords struct{}

func (fakeWords) Next(_ int, _ *rand.Rand) Word {
	return Word{Text: "たこ", KeystrokeCount: 4}
}

func newTestSession(n int) *Session {
	return newTestSessionWith(DefaultParameters(), n)
}

// disableCull は足切りを事実上無効化する（そのテストの関心事でない場合に使う）。
//
// 本戦の足切りは**時刻**で発火するので、予選のように周期を 0 にする
// （storm.intervalTicks = 0）やり方は使えない。ステージ時刻を遠い未来へ飛ばす。
func disableCull(s *Session) {
	for i := range s.params.Cull.Stages {
		s.params.Cull.Stages[i].AtMs = 1_000_000_000 + i*1000
	}
}

func newTestSessionWith(params GameParameters, n int) *Session {
	params.Matching.ReadyCountdownMs = 0
	params.Matching.RosterWaitMs = 0
	inits := make([]PlayerInit, n)
	for i := range inits {
		id := PlayerId(fmt.Sprintf("s-%d", i+1))
		inits[i] = PlayerInit{Id: id, DisplayName: string(id)}
	}
	s := NewSession("test-match", params, fakeWords{}, rand.New(rand.NewSource(42)), inits)
	s.customerSeq = 1000000
	return s
}

func placeAssigned(s *Session, cid proto.CustomerId, store PlayerId, attr proto.CustomerAttribute, orderCount, keystrokes int) {
	c := &customer{
		attribute:      attr,
		orderCount:     orderCount,
		keystrokeTotal: keystrokes,
		assignedStore:  &store,
	}
	s.customers[cid] = c
	s.storeQueues[store] = append(s.storeQueues[store], cid)
}

// restCustomer は未割当（restPool）の客を1人置く。
func restCustomer(s *Session, cid proto.CustomerId, attr proto.CustomerAttribute, orderCount int) {
	s.customers[cid] = &customer{attribute: attr, orderCount: orderCount}
	s.restPool = append(s.restPool, cid)
}

// assertZeroOnWire は「サーバーが値を入れない」廃止フィールドが、実際のワイヤ形式で
// ゼロ値（または omitempty で欠落）になっていることを確かめる。
//
// 🔴 **廃止フィールドを Go の識別子で名指ししないこと。** 名指しすると staticcheck の
// SA1019 になり、「移行が終わったか」を lint で検知する仕組み（plan-h20 §2.2）が死ぬ。
// そもそも「サーバーは値を入れない」はワイヤ上の主張なので、JSON で見るほうが忠実。
func assertZeroOnWire(t *testing.T, v any, keys ...string) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range keys {
		got, ok := m[k]
		if !ok {
			continue // omitempty で消えているのも「値を入れていない」
		}
		switch x := got.(type) {
		case float64:
			if x != 0 {
				t.Fatalf("廃止フィールド %q に値が入っている: %v （ワイヤ: %s）", k, x, raw)
			}
		case string:
			if x != "" {
				t.Fatalf("廃止フィールド %q に値が入っている: %q （ワイヤ: %s）", k, x, raw)
			}
		default:
			t.Fatalf("廃止フィールド %q の型が想定外: %T", k, got)
		}
	}
}

// ── テストケース ──────────────────────────────────────────────

func TestSession_CountdownDelay(t *testing.T) {
	params := DefaultParameters()
	params.Matching.ReadyCountdownMs = 5000 // 5秒
	// newTestSessionWith は ReadyCountdownMs を 0 に上書きしてしまうので直接生成する
	inits := make([]PlayerInit, 1)
	inits[0] = PlayerInit{Id: "test-player"}
	s := NewSession("test", params, fakeWords{}, rand.New(rand.NewSource(42)), inits)

	s.Start(0)
	if s.elapsedMs != -5000 {
		t.Fatalf("Start時のelapsedMsが -5000 になっていない: %d", s.elapsedMs)
	}

	out := s.Tick(2000)
	if s.elapsedMs != -3000 {
		t.Fatalf("2秒後のelapsedMsが -3000 になっていない: %d", s.elapsedMs)
	}
	if len(out) != 0 {
		t.Fatalf("カウントダウン中に出力が出てはいけない")
	}

	_ = s.Tick(3000)
	if s.elapsedMs != 0 {
		t.Fatalf("5秒後のelapsedMsが 0 になっていない: %d", s.elapsedMs)
	}

	// 0以降でティックが進めばイベントが出る
	_ = s.Tick(100)
	if s.elapsedMs != 100 {
		t.Fatalf("5.1秒後のelapsedMsが 100 になっていない: %d", s.elapsedMs)
	}
}

func TestBroadcastMsg(t *testing.T) {
	msg := proto.StoreEliminated{StoreId: "test", Reason: proto.ElimCull, FinalRank: 4}
	o := broadcastMsg(msg)
	if !o.To.Broadcast {
		t.Fatal("Broadcast=true のはず")
	}
	if o.To.PlayerId != "" {
		t.Fatalf("Broadcast 時 PlayerId は空のはず: %q", o.To.PlayerId)
	}
	if _, ok := o.Msg.(proto.StoreEliminated); !ok {
		t.Fatalf("StoreEliminated でない: %T", o.Msg)
	}
}

// ── stepDistribute / stepRank テスト ──────────────────────────

func TestStepDistribute_EvenInitial(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 5}

	for i := 0; i < 9; i++ {
		cid := proto.CustomerId(fmt.Sprintf("d-%d", i))
		s.customers[cid] = &customer{
			attribute:  proto.AttrNormal,
			orderCount: 1,
		}
		s.restPool = append(s.restPool, cid)
	}

	out := s.stepDistribute(nil)
	if len(out) != 9 {
		t.Fatalf("9件の CustomerArrived のはず: %d", len(out))
	}

	for _, sid := range s.order {
		ql := len(s.storeQueues[sid])
		if ql == 0 {
			t.Fatalf("店 %s に1人も分配されていない", sid)
		}
	}

	if len(s.restPool) != 0 {
		t.Fatalf("restPool が空のはず: %d", len(s.restPool))
	}
}

func TestStepDistribute_QueueLengthSuppression(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 10}

	storeA := s.order[0]
	storeB := s.order[1]
	for i := 0; i < 5; i++ {
		cid := proto.CustomerId(fmt.Sprintf("pre-%d", i))
		s.customers[cid] = &customer{
			attribute:  proto.AttrNormal,
			orderCount: 1,
		}
		s.restPool = append(s.restPool, cid)
		s.assignCustomer(cid, storeA)
	}

	for i := 0; i < 20; i++ {
		cid := proto.CustomerId(fmt.Sprintf("q-%d", i))
		s.customers[cid] = &customer{
			attribute:  proto.AttrNormal,
			orderCount: 1,
		}
		s.restPool = append(s.restPool, cid)
	}

	s.stepDistribute(nil)

	qA := len(s.storeQueues[storeA])
	qB := len(s.storeQueues[storeB])

	newA := qA - 5
	if newA >= qB {
		t.Fatalf("行列が短い店B(%d) のほうが多く分配されるはず (店Aの新規=%d)", qB, newA)
	}
}

func TestStepDistribute_ClaimerBlockedInEarly(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.phase = proto.PhaseEarly
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 5}

	for i := 0; i < 5; i++ {
		cid := proto.CustomerId(fmt.Sprintf("cl-%d", i))
		s.customers[cid] = &customer{
			attribute:  proto.AttrClaimer,
			orderCount: 1,
		}
		s.restPool = append(s.restPool, cid)
	}

	out := s.stepDistribute(nil)

	if len(out) != 0 {
		t.Fatalf("Early で Claimer は分配されないはず: %d 件出力", len(out))
	}
	if len(s.restPool) != 5 {
		t.Fatalf("restPool に5人残るはず: %d", len(s.restPool))
	}

	s.phase = proto.PhaseMid
	out = s.stepDistribute(nil)
	if len(out) != 5 {
		t.Fatalf("Mid では Claimer が分配されるはず: %d 件出力", len(out))
	}
}

func TestStepDistribute_EmptyRestPool(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 3}

	out := s.stepDistribute(nil)
	if out != nil {
		t.Fatalf("空 restPool で出力なしのはず: %v", out)
	}
}

// ── stepPhase / stepHeat テスト ──────────────────────────────

func filterMsg[T any](out []Outbound) []T {
	var result []T
	for _, o := range out {
		if msg, ok := o.Msg.(T); ok {
			result = append(result, msg)
		}
	}
	return result
}

func TestStepPhase_AliveThreshold(t *testing.T) {
	s := newTestSession(99)
	s.Start(0)
	disableCull(s)

	if s.phase != proto.PhaseEarly {
		t.Fatalf("初期は Early のはず: %v", s.phase)
	}

	s.aliveCount = s.params.Phase.MidAliveThreshold
	out := s.Tick(150)
	if s.phase != proto.PhaseMid {
		t.Fatalf("aliveCount=%d で Mid に移行するはず: %v", s.params.Phase.MidAliveThreshold, s.phase)
	}
	phaseChanges := filterMsg[proto.PhaseChange](out)
	if len(phaseChanges) == 0 {
		t.Fatal("PhaseChange が配信されるはず")
	}
	if phaseChanges[0].Phase != proto.PhaseMid {
		t.Fatalf("PhaseChange.Phase=Mid のはず: %v", phaseChanges[0].Phase)
	}

	s.aliveCount = s.params.Phase.LateAliveThreshold
	_ = s.Tick(150)
	if s.phase != proto.PhaseLate {
		t.Fatalf("aliveCount=%d で Late に移行するはず: %v", s.params.Phase.LateAliveThreshold, s.phase)
	}
}

func TestStepPhase_TimeThreshold(t *testing.T) {
	s := newTestSession(99)
	// 実 tick の数百倍の dt を1回で流す。予選では「客が一斉に離脱して店が全滅し、
	// Tick が素通りする」のを避けるため我慢を伸ばす必要があったが、
	// 本戦では客が逃げないので下準備は要らない。
	s.Start(0)
	disableCull(s)

	midMs := s.params.Phase.MidTimeMs
	s.Tick(midMs)
	if s.phase != proto.PhaseMid {
		t.Fatalf("elapsedMs=%d で Mid に移行するはず: %v", midMs, s.phase)
	}

	lateMs := s.params.Phase.LateTimeMs - midMs
	s.Tick(lateMs)
	if s.phase != proto.PhaseLate {
		t.Fatalf("elapsedMs=%d で Late に移行するはず: %v", s.params.Phase.LateTimeMs, s.phase)
	}
}

func TestStepHeat_Calculation(t *testing.T) {
	s := newTestSession(99)
	s.Start(0)
	disableCull(s)
	hp := s.params.Heat

	s.Tick(150)
	wantEarly := hp.Base + hp.PhaseEarly
	if s.heatLevel != wantEarly {
		t.Fatalf("Early全員生存の fire=%d のはず: %d", wantEarly, s.heatLevel)
	}

	s.aliveCount = 49
	s.phase = proto.PhaseMid
	s.Tick(150)
	wantMid := hp.Base + int(hp.PerAliveDrop*float64(99-49)) + hp.PhaseMid
	if s.heatLevel != wantMid {
		t.Fatalf("Mid, alive=49 の fire=%d のはず: %d", wantMid, s.heatLevel)
	}
}

// ── Plan-05: checkFinish / Results テスト ─────────────────────

// ── proto v0.3.0 追随（#33）──

// summaries() は脱落店にだけ finalRank を入れる。
//
// 生存店に 0 を入れて送ると「順位0」という存在しない順位をクライアントに渡すことになる。
// 契約側は omitempty つきポインタなので、nil のままなら JSON にキーごと出ない。
func TestSummaries_FinalRankOnlyForEliminated(t *testing.T) {
	s := newTestSession(3)
	s.state = Running

	// s-2 だけ脱落済みにする。
	dead := s.stores[PlayerId("s-2")]
	dead.alive = false
	dead.finalRank = 3

	for _, sum := range s.summaries() {
		switch sum.StoreId {
		case "s-2":
			if sum.FinalRank == nil {
				t.Fatal("脱落店に finalRank が入っていない")
			}
			if *sum.FinalRank != 3 {
				t.Fatalf("finalRank=%d, want 3", *sum.FinalRank)
			}
		default:
			if sum.FinalRank != nil {
				t.Fatalf("生存店 %s に finalRank が入っている: %d", sum.StoreId, *sum.FinalRank)
			}
		}
	}

	// 実際の JSON にもキーが出ないことを確認する。
	alive := s.summaries()[0]
	b, err := json.Marshal(alive)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "finalRank") {
		t.Fatalf("生存店の JSON に finalRank キーが出ている: %s", b)
	}
}

// ── proto v0.3.0 値算出（#64）──

// 演出しきい値が公開パラメータに載る（既定値）。
func TestPublicParams_PresentationThresholds(t *testing.T) {
	s := newTestSession(3)
	p := s.publicParams()
	def := DefaultParameters().Presentation
	if p.FinalStageAliveThreshold != def.FinalStageAliveThreshold {
		t.Fatalf("finalStageAliveThreshold=%d, want %d", p.FinalStageAliveThreshold, def.FinalStageAliveThreshold)
	}
	if p.FinalRushAliveThreshold != def.FinalRushAliveThreshold {
		t.Fatalf("finalRushAliveThreshold=%d, want %d", p.FinalRushAliveThreshold, def.FinalRushAliveThreshold)
	}
	if def.FinalStageAliveThreshold == 0 || def.FinalRushAliveThreshold == 0 {
		t.Fatal("既定値が0のまま（クライアントが演出切替できない）")
	}
}

// ── 我慢ゲージのタイムアウトを全属性で保証する（#29）──
//
// Textro #78 の類型: 種別で時間切れ判定を分岐した結果、一部の種別がタイムアウトから
// 漏れ、キューの先頭が永遠に消化されず固まった。
//
// Takoda の相当機構は「客の我慢ゲージ → 離脱 → 信用減」だった。属性ごとに離脱の
// 発火可否を分岐すると、漏れた属性の客が行列に居座って店の対応が永久に止まる。
// 属性差は「減算量やペナルティ量」で表現し、**離脱が起きうること自体は全属性で不変**。

// heatLevel が heat.maxLevel を超えないこと（#75）。
//
// 超えた値を配ると、WordSource は下の段階へ降りるだけなので難度は変わらないのに
// クライアントの heatLevel 表示と運営UIの maxLevel が実態と食い違う。
func TestStepHeat_ClampsToMaxLevel(t *testing.T) {
	params := DefaultParameters()
	params.Heat.MaxLevel = 3
	params.Heat.Base = 0
	params.Heat.PerAliveDrop = 1 // 1店脱落ごとに +1（すぐ上限を超える）
	params.Heat.PhaseEarly = 0
	params.Heat.PhaseMid = 5
	params.Heat.PhaseLate = 10

	s := newTestSessionWith(params, 20)
	s.Start(0)
	maxSeen := 0
	for i := 0; i < 3000 && s.State() != Finished; i++ {
		for _, o := range s.Tick(params.Session.TickIntervalMs) {
			if d, ok := o.Msg.(proto.DifficultyUpdate); ok {
				if d.HeatLevel > maxSeen {
					maxSeen = d.HeatLevel
				}
				if d.HeatLevel > params.Heat.MaxLevel {
					t.Fatalf("heatLevel %d が maxLevel %d を超えた", d.HeatLevel, params.Heat.MaxLevel)
				}
			}
		}
	}
	if maxSeen != params.Heat.MaxLevel {
		t.Fatalf("上限 %d に到達していない（maxSeen=%d）。テストが上限を突いていない", params.Heat.MaxLevel, maxSeen)
	}
}

// heat.base に負値が入っても heatLevel が負にならないこと。
//
// 負のまま WordSource へ渡すと下降ループが1回も回らず、全店がフォールバック語固定になる。
func TestStepHeat_NeverGoesNegative(t *testing.T) {
	params := DefaultParameters()
	params.Heat.Base = -50
	params.Heat.PerAliveDrop = 0
	params.Heat.PhaseEarly = 0
	params.Heat.PhaseMid = 0
	params.Heat.PhaseLate = 0

	s := newTestSessionWith(params, 5)
	s.Start(0)
	for i := 0; i < 500 && s.State() != Finished; i++ {
		for _, o := range s.Tick(params.Session.TickIntervalMs) {
			if d, ok := o.Msg.(proto.DifficultyUpdate); ok && d.HeatLevel < 0 {
				t.Fatalf("heatLevel が負になった: %d", d.HeatLevel)
			}
		}
	}
}

// ── 本戦: スコア制（plan-h21）────────────────────────────────

// ApplyOrderServed 1回で score が W_TAKOYAKI×たこ焼き数 − W_MISS×ミス数 ぶん増える。
//
// 重みを取り違える変異（掛ける相手を入れ替える／符号を逆にする）で落ちるよう、
// たこ焼き数・ミス数・両重みを**すべて異なる値**にしてある。
func TestApplyOrderServed_ScoreDelta(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	params.Score = ScoreParams{WeightTakoyaki: 100, WeightMiss: 7}
	params.Sanity.MinMsPerWord = 0
	s := newTestSessionWith(params, 2)
	s.Start(0)

	// たこ焼き3個・打鍵50・ミス4 → 100*3 − 7*4 = 272
	placeAssigned(s, "c-1", "s-1", proto.AttrNormal, 3, 50)
	out := s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-1", ElapsedMs: 3000, MissCount: 4})

	const want = 100*3 - 7*4
	if got := s.stores["s-1"].score; got != want {
		t.Fatalf("score=%d, want %d (=W_T×3 − W_M×4)", got, want)
	}
	if got := s.stores["s-1"].served.takoyaki; got != 3 {
		t.Fatalf("takoyaki=%d, want 3", got)
	}

	// 自店への EvaluationUpdate に score が載る（自店順位の権威）。
	evs := filterMsg[proto.EvaluationUpdate](out)
	if len(evs) != 1 {
		t.Fatalf("EvaluationUpdate は1件のはず: %d", len(evs))
	}
	if evs[0].Score != want {
		t.Fatalf("EvaluationUpdate.Score=%d, want %d", evs[0].Score, want)
	}

	// 2人目を捌けば累積する（上書きでない）。
	placeAssigned(s, "c-2", "s-1", proto.AttrNormal, 1, 10)
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-2", ElapsedMs: 1000, MissCount: 0})
	if got := s.stores["s-1"].score; got != want+100 {
		t.Fatalf("累積していない: score=%d, want %d", got, want+100)
	}
}

// 属性はスコアに一切影響しない（予選の「同じように打ったのに評価が違う」の再発防止）。
func TestApplyOrderServed_AttributeDoesNotAffectScore(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	params.Score = ScoreParams{WeightTakoyaki: 100, WeightMiss: 30}

	scores := map[proto.CustomerAttribute]int{}
	for _, attr := range []proto.CustomerAttribute{
		proto.AttrNormal, proto.AttrBonus, proto.AttrClaimer, proto.AttrBuzz,
	} {
		s := newTestSessionWith(params, 2)
		s.Start(0)
		placeAssigned(s, "c-1", "s-1", attr, 2, 20)
		s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-1", ElapsedMs: 3000, MissCount: 2})
		scores[attr] = s.stores["s-1"].score
	}
	want := scores[proto.AttrNormal]
	for attr, got := range scores {
		if got != want {
			t.Fatalf("属性 %s のスコアが違う: %d, want %d（属性で加減点してはいけない）", attr, got, want)
		}
	}
}

// スコアは 0 でクランプしない（plan-h21 §1.1）。
//
// 0 で止めると下位が全員ぴったり 0 に密集し、足切りで切る店の選択が恣意的になる。
func TestApplyOrderServed_ScoreGoesNegative(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	params.Score = ScoreParams{WeightTakoyaki: 100, WeightMiss: 30}
	s := newTestSessionWith(params, 2)
	s.Start(0)

	// たこ焼き1個(+100)・ミス10(−300) → −200
	placeAssigned(s, "c-1", "s-1", proto.AttrNormal, 1, 40)
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-1", ElapsedMs: 5000, MissCount: 10})

	if got := s.stores["s-1"].score; got != -200 {
		t.Fatalf("score=%d, want -200（0でクランプしてはいけない）", got)
	}
}

// ミス数は打鍵数を超えない（申告のサニティ検証は残る）。
func TestApplyOrderServed_MissClampedToKeystrokes(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	params.Score = ScoreParams{WeightTakoyaki: 100, WeightMiss: 1}
	s := newTestSessionWith(params, 2)
	s.Start(0)

	// 打鍵10に対してミス9999を申告 → 10 にクランプされる。
	placeAssigned(s, "c-1", "s-1", proto.AttrNormal, 1, 10)
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-1", ElapsedMs: 3000, MissCount: 9999})

	if got := s.stores["s-1"].score; got != 100-10 {
		t.Fatalf("score=%d, want 90（ミスは打鍵数でクランプ）", got)
	}
}

// stepRank はスコア降順に rank を振る。
func TestStepRank_ScoreDescending(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.aliveCount = 3

	s.stores["s-1"].score = 100
	s.stores["s-2"].score = 300
	s.stores["s-3"].score = 200

	out := s.stepRank(nil)
	if len(out) != 3 {
		t.Fatalf("3件の EvaluationUpdate のはず: %d", len(out))
	}

	want := map[PlayerId]int{"s-2": 1, "s-3": 2, "s-1": 3}
	for sid, w := range want {
		if got := s.stores[sid].rank; got != w {
			t.Fatalf("%s の rank=%d, want %d", sid, got, w)
		}
	}
	for _, o := range out {
		ev := o.Msg.(proto.EvaluationUpdate)
		if ev.AliveCount != 3 {
			t.Fatalf("AliveCount=%d, want 3", ev.AliveCount)
		}
		if ev.Rank != s.stores[o.To.PlayerId].rank {
			t.Fatalf("配信 rank と店の rank が食い違う: %d vs %d", ev.Rank, s.stores[o.To.PlayerId].rank)
		}
		// 相対評価・星は廃止。ゼロ値のまま送る。
		assertZeroOnWire(t, ev, "evalRaw", "normalized", "starRating", "starDelta")
	}
}

// 負のスコアの店も含めて順位が付く（クランプされていないことの順位側の確認）。
func TestStepRank_HandlesNegativeScores(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.aliveCount = 3

	s.stores["s-1"].score = -500
	s.stores["s-2"].score = 0
	s.stores["s-3"].score = -100

	s.stepRank(nil)

	want := map[PlayerId]int{"s-2": 1, "s-3": 2, "s-1": 3}
	for sid, w := range want {
		if got := s.stores[sid].rank; got != w {
			t.Fatalf("%s の rank=%d, want %d", sid, got, w)
		}
	}
}

// タイブレークは score → 正確性 → 速度 → storeId（plan-h21 §2.1）。
func TestWeakerForRank_TiebreakOrder(t *testing.T) {
	mk := func(id PlayerId, score, keys, misses, count int, elapsedSum int64) *storeState {
		return &storeState{id: id, score: score, served: servedStats{
			keystrokes: keys, misses: misses, count: count, elapsedSum: elapsedSum,
		}}
	}

	// 1) score が低い方が下位（他の項目が全部有利でも覆らない）。
	lowScore := mk("a", 100, 100, 0, 10, 10000)  // 精度100%・速い
	highScore := mk("b", 200, 100, 90, 1, 99000) // 精度10%・遅い
	if !weakerForRank(lowScore, highScore) {
		t.Fatal("score が低い方が下位のはず（score が最優先）")
	}

	// 2) score が同じなら正確性。
	sloppy := mk("a", 100, 100, 50, 10, 10000) // 精度50%
	precise := mk("b", 100, 100, 10, 10, 10000)
	if !weakerForRank(sloppy, precise) {
		t.Fatal("正確性が低い方が下位のはず")
	}

	// 3) score・正確性が同じなら速度（遅い方が下位）。
	slow := mk("a", 100, 100, 10, 10, 90000)
	fast := mk("b", 100, 100, 10, 10, 10000)
	if !weakerForRank(slow, fast) {
		t.Fatal("平均所要が大きい（遅い）方が下位のはず")
	}

	// 4) 全部同値でも storeId で決定的に順序が付く（揺れない）。
	x, y := mk("a", 100, 100, 10, 10, 10000), mk("b", 100, 100, 10, 10, 10000)
	if weakerForRank(x, y) == weakerForRank(y, x) {
		t.Fatal("同値でも決定的な順序が付くべき（map 反復順に依存させない）")
	}
}

// 🔴 未提供店（keystrokes=0 / count=0）を含めてもゼロ除算・panic せず、
// かつ提供済みの店より下位に来る。20秒地点で必ず出る状況。
func TestStepRank_UnservedStoreIsSafeAndLast(t *testing.T) {
	s := newTestSession(3)
	s.state = Running
	s.aliveCount = 3

	// 全店 score=0。s-2 だけ提供実績があり、s-1/s-3 は1件も提供していない。
	s.stores["s-2"].served = servedStats{count: 1, keystrokes: 10, misses: 0, elapsedSum: 3000}

	s.stepRank(nil)

	if s.stores["s-2"].rank != 1 {
		t.Fatalf("提供実績のある店が1位のはず: rank=%d", s.stores["s-2"].rank)
	}
	// 未提供の2店は accuracy=0 / 平均所要=+∞ で最下位側。順序は storeId で決定的。
	if s.stores["s-1"].rank != 2 || s.stores["s-3"].rank != 3 {
		t.Fatalf("未提供店の順序が決定的でない: s-1=%d s-3=%d",
			s.stores["s-1"].rank, s.stores["s-3"].rank)
	}

	// 未提供店のタイブレーク値そのものも確認する（+∞ が NaN になっていないこと）。
	un := s.stores["s-1"]
	if un.rankAccuracy() != 0 {
		t.Fatalf("未提供店の rankAccuracy=%v, want 0", un.rankAccuracy())
	}
	if !math.IsInf(un.rankAvgElapsedMs(), 1) {
		t.Fatalf("未提供店の rankAvgElapsedMs=%v, want +Inf", un.rankAvgElapsedMs())
	}
}

// 99店・全員未提供でも panic せず、並びが毎回同じになる（matchsim の再現性）。
func TestStepRank_DeterministicWithAllUnserved(t *testing.T) {
	rankOnce := func() []PlayerId {
		s := newTestSession(99)
		s.state = Running
		s.stepRank(nil)
		ranked := make([]PlayerId, 99)
		for _, sid := range s.order {
			ranked[s.stores[sid].rank-1] = sid
		}
		return ranked
	}
	first := rankOnce()
	for i := 0; i < 5; i++ {
		if got := rankOnce(); !equalIds(got, first) {
			t.Fatalf("全員同値のとき並びが揺れている\n1回目=%v\n%d回目=%v", first, i+2, got)
		}
	}
}

func equalIds(a, b []PlayerId) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 分配はスコアを見ない（plan-h21 §4）。
//
// スコアで客の来やすさを変えると「高スコア→客増→さらに伸びる」の正のフィードバックで
// 序盤の小差が終盤に発散し、決勝の逆転劇が死ぬ。
func TestStepDistribute_IgnoresScore(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	// 閾値は 400人が全部入る大きさにする（途中で候補から外れると偏りが測れない）。
	s.params.Distribution = DistributionParams{QueueRefillThreshold: 1000}

	// 極端なスコア差を付ける。行列長は両方0。
	s.stores["s-1"].score = 100000
	s.stores["s-2"].score = -100000

	for i := 0; i < 400; i++ {
		restCustomer(s, proto.CustomerId(fmt.Sprintf("n-%d", i)), proto.AttrNormal, 1)
	}
	s.stepDistribute(nil)

	q1 := len(s.storeQueues["s-1"])
	q2 := len(s.storeQueues["s-2"])
	if q1+q2 != 400 {
		t.Fatalf("400人が分配されるはず: s-1=%d s-2=%d", q1, q2)
	}
	// 重みは行列長のみ。スコア差があっても偏らない（±20% 以内に収まる）。
	diff := q1 - q2
	if diff < 0 {
		diff = -diff
	}
	if diff > 80 {
		t.Fatalf("スコアで分配が偏っている: s-1=%d s-2=%d（差 %d）", q1, q2, diff)
	}
}

// tick が本戦で使う種類のメッセージしか返さない。
//
// 我慢ゲージ・信用の廃止で CustomerLeft / CreditUpdate は internal/proto の再輸出ごと
// 消えた（h23 §5.1）ので、型を名指しで検査することはもうできない。代わりに
// **許可リストにない型が出たら落とす**形にした。廃止処理が復活したらここで気づける。
//
// 客を行列に置いたまま長時間 tick を回しても離脱も信用減も起きないこと
// （＝「一度出たお題は必ず打ち切られる」）も同時に見ている。
func TestTick_EmitsOnlyHonsenMessages(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 3)
	disableCull(s) // 足切りは別テストの担当（200秒回すので既定だと全店落ちる）
	s.Start(0)

	for _, sid := range s.order {
		placeAssigned(s, proto.CustomerId("c-"+string(sid)), sid, proto.AttrNormal, 1, 10)
	}

	allowed := map[string]bool{
		"proto.EvaluationUpdate":         true,
		"proto.ForcedEliminationWarning": true,
		"proto.CustomerView":             true,
		"proto.DifficultyUpdate":         true,
		"proto.PhaseChange":              true,
		"proto.StoreEliminatedBatch":     true,
		"proto.RankingSnapshot":          true,
		"proto.PersonalResult":           true,
		"proto.MatchEnd":                 true,
	}
	for i := 0; i < 200; i++ {
		out := s.Tick(1000) // 合計200秒。予選なら全員とっくに離脱している
		for _, o := range out {
			if name := fmt.Sprintf("%T", o.Msg); !allowed[name] {
				t.Fatalf("許可されていないメッセージが出た: %s（廃止処理の復活を疑う）", name)
			}
		}
	}

	// 置いた客は行列に残ったまま、店は全員生存している。
	if s.aliveCount != 3 {
		t.Fatalf("自滅が起きている: aliveCount=%d, want 3", s.aliveCount)
	}
	for _, sid := range s.order {
		if len(s.storeQueues[sid]) != 1 {
			t.Fatalf("%s の客が消えている: %v", sid, s.storeQueues[sid])
		}
	}
}

// CustomerArrived に我慢ゲージの値が載らない。
func TestAdmitCustomer_NoPatienceFields(t *testing.T) {
	s := newTestSession(2)
	s.state = Running
	s.elapsedMs = 4200

	restCustomer(s, "c-1", proto.AttrNormal, 2)
	ob, ok := s.admitCustomer("c-1", s.order[0])
	if !ok {
		t.Fatal("admitCustomer が失敗した")
	}
	cv := ob.Msg.(proto.CustomerView)
	assertZeroOnWire(t, cv, "patienceMaxMs", "patienceStartedAtServerMs")
	if cv.OrderCount != 2 || len(cv.Words) != 2 {
		t.Fatalf("お題が2語配られるはず: %+v", cv)
	}
}

// 公開パラメータにスコアの重みが載り、廃止キーには値が入らない。
func TestPublicParams_ScoreWeightsAndNoRetiredKeys(t *testing.T) {
	params := DefaultParameters()
	params.Score = ScoreParams{WeightTakoyaki: 120, WeightMiss: 25}
	s := newTestSessionWith(params, 3)

	p := s.publicParams()
	if p.ScoreWeightTakoyaki != 120 || p.ScoreWeightMiss != 25 {
		t.Fatalf("スコア重みが配られていない: %+v", p)
	}
	if p.MaxStores != 3 {
		t.Fatalf("maxStores=%d, want 3", p.MaxStores)
	}
	// 廃止フィールドはゼロ値のまま（クライアントに「効く値」と誤解させない）。
	assertZeroOnWire(t, p, "initialLife", "stormThresholdPct", "patienceLateMul", "patienceAlertMs")

	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "matchTimeLimitMs") {
		t.Fatalf("公開パラメータに matchTimeLimitMs が残っている: %s", b)
	}
}

// リザルトが score / takoyakiCount を載せ、廃止フィールドを載せないこと。
func TestPersonalResult_CarriesScoreNotCredit(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	params.Score = ScoreParams{WeightTakoyaki: 100, WeightMiss: 30}
	s := newTestSessionWith(params, 2)
	s.Start(0)

	// s-1 が2人（たこ焼き 2+4=6個）を捌く。
	placeAssigned(s, "c-1", "s-1", proto.AttrNormal, 2, 20)
	placeAssigned(s, "c-2", "s-1", proto.AttrBuzz, 4, 40)
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-1", ElapsedMs: 3000, MissCount: 1})
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-2", ElapsedMs: 5000, MissCount: 2})

	st := s.stores["s-1"]
	st.finalRank = 1
	got := s.buildPersonalResult(st)

	wantScore := 100*6 - 30*3
	if got.Score != wantScore {
		t.Fatalf("Score=%d, want %d", got.Score, wantScore)
	}
	if got.TakoyakiCount != 6 {
		t.Fatalf("TakoyakiCount=%d, want 6（客数2ではなくたこ焼き数）", got.TakoyakiCount)
	}
	if got.Stats.ServedCount != 2 {
		t.Fatalf("ServedCount=%d, want 2（提供した客の数）", got.Stats.ServedCount)
	}
	if got.Stats.TotalMisses != 3 {
		t.Fatalf("TotalMisses=%d, want 3", got.Stats.TotalMisses)
	}
	// 廃止フィールドは入れない（reason は omitempty なのでキーごと消える）。
	assertZeroOnWire(t, got, "reason", "creditLeft", "evalRaw", "evalNormalized")
}

// 客が逃げないので LeftCount / AttributeTally.Left は常に 0（集計欄は残す）。
func TestMatchStats_LeftCountIsAlwaysZero(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 2)
	s.Start(0)

	placeAssigned(s, "c-1", "s-1", proto.AttrNormal, 2, 10)
	placeAssigned(s, "c-2", "s-1", proto.AttrClaimer, 1, 5)
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-1", ElapsedMs: 2000, MissCount: 1})

	// 残った客を放置して長時間回しても帰らない。
	for i := 0; i < 100; i++ {
		s.Tick(1000)
	}

	got := s.buildMatchStats(s.stores["s-1"])
	if got.LeftCount != 0 {
		t.Fatalf("LeftCount=%d, want 0（客は逃げない）", got.LeftCount)
	}
	for _, a := range []proto.AttributeTally{got.Normal, got.Bonus, got.Claimer, got.Buzz} {
		if a.Left != 0 {
			t.Fatalf("AttributeTally.Left=%d, want 0: %+v", a.Left, got)
		}
	}
	if got.Normal.Served != 1 {
		t.Fatalf("Normal.Served=%d, want 1", got.Normal.Served)
	}
}

// 1件も提供していない店でもリザルト集計がゼロ除算しない。
func TestMatchStats_NoServeIsSafe(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 2)
	s.Start(0)

	got := s.buildMatchStats(s.stores["s-1"])
	if got.ServedCount != 0 || got.AvgAccuracy != 0 || got.AvgElapsedMs != 0 || got.FastestMs != 0 {
		t.Fatalf("提供0なのに値が入っている: %+v", got)
	}
}

// ── 本戦: 時刻足切りと決着（plan-h22）──────────────────────

// cullAt は指定ステージの時刻へ一気に進めて tick を1回回す。
// eliminated は足切りバーストから脱落エントリを取り出す（h23 で Batch に畳まれた）。
func eliminated(out []Outbound) []proto.StoreEliminated {
	var all []proto.StoreEliminated
	for _, b := range filterMsg[proto.StoreEliminatedBatch](out) {
		all = append(all, b.Entries...)
	}
	return all
}

func cullAt(s *Session, stageIdx int) []Outbound {
	target := int64(s.params.Cull.Stages[stageIdx].AtMs)
	return s.Tick(int(target - s.elapsedMs))
}

// ステージ時刻に到達すると、生存数がちょうど targetAliveCount になる。
func TestStepCull_ReachesTargetAliveCount(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 99)
	s.Start(0)

	for i, stage := range s.params.Cull.Stages {
		cullAt(s, i)
		if s.aliveCount != stage.TargetAliveCount {
			t.Fatalf("ステージ%d (%dms) 後の生存=%d, want %d",
				i+1, stage.AtMs, s.aliveCount, stage.TargetAliveCount)
		}
	}
}

// 20秒より前には誰も脱落しない（企画 C4「どれだけ弱くても20秒は遊べる」）。
func TestStepCull_NobodyEliminatedBeforeFirstStage(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 99)
	s.Start(0)

	firstAt := s.params.Cull.Stages[0].AtMs
	for s.elapsedMs < int64(firstAt)-int64(params.Session.TickIntervalMs) {
		out := s.Tick(params.Session.TickIntervalMs)
		if elim := eliminated(out); len(elim) > 0 {
			t.Fatalf("%dms 時点で脱落が発生した（第1ステージは %dms）: %+v", s.elapsedMs, firstAt, elim)
		}
	}
	if s.aliveCount != 99 {
		t.Fatalf("第1ステージ前に生存が減っている: %d", s.aliveCount)
	}
}

// スコア下位から切られる（上位は切られない）。
func TestStepCull_CutsLowestScoresFirst(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	// 10店 → 第1ステージで生存4まで落とす（6店カット）。
	params.Cull.Stages[0] = CullStage{AtMs: 20000, TargetAliveCount: 4}
	s := newTestSessionWith(params, 10)
	s.Start(0)

	// s-1 が最弱、s-10 が最強。
	for i, sid := range s.order {
		s.stores[sid].score = i * 100
	}

	out := cullAt(s, 0)
	elim := map[PlayerId]int{}
	for _, e := range eliminated(out) {
		elim[e.StoreId] = e.FinalRank
	}
	if len(elim) != 6 {
		t.Fatalf("6店が脱落するはず: %v", elim)
	}
	// 下位6店（s-1..s-6）が落ち、上位4店は残る。
	for _, sid := range []PlayerId{"s-1", "s-2", "s-3", "s-4", "s-5", "s-6"} {
		if _, ok := elim[sid]; !ok {
			t.Fatalf("スコア下位の %s が切られていない: %v", sid, elim)
		}
	}
	for _, sid := range []PlayerId{"s-7", "s-8", "s-9", "s-10"} {
		if _, ok := elim[sid]; ok {
			t.Fatalf("スコア上位の %s が切られている: %v", sid, elim)
		}
	}
	// 同一ステージ内はスコア昇順で下から積む（最弱が最下位）。
	if elim["s-1"] != 10 || elim["s-6"] != 5 {
		t.Fatalf("finalRank の積み方が違う: s-1=%d(want 10) s-6=%d(want 5)", elim["s-1"], elim["s-6"])
	}
}

// 生存数が既に目標以下のステージはスキップされる（切る数が負にならない）。
func TestStepCull_SkipsWhenAlreadyBelowTarget(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	// 5店で開始。第1ステージの目標75は既に下回っている。
	s := newTestSessionWith(params, 5)
	s.Start(0)

	out := cullAt(s, 0)
	if elim := eliminated(out); len(elim) > 0 {
		t.Fatalf("目標を既に下回っているのに脱落が発生した: %+v", elim)
	}
	if s.aliveCount != 5 {
		t.Fatalf("生存=%d, want 5", s.aliveCount)
	}
	// ステージは消化済みとして進んでいる（同じステージを何度も実行しない）。
	if s.cullStageIdx != 1 {
		t.Fatalf("cullStageIdx=%d, want 1", s.cullStageIdx)
	}
}

// 120秒で全店が脱落し、finalRank が 1..N で重複しない。「生存1店」が発生しない。
func TestStepCull_FinalStageEliminatesEveryone(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 99)
	s.Start(0)

	sawAliveOne := false
	for i := range s.params.Cull.Stages {
		cullAt(s, i)
		if s.aliveCount == 1 {
			sawAliveOne = true
		}
	}
	if sawAliveOne {
		t.Fatal("「生存1店」の状態が発生した（本戦では全店同時脱落で終わる）")
	}
	if s.aliveCount != 0 {
		t.Fatalf("最終ステージ後の生存=%d, want 0", s.aliveCount)
	}
	if s.state != Finished {
		t.Fatalf("state=%v, want Finished", s.state)
	}

	// finalRank が 1..99 で重複なし。
	seen := map[int]PlayerId{}
	for _, sid := range s.order {
		fr := s.stores[sid].finalRank
		if fr < 1 || fr > 99 {
			t.Fatalf("%s の finalRank=%d が範囲外", sid, fr)
		}
		if prev, dup := seen[fr]; dup {
			t.Fatalf("finalRank=%d が重複: %s と %s", fr, prev, sid)
		}
		seen[fr] = sid
	}
	if len(seen) != 99 {
		t.Fatalf("finalRank の種類=%d, want 99", len(seen))
	}
}

// 全店が同じ経路（executeCull → PersonalResult）を通り、最後に MatchEnd が全員へ届く。
func TestCheckFinish_EveryoneGetsResultThenMatchEnd(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 10)
	s.Start(0)

	results := map[PlayerId]proto.PersonalResult{}
	var ends int
	for i := range s.params.Cull.Stages {
		for _, o := range cullAt(s, i) {
			switch m := o.Msg.(type) {
			case proto.PersonalResult:
				if _, dup := results[o.To.PlayerId]; dup {
					t.Fatalf("%s が PersonalResult を2回受け取っている", o.To.PlayerId)
				}
				results[o.To.PlayerId] = m
			case proto.MatchEnd:
				ends++
			}
		}
	}

	if len(results) != 10 {
		t.Fatalf("PersonalResult を受け取った店=%d, want 10（優勝者も同じ経路を通る）", len(results))
	}
	if ends != 10 {
		t.Fatalf("MatchEnd=%d, want 10（全員へ1通ずつ）", ends)
	}
	// 優勝者にも finalRank=1 の PersonalResult が届いている。
	var winners int
	for sid, r := range results {
		if r.FinalRank == 1 {
			winners++
			// 優勝店にも脱落理由は付かない（脱落経路が足切りの1本だけになったため）。
			t.Run("winner="+string(sid), func(t *testing.T) { assertZeroOnWire(t, r, "reason") })
		}
	}
	if winners != 1 {
		t.Fatalf("finalRank=1 の店が %d 店", winners)
	}
}

// 予告は常時配信され、UntilMs が次ステージまでの残り時間と一致する。
func TestCullWarning_AlwaysBroadcastWithCountdown(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 10)
	s.Start(0)

	out := s.Tick(5000)
	warns := filterMsg[proto.ForcedEliminationWarning](out)
	if len(warns) != 10 {
		t.Fatalf("予告は生存店それぞれへ毎tick届くはず: %d件", len(warns))
	}
	w := warns[0]
	if w.UntilMs != s.params.Cull.Stages[0].AtMs-5000 {
		t.Fatalf("UntilMs=%d, want %d", w.UntilMs, s.params.Cull.Stages[0].AtMs-5000)
	}
	if w.StageIndex != 1 || w.StageTotal != CullStageCount {
		t.Fatalf("StageIndex/Total=%d/%d, want 1/%d", w.StageIndex, w.StageTotal, CullStageCount)
	}
	// 予選の廃止フィールドには値を入れない。
	assertZeroOnWire(t, w, "untilTick", "thresholdPct")

	// さらに進めると残りが減る。
	out = s.Tick(5000)
	warns = filterMsg[proto.ForcedEliminationWarning](out)
	if warns[0].UntilMs != s.params.Cull.Stages[0].AtMs-10000 {
		t.Fatalf("UntilMs が減っていない: %d", warns[0].UntilMs)
	}
}

// 予告の対象（CutStoreIds / SelfAtRisk）と実際に切られた店が一致する。
func TestCullWarning_MatchesActualElimination(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	params.Cull.Stages[0] = CullStage{AtMs: 20000, TargetAliveCount: 7}
	s := newTestSessionWith(params, 10)
	s.Start(0)
	for i, sid := range s.order {
		s.stores[sid].score = i * 100
	}

	// 直前の予告を取る。
	out := s.Tick(19000)
	warns := filterMsg[proto.ForcedEliminationWarning](out)
	if len(warns) != 10 {
		t.Fatalf("予告が10件出ていない: %d", len(warns))
	}
	predicted := map[proto.StoreId]bool{}
	for _, id := range warns[0].CutStoreIds {
		predicted[id] = true
	}
	if len(predicted) != 3 {
		t.Fatalf("予告の対象=%d店, want 3", len(predicted))
	}
	if warns[0].CutLineRank != 8 {
		t.Fatalf("CutLineRank=%d, want 8（targetAliveCount+1）", warns[0].CutLineRank)
	}

	// selfAtRisk が予告対象と一致する。
	atRisk := map[PlayerId]bool{}
	for _, o := range out {
		if w, ok := o.Msg.(proto.ForcedEliminationWarning); ok && w.SelfAtRisk {
			atRisk[o.To.PlayerId] = true
		}
	}
	if len(atRisk) != 3 {
		t.Fatalf("selfAtRisk=true の店=%d, want 3", len(atRisk))
	}

	// 実際に切られた店と一致する。
	out = cullAt(s, 0)
	actual := map[proto.StoreId]bool{}
	for _, e := range eliminated(out) {
		actual[e.StoreId] = true
	}
	if len(actual) != len(predicted) {
		t.Fatalf("予告と実行で数が違う: 予告=%d 実行=%d", len(predicted), len(actual))
	}
	for id := range predicted {
		if !actual[id] {
			t.Fatalf("予告された %s が実際には落ちていない", id)
		}
		if !atRisk[PlayerId(id)] {
			t.Fatalf("%s が cutStoreIds にいるのに selfAtRisk=false", id)
		}
	}
}

// ★最終ステージだけ CutLineRank=2 で、1位は表示上「対象外」になる（plan-h22 §3.2）。
// 処理層は全店脱落のまま。
func TestCullWarning_FinalStageShowsRankTwoButCutsEveryone(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 10)
	s.Start(0)
	for i, sid := range s.order {
		s.stores[sid].score = i * 100
	}

	// 最終ステージの直前まで進める（中間ステージは10店なので全部スキップされる）。
	last := len(s.params.Cull.Stages) - 1
	out := s.Tick(s.params.Cull.Stages[last].AtMs - 1000)
	warns := filterMsg[proto.ForcedEliminationWarning](out)
	if len(warns) == 0 {
		t.Fatal("最終ステージの予告が出ていない")
	}
	w := warns[0]
	if w.CutLineRank != 2 {
		t.Fatalf("最終ステージの CutLineRank=%d, want 2（表示層）", w.CutLineRank)
	}
	// 表示層: 最強の s-10（1位）は対象外。
	if len(w.CutStoreIds) != 9 {
		t.Fatalf("CutStoreIds=%d件, want 9（1位を除く）", len(w.CutStoreIds))
	}
	for _, id := range w.CutStoreIds {
		if id == "s-10" {
			t.Fatal("1位の s-10 が表示上の脱落対象に入っている")
		}
	}
	for _, o := range out {
		if ww, ok := o.Msg.(proto.ForcedEliminationWarning); ok && o.To.PlayerId == "s-10" && ww.SelfAtRisk {
			t.Fatal("1位に selfAtRisk=true が届いている（CutLineRank=2 と矛盾する）")
		}
	}

	// 処理層: 全店脱落する。
	out = cullAt(s, last)
	if got := len(eliminated(out)); got != 10 {
		t.Fatalf("最終ステージの脱落=%d店, want 10（1位も落ちる）", got)
	}
	if s.aliveCount != 0 {
		t.Fatalf("生存=%d, want 0", s.aliveCount)
	}
}

// 大きい dt で複数ステージを跨いでも、1ステージずつ順に消化される。
func TestStepCull_CatchesUpAcrossStages(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 99)
	s.Start(0)

	// 1tick で最終ステージまで飛ばす。
	last := len(s.params.Cull.Stages) - 1
	s.Tick(s.params.Cull.Stages[last].AtMs)

	if s.cullStageIdx != len(s.params.Cull.Stages) {
		t.Fatalf("cullStageIdx=%d, want %d", s.cullStageIdx, len(s.params.Cull.Stages))
	}
	if s.aliveCount != 0 || s.state != Finished {
		t.Fatalf("alive=%d state=%v, want 0/Finished", s.aliveCount, s.state)
	}
	// 途中のステージを飛ばしていない＝finalRank が 1..99 で埋まる。
	seen := map[int]bool{}
	for _, sid := range s.order {
		seen[s.stores[sid].finalRank] = true
	}
	if len(seen) != 99 {
		t.Fatalf("finalRank の種類=%d, want 99（ステージを飛ばしている）", len(seen))
	}
}

// MatchStart の公開パラメータに cullSchedule が載る（非nil・全ステージ）。
func TestPublicParams_CarriesCullSchedule(t *testing.T) {
	s := newTestSession(3)
	p := s.publicParams()

	if len(p.CullSchedule) != CullStageCount {
		t.Fatalf("cullSchedule=%d件, want %d", len(p.CullSchedule), CullStageCount)
	}
	for i, st := range p.CullSchedule {
		want := s.params.Cull.Stages[i]
		if st.AtMs != want.AtMs || st.TargetAliveCount != want.TargetAliveCount {
			t.Fatalf("cullSchedule[%d]=%+v, want %+v", i, st, want)
		}
	}

	// nil スライスは JSON で null になり C#/TS 側が落ちる。必ず配列で出す。
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"cullSchedule":null`) {
		t.Fatalf("cullSchedule が null で出ている: %s", b)
	}
}

// ── 本戦: 配信順序（plan-h23 §3）─────────────────────────────

// msgNames は Outbound の型名を順番に並べた列を返す（順序契約の検査用）。
func msgNames(out []Outbound) []string {
	names := make([]string, 0, len(out))
	for _, o := range out {
		names = append(names, fmt.Sprintf("%T", o.Msg))
	}
	return names
}

// firstIndexOf は型名が最初に現れた位置（無ければ -1）。
func firstIndexOf(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i
		}
	}
	return -1
}

// lastIndexOf は型名が最後に現れた位置（無ければ -1）。
func lastIndexOf(names []string, want string) int {
	for i := len(names) - 1; i >= 0; i-- {
		if names[i] == want {
			return i
		}
	}
	return -1
}

// 🔴 足切りステージの配信順序（plan-h23 §3.1）。
//
//	1. StoreEliminatedBatch → 2. PersonalResult → 3. EvaluationUpdate
//	→ 4. RankingSnapshot → 5. ForcedEliminationWarning
//
// **順位を配る前に脱落を配る。** 逆順だと脱落者を含んだ順位が一瞬表示される（予選 4-A）。
// game が append した順序がそのまま配信順序になるので、ここが契約そのもの。
func TestCullBurst_OutboundOrder(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	params.Cull.Stages[0] = CullStage{AtMs: 20000, TargetAliveCount: 6}
	s := newTestSessionWith(params, 10)
	s.Start(0)
	for i, sid := range s.order {
		s.stores[sid].score = i * 100
	}

	names := msgNames(cullAt(s, 0))

	batch := firstIndexOf(names, "proto.StoreEliminatedBatch")
	result := firstIndexOf(names, "proto.PersonalResult")
	lastResult := lastIndexOf(names, "proto.PersonalResult")
	eval := firstIndexOf(names, "proto.EvaluationUpdate")
	lastEval := lastIndexOf(names, "proto.EvaluationUpdate")
	snap := firstIndexOf(names, "proto.RankingSnapshot")
	warn := firstIndexOf(names, "proto.ForcedEliminationWarning")

	for name, idx := range map[string]int{
		"StoreEliminatedBatch": batch, "PersonalResult": result,
		"EvaluationUpdate": eval, "RankingSnapshot": snap, "ForcedEliminationWarning": warn,
	} {
		if idx < 0 {
			t.Fatalf("%s が配信されていない: %v", name, names)
		}
	}

	if batch >= result {
		t.Fatalf("1.StoreEliminatedBatch は 2.PersonalResult より前のはず: %v", names)
	}
	if lastResult >= eval {
		t.Fatalf("2.PersonalResult は 3.EvaluationUpdate より前のはず: %v", names)
	}
	if lastEval >= snap {
		t.Fatalf("3.EvaluationUpdate は 4.RankingSnapshot より前のはず: %v", names)
	}
	if snap >= warn {
		t.Fatalf("4.RankingSnapshot は 5.ForcedEliminationWarning より前のはず: %v", names)
	}

	// バーストより前に EvaluationUpdate が出ていないこと。
	// 出ていると「脱落者を含んだ古い順位 → 脱落 → 新しい順位」の順で届く（予選 4-A）。
	if eval < batch {
		t.Fatalf("足切りの前に古い順位が配信されている: %v", names)
	}
}

// 🔴 試合終了（120秒）の配信順序（plan-h23 §3.2）。
//
//	1. StoreEliminatedBatch → 2. PersonalResult → 3. RankingSnapshot → 4. MatchEnd
//
// **3 を省略しない。** StoreEliminated は score を持たないので、これが無いと
// 「優勝 たこ太 12,400点」が出せない（MatchEnd を拡張せずに済ませる条件）。
func TestMatchEnd_OutboundOrder(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 10)
	s.Start(0)

	last := len(s.params.Cull.Stages) - 1
	names := msgNames(cullAt(s, last))

	batch := firstIndexOf(names, "proto.StoreEliminatedBatch")
	lastResult := lastIndexOf(names, "proto.PersonalResult")
	snap := firstIndexOf(names, "proto.RankingSnapshot")
	end := firstIndexOf(names, "proto.MatchEnd")

	if batch < 0 || lastResult < 0 || snap < 0 || end < 0 {
		t.Fatalf("終了時の配信が欠けている: %v", names)
	}
	if batch >= lastResult {
		t.Fatalf("StoreEliminatedBatch は PersonalResult より前のはず: %v", names)
	}
	if lastResult >= snap {
		t.Fatalf("PersonalResult は RankingSnapshot より前のはず: %v", names)
	}
	if snap >= end {
		t.Fatalf("🔴 RankingSnapshot は MatchEnd より前のはず（最終スコアが配れない）: %v", names)
	}

	// 生存店が居ないので EvaluationUpdate は出ない。
	if firstIndexOf(names, "proto.EvaluationUpdate") >= 0 {
		t.Fatalf("全店脱落後に EvaluationUpdate が出ている: %v", names)
	}
	// 予告も出ない（次のステージが無い）。
	if firstIndexOf(names, "proto.ForcedEliminationWarning") >= 0 {
		t.Fatalf("全ステージ消化後に予告が出ている: %v", names)
	}
}

// 足切りの脱落は1メッセージに畳まれる（24店脱落でも Envelope は1つ）。
func TestCullBurst_EliminationIsSingleBatch(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 99)
	s.Start(0)

	out := cullAt(s, 0) // 99 → 75（24店脱落）
	batches := filterMsg[proto.StoreEliminatedBatch](out)
	if len(batches) != 1 {
		t.Fatalf("バッチが %d 通（1通に畳めていない）", len(batches))
	}
	if len(batches[0].Entries) != 24 {
		t.Fatalf("バッチの中身=%d店, want 24", len(batches[0].Entries))
	}
	if batches[0].StageIndex != 1 {
		t.Fatalf("StageIndex=%d, want 1（1始まり）", batches[0].StageIndex)
	}
	// 個別の StoreEliminated は送らない。
	if got := filterMsg[proto.StoreEliminated](out); len(got) > 0 {
		t.Fatalf("個別の StoreEliminated が %d 通出ている（Batch に畳むはず）", len(got))
	}
}

// RankingSnapshot が全99店を含み、生存店＝現在順位／脱落店＝確定順位になっている。
func TestRankingSnapshot_AllStoresWithCorrectRank(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 99)
	s.Start(0)
	for i, sid := range s.order {
		s.stores[sid].score = i * 100
	}

	out := cullAt(s, 0) // 99 → 75
	snaps := filterMsg[proto.RankingSnapshot](out)
	if len(snaps) != 1 {
		t.Fatalf("RankingSnapshot=%d通, want 1", len(snaps))
	}
	snap := snaps[0]
	if len(snap.Entries) != 99 {
		t.Fatalf("entries=%d, want 99（脱落店も含めて全店）", len(snap.Entries))
	}

	var aliveN, deadN int
	seenRank := map[int]bool{}
	for _, e := range snap.Entries {
		st := s.stores[e.StoreId]
		if e.Score != st.score {
			t.Fatalf("%s の score=%d, want %d", e.StoreId, e.Score, st.score)
		}
		if e.Alive {
			aliveN++
			if e.Rank != st.rank {
				t.Fatalf("生存店 %s の rank=%d, want 現在順位 %d", e.StoreId, e.Rank, st.rank)
			}
		} else {
			deadN++
			if e.Rank != st.finalRank {
				t.Fatalf("脱落店 %s の rank=%d, want 確定順位 %d", e.StoreId, e.Rank, st.finalRank)
			}
		}
		if seenRank[e.Rank] {
			t.Fatalf("rank=%d が重複している（99店を1本の Rank で並べられない）", e.Rank)
		}
		seenRank[e.Rank] = true
	}
	if aliveN != 75 || deadN != 24 {
		t.Fatalf("生存=%d 脱落=%d, want 75/24", aliveN, deadN)
	}
}

// 足切りの瞬間に1接続へ届く通数が、送信キューの深さに対して十分小さいこと。
//
// 🔴 これが h23 で StoreEliminatedBatch を入れた理由そのもの（plan-h23 §2.1）。
// 送信キューは sendBuffer=64（internal/transport/connection.go）で、**溢れた接続は
// 即座に切られる**（slow-consumer eviction）。1店1メッセージのままだと24店脱落＝
// 24 Envelope が1tickで殺到し、軽く詰まっただけの健全なクライアントが
// **最も盛り上がる瞬間に**切断され得る。
func TestCullBurst_PerConnectionEnvelopeCountIsSmall(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 99)
	s.Start(0)

	out := cullAt(s, 0) // 99 → 75（24店脱落）

	// 生存店1つと脱落店1つが受け取る通数を数える（broadcast は全員に届く）。
	var alive, dead PlayerId
	for _, sid := range s.order {
		if s.stores[sid].alive && alive == "" {
			alive = sid
		}
		if !s.stores[sid].alive && dead == "" {
			dead = sid
		}
	}
	count := func(target PlayerId) int {
		n := 0
		for _, o := range out {
			if o.To.Broadcast || o.To.PlayerId == target {
				n++
			}
		}
		return n
	}

	// sendBuffer=64 に対する余裕。畳めていれば十数通で収まる。
	const budget = 16
	if got := count(alive); got > budget {
		t.Fatalf("生存店へ %d 通（上限 %d）。sendBuffer=64 を圧迫する", got, budget)
	}
	if got := count(dead); got > budget {
		t.Fatalf("脱落店へ %d 通（上限 %d）。sendBuffer=64 を圧迫する", got, budget)
	}
}

// ── 本戦: 注文単位の記録（plan-h03）───────────────────────────

// ApplyOrderServed 1回で attempt が1件積まれ、中身が引数と一致する。
//
// フィールドを取り違えても気づけるよう、**全項目を別々の値**にしてある
// （orderCount 3 / keystrokes 40 / elapsed 3300 / miss 7 / heat 5）。
func TestApplyOrderServed_RecordsAttempt(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	params.Sanity.MinMsPerWord = 0 // クランプを効かせずに申告値をそのまま見る
	s := newTestSessionWith(params, 2)
	s.Start(0)
	s.heatLevel = 5

	placeAssigned(s, "c-1", "s-1", proto.AttrBuzz, 3, 40)
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-1", ElapsedMs: 3300, MissCount: 7})

	got := s.Attempts()
	if len(got) != 1 {
		t.Fatalf("attempts=%d, want 1", len(got))
	}
	want := OrderAttempt{
		StoreId: "s-1", CustomerId: "c-1", Attribute: proto.AttrBuzz,
		HeatLevel: 5, OrderCount: 3, Keystrokes: 40, ElapsedMs: 3300, MissCount: 7,
	}
	if got[0] != want {
		t.Fatalf("attempt が引数と一致しない\n got=%+v\nwant=%+v", got[0], want)
	}
}

// keystrokes は**サーバーが発行したお題語の合計**であって、クライアント申告ではない。
//
// OrderServed は打鍵数を持たないので、ここを取り違えると精度の分母が嘘になる。
func TestAttempt_KeystrokesComeFromServer(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 2)
	s.Start(0)

	// サーバーが発行した打鍵数は 40。クライアントは打鍵数を送れない。
	placeAssigned(s, "c-1", "s-1", proto.AttrNormal, 2, 40)
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-1", ElapsedMs: 5000, MissCount: 1})

	if got := s.Attempts()[0].Keystrokes; got != 40 {
		t.Fatalf("Keystrokes=%d, want 40（サーバー発行語の打鍵合計）", got)
	}
}

// elapsed / miss は**クランプ後**の値を残す。
//
// クライアントの異常値（下限割れ・keys超過のミス）をそのまま残すと BOT プロファイルが汚れる。
func TestAttempt_StoresClampedValues(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	params.Sanity.MinMsPerWord = 200
	s := newTestSessionWith(params, 2)
	s.Start(0)

	// 注文2語 → 下限 400ms。10ms の申告は 400 に持ち上がる。
	// ミス 9999 は打鍵数 10 にクランプされる。
	placeAssigned(s, "c-1", "s-1", proto.AttrNormal, 2, 10)
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-1", ElapsedMs: 10, MissCount: 9999})

	a := s.Attempts()[0]
	if a.ElapsedMs != 400 {
		t.Fatalf("ElapsedMs=%d, want 400（下限クランプ後）", a.ElapsedMs)
	}
	if a.MissCount != 10 {
		t.Fatalf("MissCount=%d, want 10（打鍵数でクランプ後）", a.MissCount)
	}
}

// heatLevel は**提供時点**の値を残す（後から現在の heat で代用しない）。
//
// ここを取り違えると「難度別の速度・ミス率分布」が崩れ、h04 のプロファイル生成が狂う。
func TestAttempt_HeatIsCapturedAtServeTime(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 2)
	s.Start(0)

	s.heatLevel = 3
	placeAssigned(s, "c-1", "s-1", proto.AttrNormal, 1, 10)
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-1", ElapsedMs: 2000, MissCount: 0})

	// 提供後に heat が上がっても、既に記録した値は動かない。
	s.heatLevel = 12
	placeAssigned(s, "c-2", "s-1", proto.AttrNormal, 1, 10)
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-2", ElapsedMs: 2000, MissCount: 0})

	got := s.Attempts()
	if len(got) != 2 {
		t.Fatalf("attempts=%d, want 2", len(got))
	}
	if got[0].HeatLevel != 3 || got[1].HeatLevel != 12 {
		t.Fatalf("提供時点の heat が残っていない: %d, %d（want 3, 12）", got[0].HeatLevel, got[1].HeatLevel)
	}
}

// 弾かれた OrderServed は記録しない（行列先頭でない客への報告など）。
func TestAttempt_RejectedOrderIsNotRecorded(t *testing.T) {
	params := DefaultParameters()
	params.Customer.Total = 0
	s := newTestSessionWith(params, 2)
	s.Start(0)

	placeAssigned(s, "c-1", "s-1", proto.AttrNormal, 1, 10)
	placeAssigned(s, "c-2", "s-1", proto.AttrNormal, 1, 10)

	// 2人目（行列先頭でない）への報告は弾かれる。
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "c-2", ElapsedMs: 2000, MissCount: 0})
	if got := len(s.Attempts()); got != 0 {
		t.Fatalf("弾かれた報告が記録された: %d件", got)
	}
	// 存在しない客も同様。
	s.ApplyOrderServed("s-1", proto.OrderServed{CustomerId: "nope", ElapsedMs: 2000, MissCount: 0})
	if got := len(s.Attempts()); got != 0 {
		t.Fatalf("存在しない客の報告が記録された: %d件", got)
	}
}
