package game

import (
	"fmt"
	"sort"

	"textro99/internal/proto"
)

// offset.go は【#21b】攻撃の発火モデル：Enter=全消費で、自分宛の予告があれば相殺に充当し、
// 余剰は撃ち返し（現作戦で新規攻撃）。予告のライフサイクル（発行・除去）もここが持つ。

// ApplyAttack は Enter 押下を解決する（決定B: consumedCombo 申告は無視し常に全消費）。
//
//   - 自分宛の未解決予告が無い     → 純粋な新規攻撃。対象不成立ならコンボ非消費で AttackFailed。
//   - 自分宛の未解決予告がある     → 全消費し、着弾が近い順に相殺充当。余剰は撃ち返し
//     （撃ち返しの対象不成立時は消失＝決定A。AttackFailed は出さない）。
func (s *Session) ApplyAttack(from PlayerId, _ proto.AttackRequest) []Outbound {
	ps := s.players[from]
	if ps == nil || !ps.alive || s.state != Running {
		return nil
	}
	if ps.p.Combo() == 0 {
		return []Outbound{to(from, proto.AttackFailed{Reason: proto.FailNoCombo})}
	}

	pending := s.sortedPending(ps)
	if len(pending) == 0 {
		// 純粋な新規攻撃: 対象を先に確認（不成立ならコンボ非消費）
		targets := s.resolveTargets(ps.strategy, s.targetingContext(ps))
		if len(targets) == 0 {
			return []Outbound{to(from, proto.AttackFailed{Reason: proto.FailNoTarget})}
		}
		m, cu := s.consumeToPower(ps)
		out := []Outbound{cu}
		out = append(out, s.emitWarnings(from, targets, m)...)
		out = append(out, s.checkFinished()...)
		return out
	}

	// 相殺パス: 全消費してから着弾が近い順に充当
	m, cu := s.consumeToPower(ps)
	out := []Outbound{cu}
	for _, wid := range pending {
		if m <= 0 {
			break
		}
		w := s.warnings[wid]
		if w == nil {
			continue
		}
		n := w.power
		if m >= n { // 完全相殺
			s.removeWarning(wid)
			out = append(out, to(from, proto.OffsetResolved{WarningId: w.id, OffsetAmount: n, RemainderDakenCount: 0}))
			m -= n
			continue
		}
		// 部分相殺: 残余威力を個数変換して即着弾
		rem := n - m
		cnt := powerToDakenCount(rem, s.params.Attack)
		s.removeWarning(wid)
		out = append(out, to(from, proto.OffsetResolved{WarningId: w.id, OffsetAmount: m, RemainderDakenCount: cnt}))
		out = append(out, s.landReceived(ps, cnt, w.attacker)...)
		m = 0
	}

	// 余剰は撃ち返し。対象不成立なら消失（決定A）。
	// 撃ち返しは新規予告を生むだけで同期的な再帰はしない（連鎖深さ1）。将来 offset を再帰化
	// する場合は attack.maxReboundChain で必ず上限を設けること（AGENTS）。
	if m > 0 && ps.alive {
		if targets := s.resolveTargets(ps.strategy, s.targetingContext(ps)); len(targets) > 0 {
			out = append(out, s.emitWarnings(from, targets, m)...)
		}
	}
	out = append(out, s.checkFinished()...)
	return out
}

// consumeToPower は全コンボを消費し、威力と ComboUpdated(Consumed) を返す。
func (s *Session) consumeToPower(ps *playerState) (int, Outbound) {
	oc := ps.p.ConsumeAllCombo()
	consumed := -oc.Delta
	power := attackPower(consumed, ps.badges, s.params.Attack)
	return power, to(ps.id, proto.ComboUpdated{ComboValue: 0, Delta: oc.Delta, Reason: proto.ComboConsumed})
}

// emitWarnings は対象集合へ予告を発行する。複数対象（作戦0）は威力を均等分配（端数消滅）。
func (s *Session) emitWarnings(from PlayerId, targets []PlayerId, power int) []Outbound {
	per := power
	if len(targets) > 1 {
		per = power / len(targets)
	}
	if per <= 0 {
		return nil
	}
	out := make([]Outbound, 0, len(targets))
	for _, tid := range targets {
		w := s.newWarning(from, tid, per)
		out = append(out, to(tid, proto.AttackIncoming{
			WarningId: w.id, AttackerId: from, Power: per, GraceMs: s.params.Attack.WarningGraceMs,
		}))
	}
	return out
}

// sortedPending は自分宛の予告IDを着弾が近い順に返す。
func (s *Session) sortedPending(ps *playerState) []proto.WarningId {
	ids := make([]proto.WarningId, 0, len(ps.pendingAgainstMe))
	for _, id := range ps.pendingAgainstMe {
		if s.warnings[id] != nil {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		return s.warnings[ids[i]].impactAtMs() < s.warnings[ids[j]].impactAtMs()
	})
	return ids
}

// newWarning は予告を発行し、被害者の pending に積む。
func (s *Session) newWarning(attacker, victim PlayerId, power int) *warning {
	s.warnSeq++
	w := &warning{
		id: fmt.Sprintf("w-%d", s.warnSeq), attacker: attacker, victim: victim,
		power: power, issuedAtMs: s.elapsedMs, graceMs: s.params.Attack.WarningGraceMs,
	}
	s.warnings[w.id] = w
	if vp := s.players[victim]; vp != nil {
		vp.pendingAgainstMe = append(vp.pendingAgainstMe, w.id)
	}
	return w
}

// removeWarning は予告を除去する（map と被害者の pending から）。
func (s *Session) removeWarning(id proto.WarningId) {
	w := s.warnings[id]
	if w == nil {
		return
	}
	delete(s.warnings, id)
	if vp := s.players[w.victim]; vp != nil {
		for i, x := range vp.pendingAgainstMe {
			if x == id {
				vp.pendingAgainstMe = append(vp.pendingAgainstMe[:i], vp.pendingAgainstMe[i+1:]...)
				break
			}
		}
	}
}
