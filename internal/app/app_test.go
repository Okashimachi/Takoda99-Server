package app_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"textro99/internal/app"
	"textro99/internal/bot"
	"textro99/internal/matchmaking"
	"textro99/internal/proto"
	"textro99/internal/transport"
)

// RunMatch が全スタック（session+room+transport+bot）を組み立て、参加者へ MatchStart を配る。
func TestRunMatch_AssemblesAndDeliversMatchStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := app.DefaultDeps()

	// 観測用プレイヤー（client 端をテストが読む）＋ Bot 1体。
	obsSrv, obsCli := transport.Pipe()
	botP := app.NewBotPlayer(ctx, "bot1", bot.DefaultConfig())
	players := []matchmaking.Player{{Id: "obs", Conn: obsSrv}, botP}

	go app.RunMatch(ctx, d, players)

	select {
	case env, ok := <-obsCli.Receive():
		if !ok {
			t.Fatal("接続が閉じた")
		}
		if env.Type != proto.TypeMatchStart {
			t.Fatalf("最初は MatchStart のはず: got %s", env.Type)
		}
		var ms proto.MatchStart
		if err := json.Unmarshal(env.Payload, &ms); err != nil {
			t.Fatalf("MatchStart 復号失敗: %v", err)
		}
		if ms.SelfPlayerId != "obs" || len(ms.Players) != 2 || ms.InitialDaken.DakenId == "" {
			t.Fatalf("MatchStart 不備: %+v", ms)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("MatchStart が届かない")
	}
}
