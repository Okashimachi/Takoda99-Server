package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"takoda99/internal/game"
)

// ConfigStore は Postgres に GameParameters を単一行(JSONB)で保持し、game.ConfigProvider を実装する。
// 併せて config-front からの保存(Save, #50) にも使う。設定は小さな単一オブジェクトなので
// 正規化せず JSONB 1行で持つ（GameParameters をそのまま marshal/unmarshal でき、検証も struct 単位）。
type ConfigStore struct {
	pool     *pgxpool.Pool
	mu       sync.RWMutex
	lastGood game.GameParameters
	cachedAt time.Time
}

const cacheTTL = 2 * time.Second

// NewConfigStore は ConfigStore を作る。
func NewConfigStore(pool *pgxpool.Pool) *ConfigStore { return &ConfigStore{pool: pool} }

// Migrate は game_config テーブルを作成し、空なら内蔵デフォルトで seed する（冪等）。
// 起動時に1回呼ぶ想定。既存レコードがあれば seed はスキップ（運用中の値を壊さない）。
func (s *ConfigStore) Migrate(ctx context.Context) error {
	const ddl = `CREATE TABLE IF NOT EXISTS game_config (
		id         int PRIMARY KEY DEFAULT 1,
		params     jsonb NOT NULL,
		updated_at timestamptz NOT NULL DEFAULT now(),
		CONSTRAINT game_config_singleton CHECK (id = 1)
	)`
	if _, err := s.pool.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	data, err := json.Marshal(game.DefaultParameters())
	if err != nil {
		return fmt.Errorf("db: seed marshal: %w", err)
	}
	const seed = `INSERT INTO game_config (id, params) VALUES (1, $1)
		ON CONFLICT (id) DO NOTHING`
	if _, err := s.pool.Exec(ctx, seed, data); err != nil {
		return fmt.Errorf("db: seed: %w", err)
	}
	return nil
}

// Load は現在の GameParameters を返す。取得・デコード・検証のいずれかに失敗しても
// 内蔵デフォルト＋理由err を返す（＝第1返り値は常に有効。決定E / 04仕様7章）。
func (s *ConfigStore) Load(ctx context.Context) (game.GameParameters, error) {
	s.mu.RLock()
	if !s.cachedAt.IsZero() && time.Since(s.cachedAt) < cacheTTL {
		cached := s.lastGood
		s.mu.RUnlock()
		return cached, nil
	}
	s.mu.RUnlock()

	def := game.DefaultParameters()
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT params FROM game_config WHERE id = 1`).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return def, fmt.Errorf("db: config 行が無い（Migrate未実行?）")
	}
	if err != nil {
		s.mu.RLock()
		if s.lastGood.Session.TickIntervalMs != 0 {
			lg := s.lastGood
			s.mu.RUnlock()
			return lg, fmt.Errorf("db: config 取得エラー、前回成功値(キャッシュ)を返します: %w", err)
		}
		s.mu.RUnlock()
		return def, fmt.Errorf("db: config 取得: %w", err)
	}
	var gp game.GameParameters
	if err := json.Unmarshal(data, &gp); err != nil {
		s.mu.RLock()
		if s.lastGood.Session.TickIntervalMs != 0 {
			lg := s.lastGood
			s.mu.RUnlock()
			return lg, fmt.Errorf("db: config デコードエラー、前回成功値(キャッシュ)を返します: %w", err)
		}
		s.mu.RUnlock()
		return def, fmt.Errorf("db: config デコード: %w", err)
	}
	backfillDefaults(&gp, def)
	backfillNewFields(&gp, def)
	if err := gp.Validate(); err != nil {
		s.mu.RLock()
		if s.lastGood.Session.TickIntervalMs != 0 {
			lg := s.lastGood
			s.mu.RUnlock()
			return lg, fmt.Errorf("db: config 検証エラー、前回成功値(キャッシュ)を返します: %w", err)
		}
		s.mu.RUnlock()
		return def, fmt.Errorf("db: config 検証: %w", err)
	}
	s.mu.Lock()
	s.lastGood = gp
	s.cachedAt = time.Now()
	s.mu.Unlock()
	return gp, nil
}

// Save は config-front(#50) からの保存。検証してから UPSERT する。検証失敗時は保存せず err。
func (s *ConfigStore) Save(ctx context.Context, gp game.GameParameters) error {
	if err := gp.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(gp)
	if err != nil {
		return fmt.Errorf("db: save marshal: %w", err)
	}
	const up = `INSERT INTO game_config (id, params, updated_at) VALUES (1, $1, now())
		ON CONFLICT (id) DO UPDATE SET params = EXCLUDED.params, updated_at = now()`
	if _, err := s.pool.Exec(ctx, up, data); err != nil {
		return fmt.Errorf("db: save: %w", err)
	}

	s.mu.Lock()
	s.lastGood = gp
	s.cachedAt = time.Now()
	s.mu.Unlock()

	return nil
}

// backfillDefaults は DB に無いグループを内蔵デフォルトで補完する。
//
// **なぜ要るか**: DB の JSON に無いキーは unmarshal でゼロ値のまま残る。そのゼロ値は
// Validate を通らない（例: `publish.evaluationIntervalMs = 0`）ので Load 全体が失敗し、
// **config が丸ごと内蔵デフォルト起動になる**。ログを見ていないと気付けないうえ、
// 「config-front から変えても何も起きない」という形で当日に効く。
//
// 🔴 **列挙で書かない。** 以前はグループを1つずつ `if gp.X == (game.XParams{})` と
// 並べていたが、**h23 で Publish を足したときに追記を忘れて本番が実際に壊れた**
// （2026-08-14・本戦10日前）。h21 で Score/Sanity を足したときは気付けたのに、
// 次に足したグループで同じ穴に落ちている。「気をつける」では再発する。
//
// そこでリフレクションで **GameParameters の全フィールドを機械的に走査**する。
// これなら**新しいグループを足しても自動で対象になる**（忘れようがない）。
// GameParameters の全フィールドは `*Params` 構造体で `==` 比較可能（AGENTS.md §1.3）なので
// `IsZero()` がそのまま「DBに無かった」の判定になる。
//
// 起動時に1回しか走らないのでリフレクションのコストは無視できる。
//
// ⚠ 補完はグループ単位。グループが**部分的に**入っている JSON（一部キーだけ欠ける）は
// 補完されない。config-front は常にフルオブジェクトを送るので実運用では起きないが、
// 手で DB を編集するときは1グループを丸ごと入れるか丸ごと消すこと。
//
// ⚠ 廃止キー（credit / patience / eval / distribution.weightFloor / storm）は DB に残り続ける。
// JSON の未知キーは無視されるので害は無いが、config-front の UI からは消すこと（h24 で対応済み）。
func backfillDefaults(gp *game.GameParameters, def game.GameParameters) {
	dst := reflect.ValueOf(gp).Elem()
	src := reflect.ValueOf(def)
	for i := 0; i < dst.NumField(); i++ {
		f := dst.Field(i)
		if !f.CanSet() {
			continue // 非公開フィールド（現状は無いが、足されても壊さない）
		}
		if f.IsZero() {
			f.Set(src.Field(i))
		}
	}
}

// backfillNewFields は「**既存グループに後から足した**フィールド」を個別に補完する
// （plan-h35 §7.3）。
//
// 🔴 **backfillDefaults（グループ単位）では拾えない穴を埋めるためにある。**
// あちらは「グループ全体がゼロのときだけ既定値を入れる」ので、本番DBに既にある
// グループへ新しいキーを足すと、そのキーは**ゼロのまま**読まれる。
// 実際 2026-08-16 に heat.perElapsedSec で事故っている（新設キーがDBに無く、
// 管理画面側の古い既定値で補完されて保存された）。
//
// ⚠ **ゼロが意味を持つ値をここに書かないこと。** ここに書いた瞬間、
// 運営が意図して 0 にした設定を既定へ巻き戻すことになる。
// 対象は「ゼロだと機能が壊れる／ゼロに意味がないフィールド」だけ。
//
//	cull.warnMaxIds … 0 だと足切り予告のIDが1件も送られず、右パネルが空になる。
//	                  0=未設定として既定 24 に戻す（game 側の EffectiveWarnMaxIds と同じ扱い）。
//	bot.tiers       … 0 だと「1打鍵0ms・重み0」の tier ができて Bot が壊れる（plan-h31 §4.1）。
//	bot.individualSpread … 0 だと個体差が消え、99体が同じ強さに戻る（h31 の目的そのもの）。
//
// ⚠ heat.perElapsedSec は**あえて入れていない**。0 は「時間で難度を上げない」という
// 妥当な（riskWarnings で警告済みの）設定であり、運営の意図を潰す側の害が大きい。
func backfillNewFields(gp *game.GameParameters, def game.GameParameters) {
	if gp.Cull.WarnMaxIds <= 0 {
		gp.Cull.WarnMaxIds = def.Cull.WarnMaxIds
	}
	// 🔴 Bot の tier は本番DBの `bot` グループ（旧スキーマ）に存在しない＝ゼロのまま読まれる。
	// 補正のロジックは game 側（EffectiveTiers）に1本化してあるので、ここはそれを当てるだけ。
	// **game 側にも同じ防御がある**のは意図的な二重化で、DB を経由しない sim・テスト・
	// DB無し起動でも安全にするため（cull.warnMaxIds と同じ形）。
	gp.Bot.Tiers = gp.Bot.EffectiveTiers()
	gp.Bot.IndividualSpread = gp.Bot.EffectiveIndividualSpread()
}

// コンパイル時に game.ConfigProvider 充足を保証する。
var _ game.ConfigProvider = (*ConfigStore)(nil)
