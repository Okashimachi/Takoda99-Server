// Package db は【インフラ・アダプタ】Postgres 永続化の実体。pgx を用いて
// game.ConfigProvider（設定の取得/保存）と将来の試合結果保存（store.ResultStore, #52）を実装する。
//
// 第三者ドライバ(pgx)をこの層に閉じ込め、純粋な層3部品(config/odai/targeting)を汚さない
// （transport が coder/websocket を閉じ込めるのと同じ役割）。合成ルート(cmd/server)が配線する。
// ライブな試合状態は in-memory のまま。ここが触るのは永続データ（config/結果/統計）だけ。
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool は接続文字列（Render 等では env DATABASE_URL）からプールを作り、疎通確認する。
// 失敗時はエラーを返す（合成ルートはデフォルト設定で起動継続する＝起動を止めない）。
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("db: プール生成: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: 疎通(Ping): %w", err)
	}
	return pool, nil
}
