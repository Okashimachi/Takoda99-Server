package game

import "math"

// attack.go は【#21a】威力算出と、威力→ダケン個数の変換（単位破綻防止の要）。純関数。

// attackPower は消費コンボとバッジ数から最終威力を算出する。
//
//	基礎威力 = consumedCombo × comboToPowerRatio
//	バッジ倍率 = 1 + min(badgePowerBonusPerBadge × badgeCount, badgePowerBonusCap)
//	最終威力 = floor(基礎威力 × バッジ倍率)
func attackPower(consumedCombo, badgeCount int, p AttackParams) int {
	base := float64(consumedCombo) * p.ComboToPowerRatio
	bonus := p.BadgePowerBonusPerBadge * float64(badgeCount)
	if bonus > p.BadgePowerBonusCap {
		bonus = p.BadgePowerBonusCap
	}
	return int(math.Floor(base * (1 + bonus)))
}

// powerToDakenCount は残余威力を実際に相手スタックへ積むダケン個数へ変換する（端数切り捨て）。
// 相殺は威力単位で行い、相殺後に残った威力だけをこの式で個数へ変換する（単位フローの要）。
func powerToDakenCount(power int, p AttackParams) int {
	if power <= 0 {
		return 0
	}
	return int(math.Floor(float64(power) * p.PowerToDakenRate))
}
