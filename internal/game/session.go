package game

import (
	"fmt"
	"math/rand"

	"textro99/internal/proto"
)

// session.go は【層1コア】1試合の状態機械。純粋・Tick(dt)駆動（時計は持たず room が dt を渡す）。
//
// スコープ境界（#20）:
//   - ここが持つ: 試合ライフサイクル(WaitingStart→Running→Finished)、per-player 状態、
//     コアループ配線（お題クリア→コンボ確定→次のお題発行→作戦切替）、終了判定。
//   - #21a〜d に委譲（本ファイルでは呼び出し口＋TODOのみ）: 威力算出/相殺/撃ち返し(#21a/#21b)、
//     スタック・トラップ・脱落(#21c)、全体/個人難易度・時間切れ着弾(#21d)。

// SessionState は試合の状態。
type SessionState int

const (
	WaitingStart SessionState = iota
	Running
	Finished
)

// Recipient は Outbound の宛先。Broadcast=true で全員。
type Recipient struct {
	PlayerId  PlayerId
	Broadcast bool
}

// Outbound は session が返す「宛先つきメッセージ」。Msg は proto.<Message> の値で、
// room が Envelope に包んで実際の接続へ送る（game は通信を知らない）。
type Outbound struct {
	To  Recipient
	Msg any
}

func to(pid PlayerId, msg any) Outbound { return Outbound{To: Recipient{PlayerId: pid}, Msg: msg} }

// issuedDaken はサーバーが発行済みのダケン（整合検証・打鍵数引きの元）。
// 時間切れ監視用のフィールド（timeLimitMs/issuedAtMs/level/typ）は #21c/#21d が追加する。
type issuedDaken struct {
	keystrokes int
}

// warning は予告(AttackWarning)の識別。expire/相殺解決・power/issuedAt は #21b で拡張する。
type warning struct {
	id       proto.WarningId
	attacker PlayerId
	victim   PlayerId
}

// playerState は1人分の横断状態。per-player に属する状態はここに集約する。
type playerState struct {
	id     PlayerId
	name   string
	p      Player // コンボ（判定式は combo.go）
	stack  int    // ダケンスタック数（増減・脱落は #21c）
	badges int
	koCount int
	strategy int // 現在の作戦（既定4）
	alive  bool

	issued           map[proto.DakenId]*issuedDaken
	pendingAgainstMe []proto.WarningId // 自分宛の予告（カウンター/巻き添えのcontext用）
	lastImpactor     *PlayerId         // 直近着弾者（リベンジ用。着弾記録は #21b/#21c）
	dakenSeq         int
}

// PlayerInit は NewSession に渡す初期プレイヤー情報。
type PlayerInit struct {
	Id          PlayerId
	DisplayName string
}

// Session は1試合。strategies/words はDIPで注入される部品実装。
type Session struct {
	id         proto.MatchId
	params     GameParameters
	strategies map[int]TargetingStrategy
	words      WordSource
	rng        *rand.Rand

	players    map[PlayerId]*playerState
	order      []PlayerId // 安定順（隣狙いの基準は targeting 側でIDソート）
	warnings   map[proto.WarningId]*warning
	state      SessionState
	elapsedMs  int64
	aliveCount int
	warnSeq    int
}

// NewSession は WaitingStart 状態の試合を作る。まだお題は配らない（Start で配る）。
func NewSession(id proto.MatchId, params GameParameters, strategies map[int]TargetingStrategy,
	words WordSource, rng *rand.Rand, inits []PlayerInit) *Session {
	s := &Session{
		id: id, params: params, strategies: strategies, words: words, rng: rng,
		players: make(map[PlayerId]*playerState, len(inits)),
		warnings: make(map[proto.WarningId]*warning),
		state:   WaitingStart,
	}
	for _, in := range inits {
		s.players[in.Id] = &playerState{
			id: in.Id, name: in.DisplayName, strategy: 4, alive: true,
			issued: make(map[proto.DakenId]*issuedDaken),
		}
		s.order = append(s.order, in.Id)
	}
	s.aliveCount = len(inits)
	return s
}

// State は現在の状態を返す。
func (s *Session) State() SessionState { return s.state }

// Start は WaitingStart→Running へ遷移し、各プレイヤーへ MatchStart（初期お題つき）を配る。
func (s *Session) Start() []Outbound {
	if s.state != WaitingStart {
		return nil
	}
	s.state = Running
	summaries := s.summaries()
	out := make([]Outbound, 0, len(s.order))
	for _, pid := range s.order {
		ps := s.players[pid]
		d := s.issueDaken(ps, proto.DakenNormal)
		out = append(out, to(pid, proto.MatchStart{
			MatchId:      s.id,
			SelfPlayerId: pid,
			Players:      summaries,
			InitialDaken: d,
			Parameters:   s.publicParams(),
		}))
	}
	return out
}

// ApplyDakenClear は判定済みのお題クリア報告を受け、コンボ確定＋次のお題発行を返す。
func (s *Session) ApplyDakenClear(from PlayerId, r proto.DakenClearReport) []Outbound {
	ps := s.players[from]
	if ps == nil || !ps.alive || s.state != Running {
		return nil
	}
	d, ok := ps.issued[r.DakenId]
	if !ok {
		return nil // dakenId 整合検証: 発行中でない報告は無視（プロトコル仕様7章）
	}
	delete(ps.issued, r.DakenId)

	outcome := ps.p.ApplyDakenClear(r.MissCount, d.keystrokes, s.params)
	// TODO(#21c): missありや時間切れ由来のスタック加算・トラップ誘発はここで扱う。
	next := s.issueDaken(ps, proto.DakenNormal)
	return []Outbound{
		to(from, proto.ComboUpdated{ComboValue: outcome.Value, Delta: outcome.Delta, Reason: comboReasonToProto(outcome.Reason)}),
		to(from, proto.DakenIssued{Daken: []proto.DakenInstance{next}}),
	}
}

// ApplyStrategy は現在の作戦を差し替える（次の攻撃で参照）。
func (s *Session) ApplyStrategy(from PlayerId, r proto.StrategySelect) []Outbound {
	ps := s.players[from]
	if ps == nil {
		return nil
	}
	if r.StrategyId >= 0 && r.StrategyId <= 9 {
		ps.strategy = r.StrategyId
	}
	return nil
}

// ApplyAttack は Enter=全コンボ消費で攻撃を解決する（骨格）。
// 決定B: consumedCombo 申告は無視し、常に保持コンボを全消費。
// 威力算出・相殺・撃ち返し・着弾は #21a/#21b。ここでは不発判定・作戦解決・予告発行まで。
func (s *Session) ApplyAttack(from PlayerId, _ proto.AttackRequest) []Outbound {
	ps := s.players[from]
	if ps == nil || !ps.alive || s.state != Running {
		return nil
	}
	if ps.p.Combo() == 0 {
		return []Outbound{to(from, proto.AttackFailed{Reason: proto.FailNoCombo})}
	}
	targets := s.resolveTargets(ps.strategy, s.targetingContext(ps))
	if len(targets) == 0 {
		// 対象不成立: コンボは消費しない（決定・03仕様1章）
		return []Outbound{to(from, proto.AttackFailed{Reason: proto.FailNoTarget})}
	}

	consumed := ps.p.ConsumeAllCombo() // 全消費
	power := -consumed.Delta            // TODO(#21a): 威力=消費コンボ×係数×バッジ倍率。今はコンボ量=威力の暫定
	out := []Outbound{to(from, proto.ComboUpdated{ComboValue: 0, Delta: consumed.Delta, Reason: proto.ComboConsumed})}

	perTarget := power
	if len(targets) > 1 { // 作戦0(全体割り)の均等分配
		perTarget = power / len(targets)
	}
	for _, tid := range targets {
		w := s.newWarning(from, tid)
		out = append(out, to(tid, proto.AttackIncoming{
			WarningId:  w.id,
			AttackerId: from,
			Power:      perTarget,
			GraceMs:    s.params.Attack.WarningGraceMs,
		}))
	}
	// TODO(#21b): 相殺充当・撃ち返し連鎖・予告expire・着弾（ここは予告発行のみ）。
	return out
}

// Tick は時間を dt 進め、終了（生存1人）を判定する。
// TODO(#21c/#21d/#21b): 時間切れ→積み残し / 予告expire→着弾 / 全体難易度上昇 / KO走査。
func (s *Session) Tick(dtMs int) []Outbound {
	if s.state != Running {
		return nil
	}
	s.elapsedMs += int64(dtMs)
	return s.checkFinished()
}

// eliminate は脱落を確定する（#21c が stack 上限到達で呼ぶ想定。バッジ総取り等は #21c/攻撃仕様）。
func (s *Session) eliminate(pid PlayerId) {
	ps := s.players[pid]
	if ps == nil || !ps.alive {
		return
	}
	ps.alive = false
	s.aliveCount--
}

// checkFinished は生存1人以下で Finished へ遷移し、優勝者へ GameOver を返す。
func (s *Session) checkFinished() []Outbound {
	if s.state != Running || s.aliveCount > 1 {
		return nil
	}
	s.state = Finished
	var out []Outbound
	for _, pid := range s.order {
		ps := s.players[pid]
		if ps.alive {
			out = append(out, to(pid, proto.GameOver{
				Rank:            1,
				KoCount:         ps.koCount,
				FinalBadgeCount: ps.badges,
				TypingStats:     proto.TypingStats{MaxCombo: ps.p.Combo()},
			}))
		}
	}
	return out
}

// ── 内部ヘルパ ────────────────────────────────────────────

// issueDaken は次のお題を発行し、台帳に記録して DakenInstance を返す。
func (s *Session) issueDaken(ps *playerState, typ proto.DakenType) proto.DakenInstance {
	lvl := s.effectiveLevel(ps)
	var w Word
	if typ == proto.DakenTrap {
		w = s.words.NextTrap(s.rng)
	} else {
		w = s.words.Next(lvl, s.rng)
	}
	ps.dakenSeq++
	id := fmt.Sprintf("%s-%d", ps.id, ps.dakenSeq)
	tl := s.timeLimitFor(lvl)
	ps.issued[id] = &issuedDaken{keystrokes: w.KeystrokeCount}
	return proto.DakenInstance{
		DakenId: id, Type: typ, Text: w.Text, DifficultyLevel: lvl,
		TimeLimitMs: tl, IssuedAtServerTimeMs: s.elapsedMs,
	}
}

// effectiveLevel は出題難易度段階。#21d が全体難易度と合成する。今は個人コンボ連動のみの簡易版。
func (s *Session) effectiveLevel(ps *playerState) int {
	step := s.params.Combo.PersonalDifficultyStep
	lvl := 0
	if step > 0 {
		lvl = ps.p.Combo() / step
	}
	if m := s.params.Combo.PersonalDifficultyMaxLevel; lvl > m {
		lvl = m
	}
	if m := s.params.Difficulty.MaxLevel; lvl > m {
		lvl = m
	}
	return lvl
}

// timeLimitFor は難易度段階のダケン制限時間。
func (s *Session) timeLimitFor(level int) int {
	o := s.params.Odai
	tl := o.BaseTimeLimitMs - o.PerLevelReductionMs*level
	if tl < o.MinTimeLimitMs {
		tl = o.MinTimeLimitMs
	}
	return tl
}

// resolveTargets は作戦idで対象を解決する。未登録は作戦4へフォールバック。
func (s *Session) resolveTargets(id int, ctx TargetingContext) []PlayerId {
	st := s.strategies[id]
	if st == nil {
		st = s.strategies[4]
	}
	if st == nil {
		return nil
	}
	return st.SelectTargets(ctx)
}

// targetingContext は現在の生存状況から作戦解決用スナップショットを組み立てる。
func (s *Session) targetingContext(ps *playerState) TargetingContext {
	alive := make([]PlayerView, 0, s.aliveCount)
	for _, pid := range s.order {
		q := s.players[pid]
		if !q.alive {
			continue
		}
		alive = append(alive, PlayerView{
			PlayerId: q.id, ComboValue: q.p.Combo(), DakenStackCount: q.stack,
			DakenStackLimit: s.params.Stack.Limit, BadgeCount: q.badges,
			IncomingWarnings: s.incomingCount(q.id),
		})
	}
	// pendingAttackers は新しい順（作戦1カウンター）。
	var pending []PlayerId
	for i := len(ps.pendingAgainstMe) - 1; i >= 0; i-- {
		if w := s.warnings[ps.pendingAgainstMe[i]]; w != nil {
			pending = append(pending, w.attacker)
		}
	}
	return TargetingContext{SelfId: ps.id, Alive: alive, PendingAttackers: pending, LastImpactorId: ps.lastImpactor, Rng: s.rng}
}

// newWarning は予告を発行し、被害者の pending に積む（expire/相殺は #21b）。
func (s *Session) newWarning(attacker, victim PlayerId) *warning {
	s.warnSeq++
	w := &warning{id: fmt.Sprintf("w-%d", s.warnSeq), attacker: attacker, victim: victim}
	s.warnings[w.id] = w
	if vp := s.players[victim]; vp != nil {
		vp.pendingAgainstMe = append(vp.pendingAgainstMe, w.id)
	}
	return w
}

// incomingCount は pid を対象に現在 Pending 中の予告数。
func (s *Session) incomingCount(pid PlayerId) int {
	n := 0
	for _, w := range s.warnings {
		if w.victim == pid {
			n++
		}
	}
	return n
}

func (s *Session) summaries() []proto.PlayerSummary {
	out := make([]proto.PlayerSummary, 0, len(s.order))
	for _, pid := range s.order {
		p := s.players[pid]
		out = append(out, proto.PlayerSummary{
			PlayerId: p.id, DisplayName: p.name, ComboValue: p.p.Combo(),
			DakenStackCount: p.stack, DakenStackLimit: s.params.Stack.Limit,
			BadgeCount: p.badges, Alive: p.alive,
		})
	}
	return out
}

func (s *Session) publicParams() proto.GameParametersPublicSubset {
	return proto.GameParametersPublicSubset{
		StackLimit:             s.params.Stack.Limit,
		TrapTriggerInterval:    s.params.Stack.TrapTriggerInterval,
		PersonalDifficultyStep: s.params.Combo.PersonalDifficultyStep,
		DifficultyMaxLevel:     s.params.Difficulty.MaxLevel,
	}
}

// comboReasonToProto は内部の理由列挙を proto の契約値へ写像する（境界での変換）。
func comboReasonToProto(r ComboReason) proto.ComboReason {
	switch r {
	case ReasonClear:
		return proto.ComboClear
	case ReasonMiss:
		return proto.ComboMiss
	case ReasonConsumed:
		return proto.ComboConsumed
	default:
		return proto.ComboClear
	}
}
