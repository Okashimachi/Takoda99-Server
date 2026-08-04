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

	t.Run("credit.initialLife<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Credit.InitialLife = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("credit.initialLife=0 はエラーになるべき")
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
	p.Credit.InitialLife = 999
	h3 := p.ConfigHash()
	if h == h3 {
		t.Error("hash should differ with changed params")
	}
}
