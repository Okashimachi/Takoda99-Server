package app

import (
	"context"
	"math/rand"

	"takoda99/internal/bot"
	"takoda99/internal/game"
	"takoda99/internal/matchmaking"
	"takoda99/internal/transport"
	"takoda99/internal/typist"
)

// bot.go は Bot 枠の生成と **tier の抽選**（plan-h31 §3）。
//
// 🔴 **抽選は app 層でやる。** `internal/game` は Bot を知らない（AGENTS.md §4.2・depguard で機械強制）。
// 一方 `internal/bot` は「渡された個体を演じる」だけにして、強さの決め方を合成ルート側に集める。
// こうしておくと h04/h05（実データから tier を生成）で差し替えるのはこの関数だけで済む。

// BotSpec は Bot 1体ぶんの個体（抽選の結果）。
//
// **生成時に決めて一生変わらない**のが要点。毎回の揺らぎ（typist.Ability.JitterMs）とは別物で、
// これを分けたことで「あの店ずっと速いな」が成立する（plan-h31 §1.1）。
type BotSpec struct {
	// Tier は抽選で当たった階層の添字（game.BotTierStrong / Normal / Weak）。
	Tier int
	// TierName は観測用のラベル（"strong" / "normal" / "weak"）。AdminSnapshot に載る。
	TierName string
	// Ability はこの個体の実力。tier の基準値に個体係数を掛けた**固定値**。
	Ability typist.Ability
}

// DrawBotSpec は tier を重みで抽選し、個体係数を掛けて1体ぶんの個体を決める（純粋・rng 注入）。
//
// rng を引数に取るのは **シードを固定すれば同じ 99体が再現できる**ようにするため
// （plan-h31 §3・matchsim やテストで配分を検証できる）。
func DrawBotSpec(bp game.BotParams, rng *rand.Rand) BotSpec {
	tiers := bp.EffectiveTiers() // 🔴 ゼロ埋め対策。理由は game.BotParams.EffectiveTiers
	idx := drawTierIndex(tiers, rng)
	t := tiers[idx]

	// 個体係数は tier を引いた**あとに1回だけ**引く。速度とミス率の両方に同じ係数が掛かる
	// （＝正の相関。独立に振ると「速いがミスだらけ」という現実にいない個体が生まれる）。
	f := typist.IndividualFactor(bp.EffectiveIndividualSpread(), rng)
	ability := typist.Individual(typist.Ability{
		MsPerKey:    float64(t.MsPerKey),
		MissRate:    t.MissRate,
		HeatPenalty: t.HeatPenalty,
		JitterMs:    bp.ElapsedJitterMs,
	}, f)

	return BotSpec{Tier: idx, TierName: game.BotTierLabel(idx), Ability: ability}
}

// drawTierIndex は重み付き抽選（customer の属性抽選と同じ流儀）。
func drawTierIndex(tiers [game.BotTierCount]game.BotTier, rng *rand.Rand) int {
	total := 0
	for _, t := range tiers {
		total += t.Weight
	}
	if total <= 0 {
		// EffectiveTiers が重み合計 0 を既定へ戻すので通常は来ない（防御）。
		return game.BotTierNormal
	}
	r := rng.Intn(total)
	for i, t := range tiers {
		if r < t.Weight {
			return i
		}
		r -= t.Weight
	}
	return game.BotTierCount - 1
}

// NewBotPlayer は Bot 枠を1つ作る。tier と個体差はここで抽選する。
func NewBotPlayer(ctx context.Context, id game.PlayerId, bp game.BotParams) matchmaking.Player {
	return NewBotPlayerWith(ctx, id, DrawBotSpec(bp, newRng()))
}

// NewBotPlayerWith は個体を指定して Bot 枠を1つ作る（テスト・再現実験用）。
func NewBotPlayerWith(ctx context.Context, id game.PlayerId, spec BotSpec) matchmaking.Player {
	srv, cli := transport.Pipe()
	b := bot.New(cli, spec.Ability, newRng())
	go b.Run(ctx)
	// Name は空にして RunMatch の fallbackName に任せる。
	// ここで採番すると接続IDベースになり、試合数が増えるほど桁が伸びて6文字を超える。
	return matchmaking.Player{Id: id, Conn: srv, IsBot: true, Tier: spec.TierName}
}
