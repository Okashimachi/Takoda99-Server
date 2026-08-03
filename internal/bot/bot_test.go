package bot

import (
	"encoding/json"
	"math/rand"
	"testing"
	"time"

	"takoda99/internal/proto"
	"takoda99/internal/transport"
)

func recv(t *testing.T, c transport.Connection) proto.Envelope {
	t.Helper()
	select {
	case env, ok := <-c.Receive():
		if !ok {
			t.Fatal("接続が閉じた")
		}
		return env
	case <-time.After(2 * time.Second):
		t.Fatal("受信タイムアウト")
		return proto.Envelope{}
	}
}

// Bot は CustomerArrived で受け取った客に対して OrderServed を送る。
func TestBot_ServesArrivedCustomer(t *testing.T) {
	srv, cli := transport.Pipe()
	b := New(cli, Config{BaseAccuracy: 1.0, BaseElapsedMs: 1000, AccuracyJitter: 0, ElapsedJitterMs: 0}, rand.New(rand.NewSource(1)))

	// CustomerArrived を受信したと仮定。
	p, _ := json.Marshal(proto.CustomerView{CustomerId: "c-1", OrderCount: 2, Words: []string{"ねこ", "いぬ"}})
	b.onMessage(proto.Envelope{Type: proto.TypeCustomerArrived, Payload: p})
	b.act() // 保持中の c-1 に対して OrderServed を送るはず

	env := recv(t, srv)
	if env.Type != proto.TypeOrderServed {
		t.Fatalf("OrderServed を送るはず: got %s", env.Type)
	}
	var r proto.OrderServed
	_ = json.Unmarshal(env.Payload, &r)
	if r.CustomerId != "c-1" {
		t.Fatalf("実在 customerId に整合すべき: got %q, want c-1", r.CustomerId)
	}
}

// CustomerArrived で受け取った複数客も保持し、順に提供する。
func TestBot_ServesMultipleCustomers(t *testing.T) {
	srv, cli := transport.Pipe()
	b := New(cli, Config{BaseAccuracy: 1.0, BaseElapsedMs: 1000, AccuracyJitter: 0, ElapsedJitterMs: 0}, rand.New(rand.NewSource(1)))

	// 2人の客が来店。
	for _, cid := range []string{"c-1", "c-2"} {
		p, _ := json.Marshal(proto.CustomerView{CustomerId: proto.CustomerId(cid), OrderCount: 1, Words: []string{"ねこ"}})
		b.onMessage(proto.Envelope{Type: proto.TypeCustomerArrived, Payload: p})
	}

	b.act()
	if r := recvOrder(t, srv); r.CustomerId != "c-1" {
		t.Fatalf("1人目 got %q want c-1", r.CustomerId)
	}
	b.act()
	if r := recvOrder(t, srv); r.CustomerId != "c-2" {
		t.Fatalf("2人目 got %q want c-2", r.CustomerId)
	}
}

func recvOrder(t *testing.T, c transport.Connection) proto.OrderServed {
	t.Helper()
	env := recv(t, c)
	var r proto.OrderServed
	_ = json.Unmarshal(env.Payload, &r)
	return r
}

// MatchEnd で onMessage が終了シグナルを返す。
func TestBot_StopsOnMatchEnd(t *testing.T) {
	_, cli := transport.Pipe()
	b := New(cli, DefaultConfig(), rand.New(rand.NewSource(1)))
	p, _ := json.Marshal(proto.MatchEnd{FinalRank: 1})
	if !b.onMessage(proto.Envelope{Type: proto.TypeMatchEnd, Payload: p}) {
		t.Fatal("MatchEnd で終了(true)を返すべき")
	}
}
