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

// seed v3 が「旧 seed が入れた語」を消すこと（plan-h30 §3.2）。
//
// 🔴 seed は upsert で DELETE しないので、**新語を入れただけでは旧語が DB に残って混ざる**。
// level 17 の 85打鍵の語が生き残ると、辞書を書き直した意味が無くなる。
// 運営が config-front で足した語まで巻き添えにしないことも同時に見る。
func TestWords_MigrateV3RemovesRetiredWords(t *testing.T) {
	s, ctx := newWordStoreForTest(t)

	// v2 までの状態を再現する（旧語が DB に入っていて、適用済みバージョンは 2）。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("テーブル作成のための Migrate: %v", err)
	}
	retired := RetiredEntries()
	if err := s.SaveAll(ctx, retired, "upsert"); err != nil {
		t.Fatalf("旧語の投入: %v", err)
	}
	for _, q := range []string{
		`DELETE FROM word_seed_version`,
		`INSERT INTO word_seed_version (version) VALUES (2)`,
	} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatalf("バージョン巻き戻し: %v", err)
		}
	}
	operator := odai.WordEntry{Text: "うちのみせのあじ", Reading: "うちのみせのあじ", Level: 9, Category: "operator"}
	if err := s.SaveAll(ctx, []odai.WordEntry{operator}, "upsert"); err != nil {
		t.Fatalf("運営語の投入: %v", err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	all, err := s.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	have := map[string]bool{}
	for _, e := range all {
		have[e.Text] = true
	}
	for _, e := range retired {
		if have[e.Text] {
			t.Fatalf("旧語が残っている: %q (level %d)。新旧が混ざって出題される", e.Text, e.Level)
		}
	}
	if !have[operator.Text] {
		t.Fatal("運営が足した語まで消えた（DELETE の対象が広すぎる）")
	}
	if len(all) != len(FallbackEntries())+1 {
		t.Fatalf("語数 %d, want %d（新辞書 + 運営語1）", len(all), len(FallbackEntries())+1)
	}
}

// 戻せること（plan-h30 §3.3）。RestoreRetired で旧語が DB へ戻る。
func TestWords_RestoreRetired(t *testing.T) {
	s, ctx := newWordStoreForTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := s.RestoreRetired(ctx); err != nil {
		t.Fatalf("RestoreRetired: %v", err)
	}
	all, err := s.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	have := map[string]bool{}
	for _, e := range all {
		have[e.Text] = true
	}
	for _, e := range RetiredEntries() {
		if !have[e.Text] {
			t.Fatalf("旧語が戻っていない: %q (level %d)", e.Text, e.Level)
		}
	}
	if want := len(FallbackEntries()) + len(RetiredEntries()); len(all) != want {
		t.Fatalf("語数 %d, want %d（新辞書 + 旧語）", len(all), want)
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
