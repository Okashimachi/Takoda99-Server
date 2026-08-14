package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
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
		match_id         TEXT NOT NULL REFERENCES match(id),
		store_id         TEXT NOT NULL,
		display_name     TEXT NOT NULL DEFAULT '',
		final_rank       INT NOT NULL,
		elimination      TEXT NOT NULL DEFAULT '',
		-- 本戦（plan-h21）で順位を決める値。credit_life / eval_raw / eval_normalized は
		-- 書かなくなったが、DROP はしない（予選の記録が入っており、消すと戻せない）。
		score            INT NOT NULL DEFAULT 0,
		takoyaki_count   INT NOT NULL DEFAULT 0,
		credit_life      INT NOT NULL DEFAULT 0,
		eval_raw         FLOAT NOT NULL DEFAULT 0,
		eval_normalized  FLOAT NOT NULL DEFAULT 0,
		served_count     INT NOT NULL DEFAULT 0,
		avg_accuracy     FLOAT NOT NULL DEFAULT 0,
		avg_elapsed_ms   INT NOT NULL DEFAULT 0,
		is_bot           BOOLEAN NOT NULL DEFAULT FALSE,
		survived_ms      INT NOT NULL DEFAULT 0,
		left_count       INT NOT NULL DEFAULT 0,
		total_keystrokes INT NOT NULL DEFAULT 0,
		total_misses     INT NOT NULL DEFAULT 0,
		fastest_ms       INT NOT NULL DEFAULT 0,
		slowest_ms       INT NOT NULL DEFAULT 0,
		normal_served    INT NOT NULL DEFAULT 0,
		normal_left      INT NOT NULL DEFAULT 0,
		bonus_served     INT NOT NULL DEFAULT 0,
		bonus_left       INT NOT NULL DEFAULT 0,
		claimer_served   INT NOT NULL DEFAULT 0,
		claimer_left     INT NOT NULL DEFAULT 0,
		buzz_served      INT NOT NULL DEFAULT 0,
		buzz_left        INT NOT NULL DEFAULT 0,
		PRIMARY KEY (match_id, store_id)
	);

	-- 既存テーブルへの後方互換マイグレーション。
	-- CREATE TABLE IF NOT EXISTS は「テーブルが既に在れば何もしない」ので、予選時点で
	-- 旧スキーマの store_result を持つ本番DBには新カラムが足されず、SaveMatch の INSERT が
	-- 実行時に「column does not exist」で失敗する（best-effort保存なので無言で全件保存漏れになる）。
	-- ADD COLUMN IF NOT EXISTS で新旧どちらのDBでも冪等に揃える。
	ALTER TABLE store_result
		ADD COLUMN IF NOT EXISTS score            INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS takoyaki_count   INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS eval_normalized  FLOAT NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS survived_ms      INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS left_count       INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS total_keystrokes INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS total_misses     INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS fastest_ms       INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS slowest_ms       INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS normal_served    INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS normal_left      INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS bonus_served     INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS bonus_left       INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS claimer_served   INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS claimer_left     INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS buzz_served      INT   NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS buzz_left        INT   NOT NULL DEFAULT 0;

	-- 注文単位の記録（plan-h03）。store_result より1段細かい粒度で、
	-- 「1注文をどう捌いたか」を heat レベル別に残す。BOT強化（h04/h05）の燃料。
	--
	-- store_result とは粒度が違うだけで出所は同じ（session の per-order 値）。
	-- 両者は積み上がるので衝突しない。
	CREATE TABLE IF NOT EXISTS order_attempt (
		id            BIGSERIAL PRIMARY KEY,
		match_id      TEXT NOT NULL REFERENCES match(id),
		store_id      TEXT NOT NULL,
		customer_id   TEXT NOT NULL,
		attribute     TEXT NOT NULL,
		heat_level    INT  NOT NULL,
		order_count   INT  NOT NULL,
		keystrokes    INT  NOT NULL,
		elapsed_ms    INT  NOT NULL,
		miss_count    INT  NOT NULL,
		is_bot        BOOLEAN NOT NULL DEFAULT FALSE,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	-- h04 は「heat 別・人間のみ」で分位を取るので、その2列で引けるようにする。
	CREATE INDEX IF NOT EXISTS order_attempt_heat_bot_idx ON order_attempt (heat_level, is_bot);`
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
		if r.IsBot {
			continue
		}
		_, err = tx.Exec(ctx,
			// credit_life / eval_raw / eval_normalized は本戦で書かない（列は DEFAULT 0 で残る）。
			`INSERT INTO store_result
			 (match_id, store_id, display_name, final_rank, elimination,
			  score, takoyaki_count, served_count, avg_accuracy, avg_elapsed_ms, is_bot,
			  survived_ms, left_count, total_keystrokes, total_misses, fastest_ms, slowest_ms,
			  normal_served, normal_left, bonus_served, bonus_left, claimer_served, claimer_left, buzz_served, buzz_left)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)`,
			m.MatchId, r.StoreId, r.DisplayName, r.FinalRank, r.Elimination,
			r.Score, r.TakoyakiCount, r.ServedCount, r.AvgAccuracy, r.AvgElapsedMs, r.IsBot,
			r.SurvivedMs, r.LeftCount, r.TotalKeystrokes, r.TotalMisses, r.FastestMs, r.SlowestMs,
			r.NormalServed, r.NormalLeft, r.BonusServed, r.BonusLeft, r.ClaimerServed, r.ClaimerLeft, r.BuzzServed, r.BuzzLeft,
		)
		if err != nil {
			return fmt.Errorf("db: insert store_result %s: %w", r.StoreId, err)
		}
	}

	if err := insertAttempts(ctx, tx, m); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// insertAttempts は注文単位の記録を一括INSERTする（plan-h03）。
//
// **store_result と同じトランザクション**で入れる。別トランザクションにすると
// 「試合結果はあるのに注文が無い（またはその逆）」という半端な状態が残りうる。
//
// tick 中には一切 INSERT しない（99店×数十注文で DB が詰まる）。session がメモリに
// 溜めたものを、試合終了時にここでまとめて流す。
func insertAttempts(ctx context.Context, tx pgx.Tx, m store.MatchResult) error {
	if len(m.Attempts) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(m.Attempts))
	for _, a := range m.Attempts {
		rows = append(rows, []any{
			m.MatchId, a.StoreId, a.CustomerId, a.Attribute, a.HeatLevel,
			a.OrderCount, a.Keystrokes, a.ElapsedMs, a.MissCount, a.IsBot,
		})
	}
	_, err := tx.CopyFrom(ctx,
		pgx.Identifier{"order_attempt"},
		[]string{"match_id", "store_id", "customer_id", "attribute", "heat_level",
			"order_count", "keystrokes", "elapsed_ms", "miss_count", "is_bot"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("db: insert order_attempt (%d件): %w", len(rows), err)
	}
	return nil
}

var _ store.ResultStore = (*ResultStore)(nil)
