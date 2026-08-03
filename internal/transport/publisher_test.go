package transport

import (
	"testing"
	"time"

	"takoda99/internal/proto"
)

func expectStoreList(t *testing.T, c Connection, wantAlive int) {
	t.Helper()
	select {
	case env, ok := <-c.Receive():
		if !ok {
			t.Fatal("接続が閉じた")
		}
		if env.Type != proto.TypeStoreListUpdate {
			t.Fatalf("StoreListUpdate のはず: got %s", env.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("配信が来ない")
	}
}

func expectNothing(t *testing.T, c Connection) {
	t.Helper()
	select {
	case env := <-c.Receive():
		t.Fatalf("間引かれるべき区間で配信が来た: %s", env.Type)
	case <-time.After(80 * time.Millisecond):
		// 何も来ない＝間引きOK
	}
}

// FullPublisher は初回に配信し、間隔未満は間引き、間隔経過で再配信する。
func TestFullPublisher_Throttles(t *testing.T) {
	p := NewFullPublisher(1000)
	srv, cli := Pipe()
	conns := map[proto.StoreId]Connection{"a": srv}
	stores := []proto.StoreSummary{{StoreId: "a", CreditLife: 3, Alive: true}}

	p.Publish(0, stores, 1, conns) // 初回 → 配信
	expectStoreList(t, cli, 1)

	p.Publish(500, stores, 1, conns) // 間隔未満 → 間引き
	expectNothing(t, cli)

	p.Publish(1000, stores, 1, conns) // 間隔経過 → 再配信
	expectStoreList(t, cli, 1)
}
