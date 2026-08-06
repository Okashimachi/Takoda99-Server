package matchmaking

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"takoda99/internal/game"
	"takoda99/internal/proto"
	"takoda99/internal/transport"
)

func recvStatus(t *testing.T, c transport.Connection) proto.MatchmakingStatus {
	t.Helper()
	select {
	case env, ok := <-c.Receive():
		if !ok {
			t.Fatal("接続が閉じた")
		}
		var s proto.MatchmakingStatus
		_ = json.Unmarshal(env.Payload, &s)
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("MatchmakingStatus 受信タイムアウト")
		return proto.MatchmakingStatus{}
	}
}

// min到達でカウントダウン→完了で開始。定員不足は Bot で補完される。
func TestMatchmaker_CountdownAndBotFill(t *testing.T) {
	afterCh := make(chan time.Time, 1)
	started := make(chan []Player, 1)
	botN := 0
	m := New(Config{
		GetParams: func() game.MatchingParams {
			return game.MatchingParams{MinPlayers: 2, MaxPlayers: 4, StartCountdownMs: 15000, MinFill: 4}
		},
		After: func(time.Duration) <-chan time.Time { return afterCh },
		Start: func(players []Player) {
			started <- players
		},
		NewBot: func() Player {
			botN++
			s, _ := transport.Pipe()
			return Player{Id: game.PlayerId(fmt.Sprintf("bot%d", botN)), Conn: s}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	sa, ca := transport.Pipe()
	sb, cb := transport.Pipe()

	m.Join(Player{Id: "a", Conn: sa})
	if st := recvStatus(t, ca); st.WaitingCount != 1 || st.CountdownMs != nil {
		t.Fatalf("1人目はカウントダウンなし: %+v", st)
	}

	m.Join(Player{Id: "b", Conn: sb})
	if st := recvStatus(t, cb); st.WaitingCount != 2 || st.CountdownMs == nil {
		t.Fatalf("min到達でカウントダウン開始: %+v", st)
	}

	afterCh <- time.Now() // カウントダウン完了
	select {
	case p := <-started:
		if len(p) != 4 {
			t.Fatalf("人間2+Bot2=4で開始すべき: got %d", len(p))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start が呼ばれない")
	}
}

// maxPlayers 到達で即開始。
func TestMatchmaker_MaxPlayersImmediateStart(t *testing.T) {
	started := make(chan []Player, 1)
	m := New(Config{
		GetParams: func() game.MatchingParams {
			return game.MatchingParams{MinPlayers: 2, MaxPlayers: 2, StartCountdownMs: 15000}
		},
		Start: func(p []Player) { started <- p },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	sa, _ := transport.Pipe()
	sb, _ := transport.Pipe()
	m.Join(Player{Id: "a", Conn: sa})
	m.Join(Player{Id: "b", Conn: sb}) // max=2 到達

	select {
	case p := <-started:
		if len(p) != 2 {
			t.Fatalf("max到達で2人開始すべき: got %d", len(p))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("max到達で即開始すべき")
	}
}

// カウントダウン中に min を割り込むとリセットされ、開始しない。
func TestMatchmaker_LeaveBelowMinCancelsCountdown(t *testing.T) {
	afterCh := make(chan time.Time, 1)
	started := make(chan []Player, 1)
	m := New(Config{
		GetParams: func() game.MatchingParams {
			return game.MatchingParams{MinPlayers: 2, MaxPlayers: 99, StartCountdownMs: 15000}
		},
		After: func(time.Duration) <-chan time.Time { return afterCh },
		Start: func(p []Player) { started <- p },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	sa, ca := transport.Pipe()
	sb, _ := transport.Pipe()
	m.Join(Player{Id: "a", Conn: sa})
	m.Join(Player{Id: "b", Conn: sb}) // カウントダウン開始
	m.Leave("b")                      // min割り込み→リセット

	// a は join-a / join-b / leave-b の3件の status を受け取る。3件目まで読めば
	// Leave が処理済み＝countdown が nil にリセット済みと確定できる（決定的に同期）。
	recvStatus(t, ca) // after join-a (waiting1)
	recvStatus(t, ca) // after join-b (waiting2, countdown)
	if st := recvStatus(t, ca); st.WaitingCount != 1 || st.CountdownMs != nil {
		t.Fatalf("leave後はwaiting1・カウントダウン解除: %+v", st)
	}

	afterCh <- time.Now() // ここで撃っても Run側 countdown は nil なので無視される
	select {
	case <-started:
		t.Fatal("カウントダウンはリセットされ開始しないはず")
	case <-time.After(300 * time.Millisecond):
		// 開始しない＝正しい
	}
}

// #79 表示名サニタイズ: trim / 制御文字除去 / ルーン単位切り詰め / 空。
func TestSanitizeDisplayName(t *testing.T) {
	long := ""
	for i := 0; i < 40; i++ {
		long += "あ"
	}
	cases := []struct{ in, want string }{
		{"りーせ", "りーせ"},
		{"  spaced  ", "spaced"},
		{"a\tb\nc", "abc"},       // 制御文字は除去
		{"\x00\x07evil", "evil"}, // NUL/ベル除去
		{"", ""},                 // 空
		{"   ", ""},              // 空白のみ→空
		{long, string([]rune(long)[:MaxDisplayNameLen])}, // 24ルーンへ切り詰め
	}
	for _, c := range cases {
		if got := SanitizeDisplayName(c.in); got != c.want {
			t.Fatalf("SanitizeDisplayName(%q)=%q, want %q", c.in, got, c.want)
		}
	}
	if n := len([]rune(SanitizeDisplayName(long))); n != MaxDisplayNameLen {
		t.Fatalf("切り詰め後のルーン数=%d, want %d", n, MaxDisplayNameLen)
	}
}

// GetParams は待機ループのたびに呼ばれる（＝config の変更が再起動なしで反映される）。
//
// 当日「人が集まらないので minPlayers を下げる」は最優先の運用シナリオで、
// これが起動時スナップショットだと運営はサーバーを再起動してしまい、
// **進行中の試合を消してしまう**。ドキュメントに書くだけでは再発するのでテストで固定する。
func TestMatchmaker_ReloadsParamsWithoutRestart(t *testing.T) {
	var mu sync.Mutex
	minPlayers := 5
	calls := 0

	started := make(chan int, 1)
	tick := make(chan time.Time, 4)

	m := New(Config{
		GetParams: func() game.MatchingParams {
			mu.Lock()
			defer mu.Unlock()
			calls++
			return game.MatchingParams{MinPlayers: minPlayers, MaxPlayers: 99, StartCountdownMs: 0, MinFill: 0}
		},
		After:  func(time.Duration) <-chan time.Time { return tick },
		Start:  func(p []Player) { started <- len(p) },
		NewBot: nil,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	// 2人だけ参加させる。minPlayers=5 なのでまだ始まらない。
	srv1, _ := transport.Pipe()
	srv2, _ := transport.Pipe()
	m.Join(Player{Id: "p-1", Conn: srv1})
	m.Join(Player{Id: "p-2", Conn: srv2})

	select {
	case n := <-started:
		t.Fatalf("minPlayers=5 なのに %d人で開始した", n)
	case <-time.After(300 * time.Millisecond):
	}

	// 運営が config から minPlayers を 2 に下げる（サーバーは再起動していない）。
	mu.Lock()
	minPlayers = 2
	mu.Unlock()

	// カウントダウンを発火させる。再読み込みされていれば、この時点で成立する。
	tick <- time.Now()

	select {
	case n := <-started:
		if n != 2 {
			t.Fatalf("2人で開始するはず: %d人", n)
		}
	case <-time.After(2 * time.Second):
		mu.Lock()
		c := calls
		mu.Unlock()
		t.Fatalf("minPlayers を下げても試合が始まらない（起動時スナップショットになっている）。GetParams 呼び出し回数=%d", c)
	}

	// 何度も読み直していること（1回だけなら実質スナップショット）。
	mu.Lock()
	c := calls
	mu.Unlock()
	if c < 2 {
		t.Fatalf("GetParams が %d 回しか呼ばれていない（毎ループ読み直していない）", c)
	}
}

// 表示名は MaxDisplayNameLen（6ルーン）で切り詰められること。
//
// クライアントはマッチング画面に99枠のグリッドを描いており、長い名前が1つでも
// 混ざるとレイアウトが崩れる。自クライアントの入力欄に上限を掛けても
// **別クライアントや直接接続から長い名前を送られる**ので、サーバー側で揃える。
func TestSanitizeDisplayName_TruncatesToMax(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"6文字ちょうどは切らない", "たこやき太郎", "たこやき太郎"},
		{"7文字は6文字に切る", "たこやき次郎丸", "たこやき次郎"},
		{"ASCII も同じ", "abcdefghij", "abcdef"},
		{"全角/半角を混ぜても文字数で数える", "あaいiうu", "あaいiうu"},
		{"短い名前はそのまま", "たこ", "たこ"},
		{"空は空", "", ""},
		{"前後の空白は落とす", "  たこ  ", "たこ"},
		{"制御文字は落とす", "た\nこ\tや\rき", "たこやき"},
		{"切り詰め後の末尾空白も落とす", "たこやき  次郎", "たこやき"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SanitizeDisplayName(c.in)
			if got != c.want {
				t.Fatalf("SanitizeDisplayName(%q) = %q, want %q", c.in, got, c.want)
			}
			if n := len([]rune(got)); n > MaxDisplayNameLen {
				t.Fatalf("%d ルーン返した（上限 %d）", n, MaxDisplayNameLen)
			}
		})
	}
}

// どんな入力でも上限を超えないこと（表示崩れの最終防波堤）。
func TestSanitizeDisplayName_NeverExceedsMax(t *testing.T) {
	inputs := []string{
		strings.Repeat("あ", 100),
		strings.Repeat("a", 1000),
		strings.Repeat("👨‍👩‍👧", 20),
		strings.Repeat("が", 50),
		"🎌🎌🎌🎌🎌🎌🎌🎌🎌🎌",
	}
	for _, in := range inputs {
		got := SanitizeDisplayName(in)
		if n := len([]rune(got)); n > MaxDisplayNameLen {
			t.Fatalf("入力 %q → %d ルーン（上限 %d）", in, n, MaxDisplayNameLen)
		}
	}
}

// 切り詰めで宙に浮いた結合文字を末尾に残さないこと。
//
// ルーン単位で切ると絵文字の合字（ZWJ 連結）や異体字セレクタの途中で切れる。
// 末尾に単独では意味を持たない文字が残ると、受け手によって豆腐や別の絵文字に化ける。
func TestSanitizeDisplayName_NoDanglingJoiner(t *testing.T) {
	// 👨‍👩‍👧 は [👨, ZWJ, 👩, ZWJ, 👧] の5ルーン。2つ繋ぐと6ルーン目が ZWJ になる。
	got := SanitizeDisplayName("👨‍👩‍👧‍👦")
	for _, r := range []rune{0x200D, 0xFE0E, 0xFE0F} {
		if rs := []rune(got); len(rs) > 0 && rs[len(rs)-1] == r {
			t.Fatalf("末尾に結合用の文字が残った: %q (U+%04X)", got, r)
		}
	}
	if n := len([]rune(got)); n > MaxDisplayNameLen {
		t.Fatalf("%d ルーン（上限 %d）", n, MaxDisplayNameLen)
	}
}
