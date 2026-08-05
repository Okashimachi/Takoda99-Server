package main

import (
	"bytes"
	"io"
	"math/rand"
	"strings"
	"testing"

	"takoda99/internal/game"
	"takoda99/internal/proto"
)

// sim は CI 常設の軽量チェック。重い99店の反復はローカルで `go run ./cmd/matchsim` を使う。
const testMaxTicks = 20000

func TestSimulate_Finishes(t *testing.T) {
	params := game.DefaultParameters()
	r := simulate(params, 20, profileNormal, rand.New(rand.NewSource(1)), testMaxTicks)
	if r.stalled {
		t.Fatalf("20店で決着しない（%d tick 走行・生存 %d 店）", r.ticks, r.aliveAtEnd)
	}
	if r.winner == "" {
		t.Fatal("決着したのに優勝店が居ない")
	}
}

// 全プリセットで決着すること。実力差が無い uniform は膠着の最悪ケースなので特に重要。
func TestSimulate_FinishesForAllProfiles(t *testing.T) {
	params := game.DefaultParameters()
	for _, p := range []profile{profileUniform, profileNormal, profileBipolar, profileWide} {
		t.Run(string(p), func(t *testing.T) {
			r := simulate(params, 20, p, rand.New(rand.NewSource(3)), testMaxTicks)
			if r.stalled {
				t.Fatalf("profile=%s で決着しない（%d tick・生存 %d 店）", p, r.ticks, r.aliveAtEnd)
			}
		})
	}
}

// ダミー店の行列が session の storeQueues とズレていないこと。
// ズレると OrderServed が弾かれ、sim の数値が実態と食い違ったまま気付けない。
func TestSimulate_NoRejectedOrders(t *testing.T) {
	params := game.DefaultParameters()
	r := simulate(params, 30, profileWide, rand.New(rand.NewSource(5)), testMaxTicks)
	if r.rejected != 0 {
		t.Fatalf("提供報告が %d 件弾かれた（ダミー店の行列が session とズレている）", r.rejected)
	}
	if r.servedTotal == 0 {
		t.Fatal("1件も提供できていない（打鍵モデルが動いていない）")
	}
}

// --seed で結果が再現すること。再現しないとパラメータ調整の前後比較ができない。
func TestSimulate_IsDeterministic(t *testing.T) {
	params := game.DefaultParameters()
	a := simulate(params, 20, profileNormal, rand.New(rand.NewSource(9)), testMaxTicks)
	b := simulate(params, 20, profileNormal, rand.New(rand.NewSource(9)), testMaxTicks)

	if a.ticks != b.ticks || a.elapsedMs != b.elapsedMs || a.winner != b.winner ||
		a.servedTotal != b.servedTotal || a.selfCollapses != b.selfCollapses || a.culls != b.culls {
		t.Fatalf("同一シードで結果が一致しない:\n a=%+v\n b=%+v", a, b)
	}
}

func TestParseProfile(t *testing.T) {
	for _, s := range []string{"uniform", "normal", "bipolar", "wide"} {
		if _, err := parseProfile(s); err != nil {
			t.Errorf("parseProfile(%q) が失敗: %v", s, err)
		}
	}
	if _, err := parseProfile("fast"); err == nil {
		t.Error("未知の profile がエラーにならない")
	}
}

// 我慢切れで帰られた客を取りこぼすと、その店は以後1人も捌けなくなる。
func TestDummyStore_LeaveClearsCurrentOrder(t *testing.T) {
	d := &dummyStore{id: "s-1", msPerKey: 100, missRate: 0, alive: true}
	rng := rand.New(rand.NewSource(1))

	d.arrive(proto.CustomerView{CustomerId: "c-1", Words: []string{"たこ"}})
	d.arrive(proto.CustomerView{CustomerId: "c-2", Words: []string{"たこ"}})

	// 打鍵中にする。
	if _, done := d.step(10, rng); done {
		t.Fatal("10ms で打ち終わってしまった")
	}
	if d.current == nil || d.current.customerId != "c-1" {
		t.Fatalf("先頭客に取り掛かっていない: %+v", d.current)
	}

	d.leave("c-1")
	if d.current != nil {
		t.Fatal("帰った客の打鍵状態が残っている")
	}

	// 次の客を最後まで捌けること。
	served := false
	for i := 0; i < 100 && !served; i++ {
		o, done := d.step(100, rng)
		if done {
			served = true
			if o.CustomerId != "c-2" {
				t.Fatalf("提供した客が違う: %s", o.CustomerId)
			}
		}
	}
	if !served {
		t.Fatal("離脱の後に次の客を捌けていない")
	}
	if len(d.queue) != 0 {
		t.Fatalf("行列が残っている: %+v", d.queue)
	}
}

// 待機中の客が帰った場合は、打鍵中の客の進行を巻き戻さないこと。
func TestDummyStore_LeaveOfWaitingCustomerKeepsProgress(t *testing.T) {
	d := &dummyStore{id: "s-1", msPerKey: 100, missRate: 0, alive: true}
	rng := rand.New(rand.NewSource(1))

	d.arrive(proto.CustomerView{CustomerId: "c-1", Words: []string{"たこやき"}})
	d.arrive(proto.CustomerView{CustomerId: "c-2", Words: []string{"たこ"}})
	d.step(100, rng)
	before := d.current.remainingMs

	d.leave("c-2")
	if d.current == nil || d.current.customerId != "c-1" {
		t.Fatal("打鍵中の客が巻き添えで消えた")
	}
	if d.current.remainingMs != before {
		t.Fatalf("打鍵の進行が巻き戻った: %d → %d", before, d.current.remainingMs)
	}
	if len(d.queue) != 1 || d.queue[0].customerId != "c-1" {
		t.Fatalf("行列の同期が壊れた: %+v", d.queue)
	}
}

// 打鍵数はローマ字換算であること。ルーン数で数えると speed 評価が
// SpeedCap に張り付いて全店同点になり、バランス検証にならない。
func TestCountKeystrokes_UsesRomajiLength(t *testing.T) {
	// たこやき = ta ko ya ki = 8打鍵（4ルーンではない）。
	if got := countKeystrokes([]string{"たこやき"}); got != 8 {
		t.Fatalf("countKeystrokes(たこやき) = %d, want 8", got)
	}
	// 複数語は合計される。
	if got := countKeystrokes([]string{"たこ", "たこ"}); got != 8 {
		t.Fatalf("countKeystrokes(たこ×2) = %d, want 8", got)
	}
}

// 膠着した試行はエラーで返すこと（気付かず素通りさせない）。
func TestRun_StalledIsAnError(t *testing.T) {
	err := run(io.Discard, 20, "normal", 1, 1, 1 /*maxTicks*/, true, 120, 180)
	if err == nil {
		t.Fatal("max-ticks=1 なのに膠着が報告されない")
	}
	if !strings.Contains(err.Error(), "膠着") {
		t.Fatalf("膠着以外のエラーになった: %v", err)
	}
}

func TestRun_RejectsBadFlags(t *testing.T) {
	if err := run(io.Discard, 1, "normal", 1, 1, 100, true, 120, 180); err == nil {
		t.Error("--stores=1 が弾かれない")
	}
	if err := run(io.Discard, 20, "fast", 1, 1, 100, true, 120, 180); err == nil {
		t.Error("未知の --profile が弾かれない")
	}
	if err := run(io.Discard, 20, "normal", 0, 1, 100, true, 120, 180); err == nil {
		t.Error("--runs=0 が弾かれない")
	}
}

func TestReportRun_ShowsBalanceMaterial(t *testing.T) {
	params := game.DefaultParameters()
	r := simulate(params, 20, profileNormal, rand.New(rand.NewSource(2)), testMaxTicks)
	r.seed = 2

	var buf bytes.Buffer
	reportRun(&buf, r, 1)
	out := buf.String()

	for _, want := range []string{"決着", "最終フェーズ", "最終heatLevel", "優勝", "脱落内訳", "生存数の推移"} {
		if !strings.Contains(out, want) {
			t.Errorf("レポートに %q が無い:\n%s", want, out)
		}
	}
}

func TestReportSummary_FlagsOutOfTargetRuns(t *testing.T) {
	results := []runResult{
		{profile: profileNormal, stores: 99, elapsedMs: 30000, ticks: 200},
		{profile: profileNormal, stores: 99, elapsedMs: 150000, ticks: 1000},
	}
	var buf bytes.Buffer
	reportSummary(&buf, results, 120, 180)
	out := buf.String()

	if !strings.Contains(out, "1/2") {
		t.Errorf("目安内の件数が 1/2 になっていない:\n%s", out)
	}
	if !strings.Contains(out, "❌") {
		t.Errorf("目安を外れたのに ❌ が出ていない:\n%s", out)
	}
}

func TestAliveDeciles_IsMonotonicAndDeduped(t *testing.T) {
	r := runResult{
		stores: 10,
		aliveTimeline: []aliveSample{
			{tick: 0, alive: 10}, {tick: 5, alive: 7}, {tick: 9, alive: 3}, {tick: 12, alive: 1},
		},
	}
	got := aliveDeciles(r)
	if len(got) == 0 {
		t.Fatal("生存数の推移が空")
	}
	seen := map[int]bool{}
	prevAlive := r.stores + 1
	for _, s := range got {
		if seen[s.tick] {
			t.Fatalf("同じ tick が重複している: %d", s.tick)
		}
		seen[s.tick] = true
		if s.alive > prevAlive {
			t.Fatalf("生存数が増えている: %d → %d", prevAlive, s.alive)
		}
		prevAlive = s.alive
	}
	if got[len(got)-1].alive != 1 {
		t.Fatalf("最後が生存1店で終わっていない: %+v", got[len(got)-1])
	}
}
