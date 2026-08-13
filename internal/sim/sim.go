// Package sim は【開発ツール】1試合をヘッドレスに tick 駆動するシミュレータ。
//
// 用途は2つある。混同しないこと。
//
//   - **バランス調整**（Plan-13 / `cmd/matchsim`）— 決着が目安時間に収まるか、数値を変えて反復する
//   - **決着保証の検証**（Plan-14 / `sim_test.go`）— そもそも試合が必ず終わるか。CI で毎回回す
//
// ⚠ 本戦（plan-h22）で決着保証の意味が変わった。予選は「storm が効いて必ず終わるか」だったが、
// 本戦は cullSchedule の最終ステージ（120秒）で**時刻により必ず終わる**ので、それ自体は自明。
// 代わりに検証するのは「脱落カーブが targetAliveCount どおりか」「20秒より前に誰も落ちないか」
// 「finalRank が重複なく 1..N を埋めるか」「同じシードで再現するか」（plan-h22 §5）。
//
// どちらも room/transport/bot を通さず game.Session を直接叩く。room は実時計(ticker)で
// 回るので「1試合を数秒で」が成立しないため。Bot の代わりに、打鍵速度とミス率の2値で実力を
// 表したダミー店（dummy.go）を持ち、ApplyOrderServed を直接呼ぶ。
//
// 99接続の WebSocket を実時間で捌けるかという**性能検証は別物**で、Plan-18 の負荷テストが担う。
//
// 依存の向きは sim → game の一方向。game は sim を知らない（.golangci.yml の depguard で機械強制）。
package sim

import (
	"math/rand"

	"takoda99/internal/game"
	"takoda99/internal/odai"
	"takoda99/internal/proto"
)

// Config は1試合ぶんのシミュレーション条件。
type Config struct {
	Params  game.GameParameters
	Stores  int
	Profile Profile

	// Rng はダミー店の打鍵モデル用。session 側の乱数はここから別 stream に分岐させる。
	Rng *rand.Rand

	// MaxTicks を超えたら膠着(Stalled)とみなして打ち切る。
	MaxTicks int
}

// PhaseChangeAt はフェーズが切り替わった時点。
type PhaseChangeAt struct {
	Tick      int
	ElapsedMs int64
	Phase     proto.Phase
	Alive     int
}

// CullStageResult は足切りステージ1段ぶんの結果。
type CullStageResult struct {
	StageIndex int // 1始まり
	ElapsedMs  int64
	Alive      int // ステージ実行直後の生存数
}

// AlivePoint は生存店数が変化した時点。
type AlivePoint struct {
	Tick      int
	ElapsedMs int64
	Alive     int
}

// Result は1試合ぶんの計測結果。
type Result struct {
	Profile Profile
	Stores  int
	TickMs  int

	Stalled   bool // MaxTicks 到達（＝決着しなかった）
	Ticks     int
	ElapsedMs int64

	FinalPhase proto.Phase
	AliveAtEnd int

	HeatLevel    int // 決着時の火力
	MaxHeatLevel int // 試合中の最大火力
	// WordMaxLevel はお題辞書に語彙が存在する最大段階。
	// これを超えて HeatLevel が上がってもお題は変わらない（難度が頭打ち）。
	WordMaxLevel int
	// TicksAtMaxHeat は難度が頭打ち（HeatLevel >= WordMaxLevel）だった tick 数。
	// ここが長いまま決着しないなら、火力では試合を畳めていない。
	TicksAtMaxHeat int

	Winner         game.PlayerId
	WinnerMsPerKey int
	WinnerMissRate float64

	PhaseChanges []PhaseChangeAt
	AliveCurve   []AlivePoint

	Culls  int // 足切りで落ちた店数（本戦では脱落経路がこれ1本なので = 全店数）
	Served int // 提供できた客の延べ数

	// CullStages は各足切りステージ直後の生存数（plan-h22 §5）。
	// targetAliveCount どおりに脱落カーブが出ているかを見る。
	CullStages []CullStageResult

	// Results は全店の最終結果（順位・スコア）。finalRank の重複検査と
	// 決定性の検証に使う。h26 のスコア分布の観測もここから取れる。
	Results []game.StoreResult

	// Rejected は session に弾かれた OrderServed の数。
	// 正常なら 0。0 でないならダミー店の行列が session の storeQueues とズレており、
	// 結果が実態と食い違っている。
	Rejected int
}

// Simulate は1試合をヘッドレスに完走させる。
//
// cfg.Rng はダミー店の打鍵モデル用で、session 側の乱数はここで別 stream に分岐させる。
// 同じ stream を共有すると、打鍵モデルを触っただけで客の生成・分配まで別物になり、
// 「パラメータを変える前後で比較する」という道具の目的が果たせなくなる。
func Simulate(cfg Config) Result {
	rng := cfg.Rng
	sessRng := rand.New(rand.NewSource(rng.Int63()))

	dummies := buildStores(cfg.Stores, cfg.Profile, rng)
	inits := make([]game.PlayerInit, cfg.Stores)
	byId := make(map[game.PlayerId]*dummyStore, cfg.Stores)
	for i, d := range dummies {
		inits[i] = game.PlayerInit{Id: d.id, DisplayName: string(d.id)}
		byId[d.id] = d
	}

	words := odai.NewStaticPool()
	// シミュレーションなので即座に開始させる
	params := cfg.Params
	params.Matching.ReadyCountdownMs = 0

	sess := game.NewSession("sim", params, words, sessRng, inits)
	tickMs := cfg.Params.Session.TickIntervalMs

	r := Result{
		Profile:      cfg.Profile,
		Stores:       cfg.Stores,
		TickMs:       tickMs,
		FinalPhase:   proto.PhaseEarly,
		WordMaxLevel: words.MaxLevel(),
	}
	tick := 0

	handle := func(out []game.Outbound) {
		for _, o := range out {
			switch m := o.Msg.(type) {
			case proto.CustomerView:
				if d := byId[o.To.PlayerId]; d != nil {
					d.arrive(m)
				}
			case proto.PhaseChange:
				r.FinalPhase = m.Phase
				r.PhaseChanges = append(r.PhaseChanges, PhaseChangeAt{
					Tick: tick, ElapsedMs: sess.ElapsedMs(), Phase: m.Phase, Alive: sess.AliveCount(),
				})
			case proto.DifficultyUpdate:
				r.HeatLevel = m.HeatLevel
				if m.HeatLevel > r.MaxHeatLevel {
					r.MaxHeatLevel = m.HeatLevel
				}
			case proto.StoreEliminatedBatch:
				// 足切りは1メッセージに畳まれて届く（h23）。
				for _, e := range m.Entries {
					if d := byId[e.StoreId]; d != nil {
						d.alive = false
					}
				}
			}
		}
	}

	handle(sess.Start(0))
	prevAlive := sess.AliveCount()
	r.AliveCurve = append(r.AliveCurve, AlivePoint{Tick: 0, Alive: prevAlive})

	totalStages := sess.CullState().StageTotal
	stagesDone := 0

	for tick = 1; tick <= cfg.MaxTicks; tick++ {
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
				r.Rejected++
			}
			handle(res)
		}

		// 難度が頭打ちの間を数える。tick 末に見るのは、この tick のお題が
		// stepHeat 後の heatLevel で出されるため。
		if r.WordMaxLevel > 0 && r.HeatLevel >= r.WordMaxLevel {
			r.TicksAtMaxHeat++
		}

		// 足切りステージが1段進んだら、その直後の生存数を記録する。
		if cs := sess.CullState(); true {
			nowDone := cs.StageIndex - 1
			if cs.StageIndex == 0 {
				nowDone = totalStages
			}
			for stagesDone < nowDone {
				stagesDone++
				r.CullStages = append(r.CullStages, CullStageResult{
					StageIndex: stagesDone, ElapsedMs: sess.ElapsedMs(), Alive: sess.AliveCount(),
				})
			}
		}

		if a := sess.AliveCount(); a != prevAlive {
			prevAlive = a
			r.AliveCurve = append(r.AliveCurve, AlivePoint{
				Tick: tick, ElapsedMs: sess.ElapsedMs(), Alive: a,
			})
		}

		if sess.State() == game.Finished {
			return finalize(r, sess, byId, tick, false)
		}
	}
	return finalize(r, sess, byId, cfg.MaxTicks, true)
}

// finalize は試合結果から集計値を埋める。
func finalize(r Result, sess *game.Session, byId map[game.PlayerId]*dummyStore,
	ticks int, stalled bool) Result {

	r.Ticks = ticks
	r.ElapsedMs = sess.ElapsedMs()
	r.Stalled = stalled
	r.AliveAtEnd = sess.AliveCount()

	r.Results = sess.Results()
	for _, res := range r.Results {
		r.Served += res.Stats.ServedCount
		if res.Elimination == string(proto.ElimCull) {
			r.Culls++
		}
		if res.FinalRank == 1 {
			r.Winner = res.StoreId
			if d := byId[res.StoreId]; d != nil {
				r.WinnerMsPerKey = d.msPerKey
				r.WinnerMissRate = d.missRate
			}
		}
	}
	return r
}
