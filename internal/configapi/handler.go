// Package configapi は【HTTPアダプタ】GameParameters の取得/保存 HTTP API（/api/params）。
//
// config-front（別リポの管理UI, #51）がブラウザから叩く前提で CORS を持ち、保存(POST)は
// 共有トークンで保護する。永続化の実体（Postgres）は Store インターフェース越しに注入され、
// この層は DB 実装に依存しない（合成ルートが db.ConfigStore を渡す）。依存は net/http/game/stdlib。
package configapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"textro99/internal/game"
)

// Store は現在の GameParameters を読み書きする口（db.ConfigStore が満たす）。
type Store interface {
	Load(ctx context.Context) (game.GameParameters, error)
	Save(ctx context.Context, gp game.GameParameters) error
}

const maxBodyBytes = 64 << 10 // 64KiB。GameParameters は極小なので十分。

type handler struct {
	store  Store
	token  string
	origin string
}

// NewHandler は /api/params 用の http.Handler を作る。
//   - store: 永続化の口。nil の場合、GET/POST は 503（DB未設定）を返す。
//   - token: POST 保護用の共有トークン。空文字なら POST は 503（無認証で書けないように）。
//   - allowOrigin: CORS の Access-Control-Allow-Origin。空なら "*"。
func NewHandler(store Store, token, allowOrigin string) http.Handler {
	if allowOrigin == "" {
		allowOrigin = "*"
	}
	return &handler{store: store, token: token, origin: allowOrigin}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.setCORS(w)
	switch r.Method {
	case http.MethodOptions:
		w.WriteHeader(http.StatusNoContent) // プリフライト
	case http.MethodGet:
		h.get(w, r)
	case http.MethodPost:
		h.post(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *handler) setCORS(w http.ResponseWriter) {
	head := w.Header()
	head.Set("Access-Control-Allow-Origin", h.origin)
	head.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	head.Set("Access-Control-Allow-Headers", "Content-Type, X-Admin-Token")
}

// get は現在の GameParameters を完全な JSON で返す（読み取りは公開）。
func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		http.Error(w, "config store not available (DATABASE_URL 未設定)", http.StatusServiceUnavailable)
		return
	}
	gp, err := h.store.Load(r.Context())
	if err != nil {
		http.Error(w, "load failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, gp)
}

// post は受信 JSON を検証して保存する（共有トークン必須）。
func (h *handler) post(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		http.Error(w, "config store not available (DATABASE_URL 未設定)", http.StatusServiceUnavailable)
		return
	}
	if h.token == "" {
		http.Error(w, "admin token not configured (CONFIG_ADMIN_TOKEN 未設定)", http.StatusServiceUnavailable)
		return
	}
	if !tokenEqual(r.Header.Get("X-Admin-Token"), h.token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var gp game.GameParameters
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&gp); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := gp.Validate(); err != nil {
		http.Error(w, "invalid params: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.store.Save(r.Context(), gp); err != nil {
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, gp) // 保存後の値を返す（config-front が反映確認できる）
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// tokenEqual は定数時間比較（タイミング攻撃対策）。
func tokenEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
