package db

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"takoda99/internal/odai"
)

// words まわりの実 Postgres 統合テスト。config_test.go と同じ方針で、
// TEST_DATABASE_URL が未設定ならスキップする（CI では DB を用意しないので通常スキップ）。
//
//	TEST_DATABASE_URL='postgres://user:pass@localhost:5432/takoda99?sslmode=disable' \
//	  go test ./internal/db/ -run Words -v
func newWordStoreForTest(t *testing.T) (*WordStore, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL 未設定のためスキップ（実DB統合テスト）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	pool, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	// 各テストを独立させるため作り直す。
	for _, q := range []string{`DROP TABLE IF EXISTS words`, `DROP TABLE IF EXISTS word_seed_version`} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("drop: %v", err)
		}
	}
	return NewWordStore(pool), ctx
}

// Migrate が辞書を全段階ぶん取り込むこと。
// 「空の時だけ seed」だと、後からコード側に足した語が永久に入らない（#86）。
func TestWords_MigrateSeedsAllLevels(t *testing.T) {
	s, ctx := newWordStoreForTest(t)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	all, err := s.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	byLevel := map[int]int{}
	for _, e := range all {
		byLevel[e.Level]++
	}
	for lv := 0; lv <= odai.MaxWordLevel; lv++ {
		if byLevel[lv] == 0 {
			t.Errorf("level %d の語が DB に入っていない", lv)
		}
	}
	if len(all) != len(FallbackEntries()) {
		t.Errorf("語数 %d, want %d", len(all), len(FallbackEntries()))
	}
}

// 2回目の Migrate は何もしない（バージョンが同じ）。
// また、運営が独自に足した語を消さないこと（upsert であって replace ではない）。
func TestWords_MigrateIsIdempotentAndKeepsOperatorWords(t *testing.T) {
	s, ctx := newWordStoreForTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	custom := odai.WordEntry{Text: "うんめー", Reading: "うんめー", Level: 0, Category: "operator"}
	if err := s.SaveAll(ctx, []odai.WordEntry{custom}, "upsert"); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate 2回目: %v", err)
	}

	all, err := s.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	found := false
	for _, e := range all {
		if e.Text == custom.Text {
			found = true
		}
	}
	if !found {
		t.Fatal("運営が足した語が Migrate で消えた（replace になっている）")
	}
	if want := len(FallbackEntries()) + 1; len(all) != want {
		t.Fatalf("語数 %d, want %d（重複投入されている可能性）", len(all), want)
	}
}

func TestWords_UpdatePartial(t *testing.T) {
	s, ctx := newWordStoreForTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	all, _ := s.LoadAll(ctx)
	target := all[0]

	newCat := "changed"
	if err := s.Update(ctx, target.ID, odai.WordPatch{Category: &newCat}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	after, _ := s.LoadAll(ctx)
	for _, e := range after {
		if e.ID != target.ID {
			continue
		}
		if e.Category != newCat {
			t.Fatalf("category = %q, want %q", e.Category, newCat)
		}
		// 指定していないフィールドは据え置き（COALESCE）。
		if e.Text != target.Text || e.Level != target.Level || e.KeystrokeCount != target.KeystrokeCount {
			t.Fatalf("指定していないフィールドが変わった: %+v → %+v", target, e)
		}
		return
	}
	t.Fatal("更新対象が消えた")
}

func TestWords_UpdateNotFound(t *testing.T) {
	s, ctx := newWordStoreForTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	cat := "x"
	err := s.Update(ctx, 999999, odai.WordPatch{Category: &cat})
	if !errors.Is(err, odai.ErrNotFound) {
		t.Fatalf("err = %v, want odai.ErrNotFound", err)
	}
}

// (text, level) を既存と同じ組み合わせに変えたら ErrConflict。
// 生の DB エラーが漏れると呼び出し側が 500 しか返せない。
func TestWords_UpdateConflict(t *testing.T) {
	s, ctx := newWordStoreForTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	all, _ := s.LoadAll(ctx)

	var a, b odai.WordEntry
	for _, e := range all {
		if e.Level == all[0].Level && e.ID != all[0].ID {
			a, b = all[0], e
			break
		}
	}
	if b.ID == 0 {
		t.Skip("同一 level の語が2つ無い")
	}

	err := s.Update(ctx, a.ID, odai.WordPatch{Text: &b.Text})
	if !errors.Is(err, odai.ErrConflict) {
		t.Fatalf("err = %v, want odai.ErrConflict", err)
	}
}

func TestWords_DeleteNotFound(t *testing.T) {
	s, ctx := newWordStoreForTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := s.Delete(ctx, 999999); !errors.Is(err, odai.ErrNotFound) {
		t.Fatalf("err = %v, want odai.ErrNotFound", err)
	}
}
