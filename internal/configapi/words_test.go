package configapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"takoda99/internal/odai"
)

// fakeWords は WordStore のテスト用実装。呼ばれた引数を記録し、返すエラーを差し替えられる。
type fakeWords struct {
	all []odai.WordEntry

	updateErr error
	deleteErr error

	updatedID    int
	updatedPatch odai.WordPatch
	deletedID    int
	savedMode    string
}

func (f *fakeWords) LoadAll(context.Context) ([]odai.WordEntry, error) { return f.all, nil }
func (f *fakeWords) LoadFiltered(context.Context, string, int, bool) ([]odai.WordEntry, error) {
	return f.all, nil
}
func (f *fakeWords) SaveAll(_ context.Context, _ []odai.WordEntry, mode string) error {
	f.savedMode = mode
	return nil
}
func (f *fakeWords) Update(_ context.Context, id int, p odai.WordPatch) error {
	f.updatedID, f.updatedPatch = id, p
	return f.updateErr
}
func (f *fakeWords) Delete(_ context.Context, id int) error {
	f.deletedID = id
	return f.deleteErr
}

func doWords(h http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	}
	if token != "" {
		r.Header.Set("X-Admin-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }

// ── PATCH（#68 / plan-23）────────────────────────────────

func TestWords_Patch_UpdatesOnlyGivenFields(t *testing.T) {
	fw := &fakeWords{}
	h := NewWordsHandler(fw, tok, nil)

	body, _ := json.Marshal(odai.WordPatch{Level: intp(7)})
	w := doWords(h, http.MethodPatch, "/api/words/42", tok, body)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}
	if fw.updatedID != 42 {
		t.Fatalf("id = %d, want 42", fw.updatedID)
	}
	if fw.updatedPatch.Level == nil || *fw.updatedPatch.Level != 7 {
		t.Fatalf("level が渡っていない: %+v", fw.updatedPatch)
	}
	// 指定していないフィールドは nil のまま（＝DB 側で COALESCE により据え置き）。
	if fw.updatedPatch.Text != nil || fw.updatedPatch.Category != nil {
		t.Fatalf("指定していないフィールドが埋まっている: %+v", fw.updatedPatch)
	}
}

// reading を変えたら打鍵数を計算し直すこと。
// ここを忘れると「読みは変えたのに打鍵数が前のまま」になり、評価の speed がズレる。
func TestWords_Patch_RecomputesKeystrokesWhenReadingChanges(t *testing.T) {
	fw := &fakeWords{}
	h := NewWordsHandler(fw, tok, nil)

	body, _ := json.Marshal(odai.WordPatch{Reading: strp("たこやき")})
	if w := doWords(h, http.MethodPatch, "/api/words/1", tok, body); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}

	want := odai.Keystrokes("たこやき") // ta ko ya ki = 8
	if fw.updatedPatch.KeystrokeCount == nil {
		t.Fatal("keystrokeCount が計算されていない")
	}
	if got := *fw.updatedPatch.KeystrokeCount; got != want {
		t.Fatalf("keystrokeCount = %d, want %d", got, want)
	}
}

// 明示的に keystrokeCount を送ったら、そちらを優先する（自動計算で上書きしない）。
func TestWords_Patch_KeepsExplicitKeystrokeCount(t *testing.T) {
	fw := &fakeWords{}
	h := NewWordsHandler(fw, tok, nil)

	body, _ := json.Marshal(odai.WordPatch{Reading: strp("たこやき"), KeystrokeCount: intp(99)})
	doWords(h, http.MethodPatch, "/api/words/1", tok, body)

	if fw.updatedPatch.KeystrokeCount == nil || *fw.updatedPatch.KeystrokeCount != 99 {
		t.Fatalf("明示指定が上書きされた: %+v", fw.updatedPatch.KeystrokeCount)
	}
}

func TestWords_Patch_NotFoundIs404(t *testing.T) {
	fw := &fakeWords{updateErr: odai.ErrNotFound}
	h := NewWordsHandler(fw, tok, nil)

	body, _ := json.Marshal(odai.WordPatch{Level: intp(1)})
	if w := doWords(h, http.MethodPatch, "/api/words/999", tok, body); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// (text, level) の衝突は 409。500 だと運営UIが原因を説明できない。
func TestWords_Patch_ConflictIs409(t *testing.T) {
	fw := &fakeWords{updateErr: odai.ErrConflict}
	h := NewWordsHandler(fw, tok, nil)

	body, _ := json.Marshal(odai.WordPatch{Text: strp("たこ")})
	if w := doWords(h, http.MethodPatch, "/api/words/1", tok, body); w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

func TestWords_Patch_RejectsBadRequests(t *testing.T) {
	fw := &fakeWords{}
	h := NewWordsHandler(fw, tok, nil)

	cases := []struct {
		name  string
		path  string
		token string
		body  []byte
		want  int
	}{
		{"トークン無し", "/api/words/1", "", []byte(`{}`), http.StatusUnauthorized},
		{"トークン不一致", "/api/words/1", "wrong", []byte(`{}`), http.StatusUnauthorized},
		{"id が数値でない", "/api/words/abc", tok, []byte(`{}`), http.StatusBadRequest},
		{"id 無し", "/api/words/", tok, []byte(`{}`), http.StatusBadRequest},
		{"不正なJSON", "/api/words/1", tok, []byte(`{`), http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if w := doWords(h, http.MethodPatch, c.path, c.token, c.body); w.Code != c.want {
				t.Fatalf("status = %d, want %d (body=%s)", w.Code, c.want, w.Body.String())
			}
		})
	}
}

// ── DELETE（#68 / plan-23）──────────────────────────────

// 存在しない id の削除は 404。以前は 500 を返していた。
func TestWords_Delete_NotFoundIs404(t *testing.T) {
	fw := &fakeWords{deleteErr: odai.ErrNotFound}
	h := NewWordsHandler(fw, tok, nil)

	if w := doWords(h, http.MethodDelete, "/api/words/999", tok, nil); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestWords_Delete_Succeeds(t *testing.T) {
	fw := &fakeWords{}
	h := NewWordsHandler(fw, tok, nil)

	if w := doWords(h, http.MethodDelete, "/api/words/7", tok, nil); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if fw.deletedID != 7 {
		t.Fatalf("deletedID = %d, want 7", fw.deletedID)
	}
}

// ── CORS ────────────────────────────────────────────────

// プリフライトで PATCH が許可されていること。
// ここが漏れるとブラウザ（config-front）から PATCH が飛ばせない。
func TestWords_CORS_AllowsPatch(t *testing.T) {
	h := NewWordsHandler(&fakeWords{}, tok, []string{"https://example.test"})

	r := httptest.NewRequest(http.MethodOptions, "/api/words/1", nil)
	r.Header.Set("Origin", "https://example.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	allow := w.Header().Get("Access-Control-Allow-Methods")
	if !bytes.Contains([]byte(allow), []byte("PATCH")) {
		t.Fatalf("Allow-Methods に PATCH が無い: %q", allow)
	}
}
