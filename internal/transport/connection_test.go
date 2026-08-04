package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"takoda99/internal/proto"
)

// Send は読まないクライアントがいてもブロックしない（#40 の回帰テスト）。
//
// room は単一 goroutine から全接続へ順に Send する。Send が実 I/O を同期で行っていた頃は、
// 応答しない半開接続が1つあるだけで Write が writeTimeout(10s) まで固まり、試合全体が停止した。
// 送信キュー＋writeLoop への非同期化で、Send は必ず即座に返る（追従不能なら接続を切る）。
func TestWSConnection_SendDoesNotBlockOnStalledClient(t *testing.T) {
	const (
		floodCount  = 500
		payloadSize = 8 * 1024 // TCPの送信バッファを確実に埋めるサイズ
		budget      = 3 * time.Second
	)

	elapsedCh := make(chan time.Duration, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r, AcceptOptions{AllowAll: true})
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()

		payload, _ := json.Marshal(strings.Repeat("x", payloadSize))
		env := proto.Envelope{Type: "Flood", Payload: payload}

		start := time.Now()
		for i := 0; i < floodCount; i++ {
			// 追従不能になれば ErrConnClosed が返る。どちらでもブロックしないことが要件。
			_ = c.Send(env)
		}
		elapsedCh <- time.Since(start)
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// クライアントは接続するだけで一切 Receive を読まない（ストールしたクライアント）。
	cli, err := Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = cli.Close() }()

	select {
	case elapsed := <-elapsedCh:
		if elapsed > budget {
			t.Fatalf("Send が %d 件で %v かかった（上限 %v）。実I/Oで同期ブロックしている疑い", floodCount, elapsed, budget)
		}
		t.Logf("Send %d 件が %v で完了（ブロックなし）", floodCount, elapsed)
	case <-time.After(20 * time.Second):
		t.Fatal("Send がブロックしている（room ごと試合が止まる状態）")
	}
}

// 実WebSocket でメッセージが往復し、切断で Receive チャネルが閉じることを検証する。
func TestWSConnection_RoundTripAndClose(t *testing.T) {
	// サーバー: 1メッセージをエコーし、クライアント切断まで生存。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r, AcceptOptions{AllowAll: true})
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
