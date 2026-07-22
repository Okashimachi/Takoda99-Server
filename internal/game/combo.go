package game

// ComboReason は1回のコンボ変化の理由。境界で proto.ComboReason へ写像する。
type ComboReason int

const (
	ReasonClear    ComboReason = iota // ノーミスクリアによる加算
	ReasonMiss                        // ミスタイプによる減衰
	ReasonConsumed                    // 攻撃発動による全消費
)

// ComboOutcome は1回のコンボ操作の確定結果（判定式の出力）。
type ComboOutcome struct {
	Value  int
	Delta  int
	Reason ComboReason
}

// Player は1人分のルール状態。per-player に属する状態はここへ集約する。
// 今後スタック・個人難易度などのフィールドが同居する。
type Player struct {
	combo int
}

// Combo は現在のコンボ値を返す。
func (p *Player) Combo() int { return p.combo }

// ApplyDakenClear は判定済みの DakenClearReport 相当（missCount）を受けてコンボを確定する。
// keystrokeCount はそのダケンのローマ字打鍵数（サーバーがダケン発行時に算出した正準値・決定C）。
//
//   - missCount == 0（ノーミスクリア）: combo += base + perChar×keystrokeCount
//   - missCount > 0（ミスあり）        : combo -= missDecay×missCount（0未満はクランプ）。加算はしない。
//
// 打鍵の正誤判定はクライアント責務。ここは判定済み結果からコンボを算出するだけ。
func (p *Player) ApplyDakenClear(missCount, keystrokeCount int, gp GameParameters) ComboOutcome {
	if missCount <= 0 {
		gain := gp.Combo.NoMissBaseGain + gp.Combo.NoMissPerCharGain*keystrokeCount
		p.combo += gain
		return ComboOutcome{Value: p.combo, Delta: gain, Reason: ReasonClear}
	}

	decay := gp.Combo.MissDecay * missCount
	newValue := p.combo - decay
	if newValue < 0 {
		newValue = 0
	}
	delta := newValue - p.combo
	p.combo = newValue
	return ComboOutcome{Value: p.combo, Delta: delta, Reason: ReasonMiss}
}

// ConsumeAllCombo は Enter=全消費のコンボ消費を確定し、消費前のコンボ量を Delta の絶対値で返す。
func (p *Player) ConsumeAllCombo() ComboOutcome {
	consumed := p.combo
	p.combo = 0
	return ComboOutcome{Value: 0, Delta: -consumed, Reason: ReasonConsumed}
}
