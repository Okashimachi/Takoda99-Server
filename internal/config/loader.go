// Package config は【層3・部品】GameParameters をリモートコンフィグから起動時取得する。
//
// interface(ConfigProvider) と GameParameters 型はコア game が所有する（game/ports.go, DIP）。
// ここは game.ConfigProvider を実装するだけ。依存は game/stdlib のみ（depguard で機械強制）。
// 取得の成否に関わらず、サーバーは必ず使用可能な GameParameters で起動する（04仕様7章）。
package config

import (
	"context"

	"takoda99/internal/game"
)

// DefaultLoader は常に内蔵デフォルトを返す。--mode solo やオフライン開発、テストで使う。
type DefaultLoader struct{}

// Load は game.DefaultParameters() を返す（err は常に nil）。
func (DefaultLoader) Load(context.Context) (game.GameParameters, error) {
	return game.DefaultParameters(), nil
}

// コンパイル時に game.ConfigProvider 充足を保証する。
var _ game.ConfigProvider = DefaultLoader{}
