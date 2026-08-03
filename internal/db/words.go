package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"takoda99/internal/odai"
)

// WordStore は Postgres の words テーブルを管理する。
type WordStore struct {
	pool *pgxpool.Pool
}

func NewWordStore(pool *pgxpool.Pool) *WordStore { return &WordStore{pool: pool} }

// Migrate は words テーブルを作成し、空なら data.go のフォールバック語彙で seed する（冪等）。
func (s *WordStore) Migrate(ctx context.Context) error {
	const ddl = `CREATE TABLE IF NOT EXISTS words (
		id              SERIAL PRIMARY KEY,
		text            TEXT NOT NULL,
		reading         TEXT NOT NULL,
		keystroke_count INT NOT NULL,
		level           INT NOT NULL DEFAULT 0,
		category        TEXT NOT NULL DEFAULT 'general',
		UNIQUE(text, level)
	)`
	if _, err := s.pool.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("db: words migrate: %w", err)
	}

	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM words`).Scan(&count); err != nil {
		return fmt.Errorf("db: words count: %w", err)
	}
	if count > 0 {
		return nil
	}
	return s.seedFallback(ctx)
}

func (s *WordStore) seedFallback(ctx context.Context) error {
	entries := FallbackEntries()
	return s.saveAll(ctx, entries, "replace")
}

// FallbackEntries は data.go の語彙を WordEntry として返す（seed 用）。
func FallbackEntries() []odai.WordEntry {
	return odai.BuildFallbackEntries()
}

// LoadAll は全単語を返す。
func (s *WordStore) LoadAll(ctx context.Context) ([]odai.WordEntry, error) {
	return s.loadFiltered(ctx, "", 0, false)
}

// LoadFiltered はフィルタ付きで単語を返す。
func (s *WordStore) LoadFiltered(ctx context.Context, category string, level int, hasLevel bool) ([]odai.WordEntry, error) {
	return s.loadFiltered(ctx, category, level, hasLevel)
}

func (s *WordStore) loadFiltered(ctx context.Context, category string, level int, hasLevel bool) ([]odai.WordEntry, error) {
	q := `SELECT id, text, reading, keystroke_count, level, category FROM words WHERE 1=1`
	args := []any{}
	i := 1
	if category != "" {
		q += fmt.Sprintf(` AND category = $%d`, i)
		args = append(args, category)
		i++
	}
	if hasLevel {
		q += fmt.Sprintf(` AND level = $%d`, i)
		args = append(args, level)
	}
	q += ` ORDER BY level, id`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: words load: %w", err)
	}
	defer rows.Close()

	var entries []odai.WordEntry
	for rows.Next() {
		var e odai.WordEntry
		if err := rows.Scan(&e.ID, &e.Text, &e.Reading, &e.KeystrokeCount, &e.Level, &e.Category); err != nil {
			return nil, fmt.Errorf("db: words scan: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// SaveAll は単語リストを保存する。mode は "replace"（全削除→挿入）または "upsert"。
func (s *WordStore) SaveAll(ctx context.Context, entries []odai.WordEntry, mode string) error {
	return s.saveAll(ctx, entries, mode)
}

func (s *WordStore) saveAll(ctx context.Context, entries []odai.WordEntry, mode string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: words tx begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if mode == "replace" {
		if _, err := tx.Exec(ctx, `DELETE FROM words`); err != nil {
			return fmt.Errorf("db: words truncate: %w", err)
		}
	}

	for _, e := range entries {
		ks := e.KeystrokeCount
		if ks == 0 {
			ks = odai.Keystrokes(e.Reading)
		}
		if mode == "upsert" {
			const upsert = `INSERT INTO words (text, reading, keystroke_count, level, category)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (text, level) DO UPDATE SET
					reading = EXCLUDED.reading,
					keystroke_count = EXCLUDED.keystroke_count,
					category = EXCLUDED.category`
			if _, err := tx.Exec(ctx, upsert, e.Text, e.Reading, ks, e.Level, e.Category); err != nil {
				return fmt.Errorf("db: words upsert: %w", err)
			}
		} else {
			const ins = `INSERT INTO words (text, reading, keystroke_count, level, category) VALUES ($1, $2, $3, $4, $5)`
			if _, err := tx.Exec(ctx, ins, e.Text, e.Reading, ks, e.Level, e.Category); err != nil {
				return fmt.Errorf("db: words insert: %w", err)
			}
		}
	}
	return tx.Commit(ctx)
}

// Delete は指定IDの単語を削除する。
func (s *WordStore) Delete(ctx context.Context, id int) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM words WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("db: words delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("db: word id=%d not found", id)
	}
	return nil
}
