package odai

import (
	"math/rand"
	"testing"
)

func rng(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// Next はプレースホルダ辞書から有効な語を返す（Text 非空・打鍵数正）。
func TestStaticPool_Next(t *testing.T) {
	p := NewStaticPool()
	w := p.Next(2, rng(1))
	if w.Text == "" || w.KeystrokeCount <= 0 {
		t.Fatalf("got %+v, want 有効な語", w)
	}
}

// データの無い高段階を要求しても、下位段階へフォールバックして有効な語を返す。
func TestStaticPool_Next_FallsBackForSparseLevel(t *testing.T) {
	p := NewStaticPool()
	w := p.Next(10, rng(1)) // 現状10段階目のデータは未整備
	if w.Text == "" || w.KeystrokeCount <= 0 {
		t.Fatalf("got %+v, want フォールバックで有効な語", w)
	}
}

// NextTrap は煽り長文を返す。
func TestStaticPool_NextTrap(t *testing.T) {
	p := NewStaticPool()
	w := p.NextTrap(rng(1))
	if w.Text == "" || w.KeystrokeCount <= 0 {
		t.Fatalf("got %+v, want 有効なトラップ語", w)
	}
}
