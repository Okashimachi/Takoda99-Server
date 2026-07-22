package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"textro99/internal/proto"
)

// 実WebSocket でメッセージが往復し、切断で Receive チャネルが閉じることを検証する。
func TestWSConnection_RoundTripAndClose(t *testing.T) {
	// サーバー: 1メッセージをエコーし、クライアント切断まで生存。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		env, ok := <-c.Receive()
		if !ok {
			return
		}
		_ = c.Send(env)         // エコー
		<-c.Receive()           // クライアント切断（チャネルclose）まで待つ
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	cli, err := Dial(ctx, url)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	if err := cli.Send(proto.Envelope{Type: "Ping", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got, ok := <-cli.Receive():
		if !ok {
			t.Fatal("接続が予期せず閉じた")
		}
		if got.Type != "Ping" {
			t.Fatalf("エコー不一致: got Type=%q, want Ping", got.Type)
		}
	case <-ctx.Done():
		t.Fatal("受信タイムアウト")
	}

	// Close で Receive チャネルが閉じる。
	if err := cli.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case _, ok := <-cli.Receive():
		if ok {
			t.Fatal("Close後は Receive が閉じるべき")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close後も Receive が閉じない")
	}

	// Close は冪等。
	_ = cli.Close()
}
