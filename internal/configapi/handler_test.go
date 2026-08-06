package configapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"takoda99/internal/game"
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
	h := NewHandler(store, tok, nil)
	w := do(h, http.MethodGet, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var gp game.GameParameters
	if err := json.Unmarshal(w.Body.Bytes(), &gp); err != nil {
		t.Fatalf("返却JSONがGameParametersでない: %v", err)
	}
	if gp.Credit.InitialLife != game.DefaultParameters().Credit.InitialLife {
		t.Fatalf("値が一致しない: %d", gp.Credit.InitialLife)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("CORS未設定: %q", got)
	}
}

func TestGet_LoadError_500(t *testing.T) {
	h := NewHandler(&fakeStore{loadErr: errors.New("db down")}, tok, nil)
	if w := do(h, http.MethodGet, "", nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
}

func TestOptions_Preflight_204(t *testing.T) {
	h := NewHandler(&fakeStore{}, tok, []string{"https://config-front.example"})
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
	h := NewHandler(store, tok, nil)
	body, _ := json.Marshal(func() game.GameParameters {
		gp := game.DefaultParameters()
		gp.Credit.InitialLife = 15
		return gp
	}())
	w := do(h, http.MethodPost, tok, body)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if store.saved == nil || store.saved.Credit.InitialLife != 15 {
		t.Fatalf("保存されていない: %+v", store.saved)
	}
}

func TestPost_NoToken_401(t *testing.T) {
	store := &fakeStore{gp: game.DefaultParameters()}
	h := NewHandler(store, tok, nil)
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
	h := NewHandler(&fakeStore{}, "", nil) // token 空
	body, _ := json.Marshal(game.DefaultParameters())
	if w := do(h, http.MethodPost, "anything", body); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", w.Code)
	}
}

func TestPost_InvalidJSON_400(t *testing.T) {
	h := NewHandler(&fakeStore{}, tok, nil)
	if w := do(h, http.MethodPost, tok, []byte("{not json")); w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestPost_InvalidValues_400_NotSaved(t *testing.T) {
	store := &fakeStore{gp: game.DefaultParameters()}
	h := NewHandler(store, tok, nil)
	bad := game.DefaultParameters()
	bad.Customer.Total = 0 // Validate で弾かれる
	body, _ := json.Marshal(bad)
	if w := do(h, http.MethodPost, tok, body); w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if store.saved != nil {
		t.Fatal("破綻値が保存された")
	}
}

func TestNilStore_503(t *testing.T) {
	h := NewHandler(nil, tok, nil)
	if w := do(h, http.MethodGet, "", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET want 503, got %d", w.Code)
	}
	body, _ := json.Marshal(game.DefaultParameters())
	if w := do(h, http.MethodPost, tok, body); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST want 503, got %d", w.Code)
	}
}

func reqWithOrigin(h http.Handler, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodOptions, "/api/params", nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func acao(w *httptest.ResponseRecorder) string {
	return w.Header().Get("Access-Control-Allow-Origin")
}

func TestCORS_MultiOrigin_EchoesAllowed(t *testing.T) {
	// 末尾スラッシュ付きでも正規化して一致すること。
	h := NewHandler(&fakeStore{}, tok, []string{"https://a.example/", "http://localhost:5173"})
	if got := acao(reqWithOrigin(h, "https://a.example")); got != "https://a.example" {
		t.Fatalf("許可Origin(スラッシュ正規化)が反映されない: %q", got)
	}
	if got := acao(reqWithOrigin(h, "http://localhost:5173")); got != "http://localhost:5173" {
		t.Fatalf("localhost が許可されない: %q", got)
	}
	// 非許可 Origin は先頭を返す（そのブラウザの Origin と不一致→ブロックされる）。
	if got := acao(reqWithOrigin(h, "https://evil.example")); got != "https://a.example" {
		t.Fatalf("非許可Originの扱いが想定外: %q", got)
	}
}

func TestCORS_Wildcard_And_Empty(t *testing.T) {
	if got := acao(reqWithOrigin(NewHandler(&fakeStore{}, tok, []string{"*"}), "http://anything")); got != "*" {
		t.Fatalf(`"*" は全許可のはず: %q`, got)
	}
	if got := acao(reqWithOrigin(NewHandler(&fakeStore{}, tok, nil), "http://anything")); got != "*" {
		t.Fatalf("空リストは * のはず: %q", got)
	}
}

// GET/POST が現在の設定のハッシュを返すこと（#68 / plan-23）。
//
// config-front は「保存した値がサーバーに乗ったか」をこれで照合する。
// ボディではなくヘッダにしているので、CORS の Expose-Headers に載っていないと
// ブラウザから読めない（載せ忘れが一番踏みやすい）。
func TestParams_ConfigHashHeader(t *testing.T) {
	gp := game.DefaultParameters()
	h := NewHandler(&fakeStore{gp: gp}, tok, []string{"https://example.test"})

	w := do(h, http.MethodGet, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	got := w.Header().Get("X-Config-Hash")
	if got == "" {
		t.Fatal("X-Config-Hash が付いていない")
	}
	if want := gp.ConfigHash(); got != want {
		t.Fatalf("X-Config-Hash = %q, want %q", got, want)
	}

	// 設定が変われば値も変わること（＝実際に中身を見ている）。
	other := gp
	other.Credit.InitialLife = gp.Credit.InitialLife + 1
	w2 := do(NewHandler(&fakeStore{gp: other}, tok, nil), http.MethodGet, "", nil)
	if w2.Header().Get("X-Config-Hash") == got {
		t.Fatal("設定を変えてもハッシュが同じ（中身を見ていない）")
	}
}

// ブラウザからヘッダを読むには Expose-Headers が要る。
func TestParams_ExposesConfigHashToBrowser(t *testing.T) {
	h := NewHandler(&fakeStore{gp: game.DefaultParameters()}, tok, []string{"https://example.test"})

	r := httptest.NewRequest(http.MethodGet, "/api/params", nil)
	r.Header.Set("Origin", "https://example.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Expose-Headers"); got != "X-Config-Hash" {
		t.Fatalf("Expose-Headers = %q, want X-Config-Hash", got)
	}
}
