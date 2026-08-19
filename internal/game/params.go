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
	Odai         OdaiParams         `json:"odai"`
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

// CustomerParams: 客システム（総数・属性ごとの出現率・注文数の抽選表）。
//
// 🔴 **属性（見た目）と注文数（難度）は独立**（plan-h36）。h35 までは
// 「クレーマーは必ず2個」「JK は必ず6個」と1対1で紐づいていたため、
// **見た目を増やさずに個数の段階だけを増やせなかった**。属性は h21 で
// 「ゲームに一切影響しない見た目専用」と決めたのに、個数だけが例外的に難度へ効いていた。
// 現在は OrderTiers が独立した抽選表になっており、属性からは何も決まらない。
type CustomerParams struct {
	Total   int           `json:"total"`
	Normal  AttributeSpec `json:"normal"`
	Bonus   AttributeSpec `json:"bonus"`
	Claimer AttributeSpec `json:"claimer"`
	Buzz    AttributeSpec `json:"buzz"`

	// OrderTiers は「たこ焼き何個の客が、どれくらいの割合で出るか」（plan-h36）。
	// 属性の抽選とは**独立に**引かれるので、同じ見た目の客に 2個も 8個もありうる。
	//
	// ⚠ **slice ではなく固定長配列**。Go では配列は要素が comparable なら `==` 可能なので、
	// AGENTS.md §1.3 の「map / slice をフィールドに入れない」を満たす（cull.stages・bot.tiers と同じ流儀）。
	//
	// 🔴 **ゼロ埋めを Validate で弾いてはいけない**。理由は EffectiveOrderTiers を参照。
	OrderTiers [OrderTierCount]OrderTier `json:"orderTiers"`
}

// AttributeSpec: 1属性分の生成パラメータ。
//
// 本戦（plan-h21）で属性はスコアに一切影響しなくなった。さらに plan-h36 で
// 注文数も切り離されたので、残っているのは**出現率(Weight)＝見た目の彩りだけ**。
// **同じ打鍵をすれば属性によらず同じスコアになる**し、属性によって注文の重さも変わらない。
// 「この属性だけ加点/減点」「この属性は重い注文が出やすい」を復活させないこと
// （予選の「同じように打ったのに評価が違う」の再来）。
type AttributeSpec struct {
	Attribute proto.CustomerAttribute `json:"attribute"`
	Weight    int                     `json:"weight"`
}

// OrderTierCount は注文数の段階数。**3段階で固定する**（plan-h36 §設計意図）。
//
// 可変長（`[]OrderTier`）にすると `==` 比較可能を保てず、config からのゼロ埋め事故も増える。
// 3段階あれば「軽い・普通・重い」は表現できる。
const OrderTierCount = 3

// OrderTier は注文数の抽選表の1段（plan-h36）。
type OrderTier struct {
	// Count はたこ焼きの個数（＝その客に配るお題の語数）。
	Count int `json:"count"`
	// Weight は出現の重み。他の段との比だけが意味を持つ（合計100である必要はない）。
	// 0 ならその段は出ない（＝段階を実質2つにする使い方）。
	Weight int `json:"weight"`
}

// DefaultOrderTiers は注文数の内蔵既定値（plan-h36 §1.1・2026-08-18 に企画側が決めた配分）。
//
//	軽い 2個 / 重み35 … **必ず達成できる客**。遅い人が「ずっと打っているのに0点」で終わらない担保
//	標準 4個 / 重み35 … 母数
//	重い 8個 / 重み30 … アート要望の「8個」。達成すると大きい
//
// 平均 4.50個。
//
// 🔴 **守るべき制約は「最も軽い段階（2個）を消さないこと」の1つだけ。**
// 8個の割合は 10%〜40% まで振っても実力相関が 0.94〜0.95 で動かなかった（h36 §1.1 の実測）。
// 効いているのは 8個の割合ではなく「軽い客が存在すること」で、4/6/8 のように全段階を重くすると
// **8個固定と同じ問題（実力相関 0.88・打ち切れないまま終わる）が戻る**。
//
// ⚠ **DB の値がここに勝つ**。本番で h36 以前の体感へ戻すときはコードではなく
// config（customer.orderTiers）を 2/3/6 相当へ寄せる（ビルド不要・docs/runbook.md）。
func DefaultOrderTiers() [OrderTierCount]OrderTier {
	return [OrderTierCount]OrderTier{
		{Count: 2, Weight: 35},
		{Count: 4, Weight: 35},
		{Count: 8, Weight: 30},
	}
}

// EffectiveOrderTiers は実際に使う注文数の抽選表を返す。**ゼロを既定値へ読み替える二重防御の1枚目**。
//
// 🔴 **ここが plan-h36 で最も危険な箇所**（#124 の再来防止）。
//
//	backfillDefaults の補完は**グループ単位**（グループ全体がゼロのときだけ既定を入れる）。
//	本番DBには `customer` グループが既にある（total / normal / bonus / claimer / buzz）ので、
//	新設した orderTiers は補完されず**ゼロのまま読まれる**。
//	そのまま使うと「注文数0個の客」が5000人生まれて試合が壊れ、
//	かといってゼロを Validate で弾くと Load が失敗して **score / cull / heat を含む
//	全設定が内蔵デフォルトへ巻き戻る**（config から何を変えても効かない状態＝#124）。
//
// なので **Validate では弾かず、ここで読み替える**。db 側の backfillNewFields でも同じ補正を
// 掛けているが、**sim・テスト・DB無し起動でも安全**にするために game 側にも置く
// （cull.EffectiveWarnMaxIds / bot.EffectiveTiers と同じ二重防御）。
//
// 補完は**要素単位**。config から2要素だけ保存されて3つ目が {0,0} になる
// 「配列のゼロ埋め」（h22 §2.2 の罠）も、その1要素だけ既定へ戻して吸収する。
//
// ⚠ Weight=0 は**潰さない**（「この段は出さない」という正当な設定）。潰すのは
// 「Count が 0 以下＝打つ語が無い客」と、抽選が成立しない「重みの合計が 0」だけ。
func (cp CustomerParams) EffectiveOrderTiers() [OrderTierCount]OrderTier {
	def := DefaultOrderTiers()
	out := cp.OrderTiers
	total := 0
	for i := range out {
		if out[i] == (OrderTier{}) {
			out[i] = def[i] // 丸ごと未設定（＝DBに無い／ゼロ埋め）
		}
		if out[i].Count <= 0 {
			out[i].Count = def[i].Count // 0個の客は「お題ゼロで即完了」になり試合が壊れる
		}
		if out[i].Weight < 0 {
			out[i].Weight = 0
		}
		total += out[i].Weight
	}
	if total <= 0 {
		return def // 全段の重みが 0 だと抽選できない
	}
	return out
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

// OdaiParams: お題（出題語）の難度を heat から独立して微調整するツマミ（plan-h35 §2.1）。
//
// 🔴 **既定値は両方 0 で、現行と完全に同じ挙動**（wordLevel() == heatLevel）。
// デプロイした瞬間に難度が変わってはいけない。動くのは運営が意図して値を入れた時だけ。
//
//	wordLevel = clamp0(heatLevel + LevelOffset ± rand(LevelSpread))
//
// game は odai（辞書）を import できない（層の依存が逆流する）ので、ここはあくまで
// 「WordSource に要求する level」を動かすだけ。上振れして辞書の上端を超えても
// WordSource が下の段階へ降りるので、追加のガードは要らない（plan-h35 §2.1）。
type OdaiParams struct {
	// LevelSpread は1語ごとの level のばらつき幅（±）。
	// 0 なら現行どおり heatLevel と完全一致。2 なら heatLevel-2 〜 heatLevel+2 から一様に選ぶ。
	//
	// 同じ heat でも語の長さに幅が出るので、「難度が上がる＝全部が一律に難しくなる」
	// という単調さが減る。
	//
	// ⚠ **0 より大きくすると wordLevel() が s.rng を1回余分に消費する。**
	// シードを固定した再現性は保たれる（同じ順序で回る）が、既存テストの期待値とは
	// ズレる。既定を 0 にしてあるのはそのため。
	LevelSpread int `json:"levelSpread"`

	// LevelOffset は heatLevel に対する下駄。お題だけをやさしく/難しくしたい時に使う。
	//
	// heat.* を触るとカーブ全体の形が変わるうえ DifficultyUpdate でクライアントの表示も動くが、
	// こちらは**カーブの形と表示を保ったまま平行移動**できるので、当日の調整として安全。
	// 負値も意味がある（-1 で1段やさしく）ので Validate では弾かない。
	// 下限は wordLevel() が 0 でクランプする。
	LevelOffset int `json:"levelOffset"`
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

	// WarnMaxIds は ForcedEliminationWarning.CutStoreIds の件数上限（plan-h35 §2.2）。
	//
	// 🔴 **24 はクライアント（みかみ）と合意済みの値**（2026-08-15）。初回の足切り（99→75）で
	// 切られるのがちょうど24店なので、最も人数が多いステージでも全員を出し切れる。
	// **当日に勝手に変えない。** 変えるなら必ずクライアントへ共有する。
	// 下げれば送信量が減る（帯域が問題になった時の逃げ道）、上げればデバッグ時に全件見える。
	//
	// 🔴 **0 は「上限なし」でも「1件も送らない」でもなく「未設定」を意味する**
	// （EffectiveWarnMaxIds で既定 24 に読み替える）。理由は §7.3 の補完の穴:
	// backfillDefaults の補完は**グループ単位**なので、本番DBに既に `cull` グループがある以上
	// この新フィールドは補完されず 0 のまま読まれる。ここで 0 を Validate で弾くと
	// **Load 全体が失敗して config が丸ごと内蔵デフォルト起動になる**（#124 と同じ事故）。
	// db 側でも読み込み時に 24 へ補正しているが、sim/テスト/DB無し起動でも安全になるよう
	// game 側でも 0 を既定として扱う（二重の防御）。
	WarnMaxIds int `json:"warnMaxIds"`
}

// DefaultCullWarnMaxIds は WarnMaxIds の既定値（＝旧ハードコード値）。
const DefaultCullWarnMaxIds = 24

// EffectiveWarnMaxIds は実際に使う予告件数の上限を返す。
// 0 以下（＝DBに無い／未設定）は既定の 24 として扱う。理由は WarnMaxIds のコメント。
func (cp CullParams) EffectiveWarnMaxIds() int {
	if cp.WarnMaxIds <= 0 {
		return DefaultCullWarnMaxIds
	}
	return cp.WarnMaxIds
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

// BotTierCount は Bot の強さ階層の数（強／中／弱）。
const BotTierCount = 3

// tier の添字。**強い順**に並べる（配列の順序そのものが意味を持つ）。
const (
	BotTierStrong = 0
	BotTierNormal = 1
	BotTierWeak   = 2
)

// BotTierLabel は tier の表示名を返す（観測用・AdminSnapshot の tier フィールド）。
// 未知の添字は空文字（＝人間と同じ扱い）。
func BotTierLabel(i int) string {
	switch i {
	case BotTierStrong:
		return "strong"
	case BotTierNormal:
		return "normal"
	case BotTierWeak:
		return "weak"
	}
	return ""
}

// BotTier は Bot 1階層ぶんの強さ（plan-h31 §1.2）。
//
// ⚠ **Tiers は slice ではなく固定長配列**。Go では配列は要素が comparable なら `==` 可能なので、
// AGENTS.md §1.3 の「map / slice をフィールドに入れない」を満たす（cull.stages と同じ流儀）。
type BotTier struct {
	// Weight は配分の重み。99体をこの比で3階層へ振り分ける（0 ならその tier は出ない）。
	Weight int `json:"weight"`

	// MsPerKey は1打鍵あたりのms。小さいほど速い＝強い。
	//
	// 🔴 **「1注文あたりの所要」ではない**。plan-h31 §1.1 は `ElapsedMs`（注文あたり）で
	// 書かれていたが、実装時に**打鍵あたりへ改めた**（plan 冒頭の訂正を参照）。理由は実測:
	//
	//	level  1注文(通常客・3語)の打鍵数   旧 bot(6000ms固定) の実効
	//	    0            約 13 打鍵            476 ms/打鍵 ← 遅すぎ
	//	   17            約130 打鍵             46 ms/打鍵 ← 人間離れ
	//
	// 注文あたりの固定時間だと、heat で語が長くなるほど**Bot だけが相対的に速くなる**
	// （plan-h31 が挙げた問題③そのもの）。打鍵あたりで持てば人間と同じ土俵に乗り、
	// sim のダミー店（msPerKey 200 前後）とも直接比較できる。
	MsPerKey int `json:"msPerKey"`

	// MissRate は打鍵1回あたりのミス率。個体差では MsPerKey と**同じ係数**で動く。
	MissRate float64 `json:"missRate"`

	// HeatPenalty は難度追従の強さ。実効時間が (1 + HeatPenalty×heatLevel) 倍になる。
	// 強い tier ほど小さく（難しい語でも速度が落ちない）、弱い tier ほど大きくすると、
	// **終盤に弱い Bot から落ちる自然な淘汰**が生まれる（plan-h31 §2.3）。
	HeatPenalty float64 `json:"heatPenalty"`
}

// BotParams: CPU（Bot）の強さ（plan-h31）。
//
// 🔴 **旧スキーマ（baseAccuracy / baseElapsedMs / accuracyJitter）は廃止した。**
// 全 Bot が同じ1つの Config から作られており、jitter は「毎回の揺らぎ」であって
// 個体差ではなかったため、99体が同じ平均へ回帰して順位表がランダムウォークしていた。
// 本番DBに残る旧キーは JSON の未知キーとして無視される（害は無いが config-front からは消すこと）。
type BotParams struct {
	// Tiers は強／中／弱の3階層。Bot 生成時に Weight で抽選する（抽選は app 層・§3）。
	//
	// 🔴 **ゼロ埋めを Validate で弾いてはいけない**。理由は EffectiveTiers を参照。
	Tiers [BotTierCount]BotTier `json:"tiers"`

	// IndividualSpread は個体差の幅（±の割合）。0.20 なら tier 値の 0.80〜1.20 倍で個体が散る。
	// 生成時に1回だけ引いて**その Bot の一生ぶん固定**する（plan-h31 §2.1）。
	//
	// 🔴 **0 は「個体差なし」ではなく「未設定」**（EffectiveIndividualSpread が既定へ読み替える）。
	// 理由は Tiers と同じで、既存グループへ後から足したキーは本番DBで 0 のまま読まれるため。
	// 個体差を実質的に切りたいなら 0.001 のような極小値を入れること。
	IndividualSpread float64 `json:"individualSpread"`

	// ElapsedJitterMs は1注文ごとの所要時間の揺らぎ幅（±ms）。**個体差ではない**。
	ElapsedJitterMs int `json:"elapsedJitterMs"`
}

// DefaultBotTiers は tier の内蔵既定値（plan-h31 §4・実測で調整）。
//
// MsPerKey は sim のダミー店（ProfileNormal: 200±50 ms/打鍵）と同じ土俵に置いてある。
// h26 が weightMiss=30 を決めたのはその分布に対してなので、Bot をここに合わせると
// 「人間が真ん中あたりに来る」が sim の結論とつながる。
func DefaultBotTiers() [BotTierCount]BotTier {
	return [BotTierCount]BotTier{
		// 強: 25% ／ 速く正確で、難度が上がっても崩れない
		{Weight: 25, MsPerKey: 150, MissRate: 0.02, HeatPenalty: 0.01},
		// 中: 50% ／ sim の ProfileNormal の中心と同じ
		{Weight: 50, MsPerKey: 200, MissRate: 0.05, HeatPenalty: 0.02},
		// 弱: 25% ／ 遅くてミスも多く、難度に弱い＝終盤で落ちる
		//
		// 🔴 **280 ではなく 400。** 280 だと Bot の幅（実効 153〜308ms/打鍵）が
		// 人間の幅より狭く、**初心者（400ms/打鍵）が必ず 100位/100 になる**。
		// 「人間が真ん中に来る」を狙った plan で、遅い人だけ自動的に最下位という
		// 体験になってしまう。弱の中心を初心者の基準速度に合わせ、幅を覆わせる。
		//
		//	弱tier   初心者の順位   速い人間   標準の人間
		//	  280      100/100        19位       54位
		//	  380       95/100        19位       54位
		//	  400       91/100        19位       54位   ← これ
		//	  440       87/100        19位       54位
		//
		// **強・中を動かさないので上位争い（誰が優勝するか）には一切影響しない。**
		// 変わるのは下位の厚みだけで、弱 Bot は早期に足切りされるので決勝の質も保たれる。
		{Weight: 25, MsPerKey: 400, MissRate: 0.10, HeatPenalty: 0.04},
	}
}

// DefaultBotIndividualSpread は個体差の既定幅（±20%）。
const DefaultBotIndividualSpread = 0.20

// EffectiveTiers は実際に使う tier を返す。**ゼロを既定値へ読み替える二重防御の1枚目**。
//
// 🔴 **ここが本 plan で最も危険な箇所**（plan-h31 §4.1・#124 の再来防止）。
//
//	backfillDefaults の補完は**グループ単位**（グループ全体がゼロのときだけ既定を入れる）。
//	本番DBには `bot` グループが既にある（旧スキーマの baseElapsedMs=6000 等）ので、
//	新設した tiers は補完されず**ゼロのまま読まれる**。
//	そのゼロを Validate で弾くと Load が失敗し、**score / cull / heat を含む全設定が
//	内蔵デフォルトへ巻き戻る**（config-front から何を変えても効かない状態＝#124）。
//
// なので **Validate では弾かず、ここで読み替える**。db 側の backfillNewFields でも同じ補正を
// 掛けているが、**sim・テスト・DB無し起動でも安全**にするために game 側にも置く
// （cull.EffectiveWarnMaxIds と同じ二重防御）。
//
// 補完は**要素単位**。config-front から2要素だけ保存されて3つ目が {0,0,0,0} になる
// 「配列のゼロ埋め」（h22 §2.2 の罠）も、その1要素だけ既定へ戻して吸収する。
func (bp BotParams) EffectiveTiers() [BotTierCount]BotTier {
	def := DefaultBotTiers()
	out := bp.Tiers
	total := 0
	for i := range out {
		if out[i] == (BotTier{}) {
			out[i] = def[i] // 丸ごと未設定（＝DBに無い／ゼロ埋め）
		}
		if out[i].MsPerKey <= 0 {
			out[i].MsPerKey = def[i].MsPerKey // 0ms/打鍵は「無限に速い」になる
		}
		if out[i].Weight < 0 {
			out[i].Weight = 0
		}
		if out[i].MissRate < 0 {
			out[i].MissRate = 0
		}
		if out[i].HeatPenalty < 0 {
			out[i].HeatPenalty = 0
		}
		total += out[i].Weight
	}
	if total <= 0 {
		return def // 全 tier の重みが 0 だと抽選できない
	}
	return out
}

// EffectiveIndividualSpread は実際に使う個体差の幅を返す。0 以下は「未設定」として既定 0.20。
// 理由は BotParams.IndividualSpread のコメント（ゼロのまま読まれる本番DBへの防御）。
func (bp BotParams) EffectiveIndividualSpread() float64 {
	if bp.IndividualSpread <= 0 {
		return DefaultBotIndividualSpread
	}
	return bp.IndividualSpread
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
	if err := gp.Bot.validate(); err != nil {
		return err
	}
	if gp.Heat.MaxLevel <= 0 {
		return fmt.Errorf("heat.maxLevel は正である必要 (got %d)", gp.Heat.MaxLevel)
	}
	if err := gp.Cull.validate(); err != nil {
		return err
	}
	// お題の下駄とばらつき（plan-h35 §2.1）。
	//
	// LevelSpread が負だと rng.Intn に 0 以下が渡って **panic する**。ここで弾く。
	// LevelOffset は負値に意味があるので弾かない（wordLevel() が 0 でクランプする）。
	if gp.Odai.LevelSpread < 0 {
		return fmt.Errorf("odai.levelSpread は非負である必要 (got %d)", gp.Odai.LevelSpread)
	}
	// 🔴 **0 は弾かない。** 0 は「未設定」で、EffectiveWarnMaxIds が既定 24 に読み替える。
	// ここで 0 を弾くと、本番DB（cull グループが既にあり warnMaxIds だけ無い）が
	// Validate に落ちて config 丸ごと内蔵デフォルト起動になる（params.go の WarnMaxIds 参照）。
	// 負値だけは明確な誤りなので弾く。
	if gp.Cull.WarnMaxIds < 0 {
		return fmt.Errorf("cull.warnMaxIds は非負である必要 (got %d)。"+
			"0 は「未設定」として既定 %d に読み替えられる", gp.Cull.WarnMaxIds, DefaultCullWarnMaxIds)
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
	// 注文数の抽選表（plan-h36）。
	//
	// 🔴 **0 は弾かない。** 本番DBには `customer` グループが既にあり、後から足した
	// orderTiers は**グループ単位の backfill をすり抜けてゼロのまま読まれる**。
	// ここで 0 を弾くと Load 全体が失敗し、score / cull / heat を含む全設定が
	// 内蔵デフォルトへ巻き戻る（#124 と同じ経路）。ゼロの吸収は EffectiveOrderTiers の役目。
	// 弾くのは「運営が意図して入れないと現れない、明確に誤った値」＝負値だけ。
	for i, t := range gp.Customer.OrderTiers {
		if t.Count < 0 {
			return fmt.Errorf("customer.orderTiers[%d].count は非負である必要 (got %d)。"+
				"0 は「未設定」として既定へ読み替えられる", i, t.Count)
		}
		if t.Weight < 0 {
			return fmt.Errorf("customer.orderTiers[%d].weight は非負である必要 (got %d)", i, t.Weight)
		}
	}
	return nil
}

// validate は Bot の破綻値を弾く（plan-h31）。
//
// 🔴 **ゼロは絶対に弾かない。** plan-h31 §1.3 は「配列のゼロ埋めを Validate で弾く」と
// 書いているが、**そのまま実装すると本番が壊れる**。本番DBには `bot` グループが既にあり、
// 新設した tiers はグループ単位の backfill をすり抜けてゼロのまま読まれるので、
// ここで弾くと Load 全体が失敗し、**全パラメータが内蔵デフォルトへ巻き戻る**（#124 と同じ経路）。
// ゼロ埋めの吸収は EffectiveTiers（＋db.backfillNewFields）の役目。
//
// ここで弾くのは「運営が意図して入れないと現れない、明確に誤った値」だけ:
// 負値と、確率として成立しない MissRate>1、個体係数が 0 以下になる IndividualSpread>=1。
func (bp BotParams) validate() error {
	for i, t := range bp.Tiers {
		if t.Weight < 0 {
			return fmt.Errorf("bot.tiers[%d].weight は非負である必要 (got %d)", i, t.Weight)
		}
		if t.MsPerKey < 0 {
			return fmt.Errorf("bot.tiers[%d].msPerKey は非負である必要 (got %d)。"+
				"0 は「未設定」として既定へ読み替えられる", i, t.MsPerKey)
		}
		if t.MissRate < 0 || t.MissRate > 1 {
			return fmt.Errorf("bot.tiers[%d].missRate は 0..1 である必要 (got %v)", i, t.MissRate)
		}
		if t.HeatPenalty < 0 {
			return fmt.Errorf("bot.tiers[%d].heatPenalty は非負である必要 (got %v)", i, t.HeatPenalty)
		}
	}
	// 1 以上だと個体係数 f = 1 + uniform(-s,+s) が 0 以下になり、所要時間が破綻する。
	if bp.IndividualSpread < 0 || bp.IndividualSpread >= 1 {
		return fmt.Errorf("bot.individualSpread は 0 以上 1 未満である必要 (got %v)。"+
			"0 は「未設定」として既定 %v に読み替えられる", bp.IndividualSpread, DefaultBotIndividualSpread)
	}
	if bp.ElapsedJitterMs < 0 {
		return fmt.Errorf("bot.elapsedJitterMs は非負である必要 (got %d)", bp.ElapsedJitterMs)
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
		// 属性は**出現率だけ**（見た目の彩り）。注文数は OrderTiers が独立に決める（plan-h36）。
		//
		// h30 は「1語を短くしたぶん、難度と報酬は何語打つかで持たせる」として
		// 属性ごとの orderCount を 2/2/1/4 → 3/3/2/6 に上げたが、その持ち方だと
		// **個数の段階を増やす＝キャラを増やす**ことになっていた。h36 で分離。
		Customer: CustomerParams{
			Total:      5000,
			Normal:     AttributeSpec{Attribute: proto.AttrNormal, Weight: 70},
			Bonus:      AttributeSpec{Attribute: proto.AttrBonus, Weight: 15},
			Claimer:    AttributeSpec{Attribute: proto.AttrClaimer, Weight: 10},
			Buzz:       AttributeSpec{Attribute: proto.AttrBuzz, Weight: 5},
			OrderTiers: DefaultOrderTiers(),
		},
		// **100 : 22**（ミス1回 = たこ焼き 0.22 個ぶんの損）。
		//
		// 判断基準は h26 から一貫して「**速さ型と正確型の平均順位が拮抗する点**」。
		// 拮抗点はゲーム側を変えるたびに動くので、**お題カーブや heat カーブを触ったら
		// 必ず `--sweep-miss` を回し直すこと**（下の経緯がその教訓そのもの）。
		//
		//	時期                     拮抗点   備考
		//	h26（1語が長い辞書）        25
		//	h30（1語を短く+orderCount）  30    ミスの罰が相対的に軽くなった
		//	h32（heat を時間主軸へ）     22    ★ここで動いたのに測り直していなかった
		//	h33（sim の人間モデル是正）   22    duel は凍結なので h33 では動かない
		//
		// 現 main での実測（`--sweep-miss --runs 10 --seed 42`・平均順位の差）:
		//
		//	W_MISS   速さ型   正確型     差     上位10の速%
		//	    22     49.4     50.6    -1.3        10%   ← 拮抗点
		//	    25     56.4     43.5   +13.0         3%
		//	    28     59.5     40.3   +19.3         0%
		//	    30     62.5     37.2   +25.3         0%   ← 旧既定。正確型に +25 位傾く
		//
		// h32 が難度を時間主軸にして**序盤から語を伸ばした**結果、ミスの絶対数が増えて
		// 罰が再び重くなった、という向き。30 のままだと「ミスを恐れて慎重に打つだけ」の
		// ゲームに寄り、企画が狙う「速さと正確さの切り替え」が起きない。
		//
		// 🔴 **平均順位と「上位10の構成」は一致しない**（2つの型で分散が違う。
		// ミス減点が速さ型の分散を抑えるため、正確型のほうが上振れしやすい）。
		// 「上位10の速%が50%付近」を基準に採るなら 22 より下になる。
		// **どちらの基準を採るかは所有者が決める**（2026-08-18 は平均順位の拮抗を採用）。
		//
		// ⚠ sim のダミー実力分布に依存する数字。**実プレイでの確認が最終判断**
		//（plan-h26 §3）。当日は config から調整でき、ビルドは要らない。
		Score: ScoreParams{
			WeightTakoyaki: 100,
			WeightMiss:     22,
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
		// 🔴 **両方 0 ＝ 現行と完全に同じ挙動**（wordLevel() == heatLevel）。
		// 当日「難しすぎる／簡単すぎる」となったら levelOffset を ±1 する。
		// heat.* と違ってカーブの形もクライアント表示も動かない（plan-h35 §2.1）。
		Odai: OdaiParams{
			LevelSpread: 0,
			LevelOffset: 0,
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
			WarnMaxIds: DefaultCullWarnMaxIds,
		},
		Distribution: DistributionParams{
			QueueRefillThreshold: 5,
		},
		Presentation: PresentationParams{
			FinalStageAliveThreshold: 20,
			FinalRushAliveThreshold:  10,
		},
		// Bot は tier（強／中／弱）＋個体差で散らす（plan-h31）。
		//
		// 🔴 **本番DBには旧スキーマの bot グループが入っている**（baseElapsedMs=6000 等）。
		// グループ単位の backfill はそこへ触らないので tiers はゼロのまま読まれ、
		// EffectiveTiers がこの既定値へ読み替える。config-front から一度保存すれば
		// DB にも実値が入る（そのため config-front 側の既定値と必ず揃えること）。
		Bot: BotParams{
			Tiers:            DefaultBotTiers(),
			IndividualSpread: DefaultBotIndividualSpread,
			// 1注文の所要は heat 次第で 3秒〜30秒に及ぶので、±500ms は「気持ち揺れる」程度。
			// 個体差（IndividualSpread）と役割が違う点に注意（こちらは毎回振り直す）。
			ElapsedJitterMs: 500,
		},
	}
}
