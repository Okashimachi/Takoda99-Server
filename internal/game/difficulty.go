package game

import (
	"sort"

	"textro99/internal/proto"
)

// difficulty.go は【#21d】難易度（全体＝経過時間 / 個人＝コンボ連動）と、
// 時間切れ（積み残し）・予告着弾の時系列処理。

// personalLevel は個人コンボ連動の難易度段階。
func (s *Session) personalLevel(ps *playerState) int {
	step := s.params.Combo.PersonalDifficultyStep
	if step <= 0 {
		return 0
	}
	l := ps.p.Combo() / step
	if m := s.params.Combo.PersonalDifficultyMaxLevel; l > m {
		l = m
	}
	return l
}

// effectiveLevel は実効難易度＝min(全体+個人, maxLevel)。
func (s *Session) effectiveLevel(ps *playerState) int {
	l := s.globalLevel + s.personalLevel(ps)
	if m := s.params.Difficulty.MaxLevel; l > m {
		l = m
	}
	return l
}

func (s *Session) difficultyUpdatedFor(ps *playerState) Outbound {
	return to(ps.id, proto.DifficultyUpdated{
		GlobalLevel: s.globalLevel, PersonalLevel: s.personalLevel(ps), EffectiveLevel: s.effectiveLevel(ps),
	})
}

// advanceGlobalDifficulty は経過時間に応じて全体難易度を上げ、生存者へ通知する。
func (s *Session) advanceGlobalDifficulty() []Outbound {
	iv := s.params.Difficulty.GlobalIntervalMs
	if iv <= 0 {
		return nil
	}
	want := int(s.elapsedMs / int64(iv))
	if m := s.params.Difficulty.MaxLevel; want > m {
		want = m
	}
	if want <= s.globalLevel {
		return nil
	}
	s.globalLevel = want
	var out []Outbound
	for _, pid := range s.order {
		if ps := s.players[pid]; ps.alive {
			out = append(out, s.difficultyUpdatedFor(ps))
		}
	}
	return out
}

// expireTimeouts は制限時間を超えた通常お題を打ち切り、積み残し（+1）としてスタックへ加算する。
func (s *Session) expireTimeouts() []Outbound {
	var out []Outbound
	for _, pid := range s.order {
		ps := s.players[pid]
		if !ps.alive {
			continue
		}
		var expired []proto.DakenId
		for id, d := range ps.issued {
			if d.typ == proto.DakenNormal && s.elapsedMs >= d.issuedAtMs+int64(d.timeLimitMs) {
				expired = append(expired, id)
			}
		}
		sort.Slice(expired, func(i, j int) bool { return expired[i] < expired[j] })
		for _, id := range expired {
			delete(ps.issued, id)
			out = append(out, to(pid, proto.DakenExpired{DakenId: id}))
			out = append(out, s.addStack(ps, 1)...) // 積み残し（通常ダケン1個分）
			if ps.alive {                            // 脱落してなければ次のお題
				nd := s.issueDaken(ps, proto.DakenNormal)
				out = append(out, to(pid, proto.DakenIssued{Daken: []proto.DakenInstance{nd}}))
			}
		}
	}
	return out
}

// expireWarnings は猶予を過ぎた予告を着弾させる（相殺されなかった攻撃）。
func (s *Session) expireWarnings() []Outbound {
	var expired []proto.WarningId
	for id, w := range s.warnings {
		if s.elapsedMs >= w.impactAtMs() {
			expired = append(expired, id)
		}
	}
	sort.Slice(expired, func(i, j int) bool { return expired[i] < expired[j] })

	var out []Outbound
	for _, id := range expired {
		w := s.warnings[id]
		if w == nil {
			continue
		}
		s.removeWarning(id)
		vp := s.players[w.victim]
		if vp == nil || !vp.alive {
			continue
		}
		cnt := powerToDakenCount(w.power, s.params.Attack)
		out = append(out, s.landReceived(vp, cnt, w.attacker)...)
	}
	return out
}
