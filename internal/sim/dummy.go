package sim

import (
	"fmt"
	"math/rand"

	"takoda99/internal/game"
	"takoda99/internal/odai"
	"takoda99/internal/proto"
	"takoda99/internal/typist"
)

// dummy.go はシミュレータ内の仮想プレイヤー（ダミー店）。
//
// 実力は **1本のスカラー `skill ∈ [0,1]`** で表し、そこから
// (1打鍵あたりms・打鍵あたりミス率・難度追従) を `typist.SkillCurve` で導く（plan-h33 §1.1）。
//
// 🔴 **乱数を振るのは skill 1本だけ**。以前は速度とミス率を独立の乱数2本で振っていたため、
// 「130ms/打鍵なのにミス率9%」「300ms/打鍵なのにミス率1%」という**実在しない個体**が
// 分布の四隅に置かれ、それが順位表の上下を作っていた。判定式（skill→実力・実力→結果）は
// **実 Bot と共有する**（internal/typist・plan-h31 §5 / plan-h33 §3）。

// Profile は店舗の実力分布のプリセット。
//
// **プリセットは「skill をどう分布させるか」の指定**であって、速度とミス率を別々に決めるものではない
// （duel と match の Bot 枠だけが例外。それぞれの定数のコメントを参照）。
type Profile string

const (
	ProfileUniform Profile = "uniform" // 全員 skill 0.5（膠着の最悪ケース）
	ProfileNormal  Profile = "normal"  // skill が正規分布（現実に近い）
	ProfileBipolar Profile = "bipolar" // skill 0.2 / 0.8 の二山（上手い/下手がはっきり）
	ProfileWide    Profile = "wide"    // skill が一様 [0,1]（実力差が非常に大きい）

	// ProfileDuel は **速さ型 vs 正確型** を半々で戦わせる（plan-h26 §2.1・最重要の検証）。
	//
	// 狙いは「どちらかが常勝でない重み比率」を見つけること。
	// 速さ型は生の打鍵が速いぶん**たこ焼きを多く作れるが、ミスも多い**。
	// 正確型はほぼミスしないが**作れる数が少ない**。この2つが拮抗する
	// score.weightMiss を探すのが h26 の中心作業。
	//
	// 🔴 **duel だけは skill 相関からも難度追従からも外す**（plan-h33 §1.1）。
	// あれは現実の母集団を模すものではなく「極端な2型をあえて作って比率を測る道具」で、
	// h26 の検証（§2.2 の表）がこの数字に乗っている。**触ると過去の測定と比較できなくなる。**
	ProfileDuel Profile = "duel"

	// ProfileMatch は**本番の卓**を模す（plan-h33 §2）。
	//
	// Bot 枠は h31 の tier 抽選（強25%/中50%/弱25%＋個体差±20%）を `GameParameters.Bot` から
	// そのまま引き、残りを人間（skill が正規分布）にする。**人間が何位に来るか**を測るための
	// プロファイルで、これが本 plan の実用的な目的。
	ProfileMatch Profile = "match"
)

// 速さ型 / 正確型 の実力値（ProfileDuel）。
//
// 実効時間（打ち直しを含む1打鍵あたり）は
//
//	速さ型 = 150 × (1 + 0.12) = 168ms
//	正確型 = 200 × (1 + 0.01) = 202ms
//
// 速さ型が約 1.20 倍のたこ焼きを作れる代わりに、ミスは 12 倍出る。
// **どちらが勝つかは weightMiss だけで決まる**ようにしてある。
//
// 🔴 **HeatPenalty は 0 のまま**（duelHeatPenalty）。h33 の難度追従を入れると
// h26 §2.2 の実測表と比較できなくなる。
const (
	duelFastMsPerKey    = 150
	duelFastMissRate    = 0.12
	duelPreciseMsPerKey = 200
	duelPreciseMissRate = 0.01
	duelHeatPenalty     = 0.0
)

// 実力クラス（ProfileDuel でのみ付く）。
const (
	ClassFast    = "fast"
	ClassPrecise = "precise"
)

// skill → 実力 の対応（plan-h33 §1.1）。**両端の選び方には根拠がある。**
//
// 🔴 **既存プロファイルとの連続性を保つように選んである。** モデル変更で測りたいのは
// 「相関と難度追従を入れた効果」であって「母集団が丸ごと遅くなった効果」ではない。
// 中央（skill 0.5）を旧実装の中心と一致させると、変化を切り分けて読める:
//
//	プロファイル   旧実装                        新実装（skill 経由）
//	uniform       200ms / 5.0%                 skill 0.5   → 200ms / 5.0%    ← 完全一致
//	normal        200±50ms / 5.0±2.0%          N(0.5,0.15) → 200±45ms / 5.0±1.2%
//	bipolar       130ms/2% ・ 300ms/10%        0.8 / 0.2   → 110ms/2.6% ・ 290ms/7.4%
//	wide          100〜499ms / 0〜20%           U(0,1)      → 50〜350ms / 1〜9%
//
// 中心 200ms/5% は h26 が `weightMiss` を決めた分布であり、h31 が Bot の中 tier を
// 合わせた値でもある（game.DefaultBotTiers のコメント）。ここを動かすと両方の根拠が同時に動く。
//
// ⚠ skill=1 の 50ms/打鍵（＝20打/秒）は人間離れしているが、これは**線形補間の端点**であって
// 個体の頻度ではない。normal では +3.3σ（99店で 4% の試合に1体出るか出ないか）、
// wide では意図的に極端な個体を作るためのもの。旧実装も `200+N(0,50)` を 50ms で
// クランプしており、到達しうる上端は同じ。
//
// HeatPenalty の 0.04 / 0.01 は plan-h33 §1.2 の指定値で、
// h31 の弱 tier / 強 tier（game.DefaultBotTiers）と同値。
const (
	skillSlowMsPerKey    = 350.0
	skillFastMsPerKey    = 50.0
	skillHighMissRate    = 0.09
	skillLowMissRate     = 0.01
	skillHighHeatPenalty = 0.04
	skillLowHeatPenalty  = 0.01
)

// skill の分布（プロファイルごと・plan-h33 §1.1 の表）。
const (
	uniformSkill     = 0.5
	normalSkillMean  = 0.5
	normalSkillSigma = 0.15
	bipolarLowSkill  = 0.2
	bipolarHighSkill = 0.8
)

// DefaultMatchHumans は ProfileMatch の人間の人数の既定（Config.Humans が 0 のとき）。
const DefaultMatchHumans = 3

// HumanCurve は skill から実力を引く曲線。**sim の母集団の仮定そのもの**。
//
// 実 Bot はこの曲線ではなく `GameParameters.Bot.Tiers`（離散 tier）から実力を引く。
// 共有しているのは**モデル**（skill/tier → Ability → Outcome の計算式・internal/typist）で、
// 分布のパラメータは「sim の母集団の仮定」と「本番 Bot の設定」で別々に持つのが正しい。
func HumanCurve() typist.SkillCurve {
	return typist.SkillCurve{
		SlowMsPerKey:    skillSlowMsPerKey,
		FastMsPerKey:    skillFastMsPerKey,
		HighMissRate:    skillHighMissRate,
		LowMissRate:     skillLowMissRate,
		HighHeatPenalty: skillHighHeatPenalty,
		LowHeatPenalty:  skillLowHeatPenalty,
	}
}

// AllProfiles は決着保証テストが回すプリセット。
//
// duel は「決着するか」ではなく「どちらが勝つか」を見るためのものなので、ここには含めない
// （含めても決着保証テストの実行時間が伸びるだけで得るものが無い）。
// match は**本番の卓そのもの**なので含める（ここで決着しないなら本番で決着しない）。
func AllProfiles() []Profile {
	return []Profile{ProfileUniform, ProfileNormal, ProfileBipolar, ProfileWide, ProfileMatch}
}

// ParseProfile は文字列を Profile に解決する。
func ParseProfile(s string) (Profile, error) {
	switch p := Profile(s); p {
	case ProfileUniform, ProfileNormal, ProfileBipolar, ProfileWide, ProfileDuel, ProfileMatch:
		return p, nil
	default:
		return "", fmt.Errorf("未知の profile %q（uniform|normal|bipolar|wide|duel|match）", s)
	}
}

// queuedOrder は「来店したが、まだ打ち終わっていない客」1人ぶん。
type queuedOrder struct {
	customerId proto.CustomerId
	keystrokes int
}

// pendingOrder は現在打鍵中の客の進行。
type pendingOrder struct {
	customerId proto.CustomerId
	keystrokes int
	missCount  int
	// totalMs は打ち切るまでに実際に掛かる時間（ミスの打ち直しを含む）。
	totalMs     int
	remainingMs int
}

// dummyStore はシミュレータ内の仮想プレイヤー。
type dummyStore struct {
	id game.PlayerId
	// skill は実力スカラー [0,1]。skill を持たない個体（duel の2型・match の Bot）は
	// noSkill。ability が唯一の情報源で、skill は観測・回帰テスト用の付帯情報。
	skill   float64
	ability typist.Ability
	class   string // 実力クラス（ProfileDuel のみ。"fast" / "precise"）
	tier    string // Bot の tier（ProfileMatch のみ。"strong"/"normal"/"weak"）
	human   bool   // ProfileMatch で人間として置いた店

	alive bool

	// queue は session 側の storeQueues[id] を写した行列（先頭 = 対応中）。
	// session は「行列先頭でない客」への OrderServed を弾くので、
	// 来店(CustomerArrived)と提供の2つで必ず同期を保つ。
	// （本戦では客が逃げないので、離脱による同期ズレはもう起きない。）
	queue   []queuedOrder
	current *pendingOrder
}

// noSkill は「skill スカラーを持たない個体」の印（duel の2型・match の Bot）。
const noSkill = -1.0

// arrive は来店を受け付けて行列末尾に積む。
func (d *dummyStore) arrive(v proto.CustomerView) {
	d.queue = append(d.queue, queuedOrder{
		customerId: v.CustomerId,
		keystrokes: countKeystrokes(v.Words),
	})
}

// step は dtMs ぶん打鍵を進め、打ち終わったら提供報告を返す。
// heatLevel は**その時点の全店共通の難度**で、打ち始める瞬間の実効速度に効く。
func (d *dummyStore) step(dtMs, heatLevel int, rng *rand.Rand) (proto.OrderServed, bool) {
	if !d.alive {
		return proto.OrderServed{}, false
	}
	if d.current == nil {
		if len(d.queue) == 0 {
			return proto.OrderServed{}, false
		}
		d.current = d.begin(d.queue[0], heatLevel, rng)
	}

	d.current.remainingMs -= dtMs
	if d.current.remainingMs > 0 {
		return proto.OrderServed{}, false
	}

	o := proto.OrderServed{
		CustomerId: d.current.customerId,
		ElapsedMs:  d.current.totalMs,
		MissCount:  d.current.missCount,
	}
	d.queue = d.queue[1:]
	d.current = nil
	return o, true
}

// begin は行列先頭の客に取り掛かり、ミス回数と所要時間を先に確定させる。
//
// 🔴 **判定式は実 Bot と共有する**（typist.Serve・plan-h31 §5）。別々に書くと必ずズレる
// ——実際 h31 の前は「sim は打鍵ごとにミス判定、実 Bot は miss∈{0,1}」とズレており、
// sim で詰めた weightMiss が実試合で成立しない状態だった。
//
// heatLevel を渡すのが h33 の変更点。難度が上がると
// `(1 + HeatPenalty×heatLevel)` 倍だけ**1打鍵あたりも遅くなる**（語が長くなるぶんとは別に効く）。
// 以前は 0 固定だったので、sim は終盤を楽観的に見積もっていた（plan-h33 §0.2②）。
func (d *dummyStore) begin(q queuedOrder, heatLevel int, rng *rand.Rand) *pendingOrder {
	keys := q.keystrokes
	if keys <= 0 {
		keys = 1
	}
	// ミス1回につき1打鍵ぶん打ち直す。報告する elapsedMs は打ち直しを含む実測時間にする
	// （実クライアントが送るのは「実際に掛かった時間」なので、そこを合わせる）。
	out := typist.Serve(d.ability, keys, heatLevel, rng)
	return &pendingOrder{
		customerId:  q.customerId,
		keystrokes:  keys,
		missCount:   out.MissCount,
		totalMs:     out.ElapsedMs,
		remainingMs: out.ElapsedMs,
	}
}

// countKeystrokes は CustomerView.Words のローマ字打鍵数を求める。
//
// CustomerView は表示テキストしか持たず reading が無い（proto にも載っていない）。
// ただし現行のお題辞書は Text がそのまま読み（ひらがな）なので、公開済みの
// odai.Keystrokes をそのまま当てれば実際の打鍵数と一致する。将来 Text に漢字が
// 入ったら概算に劣化するが、reading を proto に載せるのは契約変更＝要承認なのでここではやらない。
//
// ルーン数で概算すると打鍵数が約半分になり、speed 評価が SpeedCap(2.0) に張り付いて
// 全店が同点になる。それではバランス検証にならないので、この精度が要る。
func countKeystrokes(words []string) int {
	n := 0
	for _, w := range words {
		n += odai.Keystrokes(w)
	}
	return n
}

// buildStores は n 店ぶんの実力をプリセットに従って割り当てる。
//
// ⚠ **乱数の消費数はプロファイルごとに固定**（決定性は sim の全結論の前提）:
//
//	uniform / bipolar / duel … 0 回（実力が定数）
//	normal / wide          … skill を1回
//	match                  … Bot 1体につき tier 抽選1回＋個体係数1回、人間1名につき skill 1回
func buildStores(cfg Config) []*dummyStore {
	n := cfg.Stores
	rng := cfg.Rng
	curve := HumanCurve()
	stores := make([]*dummyStore, n)

	// ProfileMatch は末尾 humans 店を人間にする。Bot 枠を先に作るので、
	// --humans を増やしても Bot 側の抽選列はそのまま残る。
	humans := 0
	if cfg.Profile == ProfileMatch {
		humans = cfg.Humans
		if humans <= 0 {
			humans = DefaultMatchHumans
		}
		if humans > n {
			humans = n
		}
	}
	botSlots := n - humans

	for i := range stores {
		d := &dummyStore{
			id:    game.PlayerId(fmt.Sprintf("s-%d", i+1)),
			skill: noSkill,
			alive: true,
		}
		switch cfg.Profile {
		case ProfileUniform:
			d.skill = uniformSkill
		case ProfileNormal:
			d.skill = clamp(normalSkillMean+rng.NormFloat64()*normalSkillSigma, 0, 1)
		case ProfileBipolar:
			if i%2 == 0 {
				d.skill = bipolarHighSkill
			} else {
				d.skill = bipolarLowSkill
			}
		case ProfileWide:
			d.skill = rng.Float64()
		case ProfileDuel:
			// 相関にも難度追従にも乗せない。h26 の検証がこの数字に乗っている。
			if i%2 == 0 {
				d.class = ClassFast
				d.ability = typist.Ability{
					MsPerKey: duelFastMsPerKey, MissRate: duelFastMissRate, HeatPenalty: duelHeatPenalty,
				}
			} else {
				d.class = ClassPrecise
				d.ability = typist.Ability{
					MsPerKey: duelPreciseMsPerKey, MissRate: duelPreciseMissRate, HeatPenalty: duelHeatPenalty,
				}
			}
		case ProfileMatch:
			if i < botSlots {
				tier, ab := drawBotAbility(cfg.Params.Bot, rng)
				d.tier, d.ability = game.BotTierLabel(tier), ab
			} else {
				d.human = true
				d.skill = clamp(normalSkillMean+rng.NormFloat64()*normalSkillSigma, 0, 1)
			}
		}
		if d.skill != noSkill {
			d.ability = curve.At(d.skill)
		}
		if d.ability.MsPerKey < minMsPerKey {
			d.ability.MsPerKey = minMsPerKey
		}
		stores[i] = d
	}
	return stores
}

// minMsPerKey は「無限に速い個体」を作らないための下限（旧実装から据え置き）。
const minMsPerKey = 50

// drawBotAbility は h31 の Bot 抽選（tier → 個体係数）を sim 側で再現する。
//
// ⚠ **実体は app.DrawBotSpec と同じだが、そちらは import できない**
// （depguard の sim-drives-core-only は sim → app/bot を許していない。app はスパインで、
// sim は game を直接叩く開発ツールという住み分け）。
// **共有しているのは判定式（typist.IndividualFactor / typist.Individual）**で、
// ここに残るのは「重み付き抽選」という数行だけ。tier の値そのものは
// `GameParameters.Bot`（＝本番と同じ config）から引くので、値の二重管理は無い。
func drawBotAbility(bp game.BotParams, rng *rand.Rand) (int, typist.Ability) {
	tiers := bp.EffectiveTiers() // 🔴 ゼロ埋め対策。理由は game.BotParams.EffectiveTiers
	total := 0
	for _, t := range tiers {
		total += t.Weight
	}
	idx := game.BotTierNormal
	if total > 0 {
		r := rng.Intn(total)
		for i, t := range tiers {
			if r < t.Weight {
				idx = i
				break
			}
			r -= t.Weight
		}
	}
	t := tiers[idx]
	f := typist.IndividualFactor(bp.EffectiveIndividualSpread(), rng)
	return idx, typist.Individual(typist.Ability{
		MsPerKey:    float64(t.MsPerKey),
		MissRate:    t.MissRate,
		HeatPenalty: t.HeatPenalty,
		JitterMs:    bp.ElapsedJitterMs,
	}, f)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
