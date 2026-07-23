package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"textro99/internal/game"
)

// DefaultLoader は内蔵デフォルトをそのまま返す。
func TestDefaultLoader_ReturnsDefaults(t *testing.T) {
	got, err := DefaultLoader{}.Load(context.Background())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != game.DefaultParameters() {
		t.Fatalf("got %+v, want DefaultParameters()", got)
	}
}

// serveJSON は指定ボディ・ステータスを返す httptest サーバを立てる。
func serveJSON(t *testing.T, status int, body string) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// 正常系: 有効な JSON を取得して値を反映する。
func TestRemoteLoader_Success(t *testing.T) {
	want := game.DefaultParameters()
	want.Attack.WarningGraceMs = 999 // 変更が反映されるか確認するための目印
	body, _ := json.Marshal(want)

	got, err := NewRemoteLoader(serveJSON(t, http.StatusOK, string(body))).Load(context.Background())
	if err != nil {
		t.Fatalf("正常系で err=%v", err)
	}
	if got.Attack.WarningGraceMs != 999 || got != want {
		t.Fatalf("取得値が反映されていない: got %+v", got)
	}
}

// 異常系はいずれも「デフォルト＋err」を返す（第1返り値は常に有効）。
func TestRemoteLoader_FallsBackToDefaults(t *testing.T) {
	def := game.DefaultParameters()

	invalid := def
	invalid.Stack.Limit = 0
	invalidBody, _ := json.Marshal(invalid)

	cases := []struct {
		name string
		url  string
	}{
		{"HTTP 500", serveJSON(t, http.StatusInternalServerError, `{}`)},
		{"壊れたJSON", serveJSON(t, http.StatusOK, `{not json`)},
		{"必須値が異常(stack.limit=0)", serveJSON(t, http.StatusOK, string(invalidBody))},
		{"接続不可", "http://127.0.0.1:0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NewRemoteLoader(c.url).Load(context.Background())
			if err == nil {
				t.Fatalf("err が返るべき")
			}
			if got != def {
				t.Fatalf("失敗時はデフォルトを返すべき: got %+v", got)
			}
		})
	}
}
