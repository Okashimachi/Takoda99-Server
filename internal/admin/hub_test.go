package admin

import (
	"errors"
	"testing"
	"time"

	"takoda99/internal/proto"
	"takoda99/internal/transport"
)

func testEnvelope(t string) proto.Envelope {
	return proto.Envelope{Type: t, Payload: []byte(`{}`)}
}

// recvWithin は timeout 内に1件受信する（無ければ失敗）。
func recvWithin(t *testing.T, ch <-chan proto.Envelope, d time.Duration) proto.Envelope {
	t.Helper()
	select {
	case env := <-ch:
		return env
	case <-time.After(d):
		t.Fatal("timeout: 受信できなかった")
		return proto.Envelope{}
	}
}

// mustNotRecv は timeout 内に何も来ないことを確認する。
func mustNotRecv(t *testing.T, ch <-chan proto.Envelope, d time.Duration) {
	t.Helper()
	select {
	case env, ok := <-ch:
		if ok {
			t.Fatalf("届かないはずのメッセージが届いた: %+v", env)
		}
	case <-time.After(d):
	}
}

func TestBroadcastReachesAllRegistered(t *testing.T) {
	h := NewHub()

	srvA, cliA := transport.Pipe()
	srvB, cliB := transport.Pipe()
	h.Register(srvA)
	h.Register(srvB)
	if h.Count() != 2 {
		t.Fatalf("Count=%d, want 2", h.Count())
	}

	h.Broadcast(testEnvelope("StoreListUpdate"))

	if got := recvWithin(t, cliA.Receive(), time.Second); got.Type != "StoreListUpdate" {
		t.Fatalf("A: type=%q, want StoreListUpdate", got.Type)
	}
	if got := recvWithin(t, cliB.Receive(), time.Second); got.Type != "StoreListUpdate" {
		t.Fatalf("B: type=%q, want StoreListUpdate", got.Type)
	}
}

func TestUnregisterStopsDelivery(t *testing.T) {
	h := NewHub()
	srv, cli := transport.Pipe()
	h.Register(srv)
	h.Unregister(srv)
	if h.Count() != 0 {
		t.Fatalf("Count=%d, want 0", h.Count())
	}

	h.Broadcast(testEnvelope("StoreListUpdate"))
	mustNotRecv(t, cli.Receive(), 100*time.Millisecond)
}

// errConn は Send が必ず失敗する Connection（slow-consumer eviction 済み相当）。
type errConn struct{ done chan struct{} }

func newErrConn() *errConn { return &errConn{done: make(chan struct{})} }

func (c *errConn) Send(proto.Envelope) error      { return errors.New("closed") }
func (c *errConn) Receive() <-chan proto.Envelope { return nil }
func (c *errConn) Done() <-chan struct{}          { return c.done }
func (c *errConn) Close() error                   { close(c.done); return nil }

var _ transport.Connection = (*errConn)(nil)

// Send が失敗する conn は Broadcast 側で自動 Unregister され、生きている conn には届く。
func TestBroadcastEvictsDeadConn(t *testing.T) {
	h := NewHub()

	dead := newErrConn()
	srv, cli := transport.Pipe()
	h.Register(dead)
	h.Register(srv)
	if h.Count() != 2 {
		t.Fatalf("Count=%d, want 2", h.Count())
	}

	h.Broadcast(testEnvelope("StoreListUpdate"))

	// 死んだ conn は登録解除され、生きている conn には届く。
	if got := recvWithin(t, cli.Receive(), time.Second); got.Type != "StoreListUpdate" {
		t.Fatalf("alive: type=%q, want StoreListUpdate", got.Type)
	}
	if h.Count() != 1 {
		t.Fatalf("Count=%d, want 1（死んだconnがevictされる）", h.Count())
	}
}
