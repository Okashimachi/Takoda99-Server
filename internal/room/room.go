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
			r.dispatch(r.session.Tick(r.tickMs))
			r.publish()
		}
	}
}

func (r *Room) publish() {
	if r.publisher == nil {
		return
	}
	stores, aliveCount := r.session.Snapshot()
	r.publisher.Publish(r.elapsedMs, stores, aliveCount, r.conns)

	// 観測ストリームへ相乗り（session.Snapshot() を再利用・二重計算しない）。
	// h01 は payload = 既存 StoreListUpdate。h02 で AdminSnapshot に差し替える（plan-h00 §4）。
	if r.hub != nil {
		if env, ok := envelopeOf(proto.StoreListUpdate{Stores: stores, AliveCount: aliveCount}); ok {
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

func envelopeOf(msg any) (proto.Envelope, bool) {
	var typ string
	switch msg.(type) {
	case proto.MatchStart:
		typ = proto.TypeMatchStart
	case proto.CustomerView:
		typ = proto.TypeCustomerArrived
	case proto.CustomerLeft:
		typ = proto.TypeCustomerLeft
	case proto.CreditUpdate:
		typ = proto.TypeCreditUpdate
	case proto.EvaluationUpdate:
		typ = proto.TypeEvaluationUpdate
	case proto.DifficultyUpdate:
		typ = proto.TypeDifficultyUpdate
	case proto.PhaseChange:
		typ = proto.TypePhaseChange
	case proto.StoreListUpdate:
		typ = proto.TypeStoreListUpdate
	case proto.ForcedEliminationWarning:
		typ = proto.TypeForcedEliminationWarning
	case proto.StoreEliminated:
		typ = proto.TypeStoreEliminated
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
