package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func wsServer(opts AcceptOptions) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := Accept(w, r, opts); err == nil {
			_ = c.Close()
		}
	}))
}

// dialWithOrigin はブラウザを模して Origin ヘッダ付きで WS 接続する（websocket.Dial は既定で Origin を送らない）。
func dialWithOrigin(ctx context.Context, url, origin string) error {
	var h http.Header
	if origin != "" {
		h = http.Header{"Origin": {origin}}
	}
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h})
	if err == nil {
		_ = c.Close(websocket.StatusNormalClosure, "")
	}
	return err
}

// TestAccept_OriginEnforcement は結合ブロッカー報告(2026-07-26)の再現＋修正確認。
// 既定(nil opts)ではブラウザの Origin(localhost:5173) が拒否されるが、許可リスト/AllowAll で通る。
func TestAccept_OriginEnforcement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := func(s *httptest.Server) string { return "ws" + strings.TrimPrefix(s.URL, "http") }

	t.Run("既定は別オリジンを拒否（報告の症状）", func(t *testing.T) {
		s := wsServer(AcceptOptions{}) // 同一オリジンのみ
		defer s.Close()
		if err := dialWithOrigin(ctx, wsURL(s), "http://localhost:5173"); err == nil {
			t.Fatal("既定で別オリジンが通ってしまった（症状が再現しない）")
		}
	})

	t.Run("許可リストにあれば通る", func(t *testing.T) {
		s := wsServer(AcceptOptions{AllowedOriginHosts: []string{"localhost:5173"}})
		defer s.Close()
		if err := dialWithOrigin(ctx, wsURL(s), "http://localhost:5173"); err != nil {
			t.Fatalf("許可オリジンが拒否された: %v", err)
		}
		if err := dialWithOrigin(ctx, wsURL(s), "http://evil.example"); err == nil {
			t.Fatal("非許可オリジンが通ってしまった")
		}
	})

	t.Run("AllowAll はどのオリジンも通る", func(t *testing.T) {
		s := wsServer(AcceptOptions{AllowAll: true})
		defer s.Close()
		if err := dialWithOrigin(ctx, wsURL(s), "http://localhost:5173"); err != nil {
			t.Fatalf("AllowAll でブラウザ由来オリジンが拒否された: %v", err)
		}
	})
}
