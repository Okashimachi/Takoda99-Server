package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"takoda99/internal/store"
)

// ResultStore は Postgres に試合結果を書き込む。
type ResultStore struct {
	pool *pgxpool.Pool
}

func NewResultStore(pool *pgxpool.Pool) *ResultStore {
	return &ResultStore{pool: pool}
}

// Migrate は match + store_result テーブルを作成する（冪等）。
func (rs *ResultStore) Migrate(ctx context.Context) error {
	const ddl = `
	CREATE TABLE IF NOT EXISTS match (
		id            TEXT PRIMARY KEY,
		started_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		finished_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		duration_ms   INT NOT NULL,
		human_count   INT NOT NULL,
		bot_count     INT NOT NULL,
		winner_id     TEXT,
		config_hash   TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS store_result (
		match_id       TEXT NOT NULL REFERENCES match(id),
		store_id       TEXT NOT NULL,
		display_name   TEXT NOT NULL DEFAULT '',
		final_rank     INT NOT NULL,
		elimination    TEXT NOT NULL DEFAULT '',
		credit_life    INT NOT NULL DEFAULT 0,
		eval_raw       FLOAT NOT NULL DEFAULT 0,
		served_count   INT NOT NULL DEFAULT 0,
		avg_accuracy   FLOAT NOT NULL DEFAULT 0,
		avg_elapsed_ms INT NOT NULL DEFAULT 0,
		is_bot         BOOLEAN NOT NULL DEFAULT FALSE,
		PRIMARY KEY (match_id, store_id)
	);`
	_, err := rs.pool.Exec(ctx, ddl)
	if err != nil {
		return fmt.Errorf("db: result migrate: %w", err)
	}
	return nil
}

// SaveMatch は1試合の結果をトランザクションで保存する。
func (rs *ResultStore) SaveMatch(ctx context.Context, m store.MatchResult) error {
	tx, err := rs.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: result begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx,
		`INSERT INTO match (id, duration_ms, human_count, bot_count, winner_id, config_hash)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		m.MatchId, m.DurationMs, m.HumanCount, m.BotCount, m.WinnerId, m.ConfigHash,
	)
	if err != nil {
		return fmt.Errorf("db: insert match: %w", err)
	}

	for _, r := range m.Results {
		_, err = tx.Exec(ctx,
			`INSERT INTO store_result
			 (match_id, store_id, display_name, final_rank, elimination,
			  credit_life, eval_raw, served_count, avg_accuracy, avg_elapsed_ms, is_bot)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			m.MatchId, r.StoreId, r.DisplayName, r.FinalRank, r.Elimination,
			r.CreditLife, r.EvalRaw, r.ServedCount, r.AvgAccuracy, r.AvgElapsedMs, r.IsBot,
		)
		if err != nil {
			return fmt.Errorf("db: insert store_result %s: %w", r.StoreId, err)
		}
	}

	return tx.Commit(ctx)
}

var _ store.ResultStore = (*ResultStore)(nil)
