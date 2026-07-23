package game

import "textro99/internal/proto"

// stack.go は【#21c】ダケンスタックの増減・トラップ誘発（ハイウォーターマーク）・脱落確定。

// landReceived は攻撃の着弾を処理する（相殺しきれなかった分・時間切れ着弾）。
// count 個の EnemySent ダケンを送り、スタックへ加算し、直近着弾者を記録する。
func (s *Session) landReceived(ps *playerState, count int, attacker PlayerId) []Outbound {
	if count <= 0 {
		return nil // 威力小で0個なら着弾なし
	}
	a := attacker
	ps.lastImpactor = &a
	daken := make([]proto.DakenInstance, 0, count)
	for i := 0; i < count; i++ {
		daken = append(daken, s.issueDaken(ps, proto.DakenEnemySent))
	}
	out := []Outbound{to(ps.id, proto.DakenIssued{Daken: daken})}
	out = append(out, s.addStack(ps, count)...)
	return out
}

// addStack はスタックを n 増やし、トラップ誘発・脱落を処理する。
//
// トラップ誘発はハイウォーターマーク方式（trapMilestone 整数1個）。往復で連発しない。
// ⚠ トラップダケン自体は権威スタックには計上しない（誘発の連鎖を避ける。02_詳細企画書 1.3 の例に一致。
// この割り切りはプロトタイプ用でマネージャー再検討可）。脱落が先に成立する場合はトラップを出さない。
func (s *Session) addStack(ps *playerState, n int) []Outbound {
	if n <= 0 {
		return nil
	}
	ps.stack += n
	if ps.stack >= s.params.Stack.Limit { // 脱落が最優先
		out := []Outbound{s.dakenStackUpdated(ps, false)}
		return append(out, s.eliminateWithKO(ps, ps.lastImpactor)...)
	}

	var out []Outbound
	trapPending := false
	if iv := s.params.Stack.TrapTriggerInterval; iv > 0 {
		milestone := ps.stack / iv
		if milestone > ps.trapMilestone {
			num := milestone - ps.trapMilestone
			ps.trapMilestone = milestone
			trapPending = true
			daken := make([]proto.DakenInstance, 0, num)
			for i := 0; i < num; i++ {
				daken = append(daken, s.issueDaken(ps, proto.DakenTrap))
			}
			out = append(out, to(ps.id, proto.DakenIssued{Daken: daken}))
		}
	}
	out = append(out, s.dakenStackUpdated(ps, trapPending))
	return out
}

// eliminateWithKO は脱落を確定し、KO実行者へバッジ総取りを適用する。
// killer が nil / 生存していない場合は自滅（KO実行者なし・決定D。attackerId=nil で送出）。
func (s *Session) eliminateWithKO(ps *playerState, killer *PlayerId) []Outbound {
	if !ps.alive {
		return nil
	}
	ps.alive = false
	s.aliveCount--
	rank := s.aliveCount + 1 // 後に脱落するほど上位。最後の1人が1位

	var attackerId *PlayerId
	transferred := 0
	if killer != nil && *killer != ps.id {
		if kp := s.players[*killer]; kp != nil && kp.alive {
			transferred = ps.badges
			kp.badges += ps.badges
			kp.koCount++
			ps.badges = 0
			k := *killer
			attackerId = &k
		}
	}

	return []Outbound{
		broadcastMsg(proto.KoNotified{AttackerId: attackerId, VictimId: ps.id, BadgesTransferred: transferred}),
		to(ps.id, proto.GameOver{
			Rank: rank, KoCount: ps.koCount, FinalBadgeCount: ps.badges,
			TypingStats: s.typingStats(ps),
		}),
		broadcastMsg(proto.PlayerListUpdated{Players: s.summaries(), AliveCount: s.aliveCount}),
	}
}
