package game

import "testing"

func TestGameParameters_Validate(t *testing.T) {
	t.Run("デフォルトは妥当", func(t *testing.T) {
		if err := DefaultParameters().Validate(); err != nil {
			t.Fatalf("DefaultParameters は妥当なはず: %v", err)
		}
	})

	t.Run("stack.limit<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Stack.Limit = 0
		if err := gp.Validate(); err == nil {
			t.Fatal("stack.limit=0 はエラーになるべき")
		}
	})

	t.Run("difficulty.maxLevel<=0 を弾く", func(t *testing.T) {
		gp := DefaultParameters()
		gp.Difficulty.MaxLevel = -1
		if err := gp.Validate(); err == nil {
			t.Fatal("difficulty.maxLevel=-1 はエラーになるべき")
		}
	})
}
