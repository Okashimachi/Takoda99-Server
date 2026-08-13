package db

import (
	"context"
	"os"
	"testing"
	"time"

	"takoda99/internal/store"
)

// TestResultStore_SaveMatch は実 Postgres に対する統合テスト。
// TEST_DATABASE_URL が未設定ならスキップする。
// Supabaseなどでのローカル実行例:
//
//	TEST_DATABASE_URL='postgres://postgres:postgres@localhost:54322/postgres' go test ./internal/db/ -run SaveMatch -v
func TestResultStore_SaveMatch(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL 未設定のためスキップ（実DB統合テスト）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	// テーブル初期化（クリーンな状態にするため）
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS store_result CASCADE`); err != nil {
		t.Fatalf("drop store_result: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS match CASCADE`); err != nil {
		t.Fatalf("drop match: %v", err)
	}

	rs := NewResultStore(pool)

	if err := rs.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	matchId := "test-match-1"
	m := store.MatchResult{
		MatchId:    matchId,
		DurationMs: 120000,
		HumanCount: 1,
		BotCount:   0,
		WinnerId:   "p-1",
		ConfigHash: "dummy-hash",
		Results: []store.Result{
			{
				StoreId:         "p-1",
				DisplayName:     "TestPlayer",
				FinalRank:       1,
				Elimination:     "",
				Score:           12300,
				TakoyakiCount:   34,
				ServedCount:     10,
				AvgAccuracy:     0.98,
				AvgElapsedMs:    1500,
				IsBot:           false,
				SurvivedMs:      120000,
				LeftCount:       0,
				TotalKeystrokes: 150,
				TotalMisses:     3,
				FastestMs:       800,
				SlowestMs:       2000,
				NormalServed:    5,
				NormalLeft:      0,
				BonusServed:     2,
				BonusLeft:       1,
				ClaimerServed:   3,
				ClaimerLeft:     0,
				BuzzServed:      0,
				BuzzLeft:        0,
			},
		},
	}

	if err := rs.SaveMatch(ctx, m); err != nil {
		t.Fatalf("SaveMatch: %v", err)
	}

	// ちゃんと入っているか検証
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM store_result WHERE match_id = $1`, matchId).Scan(&count); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 result row, got %d", count)
	}

	// SurvivedMs や他データが正しく保存されているか
	var survivedMs int
	var evalNormalized float64
	if err := pool.QueryRow(ctx, `SELECT survived_ms, eval_normalized FROM store_result WHERE match_id = $1 AND store_id = 'p-1'`, matchId).Scan(&survivedMs, &evalNormalized); err != nil {
		t.Fatalf("Select data: %v", err)
	}
	if survivedMs != 120000 {
		t.Errorf("Expected survived_ms 120000, got %d", survivedMs)
	}
	if evalNormalized != 1.0 {
		t.Errorf("Expected eval_normalized 1.0, got %f", evalNormalized)
	}
}
