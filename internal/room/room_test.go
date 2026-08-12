package room

import (
	"context"
	"encoding/json"
	"math/rand"
	"testing"
	"time"

	"takoda99/internal/admin"
	"takoda99/internal/game"
	"takoda99/internal/proto"
	"takoda99/internal/transport"
)

// ── テスト用スタブ・時計 ──

type stubWords struct{}

func (stubWords) Next(int, *rand.Rand) game.Word { return game.Word{Text: "ねこ", KeystrokeCount: 4} }

// nopClock は tick を発火しない（コアループの入出力だけを試すため）。
type nopClock struct{}

func (nopClock) NewTicker(time.Duration) Ticker { return nopTicker{} }

type nopTicker struct{}

func (nopTicker) C() <-chan time.Time { return nil } // nil チャネルは select で永久に発火しない
func (nopTicker) Stop()               {}

// manualClock/manualTicker はテストから任意のタイミングで1 tick を発火させる。
type manualTicker struct{ c chan time.Time }

func (m manualTicker) C() <-chan time.Time { return m.c }
func (m manualTicker) Stop()               {}

type manualClock struct{ ticker manualTicker }

func (m manualClock) NewTicker(time.Duration) Ticker { return m.ticker }

func recvEnv(t *testing.T, c transport.Connection) proto.Envelope {
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

// Room が session を回し、Connection 経由で MatchStart 配信・OrderServed 往復ができる。
func TestRoom_CoreLoopThroughConnection(t *testing.T) {
	sess := game.NewSession("m1", game.DefaultParameters(),
		stubWords{},
		rand.New(rand.NewSource(1)),
		[]game.PlayerInit{{Id: "a", DisplayName: "a"}, {Id: "b", DisplayName: "b"}})

	sa, ca := transport.Pipe() // a: server端 / client端
	sb, _ := transport.Pipe()
	conns := map[game.PlayerId]transport.Connection{"a": sa, "b": sb}

	rm := New(sess, conns, 150, nopClock{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rm.Run(ctx)

	// Start で a に MatchStart が届く。
	env := recvEnv(t, ca)
	if env.Type != proto.TypeMatchStart {
		t.Fatalf("最初は MatchStart のはず: got %s", env.Type)
	}
	var ms proto.MatchStart
	if err := json.Unmarshal(env.Payload, &ms); err != nil || ms.SelfStoreId != "a" {
		t.Fatalf("MatchStart 復号失敗: %v %+v", err, ms)
	}
	if len(ms.Stores) != 2 {
		t.Fatalf("Stores は2店のはず: got %d", len(ms.Stores))
	}
}

// hub を注入すると、publish() のたびに観測 conn へ StoreListUpdate が届く（plan-h01）。
func TestRoom_BroadcastsToAdminHub(t *testing.T) {
	sess := game.NewSession("m1", game.DefaultParameters(),
		stubWords{},
		rand.New(rand.NewSource(1)),
		[]game.PlayerInit{{Id: "a", DisplayName: "a"}, {Id: "b", DisplayName: "b"}})

	sa, _ := transport.Pipe()
	sb, _ := transport.Pipe()
	conns := map[game.PlayerId]transport.Connection{"a": sa, "b": sb}

	tickCh := make(chan time.Time, 1)
	rm := New(sess, conns, 150, manualClock{ticker: manualTicker{c: tickCh}},
		transport.NewFullPublisher(0))

	// 観測者を hub に登録（/admin/ws 相当）。
	hub := admin.NewHub()
	obsSrv, obsCli := transport.Pipe()
	hub.Register(obsSrv)
	rm.SetAdminHub(hub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rm.Run(ctx)

	// 1 tick 発火 → publish() → hub.Broadcast。
	tickCh <- time.Now()

	if env := recvEnv(t, obsCli); env.Type != proto.TypeStoreListUpdate {
		t.Fatalf("観測者は StoreListUpdate を受けるはず: got %s", env.Type)
	}
}

// Room の Run が終了したら全接続を閉じる。クライアントがリザルト画面を開いたまま
// 放置しても、サーバー側に無駄な接続が残らないことを確認する回帰テスト。
func TestRoom_ClosesConnectionsOnExit(t *testing.T) {
	// 2プレイヤーで構成。ctx キャンセルで Run を終了させ、接続が閉じることを確認する。
	sess := game.NewSession("m1", game.DefaultParameters(),
		stubWords{},
		rand.New(rand.NewSource(1)),
		[]game.PlayerInit{{Id: "a", DisplayName: "a"}, {Id: "b", DisplayName: "b"}})

	sa, ca := transport.Pipe()
	sb, _ := transport.Pipe()
	conns := map[game.PlayerId]transport.Connection{"a": sa, "b": sb}

	tickCh := make(chan time.Time, 1)
	rm := New(sess, conns, 150, manualClock{ticker: manualTicker{c: tickCh}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rm.Run(ctx); close(done) }()

	// Start で MatchStart が届く。
	if env := recvEnv(t, ca); env.Type != proto.TypeMatchStart {
		t.Fatalf("最初は MatchStart のはず: got %s", env.Type)
	}

	// ctx キャンセルで Run を終了させる。
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run が ctx キャンセルで終了しない")
	}

	// Run 終了後に接続が閉じることを確認する。
	drained := false
	for i := 0; i < 4 && !drained; i++ {
		select {
		case _, ok := <-ca.Receive():
			if !ok {
				drained = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("接続が閉じない（closeConns が効いていない）")
		}
	}
	if !drained {
		t.Fatal("Run 終了後も接続が閉じていない")
	}
}
