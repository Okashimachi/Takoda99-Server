package admin

import (
	"embed"
	"io/fs"
	"net/http"
)

// webdist は別リポ Takoda99-DashBoard のビルド成果物（素の静的サイト）を同梱したもの。
// scripts/sync-dashboard.sh が更新する（plan-h00 §5・単一デプロイを保つための同梱）。
//
//go:embed all:webdist
var webdist embed.FS

// StaticHandler は同梱ダッシュボードを配信する http.Handler を返す（GET /admin 用）。
//
// 認証はしない（トークンは観測ストリーム /admin/ws 側で検証する。ページ自体は静的資産のみで
// 秘密を含まない）。ページを開いた後、JS が自身の URL の ?token= を読んで WS を張る（plan-h00 §5）。
func StaticHandler() http.Handler {
	sub, err := fs.Sub(webdist, "webdist")
	if err != nil {
		// embed 済みなので実行時に失敗しない。保険として 500 を返す。
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "dashboard assets unavailable", http.StatusInternalServerError)
		})
	}
	return http.FileServer(http.FS(sub))
}
