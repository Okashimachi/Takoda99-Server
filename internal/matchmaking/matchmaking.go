// Package matchmaking は【スパイン】試合前の待機・編成。人数下限＋カウントダウンで開始し、
// 定員不足を Bot で補完する（02_マッチング仕様）。待機者へ MatchmakingStatus を配信する。
//
// 依存: game(MatchingParams/PlayerId) / transport(Connection) / proto(契約)。
// 「試合をどう構築するか（session+room）」と「Bot枠をどう作るか」は Config のコールバックで
// 注入する（合成ルート #38 が渡す）。matchmaking 自身は「いつ・誰で始めるか」だけを持つ。
package matchmaking

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"takoda99/internal/game"
	"takoda99/internal/proto"
	"takoda99/internal/transport"
)

// Player は待機中／開始対象の1名。Conn はサーバー側の接続ハンドル。
// Name は盤面表示名（サニタイズ済み・空ならフォールバックは試合構築側が決める）。
type Player struct {
	Id    game.PlayerId
	Conn  transport.Connection
	Name  string
	IsBot bool
	// Tier は Bot の強さ階層（"strong" / "normal" / "weak"）。人間は空（plan-h31）。
	//
	// **試合の進行には一切影響しない**（観測ダッシュボードで「強 Bot が上位を独占していないか」
	// 「人間はどの層に混ざっているか」を見るためだけの値）。抽選は app.DrawBotSpec が行う。
	Tier string
}

// MaxDisplayNameLen は表示名の最大文字数。
//
// **数え方は Unicode コードポイント（Go の `[]rune`）**。UTF-8 バイト数でも
// UTF-16 コードユニット数でもない。クライアント側の入力欄（Unity の
// TMP_InputField.characterLimit は UTF-16 単位）とは数え方が違うので完全一致しない。
// **サーバーの数え方が正**で、クライアント側は入力補助として近い値を掛けるだけでよい。
//
// 6文字なのはマッチング画面の参加者一覧（9×11＝99枠）に収めるため。
// 長い名前を送られると他プレイヤーの画面が崩れるので、サーバー側で揃える。
const MaxDisplayNameLen = 6

// SanitizeDisplayName は受信した表示名を安全化する（#79）。
// 前後空白を除去し、制御文字（改行・タブ等）を落とし、MaxDisplayNameLen ルーンで切り詰める。
// 結果が空なら空文字を返す（フォールバック名の割り当ては呼び出し側の責務）。
func SanitizeDisplayName(raw string) string {
	cleaned := make([]rune, 0, len(raw))
	for _, r := range raw {
		if unicode.IsControl(r) {
			continue
		}
		cleaned = append(cleaned, r)
	}
	name := strings.TrimSpace(string(cleaned))
	rs := []rune(name)
	if len(rs) > MaxDisplayNameLen {
		rs = trimDanglingJoiners(rs[:MaxDisplayNameLen])
		name = strings.TrimSpace(string(rs))
	}
	return name
}

// trimDanglingJoiners は切り詰めで宙に浮いた結合用の文字を末尾から落とす。
//
// ルーン単位で切ると絵文字の合字（👨‍👩‍👧 は ZWJ で3つの絵文字を繋いだもの）や
// 異体字セレクタの途中で切れて、末尾に**単独では意味を持たない文字**が残る。
// そのまま配ると受け手によって豆腐や別の絵文字に化けるので、末尾だけ整える。
//
// 完全な書記素クラスタ対応ではない（外部ライブラリが要る）。
// 「壊れた合字を配らない」ための最小限の後始末。
func trimDanglingJoiners(rs []rune) []rune {
	for len(rs) > 0 {
		r := rs[len(rs)-1]
		switch {
		case r == 0x200D, // ZWJ（絵文字の合字を繋ぐ）
			r == 0xFE0E, r == 0xFE0F, // 異体字セレクタ（字形指定）
			unicode.Is(unicode.Mn, r), // 結合マーク（濁点・アクセント等）
			unicode.Is(unicode.Me, r):
			rs = rs[:len(rs)-1]
		default:
			return rs
		}
	}
	return rs
}

// FallbackDisplayName は名前を送らなかったプレイヤーと Bot に割り当てる表示名。
//
// **MaxDisplayNameLen（6文字）に必ず収まること。** クライアントはマッチング画面に99枠の
// グリッドを描いており、ここが溢れるとレイアウトが崩れる。しかも崩れるのは
// 「名前を入力しなかった人」と Bot だけなので、**手元のテストでは気付かず本番で初めて出る**。
//
// 採番は**試合内の通し番号**（1..99）。接続IDの `p-1234` を使うと試合数が増えるほど桁が伸びて
// 6文字を超える（`p-12345` で7文字）。試合内番号なら `ゲスト99` / `CPU99` の5文字で頭打ちになる。
//
//	名前なしの人間 : ゲスト1 〜 ゲスト99  （最大5文字）
//	Bot            : CPU1   〜 CPU99    （最大5文字）
//
// **待機中の participants と試合開始後の stores[] で同じ名前になる**ことが重要。
// startMatch が待機プールの順をそのまま席順にするので、待機プールの添字+1 を渡せば一致する。
func FallbackDisplayName(isBot bool, seat int) string {
	if isBot {
		return fmt.Sprintf("CPU%d", seat)
	}
	return fmt.Sprintf("ゲスト%d", seat)
}

// Config は matchmaking の依存と数値。
type Config struct {
	// GetParams は現在のマッチング用パラメータを返す（動的リロード対応）。
	GetParams func() game.MatchingParams
	// After は指定時間後に発火するチャネルを返す（本番=time.After、テスト=手動）。
	After func(time.Duration) <-chan time.Time
	// Start は集まったプレイヤー（人間＋Bot補完）で試合を開始する（session+room 構築）。
	Start func(players []Player)
	// NewBot は Bot 枠を1つ作って返す（nil なら補完しない）。合成ルートが Pipe＋bot起動して返す。
	NewBot func() Player
}

type eventKind int

const (
	evJoin eventKind = iota
	evLeave
)

type event struct {
	kind eventKind
	p    Player
	id   game.PlayerId
}

// Matchmaker は単一の待機プールを回す。
type Matchmaker struct {
	cfg    Config
	events chan event // join/leave を単一FIFOで処理し、順序の曖昧さを避ける
}

// New は Matchmaker を作る。After 未指定なら time.After を使う。
func New(cfg Config) *Matchmaker {
	if cfg.After == nil {
		cfg.After = time.After
	}
	return &Matchmaker{cfg: cfg, events: make(chan event, 256)}
}

// Join は待機プールへ参加させる。
func (m *Matchmaker) Join(p Player) { m.events <- event{kind: evJoin, p: p, id: p.Id} }

// Leave は待機プールから離脱させる。
func (m *Matchmaker) Leave(id game.PlayerId) { m.events <- event{kind: evLeave, id: id} }

// Run は待機プールを回す。ctx キャンセルで終了。
//
// 開始条件（いずれか）: 人数が maxPlayers 到達（即開始）／ minPlayers 到達後 startCountdownMs 経過。
// カウントダウン中に minPlayers を割り込むとリセット。
func (m *Matchmaker) Run(ctx context.Context) {
	var pool []Player
	var countdown <-chan time.Time // nil のときカウントダウンなし
	var countdownStart time.Time   // カウントダウン開始時刻（残り時間算出用）
	var countdownMin int
	var countdownDurationMs int

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	update := func(forceBroadcast bool) {
		params := m.cfg.GetParams()
		min := params.MinPlayers
		max := params.MaxPlayers
		changed := false

		if max > 0 && len(pool) >= max {
			m.startMatch(pool, params)
			pool, countdown = nil, nil
			return
		}

		if countdown == nil && len(pool) >= min {
			countdownMin = min
			countdownDurationMs = params.StartCountdownMs
			countdownStart = time.Now()
			countdown = m.cfg.After(time.Duration(params.StartCountdownMs) * time.Millisecond)
			changed = true
		} else if countdown != nil && len(pool) < countdownMin {
			countdown = nil
			changed = true
		}

		if countdown != nil {
			params.MinPlayers = countdownMin
			params.StartCountdownMs = countdownDurationMs
		}

		if forceBroadcast || changed {
			m.broadcast(pool, countdown != nil, countdownStart, params)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return

		case ev := <-m.events:
			switch ev.kind {
			case evJoin:
				pool = append(pool, ev.p)
			case evLeave:
				pool = removePlayer(pool, ev.id)
			}
			// ここで強制ブロードキャストしない。会場で99人が一斉に参加すると
			// 「1 join ごとにプール全員へ配信」で O(N^2)（99人なら約4,900通）の
			// バーストになり、送信キューを溢れさせて待機者を切断してしまう。
			// 人数の定期通知は下の ticker（1秒）に任せ、ここではカウントダウンの
			// 開始/中断のような状態変化があった時だけ配信する（update 内の changed）。
			update(false)

		case <-ticker.C:
			if len(pool) > 0 {
				update(true)
			}

		case <-countdown:
			m.startMatch(pool, m.cfg.GetParams())
			pool, countdown = nil, nil
		}
	}
}

// startMatch は Bot 補完し、最終的な参加者一覧を配信して待機したのちに Start を呼ぶ。
func (m *Matchmaker) startMatch(pool []Player, params game.MatchingParams) {
	players := append([]Player(nil), pool...)
	for len(players) < params.MinFill && m.cfg.NewBot != nil {
		players = append(players, m.cfg.NewBot())
	}

	// 席順と表示名を確定させる
	for i := range players {
		if players[i].Name == "" {
			players[i].Name = FallbackDisplayName(players[i].IsBot, i+1)
		}
	}

	// フェーズ3：人数確定待機（REQ-04）。Bot込みの99人を最終配信し、指定秒数待機する。
	m.broadcast(players, false, time.Time{}, params)
	if params.RosterWaitMs > 0 && m.cfg.After != nil {
		<-m.cfg.After(time.Duration(params.RosterWaitMs) * time.Millisecond)
	}

	if m.cfg.Start != nil {
		m.cfg.Start(players)
	}
}

// broadcast は待機者へ現在の MatchmakingStatus を配信する。
// broadcast は待機者へ現在の MatchmakingStatus を配る。
//
// ⚠ **宛先ごとに内容が違う**（SelfStoreId が受信者自身を指すため）。
// 参加者一覧は全員で共通なので1回だけ組み立て、封筒だけ宛先ごとに作り直す。
//
// 帯域: 99人 × (id + 6文字の名前 + JSON) ≒ 5KB/通。1秒ティッカー × 99宛先で約490KB/秒。
// ただし minPlayers 到達で即カウントダウンに入るため「99人が長時間待つ」状態は起きず、
// 実質カウントダウンの窓（既定15秒）だけ。1試合あたり約7MB で、試合中の
// StoreListUpdate（645MB）の約1%。
func (m *Matchmaker) broadcast(pool []Player, counting bool, countdownStart time.Time, params game.MatchingParams) {
	var countdown *int
	if counting {
		remaining := params.StartCountdownMs - int(time.Since(countdownStart).Milliseconds())
		if remaining < 0 {
			remaining = 0
		}
		countdown = &remaining
	}

	// 名前は待機プールの添字から決める。startMatch が pool の順をそのまま席順にするので、
	// ここで割り当てた名前は試合開始後の stores[] と一致する。
	participants := make([]proto.MatchmakingParticipant, 0, len(pool))
	for i, p := range pool {
		name := p.Name
		if name == "" {
			name = FallbackDisplayName(p.IsBot, i+1)
		}
		participants = append(participants, proto.MatchmakingParticipant{
			StoreId:     string(p.Id),
			DisplayName: name,
			IsBot:       p.IsBot,
		})
	}

	for _, p := range pool {
		data, err := json.Marshal(proto.MatchmakingStatus{
			WaitingCount: len(pool),
			MinPlayers:   params.MinPlayers,
			CountdownMs:  countdown,
			SelfStoreId:  string(p.Id),
			Participants: participants,
		})
		if err != nil {
			continue
		}
		_ = p.Conn.Send(proto.Envelope{Type: proto.TypeMatchmakingStatus, Payload: data})
	}
}

func removePlayer(pool []Player, id game.PlayerId) []Player {
	for i, p := range pool {
		if p.Id == id {
			return append(pool[:i], pool[i+1:]...)
		}
	}
	return pool
}
