package bot

import (
	"encoding/json"
	"math/rand"
	"testing"
	"time"

	"takoda99/internal/proto"
	"takoda99/internal/transport"
	"takoda99/internal/typist"
)

func recv(t *testing.T, c transport.Connection) proto.Envelope {
	t.Helper()
	select {
	case env, ok := <-c.Receive():
		if !ok {
			t.Fatal("接続が閉じた")
		}
		return env
	case <-time.After(2 * time.Second):
		t.Fatal("受信タイムアウト")
		return proto.Envelope{}
	}
}

// testAbility は決定的な検証用の実力（揺らぎ無し）。
func testAbility() typist.Ability {
	return typist.Ability{MsPerKey: 100, MissRate: 0, HeatPenalty: 0, JitterMs: 0}
}

// arrive は CustomerArrived を1件流し込む。
func arrive(b *Bot, cid string, words ...string) {
	p, _ := json.Marshal(proto.CustomerView{
		CustomerId: proto.CustomerId(cid), OrderCount: len(words), Words: words,
	})
	b.onMessage(proto.Envelope{Type: proto.TypeCustomerArrived, Payload: p})
}

// serve は「取り掛かって打ち切る」を1回ぶん進める（Run のタイマー待ちを省いた形）。
func serve(b *Bot) {
	b.startNext()
	b.finishCurrent()
}

// Bot は CustomerArrived で受け取った客に対して OrderServed を送る。
func TestBot_ServesArrivedCustomer(t *testing.T) {
	srv, cli := transport.Pipe()
	b := New(cli, testAbility(), rand.New(rand.NewSource(1)))

	arrive(b, "c-1", "ねこ", "いぬ")
	serve(b)

	env := recv(t, srv)
	if env.Type != proto.TypeOrderServed {
		t.Fatalf("OrderServed を送るはず: got %s", env.Type)
	}
	var r proto.OrderServed
	_ = json.Unmarshal(env.Payload, &r)
	if r.CustomerId != "c-1" {
		t.Fatalf("実在 customerId に整合すべき: got %q, want c-1", r.CustomerId)
	}
}

// CustomerArrived で受け取った複数客も保持し、順に提供する。
func TestBot_ServesMultipleCustomers(t *testing.T) {
	srv, cli := transport.Pipe()
	b := New(cli, testAbility(), rand.New(rand.NewSource(1)))

	for _, cid := range []string{"c-1", "c-2"} {
		arrive(b, cid, "ねこ")
	}

	serve(b)
	if r := recvOrder(t, srv); r.CustomerId != "c-1" {
		t.Fatalf("1人目 got %q want c-1", r.CustomerId)
	}
	serve(b)
	if r := recvOrder(t, srv); r.CustomerId != "c-2" {
		t.Fatalf("2人目 got %q want c-2", r.CustomerId)
	}
}

func recvOrder(t *testing.T, c transport.Connection) proto.OrderServed {
	t.Helper()
	env := recv(t, c)
	var r proto.OrderServed
	_ = json.Unmarshal(env.Payload, &r)
	return r
}

// MatchEnd で onMessage が終了シグナルを返す。
func TestBot_StopsOnMatchEnd(t *testing.T) {
	_, cli := transport.Pipe()
	b := New(cli, testAbility(), rand.New(rand.NewSource(1)))
	p, _ := json.Marshal(proto.PersonalResult{FinalRank: 1})
	if !b.onMessage(proto.Envelope{Type: proto.TypeMatchEnd, Payload: p}) {
		t.Fatal("MatchEnd で終了(true)を返すべき")
	}
}

// ── plan-h31 ────────────────────────────────────────────────────────

// 🔴 所要時間が**お題の打鍵数に比例する**こと（plan-h31 §1.1・§2.3）。
//
// ここが定数（旧実装の BaseElapsedMs）だと、heat が上がって語が長くなっても Bot の
// 提供ペースが変わらず、**終盤ほど Bot が人間より相対的に速くなる**。
// 「1注文あたりの固定時間」に戻す変異でこのテストが落ちる。
func TestBot_ElapsedScalesWithKeystrokes(t *testing.T) {
	_, cli := transport.Pipe()
	b := New(cli, testAbility(), rand.New(rand.NewSource(1)))

	arrive(b, "short", "ねこ")                      // 打鍵数 小
	arrive(b, "long", "たこやきたべたい", "おおさかのそら") // 打鍵数 大

	d1, ok := b.startNext()
	if !ok {
		t.Fatal("1件目に取り掛かれていない")
	}
	b.finishCurrent()
	d2, ok := b.startNext()
	if !ok {
		t.Fatal("2件目に取り掛かれていない")
	}
	b.finishCurrent()

	if d2 <= d1 {
		t.Fatalf("長い語のほうが時間が掛かるべき: short=%v long=%v", d1, d2)
	}
}

// 🔴 ミス数が**打鍵数ベース**であること（plan-h31 §2.2）。
//
// 旧実装は `miss ∈ {0,1}` 固定だったので、Bot は1注文で weightMiss ぶんしか引かれず、
// 数ミスしうる人間に対して構造的に有利だった。0/1 に戻す変異でこのテストが落ちる。
func TestBot_MissCountGrowsWithKeystrokes(t *testing.T) {
	srv, cli := transport.Pipe()
	ab := typist.Ability{MsPerKey: 1, MissRate: 0.5}
	b := New(cli, ab, rand.New(rand.NewSource(42)))

	// 十分に長い語（打鍵数 30 以上）を1件。
	long := []string{"たこやきたべたい", "おおさかのそら", "こなをまぜてやく", "ふねにのってでかける"}
	arrive(b, "c-long", long...)
	serve(b)

	r := recvOrder(t, srv)
	keys := countKeystrokes(long)
	if keys < 30 {
		t.Fatalf("前提: 検証用の語が短すぎる keys=%d", keys)
	}
	if r.MissCount <= 1 {
		t.Fatalf("miss=%d（0/1 固定になっている）。打鍵数 %d・ミス率0.5 なら数十件出るはず",
			r.MissCount, keys)
	}
}

// 🔴 難度追従（plan-h31 §2.3）。DifficultyUpdate で heat が上がると所要時間が伸びる。
//
// Bot は heat を DifficultyUpdate（全店共通）から受け取る。受け取りを消す変異で落ちる。
func TestBot_SlowsDownWithHeat(t *testing.T) {
	_, cli := transport.Pipe()
	ab := typist.Ability{MsPerKey: 100, HeatPenalty: 0.05}
	b := New(cli, ab, rand.New(rand.NewSource(1)))

	arrive(b, "c-1", "たこやき")
	base, _ := b.startNext()
	b.finishCurrent()

	p, _ := json.Marshal(proto.DifficultyUpdate{HeatLevel: 10})
	b.onMessage(proto.Envelope{Type: proto.TypeDifficultyUpdate, Payload: p})
	if b.heatLevel != 10 {
		t.Fatalf("DifficultyUpdate を取り込んでいない: heatLevel=%d", b.heatLevel)
	}

	arrive(b, "c-2", "たこやき")
	hot, _ := b.startNext()
	b.finishCurrent()

	if hot <= base {
		t.Fatalf("heat が上がったら遅くなるべき: base=%v hot=%v", base, hot)
	}
	// HeatPenalty 0.05 × heat 10 = 1.5倍（±1ms の丸め誤差は許容）。
	if got, want := float64(hot)/float64(base), 1.5; got < want-0.05 || got > want+0.05 {
		t.Fatalf("難度係数が想定と違う: %.3f 倍, want %.2f 倍", got, want)
	}
}

// 🔴 個体差が**固定**であること（plan-h31 §1.1・§7）。
//
// 同じ Bot が2回打っても基準がぶれない（揺らぎ 0 なら同じ語で同じ所要時間になる）。
// 「毎回 tier 値から個体係数を引き直す」実装だとここが落ちる。
func TestBot_AbilityIsFixedAcrossOrders(t *testing.T) {
	_, cli := transport.Pipe()
	b := New(cli, testAbility(), rand.New(rand.NewSource(7)))

	arrive(b, "c-1", "たこやき")
	arrive(b, "c-2", "たこやき")

	d1, _ := b.startNext()
	b.finishCurrent()
	d2, _ := b.startNext()
	b.finishCurrent()

	if d1 != d2 {
		t.Fatalf("同じ個体・同じ語なら所要時間は同じであるべき: %v vs %v", d1, d2)
	}
}

// 打鍵数は CustomerArrived.words から数える（proto を変えずに済んでいることの固定）。
func TestBot_CountsKeystrokesFromWords(t *testing.T) {
	if got := countKeystrokes([]string{"ねこ"}); got != 4 { // ne-ko
		t.Fatalf("countKeystrokes(ねこ)=%d, want 4", got)
	}
	if countKeystrokes(nil) != 0 {
		t.Fatal("語が無ければ 0")
	}
}
