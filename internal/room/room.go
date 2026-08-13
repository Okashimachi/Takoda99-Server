// Package room は【スパイン】試合の駆動役。1試合=1goroutineで、Connection からの入力(inbox)と
// tick を単一ループで直列処理し、game.Session（純粋コア）を回して Outbound を接続へ配信する。
//
// 依存: game(コア) と transport(通信) と proto(契約)。game/transport はここを import しない。
// tick 周期は GameParameters（session.tickIntervalMs）由来。時計は Clock で注入（本番=実時間/テスト=手動）。
package room

import (
	"context"
	"encoding/json"
	"time"

	"takoda99/internal/admin"
	"takoda99/internal/game"
	"takoda99/internal/proto"
	"takoda99/internal/transport"
)

// Ticker / Clock は tick の時計を抽象化する（本番=実時間、テスト/シミュレーション=手動）。
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type Clock interface {
	NewTicker(d time.Duration) Ticker
}

// RealClock は time.Ticker を使う本番用 Clock。
type RealClock struct{}

func (RealClock) NewTicker(d time.Duration) Ticker { return realTicker{time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

type inbound struct {
	pid game.PlayerId
	env proto.Envelope
}

// Room は1試合を駆動する。session/conns/tickMs/clock/publisher を注入し、Run(ctx) で回す。
type Room struct {
	session   *game.Session
	conns     map[game.PlayerId]transport.Connection
	tickMs    int
	clock     Clock
	publisher transport.StatePublisher
	hub       *admin.Hub // 観測ファンアウト（nil 安全・sim/既存テストでは未注入）
	inbox     chan inbound
	done      chan struct{}
	elapsedMs int64

	// 間引きの前回送信時刻（plan-h23 §4）。game は毎tick返し、ここで捨てる。
	lastEvalMs int64
	lastWarnMs int64
	throttleOn bool
}

// SetAdminHub は観測用の AdminHub を注入する（nil 安全）。
//
// room.New の署名を変えないためのセッター。試合ごとに生成される Room に、プロセス共有の
// hub を app.RunMatch から渡す（配線の正典は plan-h00 §3）。未注入(nil)なら publish() は
// 観測配信をしない＝sim/既存テストに非破壊。
func (r *Room) SetAdminHub(h *admin.Hub) { r.hub = h }

// New は Room を作る。conns は playerId→接続。tickMs は tick 周期(ms)。
func New(session *game.Session, conns map[game.PlayerId]transport.Connection, tickMs int, clock Clock, publisher transport.StatePublisher) *Room {
	if tickMs <= 0 {
		tickMs = game.DefaultParameters().Session.TickIntervalMs
	}
	return &Room{
		session: session, conns: conns, tickMs: tickMs, clock: clock, publisher: publisher,
		inbox: make(chan inbound, 256), done: make(chan struct{}),
	}
}

// Run は試合を最後まで（Finished）駆動する。ctx キャンセルでも抜ける。
func (r *Room) Run(ctx context.Context) {
	defer close(r.done)
	defer r.closeConns()
	for pid, c := range r.conns {
		go r.readConn(pid, c)
	}

	startsAt := time.Now().UnixMilli() + int64(r.session.Params().Matching.ReadyCountdownMs)
	r.dispatch(r.session.Start(startsAt))
	if r.session.State() == game.Finished {
		return
	}

	ticker := r.clock.NewTicker(time.Duration(r.tickMs) * time.Millisecond)
	defer ticker.Stop()
	for r.session.State() != game.Finished {
		select {
		case <-ctx.Done():
			return
		case in := <-r.inbox:
			r.dispatch(r.handle(in))
		case <-ticker.C():
			r.elapsedMs += int64(r.tickMs)
			r.dispatchTick(r.session.Tick(r.tickMs))
			r.publish()
		}
	}
}

func (r *Room) publish() {
	if r.publisher == nil {
		return
	}
	r.publisher.Publish(r.elapsedMs, r.session.RankingEntries(), r.conns)

	// 観測ストリームへ相乗り。h02: payload を AdminSnapshot（客分配・フェーズ・heat・storm 込み）
	// に差し替え（plan-h00 §4 / plan-h02 §1.3）。session の純粋 getter を読むだけで、session を
	// 触るのはこの room goroutine だけなのでデータ競合しない。
	if r.hub != nil {
		if env, ok := admin.SnapshotEnvelope(r.session); ok {
			r.hub.Broadcast(env)
		}
	}
}

func (r *Room) closeConns() {
	for _, c := range r.conns {
		_ = c.Close()
	}
}

func (r *Room) readConn(pid game.PlayerId, c transport.Connection) {
	for {
		select {
		case env, ok := <-c.Receive():
			if !ok {
				return
			}
			select {
			case r.inbox <- inbound{pid: pid, env: env}:
			case <-r.done:
				return
			}
		case <-r.done:
			return
		}
	}
}

func (r *Room) handle(in inbound) []game.Outbound {
	switch in.env.Type {
	case proto.TypeOrderServed:
		var m proto.OrderServed
		if json.Unmarshal(in.env.Payload, &m) == nil {
			return r.session.ApplyOrderServed(in.pid, m)
		}
	}
	return nil
}

// dispatchTick は tick 由来の Outbound を、間引きを効かせて配る（plan-h23 §4）。
//
// 🔴 **足切りバースト（StoreEliminatedBatch を含む配信）は間引かない。**
// 順位が大量に入れ替わった直後を落とすと、次の配信まで表示がズレたままになる。
//
// 🔴 **OrderServed の即レスはこちらを通さない**（dispatch を直接使う）。
// クライアントは「提供したのに EvaluationUpdate が返らない＝リジェクト」で不正申告を
// 検知しているので、ここを間引くとリジェクトが判別できなくなる。
func (r *Room) dispatchTick(out []game.Outbound) {
	r.dispatch(r.throttle(out))
}

// throttle は tick 由来の Outbound から、間引き対象で今回送らないものを落とす。
//
// 判定は**配信ごとに1回**（メッセージごとではない）。99店ぶんの EvaluationUpdate が
// 1回の配信に並ぶので、1件ずつ時計を進めると先頭の1件しか通らなくなる。
func (r *Room) throttle(out []game.Outbound) []game.Outbound {
	burst := false
	for _, o := range out {
		if _, ok := o.Msg.(proto.StoreEliminatedBatch); ok {
			burst = true
			break
		}
	}

	pp := r.session.Params().Publish
	// 起動直後（!throttleOn）は必ず通す。間隔経過を待つと、試合開始から最初の1回ぶん
	// 何も届かない時間ができる。
	sendEval := burst || !r.throttleOn || r.elapsedMs-r.lastEvalMs >= int64(pp.EvaluationIntervalMs)
	sendWarn := burst || !r.throttleOn || r.elapsedMs-r.lastWarnMs >= int64(pp.WarningIntervalMs)
	r.throttleOn = true
	if sendEval {
		r.lastEvalMs = r.elapsedMs
	}
	if sendWarn {
		r.lastWarnMs = r.elapsedMs
	}

	kept := make([]game.Outbound, 0, len(out))
	sawSnapshot := false
	for _, o := range out {
		switch m := o.Msg.(type) {
		case proto.EvaluationUpdate:
			if !sendEval {
				continue
			}
		case proto.ForcedEliminationWarning:
			if !sendWarn {
				continue
			}
		case proto.RankingSnapshot:
			// game が順序契約の中で全量を流した（plan-h23 §3.1 の4）。
			// 定期配信側の時計と差分のベースラインを揃えて二重送信を避ける。
			if r.publisher != nil && !sawSnapshot {
				sawSnapshot = true
				r.publisher.MarkSnapshotSent(r.elapsedMs, m.Entries)
			}
		}
		kept = append(kept, o)
	}
	return kept
}

func (r *Room) dispatch(out []game.Outbound) {
	for _, o := range out {
		env, ok := envelopeOf(o.Msg)
		if !ok {
			continue
		}
		if o.To.Broadcast {
			for _, c := range r.conns {
				_ = c.Send(env)
			}
			continue
		}
		if c := r.conns[o.To.PlayerId]; c != nil {
			_ = c.Send(env)
		}
	}
}

// envelopeOf は Outbound の中身を type タグ付き Envelope に包む。
//
// 🔴 **ここに無い型は黙って捨てられる。** 新しいメッセージを game から返すときは
// 必ずここへ追加すること。忘れると「なぜか届かない」という追いにくい不具合になる。
func envelopeOf(msg any) (proto.Envelope, bool) {
	var typ string
	switch msg.(type) {
	case proto.MatchStart:
		typ = proto.TypeMatchStart
	case proto.CustomerView:
		typ = proto.TypeCustomerArrived
	case proto.EvaluationUpdate:
		typ = proto.TypeEvaluationUpdate
	case proto.DifficultyUpdate:
		typ = proto.TypeDifficultyUpdate
	case proto.PhaseChange:
		typ = proto.TypePhaseChange
	case proto.ForcedEliminationWarning:
		typ = proto.TypeForcedEliminationWarning
	case proto.StoreEliminated:
		typ = proto.TypeStoreEliminated
	case proto.StoreEliminatedBatch:
		typ = proto.TypeStoreEliminatedBatch
	case proto.RankingSnapshot:
		typ = proto.TypeRankingSnapshot
	case proto.RankingDelta:
		typ = proto.TypeRankingDelta
	// 🔴 h23 まで **ここに PersonalResult が無く、リザルトが1通も届いていなかった**。
	// envelopeOf に無い型は dispatch が黙って捨てるので、送っているつもりで
	// 届いていないことに誰も気づけない（plan-h23 §5 が警告していた失敗そのもの）。
	case proto.PersonalResult:
		typ = proto.TypePersonalResult
	case proto.MatchEnd:
		typ = proto.TypeMatchEnd
	case proto.MatchmakingStatus:
		typ = proto.TypeMatchmakingStatus
	default:
		return proto.Envelope{}, false
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return proto.Envelope{}, false
	}
	return proto.Envelope{Type: typ, Payload: data}, true
}
