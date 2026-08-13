// Package game は【層1・コア】試合の権威。純粋な計算のみで、ネットワーク・時間・I/O を持たない。
//
// たこ焼き経営BR の状態機械（Tick(dt) で進む）を内包する。game は他の internal 部品/スパインを
// import しない（proto は契約として参照可）。継ぎ目は ports.go（DIP）。すべての調整値は
// GameParameters 経由。
package game

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"takoda99/internal/proto"
)

// ConfigHash は GameParameters の JSON を SHA256 して先頭8文字を返す。
func (gp GameParameters) ConfigHash() string {
	b, _ := json.Marshal(gp)
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:4])
}

// GameParameters は数値バランスの全項目。正典は
// Takoda99-Docs/02_共通仕様/03_パラメータ仕様.md。
// サーバーが起動時に config(ConfigProvider) 経由で外部取得し、失敗時は DefaultParameters()。
// クライアントへは MatchStart で公開サブセット（proto側）に絞って配信する。
type GameParameters struct {
	Session      SessionParams      `json:"session"`
	Matching     MatchingParams     `json:"matching"`
	Customer     CustomerParams     `json:"customer"`
	Score        ScoreParams        `json:"score"`
	Sanity       SanityParams       `json:"sanity"`
	Phase        PhaseParams        `json:"phase"`
	Heat         HeatParams         `json:"heat"`
	Storm        StormParams        `json:"storm"`
	Distribution DistributionParams `json:"distribution"`
	Presentation PresentationParams `json:"presentation"`
	Bot          BotParams          `json:"bot"`
}

// SessionParams: 試合ループの調整値。tick 周期・状態配信間隔もハードコードせずここで持つ。
//
// 制限時間の項目は持たない（proto v0.3.0 で matchTimeLimitMs を契約から削除）。
// 本戦では決着を cullSchedule の最終ステージ（120秒・全店脱落）が保証する（plan-h22）。
// 「試合時間」を表す調整値をここへ足さないこと。
type SessionParams struct {
	TickIntervalMs    int `json:"tickIntervalMs"`
	PublishIntervalMs int `json:"publishIntervalMs"`
}

// MatchingParams: マッチング（試合前）。minPlayers は当日運用で下げられるよう可変性が重要。
type MatchingParams struct {
	MinPlayers       int `json:"minPlayers"`
	MaxPlayers       int `json:"maxPlayers"`
	StartCountdownMs int `json:"startCountdownMs"`
	MinFill          int `json:"minFill"`
	RosterWaitMs     int `json:"rosterWaitMs"`
	ReadyCountdownMs int `json:"readyCountdownMs"`
}

// CustomerParams: 客システム（総数・属性ごとの出現率/注文数）。
type CustomerParams struct {
	Total   int           `json:"total"`
	Normal  AttributeSpec `json:"normal"`
	Bonus   AttributeSpec `json:"bonus"`
	Claimer AttributeSpec `json:"claimer"`
	Buzz    AttributeSpec `json:"buzz"`
}

// AttributeSpec: 1属性分の生成パラメータ。
//
// 本戦（plan-h21）で属性はスコアに一切影響しなくなった。残っているのは
// 出現率(Weight)と注文数(OrderCount)＝見た目の彩りとお題量の差だけで、
// **同じ打鍵をすれば属性によらず同じスコアになる**。
// 「この属性だけ加点/減点」を復活させないこと（予選の「同じように打ったのに評価が違う」の再来）。
type AttributeSpec struct {
	Attribute  proto.CustomerAttribute `json:"attribute"`
	Weight     int                     `json:"weight"`
	OrderCount int                     `json:"orderCount"`
}

// ScoreParams: スコアの重み（本戦・plan-h21）。順位を決める唯一の値。
//
//	deltaScore = WeightTakoyaki×たこ焼き数(orderCount) − WeightMiss×ミス数
//
// int なのは意図的で、重みが整数なら累積に誤差が乗らない（float にしない）。
// **速度の項は持たない**。速さは「時間内に何個作れたか」に自然に表れる。
//
// この2つの比率が本作の面白さの中心で、h26 で最も回数を重ねて詰める値になる。
type ScoreParams struct {
	WeightTakoyaki int `json:"weightTakoyaki"` // たこ焼き1個あたりの加点
	WeightMiss     int `json:"weightMiss"`     // ミス1打鍵あたりの減点
}

// SanityParams: クライアント申告値の妥当性チェック（不正・計測ブレの下限）。
//
// 旧 EvalParams のうち、評価の廃止後も残る唯一の項目。ゲームバランスではなく
// 「あり得ない申告を弾く」ためのもの。
type SanityParams struct {
	// MinMsPerWord は1単語あたりの所要msの下限。OrderServed.ElapsedMs がこれを
	// 下回る申告は下限へクランプする（スコアには使わないが、統計の汚染を防ぐ）。
	MinMsPerWord int `json:"minMsPerWord"`
}

// PhaseParams: フェーズ遷移（Early → Mid → Late）。
type PhaseParams struct {
	MidAliveThreshold  int `json:"midAliveThreshold"`
	LateAliveThreshold int `json:"lateAliveThreshold"`
	MidTimeMs          int `json:"midTimeMs"`
	LateTimeMs         int `json:"lateTimeMs"`
}

// HeatParams: 火力（お題難易度の全体上昇）。
type HeatParams struct {
	Base         int     `json:"base"`
	PerAliveDrop float64 `json:"perAliveDrop"`
	PhaseEarly   int     `json:"phaseEarly"`
	PhaseMid     int     `json:"phaseMid"`
	PhaseLate    int     `json:"phaseLate"`
	// MaxLevel は heatLevel の上限（stepHeat で clamp する）。
	//
	// **お題辞書に語彙がある最大段階と揃える**こと。超えて設定しても WordSource が
	// 下の段階へ降りるだけで難度は変わらず、クライアントへ配る heatLevel だけが
	// 実態と食い違う（#75）。game は odai を import できない（層の依存が逆流するため）ので、
	// 辞書側の `odai.MaxWordLevel` と数値で揃える運用にしている。
	MaxLevel int `json:"maxLevel"`
}

// StormParams: 下位淘汰（定期的に下位%を強制脱落）。
type StormParams struct {
	IntervalTicks int     `json:"intervalTicks"`
	WarnTicks     int     `json:"warnTicks"`
	ThresholdPct  float64 `json:"thresholdPct"`
}

// DistributionParams: 客の分配（restPool→店の行列）。
//
// 本戦（plan-h21 §4）で分配重みは「行列が短い店ほど来やすい」だけになった。
// 評価/スコアによる重み付け（旧 WeightFloor）は廃止。**スコアで客の来やすさを変えないこと**：
// 「スコアが高い→客が増える→さらに伸びる」の正のフィードバックが二重にかかり、
// 序盤の小差が終盤に発散して決勝20秒の逆転劇が死ぬ。
type DistributionParams struct {
	// QueueRefillThreshold は補充の発火点。行列がこれを下回った店だけが分配候補になる。
	// 「お題が途切れない」保証は重みではなくこの閾値が担っている。
	QueueRefillThreshold int `json:"queueRefillThreshold"`
}

// PresentationParams: クライアントの演出切替に使うしきい値。ゲーム進行には影響しない
// （サーバーは判定に使わず、公開パラメータとして配るだけ）。
//
// フェーズ(PhaseParams)とは別物。フェーズは我慢短縮・火力・客属性の解禁といった
// **ルール**を変えるが、こちらは見た目の切り替えだけを担う。
type PresentationParams struct {
	// FinalStageAliveThreshold は終盤演出へ切り替える生存店数。
	FinalStageAliveThreshold int `json:"finalStageAliveThreshold"`
	// FinalRushAliveThreshold は最終盤演出へ切り替える生存店数。
	FinalRushAliveThreshold int `json:"finalRushAliveThreshold"`
}

// BotParams: CPU（Bot）の強さ。
type BotParams struct {
	BaseAccuracy    float64 `json:"baseAccuracy"`
	BaseElapsedMs   int     `json:"baseElapsedMs"`
	AccuracyJitter  float64 `json:"accuracyJitter"`
	ElapsedJitterMs int     `json:"elapsedJitterMs"`
}

// Validate は破綻値を弾く最小限の検証。
func (gp GameParameters) Validate() error {
	if gp.Customer.Total <= 0 {
		return fmt.Errorf("customer.total は正である必要 (got %d)", gp.Customer.Total)
	}
	if gp.Session.TickIntervalMs <= 0 {
		return fmt.Errorf("session.tickIntervalMs は正である必要 (got %d)", gp.Session.TickIntervalMs)
	}
	if gp.Bot.BaseElapsedMs <= 0 {
		return fmt.Errorf("bot.baseElapsedMs は正である必要 (got %d)", gp.Bot.BaseElapsedMs)
	}
	if gp.Bot.BaseAccuracy < 0 || gp.Bot.BaseAccuracy > 1 {
		return fmt.Errorf("bot.baseAccuracy は 0..1 である必要 (got %v)", gp.Bot.BaseAccuracy)
	}
	if gp.Heat.MaxLevel <= 0 {
		return fmt.Errorf("heat.maxLevel は正である必要 (got %d)", gp.Heat.MaxLevel)
	}
	if gp.Storm.IntervalTicks < 0 {
		return fmt.Errorf("storm.intervalTicks は非負である必要 (got %d)", gp.Storm.IntervalTicks)
	}
	if gp.Storm.ThresholdPct < 0 || gp.Storm.ThresholdPct > 1 {
		return fmt.Errorf("storm.thresholdPct は 0..1 の範囲である必要 (got %f)", gp.Storm.ThresholdPct)
	}
	if gp.Phase.MidAliveThreshold < 0 {
		return fmt.Errorf("phase.midAliveThreshold は非負である必要 (got %d)", gp.Phase.MidAliveThreshold)
	}
	if gp.Matching.MinPlayers <= 0 {
		return fmt.Errorf("matching.minPlayers は正である必要 (got %d)", gp.Matching.MinPlayers)
	}
	if gp.Distribution.QueueRefillThreshold <= 0 {
		return fmt.Errorf("distribution.queueRefillThreshold は正である必要 (got %d)", gp.Distribution.QueueRefillThreshold)
	}
	// スコアの重み（本戦の順位を決める唯一の値・plan-h21 §5.3）。
	// weightTakoyaki が 0 以下だと「たこ焼きを作っても点が入らない」＝順位が付かない。
	if gp.Score.WeightTakoyaki <= 0 {
		return fmt.Errorf("score.weightTakoyaki は正である必要 (got %d)", gp.Score.WeightTakoyaki)
	}
	// weightMiss は 0 を許す（ミスを罰しない設定でバランスを見たい場合がある）。
	// 負値だけは弾く（ミスするほど加点される逆転した挙動になる）。
	if gp.Score.WeightMiss < 0 {
		return fmt.Errorf("score.weightMiss は非負である必要 (got %d)", gp.Score.WeightMiss)
	}
	if gp.Sanity.MinMsPerWord < 0 {
		return fmt.Errorf("sanity.minMsPerWord は非負である必要 (got %d)", gp.Sanity.MinMsPerWord)
	}
	for _, spec := range []AttributeSpec{gp.Customer.Normal, gp.Customer.Bonus, gp.Customer.Claimer, gp.Customer.Buzz} {
		if spec.OrderCount <= 0 {
			return fmt.Errorf("customer.%s.orderCount は正である必要 (got %d)", spec.Attribute, spec.OrderCount)
		}
	}
	return nil
}

// DefaultParameters はリモートコンフィグ取得失敗時のフォールバック内蔵デフォルト。
func DefaultParameters() GameParameters {
	return GameParameters{
		Session: SessionParams{
			TickIntervalMs:    150,
			PublishIntervalMs: 250,
		},
		Matching: MatchingParams{
			MinPlayers:       20,
			MaxPlayers:       99,
			StartCountdownMs: 15000,
			MinFill:          99,
			RosterWaitMs:     3000,
			ReadyCountdownMs: 5000,
		},
		Customer: CustomerParams{
			Total:   5000,
			Normal:  AttributeSpec{Attribute: proto.AttrNormal, Weight: 70, OrderCount: 2},
			Bonus:   AttributeSpec{Attribute: proto.AttrBonus, Weight: 15, OrderCount: 2},
			Claimer: AttributeSpec{Attribute: proto.AttrClaimer, Weight: 10, OrderCount: 1},
			Buzz:    AttributeSpec{Attribute: proto.AttrBuzz, Weight: 5, OrderCount: 4},
		},
		// 仮値。W_TAKOYAKI=100 / W_MISS=30 なら1語あたり3.3ミス超で初めて減点が勝つ。
		// この比率の詰めは h26（バランス検証）で行う。
		Score: ScoreParams{
			WeightTakoyaki: 100,
			WeightMiss:     30,
		},
		Sanity: SanityParams{
			MinMsPerWord: 200,
		},
		Phase: PhaseParams{
			MidAliveThreshold:  70,
			LateAliveThreshold: 30,
			MidTimeMs:          30000,
			LateTimeMs:         90000,
		},
		Heat: HeatParams{
			Base:         0,
			PerAliveDrop: 0.1,
			PhaseEarly:   0,
			PhaseMid:     3,
			PhaseLate:    8,
			// odai.MaxWordLevel（辞書の上端）と一致させる。
			MaxLevel: 17,
		},
		Storm: StormParams{
			IntervalTicks: 200,
			WarnTicks:     30,
			ThresholdPct:  0.10,
		},
		Distribution: DistributionParams{
			QueueRefillThreshold: 5,
		},
		Presentation: PresentationParams{
			FinalStageAliveThreshold: 20,
			FinalRushAliveThreshold:  10,
		},
		Bot: BotParams{
			BaseAccuracy:    0.85,
			BaseElapsedMs:   4500,
			AccuracyJitter:  0.1,
			ElapsedJitterMs: 500,
		},
	}
}
