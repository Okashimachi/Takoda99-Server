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

	t.Run("bot.serveIntervalMs<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Bot.ServeIntervalMs = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("bot.serveIntervalMs=0 はエラーになるべき")
		}
	})

	t.Run("bot.missRate が範囲外を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Bot.MissRate = 1.5
		if err := gp.Validate(); err == nil {
			t.Fatal("bot.missRate=1.5 はエラーになるべき")
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
