package bot

import (
	"encoding/json"
	"math/rand"
	"testing"
	"time"

	"textro99/internal/proto"
	"textro99/internal/transport"
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

func msEnv(dakenId string) proto.Envelope {
	p, _ := json.Marshal(proto.MatchStart{InitialDaken: proto.DakenInstance{DakenId: dakenId}})
	return proto.Envelope{Type: proto.TypeMatchStart, Payload: p}
}

// Bot はサーバー発行の実在 dakenId に対してクリア報告する（チート検証と整合）。
func TestBot_ClearsRealDakenId(t *testing.T) {
	srv, cli := transport.Pipe()
	b := New(cli, Config{ClearIntervalMs: 1000, MissRate: 0, AttackEvery: 100}, rand.New(rand.NewSource(1)))

	b.onMessage(msEnv("a-1")) // サーバーが a-1 を発行したと仮定
	b.act()                   // 保持中の a-1 をクリア報告するはず

	env := recv(t, srv)
	if env.Type != proto.TypeDakenClearReport {
		t.Fatalf("DakenClearReport を送るはず: got %s", env.Type)
	}
	var r proto.DakenClearReport
	_ = json.Unmarshal(env.Payload, &r)
	if r.DakenId != "a-1" {
		t.Fatalf("実在 dakenId に整合すべき: got %q, want a-1", r.DakenId)
	}
}

// DakenIssued で受け取った複数お題も保持し、順にクリアする。
func TestBot_ClearsIssuedDaken(t *testing.T) {
	srv, cli := transport.Pipe()
	b := New(cli, Config{ClearIntervalMs: 1000, MissRate: 0, AttackEvery: 100}, rand.New(rand.NewSource(1)))

	p, _ := json.Marshal(proto.DakenIssued{Daken: []proto.DakenInstance{{DakenId: "x-1"}, {DakenId: "x-2"}}})
	b.onMessage(proto.Envelope{Type: proto.TypeDakenIssued, Payload: p})

	b.act()
	if r := recvClear(t, srv); r.DakenId != "x-1" {
		t.Fatalf("1つ目 got %q want x-1", r.DakenId)
	}
	b.act()
	if r := recvClear(t, srv); r.DakenId != "x-2" {
		t.Fatalf("2つ目 got %q want x-2", r.DakenId)
	}
}

func recvClear(t *testing.T, c transport.Connection) proto.DakenClearReport {
	t.Helper()
	env := recv(t, c)
	var r proto.DakenClearReport
	_ = json.Unmarshal(env.Payload, &r)
	return r
}

// AttackEvery ごとに攻撃する。
func TestBot_AttacksEveryN(t *testing.T) {
	srv, cli := transport.Pipe()
	b := New(cli, Config{ClearIntervalMs: 1000, MissRate: 0, AttackEvery: 2}, rand.New(rand.NewSource(1)))
	for i := 0; i < 2; i++ {
		b.onMessage(msEnv("d"))
	}
	// 追加でお題を積んでおく
	for i := 0; i < 4; i++ {
		b.onMessage(msEnv("d"))
	}

	sawAttack := false
	// 2回クリアすると1回攻撃が挟まる。数件読んで AttackRequest を確認。
	b.act() // clear #1
	recv(t, srv)
	b.act() // clear #2 → この後 attack
	// clear #2 と attack の2通が来る
	for i := 0; i < 2; i++ {
		if recv(t, srv).Type == proto.TypeAttackRequest {
			sawAttack = true
		}
	}
	if !sawAttack {
		t.Fatal("2回クリアごとに AttackRequest が来るべき")
	}
}

// GameOver で onMessage が終了シグナルを返す。
func TestBot_StopsOnGameOver(t *testing.T) {
	_, cli := transport.Pipe()
	b := New(cli, DefaultConfig(), rand.New(rand.NewSource(1)))
	p, _ := json.Marshal(proto.GameOver{Rank: 1})
	if !b.onMessage(proto.Envelope{Type: proto.TypeGameOver, Payload: p}) {
		t.Fatal("GameOver で終了(true)を返すべき")
	}
}
