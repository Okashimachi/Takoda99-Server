package config

import (
	"context"
	"errors"
	"net/http"
	"time"

	"textro99/internal/game"
)

// RemoteLoader はリモートの設定エンドポイント（HTTP で GameParameters の JSON を返すもの）から取得する。
// 取得元の方式は未定：GAS/スプレッドシートでも、簡易 Web アプリでも、静的 JSON でもよい（ここでは決め打ちしない）。
//
// ┌─────────────────────────────────────────────────────────────────┐
// │ 【後輩の実装対象】この struct の Load を完成させる。詳細は担当 issue を読むこと。 │
// │ 触ってよいのは internal/config/ 配下のみ。internal/game は読み取りのみ（編集不可）。│
// └─────────────────────────────────────────────────────────────────┘
//
// 現状は未実装のスタブで、常に内蔵デフォルト＋sentinel err を返す（ビルド・起動を止めないため）。
type RemoteLoader struct {
	// URL はリモートコンフィグのエンドポイント（GameParameters を JSON で返す）。
	URL string
	// Client はHTTPクライアント。未指定なら DefaultHTTPClient を使う。テストで差し替え可能にする。
	Client *http.Client
}

// ErrNotImplemented はスタブが返す sentinel。後輩が Load を実装したら参照ごと削除してよい。
var ErrNotImplemented = errors.New("config: RemoteLoader.Load is not implemented yet")

// DefaultHTTPClient はタイムアウト付きの既定クライアント。
var DefaultHTTPClient = &http.Client{Timeout: 5 * time.Second}

// NewRemoteLoader は URL を指定して RemoteLoader を作る。
func NewRemoteLoader(url string) *RemoteLoader {
	return &RemoteLoader{URL: url, Client: DefaultHTTPClient}
}

// Load はリモートから GameParameters を取得する。
//
// 【実装する挙動（担当 issue の受け入れ条件と一致させること）】
//  1. l.URL へ HTTP GET（ctx を尊重）。
//  2. 200 でなければ or ボディが壊れていれば失敗として扱う。
//  3. JSON を game.GameParameters にデコード（json タグは game/params.go 参照。ネスト構造そのまま）。
//  4. 妥当性を検証（例: stack.limit > 0 等）。
//  5. 成功: (取得した GameParameters, nil) を返す。
//     失敗: (game.DefaultParameters(), 理由を包んだ err) を返す ＝ 第1返り値は常に有効。
//
// ※ 現状はスタブ。実装が済むまで合成ルートは DefaultLoader を使う。
func (l *RemoteLoader) Load(_ context.Context) (game.GameParameters, error) {
	return game.DefaultParameters(), ErrNotImplemented
}

var _ game.ConfigProvider = (*RemoteLoader)(nil)
