package game

import (
	"sync"
	"testing"
)

// noMiss クリアは base + perChar×打鍵数 だけ加算する。
func TestApplyDakenClear_NoMissGain(t *testing.T) {
	gp := DefaultParameters() // base=10, perChar=1
	var p Player

	got := p.ApplyDakenClear(0, 5, gp) // 5打鍵ノーミス → +15
	if got.Delta != 15 || got.Value != 15 || got.Reason != ReasonClear {
		t.Fatalf("1発目: got %+v, want delta=15 value=15 reason=Clear", got)
	}

	got = p.ApplyDakenClear(0, 3, gp) // +13 → 28
	if got.Delta != 13 || got.Value != 28 || got.Reason != ReasonClear {
		t.Fatalf("2発目: got %+v, want delta=13 value=28 reason=Clear", got)
	}
}

// ミスありは加算せず missDecay×missCount だけ減衰する。
func TestApplyDakenClear_MissDecay(t *testing.T) {
	gp := DefaultParameters() // missDecay=3
	p := Player{combo: 20}

	got := p.ApplyDakenClear(2, 5, gp) // ミス2回 → -6、打鍵数は無視
	if got.Delta != -6 || got.Value != 14 || got.Reason != ReasonMiss {
		t.Fatalf("got %+v, want delta=-6 value=14 reason=Miss", got)
	}
}

// 減衰はコンボを負にしない（0クランプ）。
func TestApplyDakenClear_MissClampsAtZero(t *testing.T) {
	gp := DefaultParameters()
	p := Player{combo: 4}

	got := p.ApplyDakenClear(5, 2, gp) // -15 だが 0 でクランプ
	if got.Value != 0 || got.Delta != -4 || got.Reason != ReasonMiss {
		t.Fatalf("got %+v, want value=0 delta=-4 reason=Miss", got)
	}
}

// Enter=全消費。消費前コンボが Delta の絶対値として取れる。
func TestConsumeAllCombo(t *testing.T) {
	var p Player
	p.ApplyDakenClear(0, 5, DefaultParameters()) // 15
	got := p.ConsumeAllCombo()
	if got.Value != 0 || got.Delta != -15 || got.Reason != ReasonConsumed {
		t.Fatalf("got %+v, want value=0 delta=-15 reason=Consumed", got)
	}
	if p.Combo() != 0 {
		t.Fatalf("消費後コンボ=%d, want 0", p.Combo())
	}
}

// 各 Player は独立インスタンスで並行駆動しても干渉しない（per-player 状態の閉じ確認・-race）。
func TestPlayers_IndependentUnderRace(t *testing.T) {
	gp := DefaultParameters()
	const n = 50
	players := make([]*Player, n)
	for i := range players {
		players[i] = &Player{}
	}

	var wg sync.WaitGroup
	for i := range players {
		wg.Add(1)
		go func(p *Player) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				p.ApplyDakenClear(0, 1, gp) // 各回 +11
			}
		}(players[i])
	}
	wg.Wait()

	for i, p := range players {
		if p.Combo() != 100*11 {
			t.Fatalf("player %d: combo=%d, want %d", i, p.Combo(), 100*11)
		}
	}
}
