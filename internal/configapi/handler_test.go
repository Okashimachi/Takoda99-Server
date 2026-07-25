package configapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"textro99/internal/game"
)

type fakeStore struct {
	gp      game.GameParameters
	loadErr error
	saveErr error
	saved   *game.GameParameters
}

func (f *fakeStore) Load(context.Context) (game.GameParameters, error) { return f.gp, f.loadErr }
func (f *fakeStore) Save(_ context.Context, gp game.GameParameters) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = &gp
	return nil
}

const tok = "s3cret"

func do(h http.Handler, method, token string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/params", bytes.NewReader(body))
	if token != "" {
		r.Header.Set("X-Admin-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGet_ReturnsFullParams(t *testing.T) {
	store := &fakeStore{gp: game.DefaultParameters()}
	h := NewHandler(store, tok, "")
	w := do(h, http.MethodGet, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var gp game.GameParameters
	if err := json.Unmarshal(w.Body.Bytes(), &gp); err != nil {
		t.Fatalf("返却JSONがGameParametersでない: %v", err)
	}
	if gp.Stack.Limit != game.DefaultParameters().Stack.Limit {
		t.Fatalf("値が一致しない: %d", gp.Stack.Limit)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("CORS未設定: %q", got)
	}
}

func TestGet_LoadError_500(t *testing.T) {
	h := NewHandler(&fakeStore{loadErr: errors.New("db down")}, tok, "")
	if w := do(h, http.MethodGet, "", nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
}

func TestOptions_Preflight_204(t *testing.T) {
	h := NewHandler(&fakeStore{}, tok, "https://config-front.example")
	w := do(h, http.MethodOptions, "", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://config-front.example" {
		t.Fatalf("Allow-Origin=%q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("Allow-Headers 未設定")
	}
}

func TestPost_Valid_SavesAndReturns(t *testing.T) {
	store := &fakeStore{gp: game.DefaultParameters()}
	h := NewHandler(store, tok, "")
	body, _ := json.Marshal(func() game.GameParameters {
		gp := game.DefaultParameters()
		gp.Stack.Limit = 15
		return gp
	}())
	w := do(h, http.MethodPost, tok, body)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if store.saved == nil || store.saved.Stack.Limit != 15 {
		t.Fatalf("保存されていない: %+v", store.saved)
	}
}

func TestPost_NoToken_401(t *testing.T) {
	store := &fakeStore{gp: game.DefaultParameters()}
	h := NewHandler(store, tok, "")
	body, _ := json.Marshal(game.DefaultParameters())
	if w := do(h, http.MethodPost, "", body); w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if w := do(h, http.MethodPost, "wrong", body); w.Code != http.StatusUnauthorized {
		t.Fatalf("誤トークンは 401 のはず, got %d", w.Code)
	}
	if store.saved != nil {
		t.Fatal("未認証で保存された")
	}
}

func TestPost_TokenNotConfigured_503(t *testing.T) {
	h := NewHandler(&fakeStore{}, "", "") // token 空
	body, _ := json.Marshal(game.DefaultParameters())
	if w := do(h, http.MethodPost, "anything", body); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", w.Code)
	}
}

func TestPost_InvalidJSON_400(t *testing.T) {
	h := NewHandler(&fakeStore{}, tok, "")
	if w := do(h, http.MethodPost, tok, []byte("{not json")); w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestPost_InvalidValues_400_NotSaved(t *testing.T) {
	store := &fakeStore{gp: game.DefaultParameters()}
	h := NewHandler(store, tok, "")
	bad := game.DefaultParameters()
	bad.Stack.Limit = 0 // Validate で弾かれる
	body, _ := json.Marshal(bad)
	if w := do(h, http.MethodPost, tok, body); w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if store.saved != nil {
		t.Fatal("破綻値が保存された")
	}
}

func TestNilStore_503(t *testing.T) {
	h := NewHandler(nil, tok, "")
	if w := do(h, http.MethodGet, "", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET want 503, got %d", w.Code)
	}
	body, _ := json.Marshal(game.DefaultParameters())
	if w := do(h, http.MethodPost, tok, body); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST want 503, got %d", w.Code)
	}
}
