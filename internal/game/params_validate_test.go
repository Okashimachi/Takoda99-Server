package game

import (
	"encoding/json"
	"testing"
)

func TestGameParameters_Validate(t *testing.T) {
	t.Run("デフォルトは妥当", func(t *testing.T) {
		if err := DefaultParameters().Validate(); err != nil {
			t.Fatalf("DefaultParameters は妥当なはず: %v", err)
		}
	})

	t.Run("customer.total<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Customer.Total = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("customer.total=0 はエラーになるべき")
		}
	})

	t.Run("session.tickIntervalMs<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Session.TickIntervalMs = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("session.tickIntervalMs=0 はエラーになるべき")
		}
	})

	// 🔴 **ゼロは弾かない**（plan-h31 §4.1・#124 の再来防止）。
	// 本番DBには `bot` グループが既にあるので新設の tiers はゼロのまま読まれる。
	// ここで弾くと Load が失敗し、score/cull/heat を含む**全設定が内蔵デフォルトへ巻き戻る**。
	t.Run("bot.tiers のゼロ埋めは弾かない（弾くと本番が既定値起動になる）", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Bot = BotParams{} // 本番DBの bot グループが旧スキーマ＝新フィールドが全部ゼロ
		if err := gp.Validate(); err != nil {
			t.Fatalf("ゼロの bot を弾いてはいけない（#124 と同じ事故になる）: %v", err)
		}
		// 弾かない代わりに、実際に使う値は既定へ読み替えられていること。
		if got := gp.Bot.EffectiveTiers(); got != DefaultBotTiers() {
			t.Fatalf("EffectiveTiers がゼロを既定へ読み替えていない: %+v", got)
		}
		if got := gp.Bot.EffectiveIndividualSpread(); got != DefaultBotIndividualSpread {
			t.Fatalf("EffectiveIndividualSpread=%v, want %v", got, DefaultBotIndividualSpread)
		}
	})

	t.Run("bot.tiers の負値は弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Bot.Tiers[BotTierWeak].MsPerKey = -1
		if err := gp.Validate(); err == nil {
			t.Fatal("msPerKey=-1 はエラーになるべき")
		}
	})

	t.Run("bot.tiers の missRate>1 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Bot.Tiers[BotTierNormal].MissRate = 1.5
		if err := gp.Validate(); err == nil {
			t.Fatal("missRate=1.5 はエラーになるべき（確率として成立しない）")
		}
	})

	t.Run("bot.individualSpread>=1 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Bot.IndividualSpread = 1
		if err := gp.Validate(); err == nil {
			t.Fatal("individualSpread=1 はエラーになるべき（個体係数が 0 以下になりうる）")
		}
	})

	t.Run("heat.maxLevel<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Heat.MaxLevel = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("heat.maxLevel=0 はエラーになるべき")
		}
	})

	t.Run("matching.minPlayers<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Matching.MinPlayers = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("matching.minPlayers=0 はエラーになるべき")
		}
	})

	t.Run("distribution.queueRefillThreshold<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Distribution.QueueRefillThreshold = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("distribution.queueRefillThreshold=0 はエラーになるべき")
		}
	})

	// ── 本戦（plan-h21 §5.3）で追加した検証 ──

	t.Run("score.weightTakoyaki<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Score.WeightTakoyaki = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("score.weightTakoyaki=0 はエラーになるべき（点が入らず順位が付かない）")
		}
		gp.Score.WeightTakoyaki = -1
		if err := gp.Validate(); err == nil {
			t.Fatal("score.weightTakoyaki=-1 はエラーになるべき")
		}
	})

	t.Run("score.weightMiss は 0 を許し負値を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Score.WeightMiss = 0
		if err := gp.Validate(); err != nil {
			t.Fatalf("score.weightMiss=0 は許容されるべき（ミスを罰しない設定）: %v", err)
		}
		gp.Score.WeightMiss = -1
		if err := gp.Validate(); err == nil {
			t.Fatal("score.weightMiss=-1 はエラーになるべき（ミスで加点される）")
		}
	})

	t.Run("sanity.minMsPerWord が負を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Sanity.MinMsPerWord = -1
		if err := gp.Validate(); err == nil {
			t.Fatal("sanity.minMsPerWord=-1 はエラーになるべき")
		}
	})

	// ── 足切りスケジュール（plan-h22 §2.2）──

	t.Run("cull: 5要素のゼロ埋めを弾く", func(t *testing.T) {
		// 🔴 これが Validate の存在理由。encoding/json は配列に要素数が足りない JSON を
		// 渡されると残りをゼロ値で埋める。config-front から5要素で保存されると
		// Stages[5]={AtMs:0,TargetAliveCount:0} になり「0秒時点で生存0＝開始直後に全店即死」。
		gp := DefaultParameters()
		var raw = `{"stages":[{"atMs":20000,"targetAliveCount":75},{"atMs":40000,"targetAliveCount":55},` +
			`{"atMs":60000,"targetAliveCount":35},{"atMs":80000,"targetAliveCount":20},` +
			`{"atMs":100000,"targetAliveCount":10}]}`
		if err := json.Unmarshal([]byte(raw), &gp.Cull); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		// 前提: ゼロ埋めが実際に起きていること（起きなければこのテストは無意味）。
		if gp.Cull.Stages[5] != (CullStage{}) {
			t.Fatalf("前提が崩れた: ゼロ埋めされていない %+v", gp.Cull.Stages[5])
		}
		if err := gp.Validate(); err == nil {
			t.Fatal("5要素のゼロ埋めはエラーになるべき（開始直後に全店即死する設定）")
		}
	})

	t.Run("cull: atMs が厳密増加でないものを弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Cull.Stages[2].AtMs = gp.Cull.Stages[1].AtMs
		if err := gp.Validate(); err == nil {
			t.Fatal("atMs が同値はエラーになるべき")
		}
		gp = DefaultParameters()
		gp.Cull.Stages[2].AtMs = gp.Cull.Stages[1].AtMs - 1
		if err := gp.Validate(); err == nil {
			t.Fatal("atMs が減少はエラーになるべき")
		}
	})

	t.Run("cull: atMs<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Cull.Stages[0].AtMs = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("atMs=0 はエラーになるべき")
		}
	})

	t.Run("cull: targetAliveCount が単調非増加でないものを弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Cull.Stages[2].TargetAliveCount = gp.Cull.Stages[1].TargetAliveCount + 1
		if err := gp.Validate(); err == nil {
			t.Fatal("生存数が増えるスケジュールはエラーになるべき")
		}
	})

	t.Run("cull: 最終ステージ以外の targetAliveCount=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Cull.Stages[3].TargetAliveCount = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("最終より前で生存0はエラーになるべき（試合が途中で終わる）")
		}
	})

	t.Run("cull: 最終ステージの targetAliveCount!=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Cull.Stages[CullStageCount-1].TargetAliveCount = 1
		if err := gp.Validate(); err == nil {
			t.Fatal("最終ステージで生存1はエラーになるべき（全店脱落で終わる）")
		}
	})

	t.Run("cull: 中間ステージの targetAliveCount は動かしてよい", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Cull.Stages[1].TargetAliveCount = 60
		gp.Cull.Stages[2].TargetAliveCount = 40
		if err := gp.Validate(); err != nil {
			t.Fatalf("中間ステージの調整は許容されるべき: %v", err)
		}
	})

	t.Run("customer.*.orderCount<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Customer.Buzz.OrderCount = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("customer.buzz.orderCount=0 はエラーになるべき")
		}
	})

	// ── お題のツマミ（plan-h35 §2）──

	t.Run("odai.levelSpread が負を弾く", func(t *testing.T) {
		// 負値は rng.Intn(2*sp+1) に 0 以下を渡して **panic する**。
		gp := DefaultParameters()
		gp.Odai.LevelSpread = -1
		if err := gp.Validate(); err == nil {
			t.Fatal("odai.levelSpread=-1 はエラーになるべき（rng.Intn が panic する）")
		}
	})

	t.Run("odai.levelOffset は負値を許す", func(t *testing.T) {
		// 「お題だけをやさしくする」が levelOffset の存在理由なので、負値は正常な使い方。
		gp := DefaultParameters()
		gp.Odai.LevelOffset = -3
		if err := gp.Validate(); err != nil {
			t.Fatalf("odai.levelOffset=-3 は許容されるべき（お題だけやさしくする用途）: %v", err)
		}
	})

	t.Run("cull.warnMaxIds は 0 を許し負値を弾く", func(t *testing.T) {
		// 🔴 **0 を弾いてはいけない。** 本番DBには cull グループが既にあるので、
		// 新設の warnMaxIds は補完されず 0 のまま読まれる（補完はグループ単位）。
		// ここで 0 を弾くと Load 全体が失敗し、**config が丸ごと内蔵デフォルト起動**になる
		// （#124・2026-08-14 と同じ事故）。0 は EffectiveWarnMaxIds が既定 24 に読み替える。
		gp := DefaultParameters()
		gp.Cull.WarnMaxIds = 0
		if err := gp.Validate(); err != nil {
			t.Fatalf("cull.warnMaxIds=0 は許容されるべき（未設定＝既定24として扱う）: %v", err)
		}
		gp.Cull.WarnMaxIds = -1
		if err := gp.Validate(); err == nil {
			t.Fatal("cull.warnMaxIds=-1 はエラーになるべき")
		}
	})
}

// TestGameParameters_IsComparable は GameParameters が `==` 比較可能なままであることを固定する
// （AGENTS.md §1.3）。map / slice のフィールドを足すと**コンパイルエラー**になる。
//
// 比較可能性は設定の差分検出と backfillDefaults（IsZero 判定）の前提。
func TestGameParameters_IsComparable(t *testing.T) {
	a := DefaultParameters()
	b := DefaultParameters()
	if a != b {
		t.Fatal("同じ DefaultParameters が等しくない")
	}
	b.Odai.LevelOffset++
	if a == b {
		t.Fatal("odai を変えたのに等しいと判定された")
	}
	b = a
	b.Cull.WarnMaxIds++
	if a == b {
		t.Fatal("cull.warnMaxIds を変えたのに等しいと判定された")
	}
}

func TestConfigHash(t *testing.T) {
	p := DefaultParameters()
	h := p.ConfigHash()
	if len(h) != 8 {
		t.Errorf("hash length=%d want 8", len(h))
	}
	h2 := p.ConfigHash()
	if h != h2 {
		t.Errorf("hash not deterministic: %s != %s", h, h2)
	}
	p.Score.WeightTakoyaki = 999
	h3 := p.ConfigHash()
	if h == h3 {
		t.Error("hash should differ with changed params")
	}
}

// MatchDurationMs は最終ステージの時刻（＝試合時間の唯一の情報源）を返す。
func TestCullParams_MatchDurationMs(t *testing.T) {
	gp := DefaultParameters()
	want := gp.Cull.Stages[CullStageCount-1].AtMs
	if got := gp.Cull.MatchDurationMs(); got != want {
		t.Fatalf("MatchDurationMs=%d, want %d", got, want)
	}
	if want != 120000 {
		t.Fatalf("既定の試合時間=%dms, want 120000（企画確定値）", want)
	}
}

// 既定の足切りスケジュールが企画確定値どおりであること（plan-h22 §1）。
//
// 20秒等間隔・99→75→55→35→20→10→0。ここを崩すと企画の
// 「どれだけ弱くても20秒は遊べる」「決勝は10人」が壊れる。
func TestDefaultCullSchedule_MatchesSpec(t *testing.T) {
	want := [CullStageCount]CullStage{
		{AtMs: 20000, TargetAliveCount: 75},
		{AtMs: 40000, TargetAliveCount: 55},
		{AtMs: 60000, TargetAliveCount: 35},
		{AtMs: 80000, TargetAliveCount: 20},
		{AtMs: 100000, TargetAliveCount: 10},
		{AtMs: 120000, TargetAliveCount: 0},
	}
	if got := DefaultParameters().Cull.Stages; got != want {
		t.Fatalf("既定の cullSchedule が企画確定値と違う\n got=%+v\nwant=%+v", got, want)
	}
}

// ── Bot の tier（plan-h31）─────────────────────────────────────────

// 既定の tier が「強いほど速く・正確で・難度に強い」という順序になっていること。
//
// 順序が崩れると「弱 tier のほうが上位を占める」ことになり、tier という概念自体が無意味になる。
// 数値は h26/h33 で動かしてよいが、**単調性は仕様**。
func TestDefaultBotTiers_MonotonicByStrength(t *testing.T) {
	tiers := DefaultBotTiers()
	for i := 1; i < BotTierCount; i++ {
		prev, cur := tiers[i-1], tiers[i]
		if cur.MsPerKey <= prev.MsPerKey {
			t.Errorf("tier[%d].msPerKey=%d は tier[%d]=%d より大きい（遅い）必要", i, cur.MsPerKey, i-1, prev.MsPerKey)
		}
		if cur.MissRate <= prev.MissRate {
			t.Errorf("tier[%d].missRate=%v は tier[%d]=%v より大きい必要", i, cur.MissRate, i-1, prev.MissRate)
		}
		if cur.HeatPenalty <= prev.HeatPenalty {
			t.Errorf("tier[%d].heatPenalty=%v は tier[%d]=%v より大きい必要（難度に弱い）", i, cur.HeatPenalty, i-1, prev.HeatPenalty)
		}
	}
	if got := BotTierLabel(BotTierStrong); got != "strong" {
		t.Errorf("BotTierLabel(strong)=%q", got)
	}
	if got := BotTierLabel(BotTierCount); got != "" {
		t.Errorf("未知の tier は空文字であるべき: %q", got)
	}
}

// 🔴 配列のゼロ埋め（config-front から2要素だけ保存された等）を**要素単位**で吸収すること。
//
// これが効かないと「重み0・1打鍵0ms」の tier ができ、当たった Bot が無限に速くなる。
// Validate で弾く方式にすると本番の config が丸ごと巻き戻るので、こちらで吸収する（§4.1）。
func TestEffectiveTiers_FillsZeroFilledElement(t *testing.T) {
	def := DefaultBotTiers()

	bp := BotParams{Tiers: def}
	bp.Tiers[BotTierWeak] = BotTier{} // JSON の要素数不足によるゼロ埋め
	got := bp.EffectiveTiers()
	if got != def {
		t.Fatalf("ゼロ埋めされた1要素だけが既定へ戻るべき\n got=%+v\nwant=%+v", got, def)
	}

	// 一部だけ入っている（重みはあるが msPerKey が無い）ケースも 0ms にしない。
	bp = BotParams{Tiers: def}
	bp.Tiers[BotTierNormal].MsPerKey = 0
	if got := bp.EffectiveTiers()[BotTierNormal].MsPerKey; got != def[BotTierNormal].MsPerKey {
		t.Fatalf("msPerKey=0 が既定へ戻っていない: %d", got)
	}

	// 全 tier の重みが 0 だと抽選できないので、まとめて既定へ戻す。
	bp = BotParams{Tiers: def}
	for i := range bp.Tiers {
		bp.Tiers[i].Weight = 0
	}
	if got := bp.EffectiveTiers(); got != def {
		t.Fatalf("重み合計0が既定へ戻っていない: %+v", got)
	}
}

// 運営が入れた値は既定へ巻き戻さない（読み替えは「ゼロ＝未設定」に限る）。
func TestEffectiveTiers_KeepsOperatorValues(t *testing.T) {
	bp := BotParams{Tiers: DefaultBotTiers(), IndividualSpread: 0.05}
	bp.Tiers[BotTierStrong].MsPerKey = 120
	got := bp.EffectiveTiers()
	if got[BotTierStrong].MsPerKey != 120 {
		t.Fatalf("運営の値が既定で上書きされた: %d", got[BotTierStrong].MsPerKey)
	}
	if s := bp.EffectiveIndividualSpread(); s != 0.05 {
		t.Fatalf("individualSpread が上書きされた: %v", s)
	}
}
