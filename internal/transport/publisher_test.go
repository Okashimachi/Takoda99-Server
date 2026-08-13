package transport

import (
	"encoding/json"
	"testing"
	"time"

	"takoda99/internal/game"
	"takoda99/internal/proto"
)

func expectType(t *testing.T, c Connection, want string) proto.Envelope {
	t.Helper()
	select {
	case env, ok := <-c.Receive():
		if !ok {
			t.Fatal("接続が閉じた")
		}
		if env.Type != want {
			t.Fatalf("type=%s, want %s", env.Type, want)
		}
		return env
	case <-time.After(time.Second):
		t.Fatalf("%s の配信が来ない", want)
	}
	return proto.Envelope{}
}

func expectNothing(t *testing.T, c Connection) {
	t.Helper()
	select {
	case env := <-c.Receive():
		t.Fatalf("間引かれるべき区間で配信が来た: %s", env.Type)
	case <-time.After(80 * time.Millisecond):
		// 何も来ない＝間引きOK
	}
}

func testPublishParams() game.PublishParams {
	p := game.DefaultParameters().Publish
	p.RankingIntervalMs = 1000
	p.RankingDeltaIntervalMs = 200
	return p
}

func entries(scores ...int) []proto.RankingEntry {
	out := make([]proto.RankingEntry, 0, len(scores))
	for i, sc := range scores {
		out = append(out, proto.RankingEntry{
			StoreId: proto.StoreId(string(rune('a' + i))), Rank: i + 1, Score: sc, Alive: true,
		})
	}
	return out
}

// 初回に全量を配信し、間隔未満は間引き、間隔経過で再配信する。
func TestRankingPublisher_ThrottlesSnapshot(t *testing.T) {
	p := NewRankingPublisher(testPublishParams())
	srv, cli := Pipe()
	conns := map[proto.StoreId]Connection{"a": srv}
	e := entries(100)

	p.Publish(0, e, conns) // 初回 → 配信
	expectType(t, cli, proto.TypeRankingSnapshot)

	p.Publish(500, e, conns) // 間隔未満 → 間引き
	expectNothing(t, cli)

	p.Publish(1000, e, conns) // 間隔経過 → 再配信
	expectType(t, cli, proto.TypeRankingSnapshot)
}

// 全量は全店ぶんを含み、rank / score / alive を運ぶ。
func TestRankingPublisher_SnapshotCarriesAllStores(t *testing.T) {
	p := NewRankingPublisher(testPublishParams())
	srv, cli := Pipe()
	conns := map[proto.StoreId]Connection{"a": srv}

	e := []proto.RankingEntry{
		{StoreId: "a", Rank: 1, Score: 12300, Alive: true},
		{StoreId: "b", Rank: 99, Score: -60, Alive: false},
	}
	p.Publish(0, e, conns)

	env := expectType(t, cli, proto.TypeRankingSnapshot)
	var snap proto.RankingSnapshot
	if err := json.Unmarshal(env.Payload, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(snap.Entries) != 2 {
		t.Fatalf("entries=%d, want 2", len(snap.Entries))
	}
	if snap.Entries[1].Rank != 99 || snap.Entries[1].Alive {
		t.Fatalf("脱落店の確定順位が運ばれていない: %+v", snap.Entries[1])
	}
}

// 差分は既定 OFF（全量のみ）。間隔が来ても RankingDelta は出ない。
func TestRankingPublisher_DeltaDisabledByDefault(t *testing.T) {
	if game.DefaultParameters().Publish.RankingDeltaEnabled {
		t.Fatal("既定は全量のみのはず（plan-h23 §1.2）")
	}
	p := NewRankingPublisher(testPublishParams())
	srv, cli := Pipe()
	conns := map[proto.StoreId]Connection{"a": srv}

	p.Publish(0, entries(100), conns)
	expectType(t, cli, proto.TypeRankingSnapshot)

	// 差分間隔(200ms)は過ぎたが全量間隔(1000ms)は未満 → 何も出ない。
	p.Publish(300, entries(200), conns)
	expectNothing(t, cli)
}

// 差分を有効化すると、変化した店だけが RankingDelta で出る。
func TestRankingPublisher_DeltaSendsOnlyChanged(t *testing.T) {
	pp := testPublishParams()
	pp.RankingDeltaEnabled = true
	p := NewRankingPublisher(pp)
	srv, cli := Pipe()
	conns := map[proto.StoreId]Connection{"a": srv}

	p.Publish(0, entries(100, 200, 300), conns) // 全量でベースラインを作る
	expectType(t, cli, proto.TypeRankingSnapshot)

	// b だけスコアが動いた。
	p.Publish(300, entries(100, 250, 300), conns)
	env := expectType(t, cli, proto.TypeRankingDelta)

	var d proto.RankingDelta
	if err := json.Unmarshal(env.Payload, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(d.Entries) != 1 {
		t.Fatalf("差分の件数=%d, want 1（変化した店だけ）: %+v", len(d.Entries), d.Entries)
	}
	if d.Entries[0].StoreId != "b" || d.Entries[0].Score != 250 {
		t.Fatalf("差分の中身が違う: %+v", d.Entries[0])
	}

	// 差分は Rank を持たない（型に無いことを JSON で固定する）。
	if _, ok := any(d.Entries[0]).(interface{ GetRank() int }); ok {
		t.Fatal("RankingChange に Rank が生えている")
	}
	raw, _ := json.Marshal(d.Entries[0])
	if string(raw) != `{"storeId":"b","score":250,"alive":true}` {
		t.Fatalf("RankingChange のワイヤ形式が違う: %s", raw)
	}
}

// 変化が無ければ差分は送らない（空の Envelope で帯域を使わない）。
func TestRankingPublisher_DeltaSkipsWhenUnchanged(t *testing.T) {
	pp := testPublishParams()
	pp.RankingDeltaEnabled = true
	p := NewRankingPublisher(pp)
	srv, cli := Pipe()
	conns := map[proto.StoreId]Connection{"a": srv}

	p.Publish(0, entries(100), conns)
	expectType(t, cli, proto.TypeRankingSnapshot)

	p.Publish(300, entries(100), conns) // 変化なし
	expectNothing(t, cli)
}

// MarkSnapshotSent は「別経路で全量が配られた」ことを伝え、二重送信を防ぐ。
//
// 足切り直後の全量は game が Outbound の順序契約の中で流す（plan-h23 §3.1 の4）。
// これを知らせないと、直後の定期配信で同じものをもう一度配ることになる。
func TestRankingPublisher_MarkSnapshotSentSuppressesDuplicate(t *testing.T) {
	p := NewRankingPublisher(testPublishParams())
	srv, cli := Pipe()
	conns := map[proto.StoreId]Connection{"a": srv}
	e := entries(100)

	p.MarkSnapshotSent(0, e) // game が配った
	p.Publish(0, e, conns)   // 定期配信は黙る
	expectNothing(t, cli)

	p.Publish(1000, e, conns) // 間隔経過 → 再開
	expectType(t, cli, proto.TypeRankingSnapshot)
}

// MarkSnapshotSent は差分のベースラインも更新する。
//
// 更新しないと、game が配った内容を「まだ配っていない」と誤認して差分に載せ、
// 同じ値を二重に送る。
func TestRankingPublisher_MarkSnapshotSentRebaselinesDelta(t *testing.T) {
	pp := testPublishParams()
	pp.RankingDeltaEnabled = true
	p := NewRankingPublisher(pp)
	srv, cli := Pipe()
	conns := map[proto.StoreId]Connection{"a": srv}

	p.MarkSnapshotSent(0, entries(100, 200))
	p.Publish(300, entries(100, 200), conns) // 内容は同じ → 差分なし
	expectNothing(t, cli)
}

var _ StatePublisher = (*RankingPublisher)(nil)
