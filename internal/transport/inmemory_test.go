package transport

import (
	"errors"
	"testing"
	"time"

	"takoda99/internal/proto"
)

func recvWithin(t *testing.T, c Connection, d time.Duration) (proto.Envelope, bool) {
	t.Helper()
	select {
	case env, ok := <-c.Receive():
		return env, ok
	case <-time.After(d):
		t.Fatal("受信タイムアウト")
		return proto.Envelope{}, false
	}
}

// 双方向にメッセージが届く。
func TestPipe_BidirectionalDelivery(t *testing.T) {
	a, b := Pipe()

	if err := a.Send(proto.Envelope{Type: "FromA"}); err != nil {
		t.Fatalf("a.Send: %v", err)
	}
	if env, ok := recvWithin(t, b, time.Second); !ok || env.Type != "FromA" {
		t.Fatalf("b が FromA を受け取れていない: %+v ok=%v", env, ok)
	}

	if err := b.Send(proto.Envelope{Type: "FromB"}); err != nil {
		t.Fatalf("b.Send: %v", err)
	}
	if env, ok := recvWithin(t, a, time.Second); !ok || env.Type != "FromB" {
		t.Fatalf("a が FromB を受け取れていない: %+v ok=%v", env, ok)
	}
}

// 片方の Close で両方の Receive が閉じ、閉じた側への Send は ErrConnClosed。
func TestPipe_CloseClosesBothReceives(t *testing.T) {
	a, b := Pipe()
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case _, ok := <-a.Receive():
		if ok {
			t.Fatal("Close後の a.Receive は閉じるべき")
		}
	case <-time.After(time.Second):
		t.Fatal("a.Receive が閉じない")
	}
	select {
	case _, ok := <-b.Receive():
		if ok {
			t.Fatal("相手Close後の b.Receive は閉じるべき")
		}
	case <-time.After(time.Second):
		t.Fatal("b.Receive が閉じない")
	}

	if err := b.Send(proto.Envelope{Type: "x"}); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("切断後の Send は ErrConnClosed: got %v", err)
	}
	// Close は冪等。
	if err := a.Close(); err != nil {
		t.Fatalf("2度目の Close: %v", err)
	}
}
