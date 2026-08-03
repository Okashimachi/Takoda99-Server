package configapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"takoda99/internal/odai"
)

// WordStore は words テーブルの読み書き口（db.WordStore が満たす）。
type WordStore interface {
	LoadAll(ctx context.Context) ([]odai.WordEntry, error)
	LoadFiltered(ctx context.Context, category string, level int, hasLevel bool) ([]odai.WordEntry, error)
	SaveAll(ctx context.Context, entries []odai.WordEntry, mode string) error
	Delete(ctx context.Context, id int) error
}

type wordsHandler struct {
	store   WordStore
	token   string
	origins []string
}

// NewWordsHandler は /api/words 用の http.Handler を作る。
func NewWordsHandler(store WordStore, token string, allowOrigins []string) http.Handler {
	norm := make([]string, 0, len(allowOrigins))
	for _, o := range allowOrigins {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o != "" {
			norm = append(norm, o)
		}
	}
	return &wordsHandler{store: store, token: token, origins: norm}
}

func (h *wordsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.setCORS(w, r)

	path := strings.TrimPrefix(r.URL.Path, "/api/words")
	path = strings.TrimPrefix(path, "/")

	switch r.Method {
	case http.MethodOptions:
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		h.getWords(w, r)
	case http.MethodPost:
		if path == "" {
			h.postWords(w, r)
		} else {
			http.Error(w, "not found", http.StatusNotFound)
		}
	case http.MethodDelete:
		if path != "" {
			h.deleteWord(w, r, path)
		} else {
			http.Error(w, "id required", http.StatusBadRequest)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *wordsHandler) setCORS(w http.ResponseWriter, r *http.Request) {
	head := w.Header()
	head.Set("Vary", "Origin")
	head.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	head.Set("Access-Control-Allow-Headers", "Content-Type, X-Admin-Token")
	head.Set("Access-Control-Allow-Origin", h.allowOriginFor(r.Header.Get("Origin")))
}

func (h *wordsHandler) allowOriginFor(reqOrigin string) string {
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

func (h *wordsHandler) getWords(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		http.Error(w, "word store not available (DATABASE_URL 未設定)", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	category := q.Get("category")
	levelStr := q.Get("level")

	var entries []odai.WordEntry
	var err error

	if category != "" || levelStr != "" {
		var level int
		var hasLevel bool
		if levelStr != "" {
			level, err = strconv.Atoi(levelStr)
			if err != nil {
				http.Error(w, "invalid level", http.StatusBadRequest)
				return
			}
			hasLevel = true
		}
		entries, err = h.store.LoadFiltered(r.Context(), category, level, hasLevel)
	} else {
		entries, err = h.store.LoadAll(r.Context())
	}
	if err != nil {
		http.Error(w, "load failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []odai.WordEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

type saveRequest struct {
	Mode  string           `json:"mode"`
	Words []odai.WordEntry `json:"words"`
}

func (h *wordsHandler) postWords(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		http.Error(w, "word store not available (DATABASE_URL 未設定)", http.StatusServiceUnavailable)
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

	var req saveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Words) == 0 {
		http.Error(w, "words array is empty", http.StatusBadRequest)
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = "upsert"
	}
	if mode != "upsert" && mode != "replace" {
		http.Error(w, "mode must be 'upsert' or 'replace'", http.StatusBadRequest)
		return
	}

	for i := range req.Words {
		if req.Words[i].Text == "" {
			http.Error(w, "words[].text is required", http.StatusBadRequest)
			return
		}
		if req.Words[i].Reading == "" {
			req.Words[i].Reading = req.Words[i].Text
		}
		if req.Words[i].KeystrokeCount == 0 {
			req.Words[i].KeystrokeCount = odai.Keystrokes(req.Words[i].Reading)
		}
		if req.Words[i].Category == "" {
			req.Words[i].Category = "general"
		}
	}

	if err := h.store.SaveAll(r.Context(), req.Words, mode); err != nil {
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	entries, err := h.store.LoadAll(r.Context())
	if err != nil {
		http.Error(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *wordsHandler) deleteWord(w http.ResponseWriter, r *http.Request, idStr string) {
	if h.store == nil {
		http.Error(w, "word store not available (DATABASE_URL 未設定)", http.StatusServiceUnavailable)
		return
	}
	if h.token == "" {
		http.Error(w, "admin token not configured", http.StatusServiceUnavailable)
		return
	}
	if !tokenEqual(r.Header.Get("X-Admin-Token"), h.token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := h.store.Delete(r.Context(), id); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
