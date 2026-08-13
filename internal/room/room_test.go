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

// hub を注入すると、publish() のたびに観測 conn へ AdminSnapshot が届く（plan-h01/h02）。
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
		transport.NewRankingPublisher(game.DefaultParameters().Publish))

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

	// h02: 観測ストリームは AdminSnapshot（客分配・フェーズ・storm 込み）を流す。
	if env := recvEnv(t, obsCli); env.Type != admin.TypeAdminSnapshot {
		t.Fatalf("観測者は AdminSnapshot を受けるはず: got %s", env.Type)
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

// ── 配信層（plan-h23）───────────────────────────────────────

// Recipient はテスト用の宛先ヘルパ（空文字＝ブロードキャスト）。
func Recipient(t *testing.T, pid game.PlayerId) game.Recipient {
	t.Helper()
	if pid == "" {
		return game.Recipient{Broadcast: true}
	}
	return game.Recipient{PlayerId: pid}
}

// newThrottleSession は間引きテスト用の最小セッション（パラメータだけ使う）。
func newThrottleSession(t *testing.T) *game.Session {
	t.Helper()
	return game.NewSession("t", game.DefaultParameters(), stubWords{},
		rand.New(rand.NewSource(1)), []game.PlayerInit{{Id: "s-1"}})
}

// envelopeOf が本戦で使う全メッセージを変換できる。
//
// 🔴 **ここに無い型は dispatch で黙って捨てられる。** 追加漏れは「なぜか届かない」という
// 追いにくい不具合になるので、game が返しうる型を網羅して固定する。
func TestEnvelopeOf_CoversAllHonsenMessages(t *testing.T) {
	cases := []struct {
		msg  any
		want string
	}{
		{proto.MatchStart{}, proto.TypeMatchStart},
		{proto.CustomerView{}, proto.TypeCustomerArrived},
		{proto.EvaluationUpdate{}, proto.TypeEvaluationUpdate},
		{proto.DifficultyUpdate{}, proto.TypeDifficultyUpdate},
		{proto.PhaseChange{}, proto.TypePhaseChange},
		{proto.ForcedEliminationWarning{}, proto.TypeForcedEliminationWarning},
		{proto.StoreEliminated{}, proto.TypeStoreEliminated},
		{proto.StoreEliminatedBatch{}, proto.TypeStoreEliminatedBatch},
		{proto.RankingSnapshot{}, proto.TypeRankingSnapshot},
		{proto.RankingDelta{}, proto.TypeRankingDelta},
		{proto.PersonalResult{}, proto.TypePersonalResult},
		{proto.MatchEnd{}, proto.TypeMatchEnd},
		{proto.MatchmakingStatus{}, proto.TypeMatchmakingStatus},
	}
	for _, c := range cases {
		env, ok := envelopeOf(c.msg)
		if !ok {
			t.Fatalf("%T が変換できない（dispatch で捨てられる）", c.msg)
		}
		if env.Type != c.want {
			t.Fatalf("%T → type=%q, want %q", c.msg, env.Type, c.want)
		}
	}

	// 未知の型は捨てる（型を足したのに envelopeOf を直し忘れたら false になる）。
	if _, ok := envelopeOf(struct{ X int }{}); ok {
		t.Fatal("未知の型が変換されている")
	}
}

// 定期の EvaluationUpdate / ForcedEliminationWarning が間引かれる（plan-h23 §4）。
func TestDispatchTick_ThrottlesPeriodicMessages(t *testing.T) {
	r := &Room{
		session:   newThrottleSession(t),
		conns:     map[game.PlayerId]transport.Connection{},
		elapsedMs: 0,
	}
	evalIv := int64(r.session.Params().Publish.EvaluationIntervalMs)

	out := []game.Outbound{
		{To: Recipient(t, "s-1"), Msg: proto.EvaluationUpdate{}},
		{To: Recipient(t, "s-1"), Msg: proto.ForcedEliminationWarning{}},
	}

	// 初回は必ず通る（起動直後に何も届かない時間を作らない）。
	if got := r.throttle(out); len(got) != 2 {
		t.Fatalf("初回=%d件, want 2", len(got))
	}
	// 間隔未満 → 両方落ちる。
	r.elapsedMs = evalIv - 1
	if got := r.throttle(out); len(got) != 0 {
		t.Fatalf("間引かれていない: %d件", len(got))
	}
	// EvaluationUpdate の間隔だけ経過 → EvaluationUpdate は通り、予告はまだ落ちる。
	r.elapsedMs = evalIv
	got := r.throttle(out)
	if len(got) != 1 {
		t.Fatalf("evalのみ通るはず: %d件", len(got))
	}
	if _, ok := got[0].Msg.(proto.EvaluationUpdate); !ok {
		t.Fatalf("通ったのが EvaluationUpdate でない: %T", got[0].Msg)
	}
}

// 🔴 足切りバースト（StoreEliminatedBatch を含む配信）は間引かれない。
//
// 順位が大量に入れ替わった直後を落とすと、次の配信まで表示がズレたままになる。
func TestDispatchTick_BurstBypassesThrottle(t *testing.T) {
	r := &Room{
		session: newThrottleSession(t),
		conns:   map[game.PlayerId]transport.Connection{},
	}
	periodic := []game.Outbound{{To: Recipient(t, "s-1"), Msg: proto.EvaluationUpdate{}}}
	r.throttle(periodic) // 初回を消費して throttle を効かせる

	r.elapsedMs = 1 // 間隔には遠く及ばない
	if got := r.throttle(periodic); len(got) != 0 {
		t.Fatalf("前提が崩れた: 間引かれていない %d件", len(got))
	}

	burst := []game.Outbound{
		{To: Recipient(t, ""), Msg: proto.StoreEliminatedBatch{}},
		{To: Recipient(t, "s-1"), Msg: proto.PersonalResult{}},
		{To: Recipient(t, "s-1"), Msg: proto.EvaluationUpdate{}},
		{To: Recipient(t, ""), Msg: proto.RankingSnapshot{}},
		{To: Recipient(t, "s-1"), Msg: proto.ForcedEliminationWarning{}},
	}
	got := r.throttle(burst)
	if len(got) != len(burst) {
		t.Fatalf("バーストが間引かれた: %d件, want %d", len(got), len(burst))
	}
}

// OrderServed の即レスは間引かれない（dispatch を直接通る）。
//
// クライアントは「提供したのに EvaluationUpdate が返らない＝リジェクト」で不正申告を
// 検知している。ここを間引くとリジェクトが判別できなくなる。
func TestDispatch_ImmediateResponseIsNotThrottled(t *testing.T) {
	srv, cli := transport.Pipe()
	r := &Room{
		session: newThrottleSession(t),
		conns:   map[game.PlayerId]transport.Connection{"s-1": srv},
	}
	// 直前に tick 由来の EvaluationUpdate を通しておく（throttle の時計を進める）。
	r.throttle([]game.Outbound{{To: Recipient(t, "s-1"), Msg: proto.EvaluationUpdate{}}})

	// 間隔未満でも dispatch は素通しする。
	r.elapsedMs = 1
	r.dispatch([]game.Outbound{{To: Recipient(t, "s-1"), Msg: proto.EvaluationUpdate{}}})

	select {
	case env := <-cli.Receive():
		if env.Type != proto.TypeEvaluationUpdate {
			t.Fatalf("type=%s, want EvaluationUpdate", env.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("即レスが間引かれた（リジェクト検知が壊れる）")
	}
}
