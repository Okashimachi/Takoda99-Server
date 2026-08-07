package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"takoda99/internal/odai"
)

// uniqueViolation は Postgres の一意制約違反(23505)を判定する。
// words は UNIQUE(text, level) を持つので、更新で既存行と衝突しうる。
func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// WordStore は Postgres の words テーブルを管理する。
type WordStore struct {
	pool *pgxpool.Pool
}

func NewWordStore(pool *pgxpool.Pool) *WordStore { return &WordStore{pool: pool} }

// CurrentSeedVersion はコード側の辞書（internal/odai/data.go）のバージョン。
//
// **辞書に語を足したり段階を増やしたら、この値を上げる。** 上げないと DB へ反映されない。
// 上げると次回の Migrate で upsert が走り、既存の語は (text, level) 一致で更新、
// 新しい語は追加される（運営が独自に足した語は消えない）。
//
//	1: 初版（level 0〜4 の100語）
//	2: level 5〜17 を追加（#75 / 計360語）
const CurrentSeedVersion = 2

// Migrate は words テーブルを作成し、コード側の辞書（data.go）を DB へ取り込む（冪等）。
//
// 「空の時だけ seed」ではない。それだと **後からコード側に足した語が永久に入らない**（#86）。
// 適用済みバージョンを word_seed_version に記録し、CurrentSeedVersion が上回る時だけ
// upsert する。辞書に語を足したら CurrentSeedVersion を上げること。
func (s *WordStore) Migrate(ctx context.Context) error {
	// 1文に1テーブル。複数ステートメントを1回の Exec に混ぜると、pgx が
	// extended protocol を選んだ時に構文エラーになる（引数の有無でプロトコルが変わる）。
	const wordsDDL = `CREATE TABLE IF NOT EXISTS words (
		id              SERIAL PRIMARY KEY,
		text            TEXT NOT NULL,
		reading         TEXT NOT NULL,
		keystroke_count INT NOT NULL,
		level           INT NOT NULL DEFAULT 0,
		category        TEXT NOT NULL DEFAULT 'general',
		UNIQUE(text, level)
	)`
	if _, err := s.pool.Exec(ctx, wordsDDL); err != nil {
		return fmt.Errorf("db: words migrate: %w", err)
	}

	// 適用済みの seed バージョンを1行ずつ記録する（履歴として残す）。
	const versionDDL = `CREATE TABLE IF NOT EXISTS word_seed_version (
		version    INT PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`
	if _, err := s.pool.Exec(ctx, versionDDL); err != nil {
		return fmt.Errorf("db: word_seed_version migrate: %w", err)
	}

	var applied int
	err := s.pool.QueryRow(ctx,
		`SELECT coalesce(max(version), 0) FROM word_seed_version`).Scan(&applied)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("db: words version check: %w", err)
	}
	if applied >= CurrentSeedVersion {
		return nil
	}

	// upsert なので、運営が config-front で編集した語は (text, level) が一致する限り
	// 上書きされるが、**運営が足した語は消えない**（replace と違い DELETE しない）。
	if err := s.saveAll(ctx, FallbackEntries(), "upsert"); err != nil {
		return fmt.Errorf("db: words upsert fallback: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO word_seed_version (version) VALUES ($1) ON CONFLICT (version) DO NOTHING`,
		CurrentSeedVersion); err != nil {
		return fmt.Errorf("db: words version bump: %w", err)
	}
	return nil
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

	batch := &pgx.Batch{}
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
			batch.Queue(upsert, e.Text, e.Reading, ks, e.Level, e.Category)
		} else {
			const ins = `INSERT INTO words (text, reading, keystroke_count, level, category) VALUES ($1, $2, $3, $4, $5)`
			batch.Queue(ins, e.Text, e.Reading, ks, e.Level, e.Category)
		}
	}
	
	br := tx.SendBatch(ctx, batch)
	for i := 0; i < len(entries); i++ {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return fmt.Errorf("db: words batch exec (index %d): %w", i, err)
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("db: words batch close: %w", err)
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
		return odai.ErrNotFound
	}
	return nil
}

// Update は指定IDの単語を部分更新する。
func (s *WordStore) Update(ctx context.Context, id int, p odai.WordPatch) error {
	const q = `UPDATE words SET
		text = COALESCE($1, text),
		reading = COALESCE($2, reading),
		keystroke_count = COALESCE($3, keystroke_count),
		level = COALESCE($4, level),
		category = COALESCE($5, category)
	WHERE id = $6`
	tag, err := s.pool.Exec(ctx, q, p.Text, p.Reading, p.KeystrokeCount, p.Level, p.Category, id)
	if err != nil {
		// (text, level) を別の語と同じ組み合わせに変えようとした場合。
		// 呼び出し側が 409 を返せるよう、DB の生エラーを漏らさず型を揃える。
		if uniqueViolation(err) {
			return odai.ErrConflict
		}
		return fmt.Errorf("db: words update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return odai.ErrNotFound
	}
	return nil
}
