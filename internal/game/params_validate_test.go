package game

import "testing"

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

	t.Run("bot.baseElapsedMs<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Bot.BaseElapsedMs = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("bot.baseElapsedMs=0 はエラーになるべき")
		}
	})

	t.Run("bot.baseAccuracy が範囲外を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Bot.BaseAccuracy = 1.5
		if err := gp.Validate(); err == nil {
			t.Fatal("bot.baseAccuracy=1.5 はエラーになるべき")
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

	t.Run("customer.*.orderCount<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Customer.Buzz.OrderCount = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("customer.buzz.orderCount=0 はエラーになるべき")
		}
	})
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
