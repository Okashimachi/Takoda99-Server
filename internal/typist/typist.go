// Package typist は【層3部品】**打ち手（Bot／sim のダミー店）の実力モデル**。
//
// 純粋関数だけを置く。状態も I/O も持たず、標準ライブラリ以外に依存しない
// （game も proto も import しない）。そのぶん **bot からも sim からも同じものを使える**。
//
// 🔴 **なぜ独立したパッケージなのか**（plan-h31 §5 / plan-h33 §3）
//
// 実 Bot（internal/bot・Connection 越しに OrderServed を作る）と
// sim のダミー店（internal/sim/dummy.go・Session を直接叩く）は、**別々に書かれていたため
// モデルがズレていた**。sim は打鍵ごとにミスを判定するのに、実 Bot は `miss ∈ {0,1}` 固定
// ——「sim で詰めた weightMiss が実試合では成立しない」という形で効く。
//
// depguard は sim → bot を禁じている（bot は Connection 越しの自動入力で別物）ので、
// **どちらからも見える下流にモデルだけを切り出す**のがこの package。
// 新しい実力パラメータを足すときは、必ずここに足して両方から使うこと。
//
// game に置かない理由: これは「打ち手の振る舞い」であって試合の権威ではない。
// game は Bot を知らない（AGENTS.md §4.2）。
package typist

// Ability は1人ぶんの実力。**生成時に決めて一生変えない**のが設計の中核で、
// 「毎回の揺らぎ」（JitterMs）とは明確に分ける（plan-h31 §1.1）。
//
// 分けないと全個体が同じ平均へ回帰し、順位表がランダムウォークする。
// 分ければ「あの店ずっと速いな」が成立し、観戦の価値にも直結する。
type Ability struct {
	// MsPerKey は1打鍵あたりのms。小さいほど速い。
	//
	// 🔴 **「1注文あたりの所要」ではなく「1打鍵あたり」**なのが要点。お題は heat が上がるほど
	// 長くなる（本戦の辞書で level 0 の1注文=約13打鍵、level 17 は約130打鍵）ので、
	// 注文あたりの固定時間で持つと**難度が上がるほど打ち手が相対的に速くなる**。
	MsPerKey float64
	// MissRate は打鍵1回あたりのミス率。所要時間と**同じ個体係数で動かす**こと（Individual）。
	MissRate float64
	// HeatPenalty は難度追従の強さ。実効時間が (1 + HeatPenalty×heatLevel) 倍になる。
	// 打鍵数の増加とは別に効く「難しい語ほど手が止まる」ぶん。0 なら難度に鈍感＝上手い。
	HeatPenalty float64
	// JitterMs は1注文ごとの所要時間の揺らぎ幅（±ms）。**個体差ではない**。
	JitterMs int
}

// Outcome は1注文を打ち切った結果。proto.OrderServed に載せる値そのもの。
type Outcome struct {
	ElapsedMs int
	MissCount int
}

// Rand は必要な乱数だけを要求する最小の口（*math/rand.Rand が満たす）。
// テストで決定的な値を差し込めるように interface にしてある。
type Rand interface {
	Float64() float64
	Intn(n int) int
}

// IndividualFactor は個体係数 f を1つ引く。**Bot 生成時に1回だけ呼ぶ**（plan-h31 §2.1）。
//
//	f = 1 + uniform(-spread, +spread)
//
// spread は 0〜1 未満。1 以上だと f が 0 以下になり所要時間が破綻するのでクランプする。
func IndividualFactor(spread float64, rng Rand) float64 {
	if spread <= 0 {
		return 1
	}
	if spread > 0.9 {
		spread = 0.9
	}
	return 1 + (rng.Float64()*2-1)*spread
}

// Individual は tier の基準値に個体係数を掛けて「その個体の実力」を作る。
//
// 🔴 **速度とミス率を同じ係数で動かす**のが要点（plan-h31 §2.1）。独立に振ると
// 「速いがミスだらけ」「遅いが完璧」という現実にはいない個体が大量に生まれる。
// 実際の人間は「上手い人は速くて正確、苦手な人は遅くてミスも多い」と**正の相関**がある。
//
// HeatPenalty と JitterMs は tier の値をそのまま引き継ぐ（個体では振らない）。
func Individual(base Ability, factor float64) Ability {
	if factor <= 0 {
		factor = 1
	}
	base.MsPerKey *= factor
	base.MissRate *= factor
	return base
}

// SkillCurve は**実力スカラー `skill ∈ [0,1]`** から Ability を引く線形補間の両端
// （plan-h33 §1.1・§1.2）。0 = 苦手／1 = 上手い。
//
// 🔴 **乱数を振るのを skill 1本だけにする**ための型。速度とミス率を独立の乱数2本で振ると
// 「130ms/打鍵なのにミス率9%」「300ms/打鍵なのにミス率1%」という**実在しない個体**が生まれ、
// それが順位表の上下を作ってしまう（plan-h33 §0.2①）。skill から両方を導けば
// **自動的に正の相関**を持つ。プロファイルは「skill をどう分布させるか」に格下げされる。
//
// Individual との使い分け:
//
//   - Individual … 「tier の基準値 × 個体係数」。**離散の tier 制**（実 Bot・plan-h31）向け
//   - SkillCurve … 「1本のスカラーから引く」。**連続分布**（sim の母集団・plan-h33）向け
//
// どちらも「速度とミス率を同じ向きに動かす」という同じ原則の実装で、だから同じ package に置く。
//
// HeatPenalty も skill に**反比例**させる（上手い人ほど難度に強い）。これで
// 「終盤に下位が崩れる」という自然な淘汰が出る（plan-h33 §1.2）。
type SkillCurve struct {
	// SlowMsPerKey / FastMsPerKey は skill=0 / skill=1 の1打鍵あたりms。
	SlowMsPerKey, FastMsPerKey float64
	// HighMissRate / LowMissRate は skill=0 / skill=1 の打鍵あたりミス率。
	HighMissRate, LowMissRate float64
	// HighHeatPenalty / LowHeatPenalty は skill=0 / skill=1 の難度追従の強さ。
	HighHeatPenalty, LowHeatPenalty float64
}

// At は skill に対応する Ability を返す。skill は [0,1] にクランプする
// （正規分布の裾がはみ出しても「無限に速い個体」を作らないため）。
//
// JitterMs は 0（毎回の揺らぎは個体の実力ではない）。必要なら呼び出し側で足す。
func (c SkillCurve) At(skill float64) Ability {
	if skill < 0 {
		skill = 0
	}
	if skill > 1 {
		skill = 1
	}
	lerp := func(atZero, atOne float64) float64 { return atZero + (atOne-atZero)*skill }
	return Ability{
		MsPerKey:    lerp(c.SlowMsPerKey, c.FastMsPerKey),
		MissRate:    lerp(c.HighMissRate, c.LowMissRate),
		HeatPenalty: lerp(c.HighHeatPenalty, c.LowHeatPenalty),
	}
}

// Serve は1注文（keystrokes 打鍵）を打ち切った結果を返す。
//
//	miss    = Σ[打鍵ごと] (rng < MissRate)
//	elapsed = (打鍵数 + ミス数) × MsPerKey × (1 + HeatPenalty×heatLevel) ± Jitter
//
// ミスは**打鍵ごとに判定する**（`miss ∈ {0,1}` にしない）。0/1 だと1注文で引かれる点が
// weightMiss ぶんで頭打ちになり、数ミスしうる人間に対して構造的に有利になる（plan-h31 §2.2）。
// 二項分布を実装せず打鍵ごとに乱数を1回引くのは、keystrokes が高々数百で読みやすい方を取ったため。
//
// ミス1回につき1打鍵ぶん打ち直す前提で所要時間に足す（報告する elapsedMs は
// 実クライアントと同じ「実際に掛かった時間」）。
//
// ⚠ **乱数の消費数は keystrokes 回（＋Jitter があれば1回）で固定**。MissRate が 0 でも
// ループを回して引く。ここを条件分岐で飛ばすと sim の既存の期待値（シード固定の再現）がズレる。
func Serve(a Ability, keystrokes, heatLevel int, rng Rand) Outcome {
	if keystrokes < 1 {
		keystrokes = 1
	}
	miss := 0
	for i := 0; i < keystrokes; i++ {
		if rng.Float64() < a.MissRate {
			miss++
		}
	}
	if heatLevel < 0 {
		heatLevel = 0
	}
	mult := 1 + a.HeatPenalty*float64(heatLevel)
	if mult < 0 {
		mult = 0
	}
	elapsed := int(float64(keystrokes+miss) * a.MsPerKey * mult)
	if a.JitterMs > 0 {
		elapsed += rng.Intn(2*a.JitterMs+1) - a.JitterMs
	}
	if elapsed < 1 {
		elapsed = 1
	}
	return Outcome{ElapsedMs: elapsed, MissCount: miss}
}

// EffectiveMsPerKey は「打ち直しを含む1打鍵あたりの実効時間」＝速さとミス率を1本にまとめた
// 真の実力の指標（小さいほど強い）。sim の実力相関の計算と、tier の強さ比較に使う。
func (a Ability) EffectiveMsPerKey() float64 {
	return a.MsPerKey * (1 + a.MissRate)
}
