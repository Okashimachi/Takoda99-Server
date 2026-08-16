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
	Publish      PublishParams      `json:"publish"`
	Matching     MatchingParams     `json:"matching"`
	Customer     CustomerParams     `json:"customer"`
	Score        ScoreParams        `json:"score"`
	Sanity       SanityParams       `json:"sanity"`
	Phase        PhaseParams        `json:"phase"`
	Heat         HeatParams         `json:"heat"`
	Cull         CullParams         `json:"cull"`
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
	TickIntervalMs int `json:"tickIntervalMs"`
}

// PublishParams: 配信頻度と方式（plan-h23）。
//
// game は毎tick分の Outbound を返し、**間引くのは配信層（room / publisher）**。
// ここはその間引き間隔。試合の判定には一切影響しないので、当日に安全に触れる部類の値。
//
// 🔴 **足切りの瞬間だけは間引かない。** 順位が大量に入れ替わった直後を落とすと、
// クライアントの表示が次の配信までズレたままになる（plan-h23 §4.1）。
// room は StoreEliminatedBatch を含む配信を「バースト」とみなして全部通す。
type PublishParams struct {
	// EvaluationIntervalMs は自店スコア・順位の配信間隔（既定 250ms = 4Hz）。
	// 唯一の指標なので遅延が体験に直結する。仕様は 2〜4Hz。
	//
	// ⚠ **OrderServed の即レスはこの間引きを通さない。** クライアントは
	// 「提供したのに EvaluationUpdate が返らない＝リジェクト」で不正申告を検知しているので、
	// ここを間引くとリジェクトが判別できなくなる（docs/client-integration.md §5.2）。
	EvaluationIntervalMs int `json:"evaluationIntervalMs"`

	// WarningIntervalMs は足切り予告の配信間隔（既定 500ms = 2Hz）。
	// 秒読みはクライアントが受信時刻起点でローカル補間するので高頻度は要らない。
	WarningIntervalMs int `json:"warningIntervalMs"`

	// RankingIntervalMs は全店ランキング全量の配信間隔（既定 1000ms = 1Hz）。
	RankingIntervalMs int `json:"rankingIntervalMs"`

	// RankingDeltaEnabled は差分配信を使うか（既定 false = 全量のみ）。
	//
	// 全量のみでも 99台合計 4.8Mbps・1試合 71MB で会場Wi-Fiには余裕がある
	// （予選の StoreListUpdate は 45Mbps・675MB だった）。差分が効くのは egress コストなので、
	// **まず全量で確実に動かし、必要になったら config で有効化する**（plan-h23 §1.2）。
	RankingDeltaEnabled bool `json:"rankingDeltaEnabled"`

	// RankingDeltaIntervalMs は差分配信の間隔（既定 500ms = 2Hz）。Enabled のときだけ効く。
	RankingDeltaIntervalMs int `json:"rankingDeltaIntervalMs"`
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
//
// heat = base + int(perAliveDrop×脱落数) + int(perElapsedSec×経過秒) + フェーズ加算
//
// 🔴 **難度の主軸は経過時間（perElapsedSec）**。フェーズは Early/Mid/Late の
// **離散イベント**なので、そこに大きな数を載せると必ず段差になる（plan-h32 §1.1）。
// 実際 phaseLate=9 のときは Late 突入で heat が一気に +8 跳ねていた。
// フェーズ加算は「区切りの補正」に留め、連続性は時間項が作る。
type HeatParams struct {
	Base         int     `json:"base"`
	PerAliveDrop float64 `json:"perAliveDrop"`
	// PerElapsedSec は経過1秒あたりの heat 上昇（plan-h32）。難度の主軸で、
	// カーブの連続性はこの項が作る。0 にすると段差だらけの旧カーブに戻る。
	//
	// 🔴 **既存DBには入っていないキー**。backfillDefaults の補完は**グループ単位**
	// （heat グループ全体がゼロのときだけ既定値を入れる）なので、heat グループが
	// 既に保存されている本番では **0 のまま**になる。config-front から手で入れること
	// （plan-h32 §4）。`make verify` の heat 行で実値を確認できる。
	PerElapsedSec float64 `json:"perElapsedSec"`
	PhaseEarly    int     `json:"phaseEarly"`
	PhaseMid      int     `json:"phaseMid"`
	PhaseLate     int     `json:"phaseLate"`
	// MaxLevel は heatLevel の上限（stepHeat で clamp する）。
	//
	// **お題辞書に語彙がある最大段階と揃える**こと。超えて設定しても WordSource が
	// 下の段階へ降りるだけで難度は変わらず、クライアントへ配る heatLevel だけが
	// 実態と食い違う（#75）。game は odai を import できない（層の依存が逆流するため）ので、
	// 辞書側の `odai.MaxWordLevel` と数値で揃える運用にしている。
	//
	// 🔴 **ここに到達しない既定値は3度目の再発**（#75 → h26 §1.2 → h32）。
	// 「既定値で試合を回すと MaxLevel に届く」は `internal/sim` の
	// TestHeat_ReachesMaxLevelWithDefaults がテストで守っている。値を触ったら sim を回すこと。
	MaxLevel int `json:"maxLevel"`
}

// CullStage: 段階的足切りの1ステージ（本戦・plan-h22）。
//
// AtMs に到達した時点で、生存数が TargetAliveCount になるまでスコア下位から脱落させる。
// 最終ステージは TargetAliveCount=0（＝全店脱落＝試合終了）。
type CullStage struct {
	AtMs             int `json:"atMs"`
	TargetAliveCount int `json:"targetAliveCount"`
}

// CullParams: 時刻足切りのスケジュール（本戦・plan-h22）。
//
// 予選の storm（tick周期で下位%を切る）を置き換えたもの。**下位%ではなく目標生存数**なのは、
// %指定だと現在の生存数に依存して結果が揺れるため。目標生存数なら脱落カーブを直接設計でき、
// Bot の強さのばらつきが脱落人数に波及しない。調整変数も targetAliveCount の1本に絞れる。
//
// ⚠ **Stages は slice ではなく配列**。Go では配列は要素が comparable なら `==` 可能なので、
// AGENTS.md §1.3 の「map / slice をフィールドに入れない」を満たす。
// ワイヤ（proto の GameParametersPublicSubset.CullSchedule）は slice なので、公開時に変換する。
//
// 🔴 段階数を変えるときは Validate と既定値を必ず一緒に直すこと。
// encoding/json は**要素数が足りない JSON を渡されると残りをゼロ値で埋める**ため、
// 「0秒時点で生存0＝開始直後に全店即死」が黙って成立してしまう（§2.2 のゼロ埋めの罠）。
type CullParams struct {
	Stages [CullStageCount]CullStage `json:"stages"`
}

// CullStageCount は足切りの段階数。20秒等間隔×6段階で企画確定（plan-h22 §1）。
const CullStageCount = 6

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
	// 配信間隔（plan-h23 §4）。0 以下だと毎tick配信になり、予選の帯域問題（#81）が戻る。
	for _, iv := range []struct {
		key string
		v   int
	}{
		{"publish.evaluationIntervalMs", gp.Publish.EvaluationIntervalMs},
		{"publish.warningIntervalMs", gp.Publish.WarningIntervalMs},
		{"publish.rankingIntervalMs", gp.Publish.RankingIntervalMs},
		{"publish.rankingDeltaIntervalMs", gp.Publish.RankingDeltaIntervalMs},
	} {
		if iv.v <= 0 {
			return fmt.Errorf("%s は正である必要 (got %d)。0 だと毎tick配信になり帯域が破綻する", iv.key, iv.v)
		}
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
	if err := gp.Cull.validate(); err != nil {
		return err
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

// validate は足切りスケジュールの破綻値を弾く（plan-h22 §2.2）。
//
// 🔴 **ゼロ埋めの罠がここの存在理由。** encoding/json は配列に要素数が足りない JSON を
// 渡されると残りをゼロ値で埋める。config-front から5要素で保存されると
// Stages[5] = {AtMs:0, TargetAliveCount:0} になり、これは「0秒時点で生存0＝
// 開始直後に全店即死」を意味する。当日これが起きたら試合が成立しない。
// AtMs > 0 と厳密増加の2つでゼロ埋めを検出できる。
func (cp CullParams) validate() error {
	prevAt := 0
	prevTarget := -1
	for i, st := range cp.Stages {
		if st.AtMs <= 0 {
			return fmt.Errorf("cull.stages[%d].atMs は正である必要 (got %d)。"+
				"段階数が %d に足りない JSON を保存するとゼロ埋めでここに来る", i, st.AtMs, CullStageCount)
		}
		if st.AtMs <= prevAt {
			return fmt.Errorf("cull.stages[%d].atMs は厳密に増加する必要 (got %d, 前段 %d)", i, st.AtMs, prevAt)
		}
		if st.TargetAliveCount < 0 {
			return fmt.Errorf("cull.stages[%d].targetAliveCount は非負である必要 (got %d)", i, st.TargetAliveCount)
		}
		if prevTarget >= 0 && st.TargetAliveCount > prevTarget {
			return fmt.Errorf("cull.stages[%d].targetAliveCount は単調非増加である必要 (got %d, 前段 %d)",
				i, st.TargetAliveCount, prevTarget)
		}
		last := i == len(cp.Stages)-1
		if last && st.TargetAliveCount != 0 {
			return fmt.Errorf("最終ステージ cull.stages[%d].targetAliveCount は 0 である必要 (got %d)。"+
				"120秒で全店が脱落して試合が終わる", i, st.TargetAliveCount)
		}
		if !last && st.TargetAliveCount <= 0 {
			return fmt.Errorf("cull.stages[%d].targetAliveCount は正である必要 (got %d)。"+
				"最終ステージより前で生存0にすると試合が途中で終わる", i, st.TargetAliveCount)
		}
		prevAt = st.AtMs
		prevTarget = st.TargetAliveCount
	}
	return nil
}

// MatchDurationMs は試合時間（＝最終ステージの時刻）を返す。
//
// **これが試合の唯一のデッドライン**。別建ての「制限時間」パラメータを足さないこと
// （時間の情報源が2つになり、片方だけ更新されて食い違う。AGENTS.md §8）。
func (cp CullParams) MatchDurationMs() int { return cp.Stages[len(cp.Stages)-1].AtMs }

// DefaultParameters はリモートコンフィグ取得失敗時のフォールバック内蔵デフォルト。
func DefaultParameters() GameParameters {
	return GameParameters{
		Session: SessionParams{
			TickIntervalMs: 150,
		},
		Publish: PublishParams{
			EvaluationIntervalMs:   250,
			WarningIntervalMs:      500,
			RankingIntervalMs:      1000,
			RankingDeltaEnabled:    false,
			RankingDeltaIntervalMs: 500,
		},
		Matching: MatchingParams{
			MinPlayers:       20,
			MaxPlayers:       99,
			StartCountdownMs: 15000,
			MinFill:          99,
			RosterWaitMs:     3000,
			ReadyCountdownMs: 5000,
		},
		// OrderCount は plan-h30 で 2/2/1/4 → 3/3/2/6 へ引き上げた。
		//
		// お題の1語を短くした（level 17 で 85打鍵 → 43打鍵前後）ぶん、**難度と報酬は
		// 「何語打つか」で持たせる**という方針転換による（plan-h30 §1）。1語が長いと
		// 打ち切るまでスコアが1点も入らないが、短い語を複数打つ形なら1語ごとに加点が入り、
		// ミスしても「次で取り返す」が成立する。
		//
		// ⚠ **DB の値がここに勝つ**。本番で戻すときはコードではなく config-front の
		// customer.*.orderCount を 2/2/1/4 に戻す（ビルド不要・docs/runbook.md）。
		Customer: CustomerParams{
			Total:   5000,
			Normal:  AttributeSpec{Attribute: proto.AttrNormal, Weight: 70, OrderCount: 3},
			Bonus:   AttributeSpec{Attribute: proto.AttrBonus, Weight: 15, OrderCount: 3},
			Claimer: AttributeSpec{Attribute: proto.AttrClaimer, Weight: 10, OrderCount: 2},
			Buzz:    AttributeSpec{Attribute: proto.AttrBuzz, Weight: 5, OrderCount: 6},
		},
		// **100 : 30**（ミス1回 = たこ焼き 0.3 個ぶんの損）。
		//
		// h26 で 25 に決めた値を、**h30（お題カーブの是正）で 30 へ改めた**。
		// 判断基準は h26 と同じ「速さ型と正確型の平均順位が拮抗する点」。
		//
		// h26 時点（1語が長い辞書）の実測:
		//
		//	W_MISS   速さ型 平均順位  正確型 平均順位  上位10の速%
		//	    10        26.5        74.0        100%   ← 速さ型の常勝
		//	    18        34.4        66.0         51%
		//	    25        48.7        51.3          1%   ← ここが拮抗点だった
		//	    30        58.8        41.0          0%
		//
		// h30 後（1語を短くし orderCount を増やした辞書）の実測。**拮抗点が動いた**:
		//
		//	W_MISS   平均順位の差   上位10の速%
		//	    18        -36.1         78%
		//	    22        -24.0         40%
		//	    25        -17.1         22%   ← ここではもう拮抗しない
		//	    28        -10.1          2%
		//	    32         +4.9          0%   ← 拮抗点は 30 前後
		//
		// 動いた理由: 1語が短くなるとミス数自体が減り、さらに orderCount 増で
		// 加点だけが増えるため、**ミスの罰が相対的に軽くなった**。
		// 25 のままだと速さ型に傾き、「正確に打つ人が早期に落ちる」という
		// 本戦リデザインの動機そのものが再発する。
		//
		// 🔴 **平均順位と「上位10の構成」は一致しない**（2つの型で分散が違う。
		// ミス減点が速さ型の分散を抑えるため、正確型のほうが上振れしやすい）。
		// 「上位10の速%が50%付近」を基準に採るなら 20〜22 になる。h26 から
		// 一貫して**平均順位の拮抗**を採っている。
		//
		// ⚠ この数字は sim のダミー実力分布に依存する。そのダミーは速度とミス率を
		// **独立に振っており現実とズレている**（plan-h33 で是正予定）。
		// **実プレイでの確認が最終判断**（plan-h26 §3）。当日は config から調整でき、
		// ビルドは要らない。
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
		// **難度カーブは経過時間が主役**（plan-h32）。
		//
		// 旧既定（perAliveDrop 0.1 / phaseMid 3 / phaseLate 9）は、
		//   ・生存項が int() 切り捨てなので**足切りの瞬間だけ段差**ができ、間は動かない
		//   ・Late 突入で **+8 跳ぶ**（別のゲームに切り替わる体感）
		//   ・本番DBの実値（perAliveDrop 0.05 / phaseMid 1）では**上端17に一度も届かない**
		// という3つの問題を抱えていた（h32 §0）。
		//
		// 時間項 0.11/秒 を主軸に置き、フェーズ加算を小さく（Late 9→2）することで
		//   ・120秒（＝cull 最終ステージ）でちょうど上端 17 に到達
		//   ・最大段差 +3 以下
		//   ・単調増加
		// になる。実測カーブは `go test -v ./internal/sim/ -run ReportHeatCurve`。
		//
		// 🔴 **この既定値はコードの中だけの話で、本番は DB の値で走る。**
		// 値を変えたら config-front 側も更新すること（h32 §4）。
		Heat: HeatParams{
			Base: 0,
			// 89店が落ちても +2 にしかならない（89×0.03=2.67）。
			// 生存項は「終盤の重み付け」であって主役ではない。
			PerAliveDrop: 0.03,
			// 120秒 × 0.12 = 14.4 → 時間だけで 14 上がる。カーブの連続性はここが作る。
			//
			// plan-h32 は 0.11 と書いていたが **0.12 に改めた**。0.11 だと上端(17)への
			// 到達が 119秒で、**上端に居るのが約1秒**しかない。h30 後の level 17 は
			// 約43打鍵（≒10秒）なので、上端の語は配られても打ち切れず
			// 「用意した最上位の語彙が死ぬ」（#75）が形を変えて残ってしまう。
			//
			//	perElapsedSec   上端到達   上端に居る時間
			//	     0.11        119秒         1秒   ← 実質使われない
			//	     0.12        109秒        11秒   ← 決勝の途中で上端へ
			//	     0.13        100秒        20秒   ← 決勝が丸ごと上端（緩急が無い）
			//
			// 0.12 なら決勝（100秒〜・生存10店）が 15〜16 で始まり 17 で終わる。
			PerElapsedSec: 0.12,
			PhaseEarly:    0,
			PhaseMid:      1,
			// ★ 9 → 2。ここを下げずに時間項を足すと、全体が持ち上がって上限で
			// 頭打ちになるだけで**跳ねは消えない**（h32 §2.2）。
			PhaseLate: 2,
			// odai.MaxWordLevel（辞書の上端）と一致させる。
			MaxLevel: 17,
		},
		// 20秒等間隔×6段階（plan-h22 §1・企画確定）。
		//
		// 動かしてよい: 中間ステージ #2〜#4 の targetAliveCount
		// 動かしてはいけない: 20秒等間隔 / 120秒（＝ゲーム時間）/ #5 の 10人（＝決勝の人数）/
		//                     #1 が20秒より早くなること（どれだけ弱くても20秒は遊べる保証）
		Cull: CullParams{
			Stages: [CullStageCount]CullStage{
				{AtMs: 20000, TargetAliveCount: 75},
				{AtMs: 40000, TargetAliveCount: 55},
				{AtMs: 60000, TargetAliveCount: 35},
				{AtMs: 80000, TargetAliveCount: 20},
				{AtMs: 100000, TargetAliveCount: 10},
				{AtMs: 120000, TargetAliveCount: 0},
			},
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
