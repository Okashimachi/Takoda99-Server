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
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS order_attempt CASCADE`); err != nil {
		t.Fatalf("drop order_attempt: %v", err)
	}
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
		// 注文単位の記録（plan-h03）。Bot 行も混ぜて、フィルタは合成ルートの責務で
		// db 層はそのまま入れることを確認する。
		Attempts: []store.OrderAttempt{
			{StoreId: "p-1", CustomerId: "c-1", Attribute: "Normal", HeatLevel: 3,
				OrderCount: 2, Keystrokes: 20, ElapsedMs: 3000, MissCount: 1, IsBot: false},
			{StoreId: "p-1", CustomerId: "c-2", Attribute: "Buzz", HeatLevel: 7,
				OrderCount: 4, Keystrokes: 40, ElapsedMs: 5000, MissCount: 0, IsBot: false},
			{StoreId: "b-9", CustomerId: "c-3", Attribute: "Claimer", HeatLevel: 7,
				OrderCount: 1, Keystrokes: 8, ElapsedMs: 1200, MissCount: 2, IsBot: true},
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

	// 本戦の値（score / takoyaki_count / survived_ms）が保存されているか。
	//
	// ⚠ このアサーションは h21 まで eval_normalized を見ていて、**h21 以降ずっと落ちていた**
	// （相対評価を廃止して書き込みを止めたため）。TEST_DATABASE_URL 未設定だと
	// スキップされるので CI では気づけない。本戦の列に張り替える。
	var survivedMs, score, takoyaki int
	if err := pool.QueryRow(ctx, `
		SELECT survived_ms, score, takoyaki_count
		  FROM store_result WHERE match_id = $1 AND store_id = 'p-1'`, matchId,
	).Scan(&survivedMs, &score, &takoyaki); err != nil {
		t.Fatalf("Select data: %v", err)
	}
	if survivedMs != 120000 {
		t.Errorf("survived_ms = %d, want 120000", survivedMs)
	}
	if score != 12300 {
		t.Errorf("score = %d, want 12300", score)
	}
	if takoyaki != 34 {
		t.Errorf("takoyaki_count = %d, want 34", takoyaki)
	}

	// 廃止した列は DROP せず残しているが、**書かない**（既定値 0 のまま）。
	// 予選の記録を消さないための措置なので、ここが 0 でなくなったら書き戻している。
	var creditLife int
	var evalRaw, evalNormalized float64
	if err := pool.QueryRow(ctx, `
		SELECT credit_life, eval_raw, eval_normalized
		  FROM store_result WHERE match_id = $1 AND store_id = 'p-1'`, matchId,
	).Scan(&creditLife, &evalRaw, &evalNormalized); err != nil {
		t.Fatalf("Select retired columns: %v", err)
	}
	if creditLife != 0 || evalRaw != 0 || evalNormalized != 0 {
		t.Errorf("廃止列に値が書かれている: credit_life=%d eval_raw=%v eval_normalized=%v",
			creditLife, evalRaw, evalNormalized)
	}

	// 注文単位の記録も同じトランザクションで入っている（plan-h03）。
	var attempts int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM order_attempt WHERE match_id = $1`, matchId).Scan(&attempts); err != nil {
		t.Fatalf("count order_attempt: %v", err)
	}
	if attempts != 3 {
		t.Errorf("order_attempt = %d件, want 3", attempts)
	}
}

// TestResultStore_SaveOrderAttempts は order_attempt への実INSERTを検証する（plan-h03）。
//
// store_result と**同一トランザクション**で入ることも同時に見ている
// （別トランザクションだと「試合結果はあるのに注文が無い」半端な状態が残りうる）。
func TestResultStore_SaveOrderAttempts(t *testing.T) {
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

	for _, q := range []string{
		`DROP TABLE IF EXISTS order_attempt CASCADE`,
		`DROP TABLE IF EXISTS store_result CASCADE`,
		`DROP TABLE IF EXISTS match CASCADE`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	rs := NewResultStore(pool)
	if err := rs.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	matchId := "test-attempts-1"
	m := store.MatchResult{
		MatchId: matchId, DurationMs: 120000, HumanCount: 1, WinnerId: "p-1", ConfigHash: "h",
		Results: []store.Result{{StoreId: "p-1", DisplayName: "P", FinalRank: 1}},
		Attempts: []store.OrderAttempt{
			{StoreId: "p-1", CustomerId: "c-1", Attribute: "Normal", HeatLevel: 3,
				OrderCount: 2, Keystrokes: 20, ElapsedMs: 3000, MissCount: 1},
			{StoreId: "p-1", CustomerId: "c-2", Attribute: "Buzz", HeatLevel: 7,
				OrderCount: 4, Keystrokes: 40, ElapsedMs: 5000, MissCount: 0},
			{StoreId: "b-9", CustomerId: "c-3", Attribute: "Claimer", HeatLevel: 7,
				OrderCount: 1, Keystrokes: 8, ElapsedMs: 1200, MissCount: 2, IsBot: true},
		},
	}
	if err := rs.SaveMatch(ctx, m); err != nil {
		t.Fatalf("SaveMatch: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM order_attempt WHERE match_id = $1`, matchId).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Fatalf("order_attempt の件数=%d, want 3", n)
	}

	// 中身が列を取り違えずに入っているか（1行を全項目で照合）。
	var (
		storeId, customerId, attribute   string
		heat, orderCount, keys, ms, miss int
		isBot                            bool
	)
	if err := pool.QueryRow(ctx, `
		SELECT store_id, customer_id, attribute, heat_level, order_count,
		       keystrokes, elapsed_ms, miss_count, is_bot
		  FROM order_attempt WHERE match_id = $1 AND customer_id = 'c-2'`, matchId,
	).Scan(&storeId, &customerId, &attribute, &heat, &orderCount, &keys, &ms, &miss, &isBot); err != nil {
		t.Fatalf("select: %v", err)
	}
	if storeId != "p-1" || attribute != "Buzz" || heat != 7 || orderCount != 4 ||
		keys != 40 || ms != 5000 || miss != 0 || isBot {
		t.Fatalf("列の取り違え: store=%s attr=%s heat=%d order=%d keys=%d ms=%d miss=%d bot=%v",
			storeId, attribute, heat, orderCount, keys, ms, miss, isBot)
	}

	// h04 は「heat 別・人間のみ」で引くので、その絞り込みが効くこと。
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM order_attempt WHERE match_id = $1 AND heat_level = 7 AND is_bot = false`,
		matchId).Scan(&n); err != nil {
		t.Fatalf("count by heat: %v", err)
	}
	if n != 1 {
		t.Fatalf("heat=7 かつ人間の件数=%d, want 1", n)
	}
}
