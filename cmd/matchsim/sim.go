package main

import (
	"math/rand"

	"takoda99/internal/game"
	"takoda99/internal/odai"
	"takoda99/internal/proto"
)

// sim.go は1試合ぶんのヘッドレス実行。session を直接 tick 駆動する。

// phaseEvent はフェーズが切り替わった時点の記録。
type phaseEvent struct {
	tick      int
	elapsedMs int64
	phase     proto.Phase
	alive     int
}

// aliveSample は生存店数が変化した時点の記録。
type aliveSample struct {
	tick      int
	elapsedMs int64
	alive     int
}

// runResult は1試合ぶんの計測結果。
type runResult struct {
	profile profile
	seed    int64
	stores  int
	tickMs  int

	stalled   bool // max-ticks に到達（＝決着しなかった）
	ticks     int
	elapsedMs int64

	finalPhase proto.Phase
	finalHeat  int
	aliveAtEnd int

	winner         game.PlayerId
	winnerMsPerKey int
	winnerMissRate float64

	phaseEvents   []phaseEvent
	aliveTimeline []aliveSample

	selfCollapses int // 自滅脱落した店数
	culls         int // 下位淘汰(storm)で落ちた店数
	servedTotal   int
	// leftTotal は我慢切れで帰られた客の延べ数。servedTotal との比が
	// 「捌けた客 / 逃した客」で、信用の減りやすさを直接表す調整材料になる。
	leftTotal int

	// rejected は session に弾かれた OrderServed の数。
	// 正常なら 0。0 でないならダミー店の行列が session の storeQueues とズレており、
	// sim の結果が実態と食い違っている（＝この数値は必ず目に入る所へ出す）。
	rejected int
}

// simulate は1試合をヘッドレスに完走させる。
//
// rng はダミー店の打鍵モデル用。session 側の乱数はここで別 stream に分岐させる。
// 同じ stream を共有すると、打鍵モデルを触っただけで客の生成・分配まで別物になり、
// 「パラメータを変える前後で比較する」というこの道具の目的が果たせなくなる。
func simulate(params game.GameParameters, n int, p profile, rng *rand.Rand, maxTicks int) runResult {
	sessRng := rand.New(rand.NewSource(rng.Int63()))

	dummies := buildStores(n, p, rng)
	inits := make([]game.PlayerInit, n)
	byId := make(map[game.PlayerId]*dummyStore, n)
	for i, d := range dummies {
		inits[i] = game.PlayerInit{Id: d.id, DisplayName: string(d.id)}
		byId[d.id] = d
	}

	sess := game.NewSession("sim", params, odai.NewStaticPool(), sessRng, inits)
	tickMs := params.Session.TickIntervalMs

	r := runResult{profile: p, stores: n, tickMs: tickMs, finalPhase: proto.PhaseEarly}
	tick := 0

	handle := func(out []game.Outbound) {
		for _, o := range out {
			switch m := o.Msg.(type) {
			case proto.CustomerView:
				if d := byId[o.To.PlayerId]; d != nil {
					d.arrive(m)
				}
			case proto.CustomerLeft:
				r.leftTotal++
				if d := byId[o.To.PlayerId]; d != nil {
					d.leave(m.CustomerId)
				}
			case proto.PhaseChange:
				r.finalPhase = m.Phase
				r.phaseEvents = append(r.phaseEvents, phaseEvent{
					tick: tick, elapsedMs: sess.ElapsedMs(), phase: m.Phase, alive: sess.AliveCount(),
				})
			case proto.DifficultyUpdate:
				r.finalHeat = m.HeatLevel
			case proto.StoreEliminated:
				if d := byId[m.StoreId]; d != nil {
					d.alive = false
				}
			}
		}
	}

	handle(sess.Start())
	prevAlive := sess.AliveCount()
	r.aliveTimeline = append(r.aliveTimeline, aliveSample{tick: 0, alive: prevAlive})

	for tick = 1; tick <= maxTicks; tick++ {
		handle(sess.Tick(tickMs))

		// ダミー店の打鍵を進め、打ち終わったら報告する。
		// ApplyOrderServed も Outbound を返すので、捨てると次の来店を取りこぼす。
		for _, d := range dummies {
			o, done := d.step(tickMs, rng)
			if !done {
				continue
			}
			res := sess.ApplyOrderServed(d.id, o)
			if len(res) == 0 {
				r.rejected++
			}
			handle(res)
		}

		if a := sess.AliveCount(); a != prevAlive {
			prevAlive = a
			r.aliveTimeline = append(r.aliveTimeline, aliveSample{
				tick: tick, elapsedMs: sess.ElapsedMs(), alive: a,
			})
		}

		if sess.State() == game.Finished {
			return finalize(r, sess, byId, tick, false)
		}
	}
	return finalize(r, sess, byId, maxTicks, true)
}

// finalize は試合結果から集計値を埋める。
func finalize(r runResult, sess *game.Session, byId map[game.PlayerId]*dummyStore,
	ticks int, stalled bool) runResult {

	r.ticks = ticks
	r.elapsedMs = sess.ElapsedMs()
	r.stalled = stalled
	r.aliveAtEnd = sess.AliveCount()

	for _, res := range sess.Results() {
		r.servedTotal += res.Stats.ServedCount
		switch res.Elimination {
		case string(proto.ElimSelfCollapse):
			r.selfCollapses++
		case string(proto.ElimCull):
			r.culls++
		}
		if res.FinalRank == 1 {
			r.winner = res.StoreId
			if d := byId[res.StoreId]; d != nil {
				r.winnerMsPerKey = d.msPerKey
				r.winnerMissRate = d.missRate
			}
		}
	}
	return r
}
