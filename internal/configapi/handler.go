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
	"strings"

	"takoda99/internal/game"
)

// Store は現在の GameParameters を読み書きする口（db.ConfigStore が満たす）。
type Store interface {
	Load(ctx context.Context) (game.GameParameters, error)
	Save(ctx context.Context, gp game.GameParameters) error
}

const maxBodyBytes = 64 << 10 // 64KiB。GameParameters は極小なので十分。

type handler struct {
	store   Store
	token   string
	origins []string // 正規化済み（前後空白・末尾スラッシュ除去）。空なら "*"
}

// NewHandler は /api/params 用の http.Handler を作る。
//   - store: 永続化の口。nil の場合、GET/POST は 503（DB未設定）を返す。
//   - token: POST 保護用の共有トークン。空文字なら POST は 503（無認証で書けないように）。
//   - allowOrigins: CORS 許可オリジンのリスト（カンマ区切りを分解済み）。空なら "*"、"*" を含めば全許可。
//     末尾スラッシュはブラウザの Origin と一致しないため自動で除去する。
func NewHandler(store Store, token string, allowOrigins []string) http.Handler {
	norm := make([]string, 0, len(allowOrigins))
	for _, o := range allowOrigins {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o != "" {
			norm = append(norm, o)
		}
	}
	return &handler{store: store, token: token, origins: norm}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.setCORS(w, r)
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

func (h *handler) setCORS(w http.ResponseWriter, r *http.Request) {
	head := w.Header()
	head.Set("Vary", "Origin")
	head.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	head.Set("Access-Control-Allow-Headers", "Content-Type, X-Admin-Token")
	head.Set("Access-Control-Expose-Headers", "X-Config-Hash")
	head.Set("Access-Control-Allow-Origin", h.allowOriginFor(r.Header.Get("Origin")))
}

// allowOriginFor は返す Access-Control-Allow-Origin 値を決める。
// リスト空→ "*"。"*" を含む→ "*"。リクエスト Origin が許可内→そのまま反映。
// それ以外→リスト先頭（非許可オリジンは自分の Origin と一致しないのでブラウザが弾く）。
func (h *handler) allowOriginFor(reqOrigin string) string {
	if len(h.origins) == 0 {
		return "*"
	}
	for _, o := range h.origins {
		if o == "*" {
			return "*"
		}
		if strings.EqualFold(o, reqOrigin) {
			return reqOrigin
		}
	}
	return h.origins[0]
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
	writeParams(w, gp)
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
	writeParams(w, gp) // 保存後の値を返す（config-front が反映確認できる）
}

// writeParams は GameParameters に configHash を1つ足して返す（plan-23 §3 案B）。
//
// ラッパで包む案A は config-front の既存パースを壊すので採らない。
// トップレベルに1キー足す形なら、config-front がそのまま POST で送り返してきても
// GameParameters のデコードは未知フィールドを無視するので壊れない。
//
// 併せてヘッダ X-Config-Hash でも返す（案C）。ボディをパースせずに反映確認したい場合や、
// POST のレスポンスを読み捨てる場合に使える。CORS の Expose-Headers に載せてある。
func writeParams(w http.ResponseWriter, gp game.GameParameters) {
	hash := gp.ConfigHash()
	w.Header().Set("X-Config-Hash", hash)

	raw, err := json.Marshal(gp)
	if err != nil {
		http.Error(w, "marshal failed", http.StatusInternalServerError)
		return
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		http.Error(w, "marshal failed", http.StatusInternalServerError)
		return
	}
	hashJSON, err := json.Marshal(hash)
	if err != nil {
		http.Error(w, "marshal failed", http.StatusInternalServerError)
		return
	}
	m["configHash"] = hashJSON
	writeJSON(w, http.StatusOK, m)
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
