package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"takoda99/internal/transport"
)

// 上限まで取れて、超過分は false（＝503）になる。
func TestConnLimiter_AcquireUpToMax(t *testing.T) {
	cl := newConnLimiter(2)

	// || で繋ぐと短絡評価で2つ目が呼ばれず、枠を1つしか取らないまま先へ進んでしまう。
	if !cl.acquire() {
		t.Fatal("1枠目は acquire できるべき")
	}
	if !cl.acquire() {
		t.Fatal("2枠目（上限ちょうど）も acquire できるべき")
	}
	if cl.acquire() {
		t.Fatal("上限超過の acquire は false を返すべき（503）")
	}
	if cl.active() != 2 {
		t.Fatalf("active=%d, want 2", cl.active())
	}

	cl.release()
	if !cl.acquire() {
		t.Fatal("release した枠は再取得できるべき")
	}
}

// 接続が切れたら枠が返る（＝居座り接続で枠が枯れない）。
// upgrade 直後に release する実装ではこのテストは意味を持たないため、
// 「接続の生存期間ぶん枠を保持する」設計の回帰テストでもある。
func TestConnLimiter_ReleasesOnDisconnect(t *testing.T) {
	cl := newConnLimiter(1)
	srv, cli := transport.Pipe()

	if !cl.acquire() {
		t.Fatal("最初の acquire は成功するべき")
	}
	cl.releaseOnDisconnect(srv)

	// 接続が生きている間は枠が埋まったまま。
	if cl.acquire() {
		t.Fatal("接続が生きている間は枠が空くべきでない")
	}

	_ = cli.Close() // クライアント切断

	if !waitFor(func() bool { return cl.active() == 0 }) {
		t.Fatal("切断しても枠が返らない（枠が枯れる）")
	}
	if !cl.acquire() {
		t.Fatal("切断後は新規接続を受け付けられるべき")
	}
}

// 実WebSocket で「クライアントがタブを閉じた」時に Done() が発火する（#Plan-09 の回帰テスト）。
//
// readLoop が ctx を cancel していなかった頃は、相手都合の切断では Done() が閉じず、
// リミッターの枠が返らなかった（送信が発生して write エラーになるまで気付けない）。
func TestWSConnection_DoneFiresOnPeerDisconnect(t *testing.T) {
	accepted := make(chan transport.Connection, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := transport.Accept(w, r, transport.AcceptOptions{AllowAll: true})
		if err != nil {
			return
		}
		accepted <- c
		<-c.Done() // ハンドラは接続が死ぬまで生かしておく
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cli, err := transport.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	var srv transport.Connection
	select {
	case srv = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("サーバー側が接続を受け付けない")
	}

	// サーバー側からは何も送らないまま、クライアントが切断する。
	_ = cli.Close()

	select {
	case <-srv.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("クライアント切断で Done() が発火しない（リミッターの枠が返らない）")
	}
}

func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
