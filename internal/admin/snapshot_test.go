package admin

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"takoda99/internal/game"
)

type fakeWords struct{}

func (fakeWords) Next(int, *rand.Rand) game.Word { return game.Word{Text: "たこ", KeystrokeCount: 4} }

func newSession(n int) *game.Session {
	p := game.DefaultParameters()
	p.Matching.ReadyCountdownMs = 0
	p.Matching.RosterWaitMs = 0
	inits := make([]game.PlayerInit, n)
	for i := range inits {
		id := game.PlayerId(string(rune('a' + i)))
		inits[i] = game.PlayerInit{Id: id, DisplayName: string(id)}
	}
	return game.NewSession("m-test", p, fakeWords{}, rand.New(rand.NewSource(7)), inits)
}

// BuildSnapshot は Start 後に matchId/phase/restPool/客総数/店数を正しく反映する。
func TestBuildSnapshot_Basic(t *testing.T) {
	s := newSession(3)
	s.Start(0)

	snap := BuildSnapshot(s, nil, nil)
	if snap.MatchId != "m-test" {
		t.Fatalf("MatchId=%q, want m-test", snap.MatchId)
	}
	if snap.Phase != "Early" {
		t.Fatalf("Phase=%q, want Early", snap.Phase)
	}
	if snap.AliveCount != 3 || len(snap.Stores) != 3 {
		t.Fatalf("AliveCount=%d stores=%d, want 3/3", snap.AliveCount, len(snap.Stores))
	}
	total := snap.Customers.Normal + snap.Customers.Bonus + snap.Customers.Claimer + snap.Customers.Buzz
	if total != s.Params().Customer.Total {
		t.Fatalf("Customers 総和=%d, want %d", total, s.Params().Customer.Total)
	}
	if snap.RestPool != s.Params().Customer.Total {
		t.Fatalf("RestPool=%d, want %d（分配前は全員 restPool）", snap.RestPool, s.Params().Customer.Total)
	}
	// restByAttr の総和も Total（分配前は全員 restPool）。
	rest := snap.RestByAttr.Normal + snap.RestByAttr.Bonus + snap.RestByAttr.Claimer + snap.RestByAttr.Buzz
	if rest != s.Params().Customer.Total {
		t.Fatalf("RestByAttr 総和=%d, want %d", rest, s.Params().Customer.Total)
	}
}

// SnapshotEnvelope は type=AdminSnapshot で包み、payload が AdminSnapshot に復号できる。
func TestSnapshotEnvelope_TypeAndPayload(t *testing.T) {
	s := newSession(2)
	s.Start(0)

	env, ok := SnapshotEnvelope(s, nil, nil)
	if !ok {
		t.Fatal("SnapshotEnvelope ok=false")
	}
	if env.Type != TypeAdminSnapshot {
		t.Fatalf("Type=%q, want %q", env.Type, TypeAdminSnapshot)
	}
	var snap AdminSnapshot
	if err := json.Unmarshal(env.Payload, &snap); err != nil {
		t.Fatalf("payload 復号失敗: %v", err)
	}
	if len(snap.Stores) != 2 {
		t.Fatalf("復号後 stores=%d, want 2", len(snap.Stores))
	}
	// 生存店は finalRank キーを出さない（omitempty＋nil）。
	for _, st := range snap.Stores {
		if st.Alive && st.FinalRank != nil {
			t.Fatalf("生存店に finalRank が入っている: %+v", st)
		}
	}
}

// AdminSnapshot が本戦の score を運び、廃止フィールドを載せないこと（plan-h21 §3.1）。
//
// これは h25（観測ダッシュボード本戦対応）の本格的な v2 化ではなく、
// 「消したフィールドの参照を Score へ付け替えた」最小対応の固定。
func TestBuildSnapshot_CarriesScore(t *testing.T) {
	s := newSession(3)
	s.Start(0)

	// session の score は外から書けないので、実際に1人捌かせて score を動かす。
	board := s.StoreBoard()
	if len(board) != 3 {
		t.Fatalf("StoreBoard len=%d, want 3", len(board))
	}

	snap := BuildSnapshot(s, nil, nil)
	if len(snap.Stores) != 3 {
		t.Fatalf("stores=%d, want 3", len(snap.Stores))
	}
	for i, st := range snap.Stores {
		if st.Score != board[i].Score {
			t.Fatalf("%s の Score=%d, want %d（StoreBoard と食い違う）", st.StoreId, st.Score, board[i].Score)
		}
	}

	// JSON に score キーが出て、廃止キーが出ないこと（front が旧キーを探しに行かないように）。
	raw, err := json.Marshal(snap.Stores[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"score"`) {
		t.Fatalf("score キーが無い: %s", raw)
	}
	for _, dead := range []string{"creditLife", "evalNormalized"} {
		if strings.Contains(string(raw), dead) {
			t.Fatalf("廃止キー %q が残っている: %s", dead, raw)
		}
	}
}

// AdminSnapshot が h26 の観測に要る内訳（takoyakiCount / missCount / isBot）と
// h31 の tier を運ぶ。
func TestBuildSnapshot_CarriesBreakdownAndIsBot(t *testing.T) {
	s := newSession(3)
	s.Start(0)

	botIds := map[game.PlayerId]bool{"b": true}
	botTiers := map[game.PlayerId]string{"b": "weak"}
	snap := BuildSnapshot(s, botIds, botTiers)

	byId := map[string]AdminStore{}
	for _, st := range snap.Stores {
		byId[st.StoreId] = st
	}
	if len(byId) != 3 {
		t.Fatalf("stores=%d, want 3", len(byId))
	}
	if !byId["b"].IsBot {
		t.Fatal("Bot が人間として出ている（P1 の観測が効かない）")
	}
	for _, id := range []string{"a", "c"} {
		if byId[id].IsBot {
			t.Fatalf("%s が Bot 扱いになっている", id)
		}
	}

	// tier（plan-h31 §6）。Bot だけに付き、人間は空のまま（h34 の表示がこれを読む）。
	if got := byId["b"].Tier; got != "weak" {
		t.Fatalf("Bot の tier=%q, want weak", got)
	}
	for _, id := range []string{"a", "c"} {
		if got := byId[id].Tier; got != "" {
			t.Fatalf("人間 %s に tier=%q が付いた", id, got)
		}
	}

	// botIds/botTiers 未注入（sim/既存テスト）でも壊れない。
	if BuildSnapshot(s, nil, nil).Stores[0].IsBot {
		t.Fatal("botIds=nil で IsBot が true になっている")
	}
	if BuildSnapshot(s, nil, nil).Stores[0].Tier != "" {
		t.Fatal("botTiers=nil で tier が入っている")
	}
}

// heat の上限と平均打鍵数が snapshot へ届く（plan-h34 §1.2）。
//
// 🔴 HeatMaxLevel が無いとダッシュボードは**上限線を引けない**。
// 「heat が上端に届かない」問題は #75 → h26 → h32 と3度再発しており、
// 目で見えることが h34 の主目的のひとつ。ここが 0 で配られると4度目を見逃す。
func TestBuildSnapshot_HeatMaxAndAvgKeystrokes(t *testing.T) {
	s := newSession(3)
	s.Start(0)

	snap := BuildSnapshot(s, nil, nil)
	if want := s.Params().Heat.MaxLevel; snap.HeatMaxLevel != want {
		t.Fatalf("HeatMaxLevel=%d, want %d", snap.HeatMaxLevel, want)
	}
	if snap.HeatMaxLevel <= 0 {
		t.Fatal("HeatMaxLevel が 0（front が上限線を引けない）")
	}
	// 分配前は誰も並んでいないので 0。
	if snap.AvgKeystrokes != 0 {
		t.Fatalf("分配前の AvgKeystrokes=%d, want 0", snap.AvgKeystrokes)
	}

	// 客が配られたら 0 でなくなる（h30 の効果を試合中に見るための値）。
	s.Tick(2000)
	snap = BuildSnapshot(s, nil, nil)
	queued := 0
	for _, st := range snap.Stores {
		queued += st.QueueLen
	}
	if queued == 0 {
		t.Fatal("客が1人も配られていない（テストの前提が崩れている）")
	}
	// AvgKeystrokes は**1注文（＝1語）あたり**。fakeWords は1語4打鍵なので、
	// 注文数がいくつでも 4 になる（客1人あたりにすると orderCount 倍になり、
	// h30 の「1語45打鍵以下」と比べられない数字になる）。
	if snap.AvgKeystrokes != 4 {
		t.Fatalf("AvgKeystrokes=%d, want 4（1語4打鍵・行列 %d 人）", snap.AvgKeystrokes, queued)
	}
}

// AdminSnapshot の JSON に廃止キーが無く、本戦の観測キーが揃っている。
//
// front はキー名で読むので、ここがズレると「表示が空」になって原因が分かりにくい。
func TestAdminSnapshot_WireKeys(t *testing.T) {
	s := newSession(2)
	s.Start(0)

	raw, err := json.Marshal(BuildSnapshot(s, nil, nil))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)

	for _, want := range []string{
		`"cull"`, `"stageIndex"`, `"stageTotal"`, `"untilMs"`, `"targetAliveCount"`, `"cutLineRank"`,
		`"score"`, `"takoyakiCount"`, `"missCount"`, `"isBot"`, `"atRisk"`, `"rank"`,
		// h34: front はこの2キーで上限線と打鍵数を描く。キー名がズレると「表示が空」になる。
		`"heatMaxLevel"`, `"avgKeystrokes"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("観測キー %s が無い: %s", want, body)
		}
	}
	for _, dead := range []string{"creditLife", "evalNormalized", "storm", "thresholdPct", "untilTick"} {
		if strings.Contains(body, dead) {
			t.Fatalf("廃止キー %q が残っている: %s", dead, body)
		}
	}
}

// AtRisk が h22 の予告対象（cullTargetIds）と同じ集合であること。
//
// 🔴 **観測を演出に寄せない。** 最終ステージの予告は表示層で1位を「対象外」に見せるが、
// 観測側は処理層の真実（1位も落ちる）を出す。ここを演出に合わせると、
// ダッシュボードで挙動を検証できなくなる。
func TestBuildSnapshot_AtRiskShowsProcessingTruth(t *testing.T) {
	s := newSession(3)
	s.Start(0)

	// 最終ステージへ進める（全店が処理層では脱落対象になる）。
	stages := s.Params().Cull.Stages
	s.Tick(stages[len(stages)-2].AtMs)

	snap := BuildSnapshot(s, nil, nil)
	atRisk := 0
	for _, st := range snap.Stores {
		if st.AtRisk {
			atRisk++
		}
	}
	if snap.Cull.StageIndex != len(stages) {
		t.Fatalf("StageIndex=%d, want %d（最終ステージへ向かっている）", snap.Cull.StageIndex, len(stages))
	}
	if snap.Cull.CutLineRank != 2 {
		t.Fatalf("CutLineRank=%d, want 2（表示層）", snap.Cull.CutLineRank)
	}
	if atRisk != snap.AliveCount {
		t.Fatalf("AtRisk=%d, want %d（最終ステージは全店が処理層の対象）", atRisk, snap.AliveCount)
	}
}
