package app_test

// クライアント結合の**受入検証**。実WSクライアント（coder/websocket 直叩き＝サーバー内部の
// Connection を介さない生ワイヤ）で試合を最後まで走らせ、**proto v0.8.0 の契約どおりに
// 実際データが飛んでくるか**を全メッセージについて確かめる。
//
// 🔴 **なぜ要るか**: 「型はあるのに実は届いていない」が実際に起きた。
// PersonalResult は proto v0.7.0 で追加され game も送出していたのに、room.envelopeOf に
// 変換分岐が無く dispatch が黙って捨てていて、**予選から1通も届いていなかった**（h23 で発覚）。
// 型定義の存在も、送出側の実装も、届くことを保証しない。**受信側から見て確かめる**しかない。
//
// 既存の TestE2E_ClientWireFlow は MatchStart / CustomerArrived / OrderServed / MatchEnd の
// 4種しか見ておらず、本戦で追加した5種（RankingSnapshot / RankingDelta /
// StoreEliminatedBatch / ForcedEliminationWarning / PersonalResult）が未検証だった。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"takoda99/internal/app"
	"takoda99/internal/bot"
	"takoda99/internal/game"
	"takoda99/internal/matchmaking"
	"takoda99/internal/proto"
	"takoda99/internal/transport"
)

// recorder は受信した Envelope を type ごとに記録する。
type recorder struct {
	mu    sync.Mutex
	order []string                     // 受信順（順序の検証に使う）
	byTyp map[string][]json.RawMessage // type ごとの生ペイロード
}

func newRecorder() *recorder {
	return &recorder{byTyp: map[string][]json.RawMessage{}}
}

func (r *recorder) add(typ string, payload json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, typ)
	cp := make(json.RawMessage, len(payload))
	copy(cp, payload)
	r.byTyp[typ] = append(r.byTyp[typ], cp)
}

func (r *recorder) count(typ string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byTyp[typ])
}

func (r *recorder) first(typ string) (json.RawMessage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.byTyp[typ]) == 0 {
		return nil, false
	}
	return r.byTyp[typ][0], true
}

func (r *recorder) last(typ string) (json.RawMessage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.byTyp[typ])
	if n == 0 {
		return nil, false
	}
	return r.byTyp[typ][n-1], true
}

// seq は受信順のスナップショット。
func (r *recorder) seq() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

// TestWireContract_AllMessagesActuallyArrive は1試合を完走させ、
// **契約上飛ぶはずの全メッセージが実際に届く**ことを確かめる。
func TestWireContract_AllMessagesActuallyArrive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	rec := newRecorder()
	srv, selfId := startMatchAndConnect(ctx, t, rec)
	defer srv.Close()

	// ── 1. 届いていること（ここが本題）──
	//
	// 「型はあるのに届かない」を検出する。1通も来ない type があれば、
	// envelopeOf の分岐漏れか、送出側が呼ばれていない。
	mustArrive := []struct {
		typ string
		why string
	}{
		{proto.TypeMatchStart, "初期状態の唯一の供給源。99店の表示名はここでしか配られない"},
		{proto.TypeCustomerArrived, "お題。届かないとゲームが止まる"},
		{proto.TypeEvaluationUpdate, "自店の score と rank。自分の順位はこれが権威"},
		{proto.TypeRankingSnapshot, "全店ランキング（全量）"},
		{proto.TypeForcedEliminationWarning, "足切りの秒読みとカットライン。常時配信"},
		{proto.TypeStoreEliminatedBatch, "足切りの脱落者。1回の足切りを1メッセージに畳む"},
		{proto.TypePersonalResult, "個人成績。★予選から1通も届いていなかった実績あり"},
		{proto.TypeMatchEnd, "試合終了の締め"},
		{proto.TypeDifficultyUpdate, "火力（お題難度）"},
	}
	for _, m := range mustArrive {
		if rec.count(m.typ) == 0 {
			t.Errorf("🔴 %s が1通も届いていない（%s）\n"+
				"   → room.envelopeOf に分岐があるか、game が Outbound を返しているかを確認",
				m.typ, m.why)
		}
	}

	// ── 2. 廃止メッセージが飛んでいないこと ──
	//
	// 本戦で送信を止めたもの。飛んでいたら移行が漏れている。
	for _, typ := range []string{"CustomerLeft", "CreditUpdate", "StoreListUpdate"} {
		if n := rec.count(typ); n > 0 {
			t.Errorf("🔴 廃止したはずの %s が %d 通飛んでいる", typ, n)
		}
	}

	// ── 3. 中身が契約どおりか ──
	t.Run("MatchStart", func(t *testing.T) {
		raw, _ := rec.first(proto.TypeMatchStart)
		var m proto.MatchStart
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.SelfStoreId != selfId {
			t.Errorf("selfStoreId=%q, want %q", m.SelfStoreId, selfId)
		}
		if len(m.Stores) == 0 {
			t.Fatal("stores[] が空。表示名を配る唯一の場なのに配られていない")
		}
		for _, s := range m.Stores {
			if s.DisplayName == "" {
				t.Errorf("store %s の displayName が空。以降 再送されないので復元不能", s.StoreId)
				break
			}
		}
		// 公開パラメータ（本戦で追加したもの）
		if len(m.Params.CullSchedule) == 0 {
			t.Error("params.cullSchedule が空。クライアントがタイムラインを描けない")
		}
		if m.Params.ScoreWeightTakoyaki <= 0 {
			t.Errorf("params.scoreWeightTakoyaki=%d（正であるべき）", m.Params.ScoreWeightTakoyaki)
		}
		if m.StartsAtServerMs == 0 {
			t.Error("startsAtServerMs が 0。開始前カウントダウンが描けない")
		}
	})

	t.Run("CustomerArrived", func(t *testing.T) {
		raw, _ := rec.first(proto.TypeCustomerArrived)
		var c proto.CustomerView
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if c.CustomerId == "" || c.OrderCount <= 0 || len(c.Words) == 0 {
			t.Errorf("お題として成立していない: %+v", c)
		}
		if len(c.Words) != c.OrderCount {
			t.Errorf("words=%d 件 なのに orderCount=%d（食い違い）", len(c.Words), c.OrderCount)
		}
	})

	t.Run("EvaluationUpdate", func(t *testing.T) {
		raw, _ := rec.last(proto.TypeEvaluationUpdate)
		var e proto.EvaluationUpdate
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if e.Rank <= 0 {
			t.Errorf("rank=%d（1以上であるべき）", e.Rank)
		}
		if e.AliveCount <= 0 {
			t.Errorf("aliveCount=%d", e.AliveCount)
		}
		// 廃止フィールドに値が入っていないこと（ワイヤ上の主張として検査する）
		assertZeroOnWireJSON(t, raw, "evalRaw", "normalized", "starRating", "starDelta")
	})

	t.Run("RankingSnapshot", func(t *testing.T) {
		raw, _ := rec.last(proto.TypeRankingSnapshot)
		var s proto.RankingSnapshot
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(s.Entries) == 0 {
			t.Fatal("entries が空")
		}
		seen := map[proto.StoreId]bool{}
		for _, e := range s.Entries {
			if e.StoreId == "" {
				t.Error("storeId が空のエントリがある")
			}
			if seen[e.StoreId] {
				t.Errorf("storeId %s が重複している", e.StoreId)
			}
			seen[e.StoreId] = true
			if e.Rank <= 0 {
				t.Errorf("%s の rank=%d（1以上であるべき）", e.StoreId, e.Rank)
			}
		}
	})

	t.Run("ForcedEliminationWarning", func(t *testing.T) {
		raw, _ := rec.first(proto.TypeForcedEliminationWarning)
		var w proto.ForcedEliminationWarning
		if err := json.Unmarshal(raw, &w); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if w.StageTotal <= 0 {
			t.Errorf("stageTotal=%d", w.StageTotal)
		}
		if w.StageIndex <= 0 || w.StageIndex > w.StageTotal {
			t.Errorf("stageIndex=%d（1..%d であるべき）", w.StageIndex, w.StageTotal)
		}
		if w.CutLineRank <= 0 {
			t.Errorf("cutLineRank=%d", w.CutLineRank)
		}
		// 🔴 cutStoreIds は nil だと JSON で null になり、C# の List<T> が null になって
		// foreach で落ちる。サーバーは常に非nilで送る約束（proto v0.8.0 のコメント）。
		if !jsonKeyIsArray(t, raw, "cutStoreIds") {
			t.Error("🔴 cutStoreIds が配列でない（null の可能性）。C# の foreach が落ちる")
		}
		assertZeroOnWireJSON(t, raw, "untilTick", "thresholdPct")
	})

	t.Run("StoreEliminatedBatch", func(t *testing.T) {
		raw, _ := rec.first(proto.TypeStoreEliminatedBatch)
		var b proto.StoreEliminatedBatch
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if b.StageIndex <= 0 {
			t.Errorf("stageIndex=%d", b.StageIndex)
		}
		if len(b.Entries) == 0 {
			t.Fatal("entries が空。足切りなのに誰も入っていない")
		}
		if !jsonKeyIsArray(t, raw, "entries") {
			t.Error("entries が配列でない")
		}
		for _, e := range b.Entries {
			if e.FinalRank <= 0 {
				t.Errorf("%s の finalRank=%d", e.StoreId, e.FinalRank)
			}
			if e.Reason != proto.ElimCull {
				t.Errorf("%s の reason=%q, want %q（本戦の脱落経路は足切りのみ）",
					e.StoreId, e.Reason, proto.ElimCull)
			}
		}
	})

	t.Run("PersonalResult", func(t *testing.T) {
		raw, ok := rec.first(proto.TypePersonalResult)
		if !ok {
			t.Skip("届いていない（上の必須チェックで既に失敗している）")
		}
		var p proto.PersonalResult
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if p.FinalRank <= 0 {
			t.Errorf("finalRank=%d", p.FinalRank)
		}
		if p.SurvivedMs <= 0 {
			t.Errorf("survivedMs=%d", p.SurvivedMs)
		}
		// 廃止フィールド（本戦では値を入れない）
		assertZeroOnWireJSON(t, raw, "creditLeft", "evalRaw", "evalNormalized")
	})

	// ── 4. 配信順序（plan-h23 §3）──
	//
	// 「順位を配る前に脱落を配る」。逆だと脱落者を含んだ順位が一瞬表示される（予選 4-A）。
	t.Run("足切り時の順序", func(t *testing.T) {
		seq := rec.seq()
		bi := indexOf(seq, proto.TypeStoreEliminatedBatch)
		if bi < 0 {
			t.Skip("足切りが観測されていない")
		}
		// Batch より後に RankingSnapshot が来ること
		if ri := indexOfFrom(seq, proto.TypeRankingSnapshot, bi); ri < 0 {
			t.Error("StoreEliminatedBatch の後に RankingSnapshot が来ていない（4-E）")
		}
	})

	// ── 5. 試合終了は MatchEnd で締める ──
	t.Run("MatchEnd が最後", func(t *testing.T) {
		seq := rec.seq()
		ei := indexOf(seq, proto.TypeMatchEnd)
		if ei < 0 {
			t.Fatal("MatchEnd が届いていない")
		}
		// PersonalResult は MatchEnd より前（脱落と同時に届く約束・4-F）
		if pi := indexOf(seq, proto.TypePersonalResult); pi >= 0 && pi > ei {
			t.Error("PersonalResult が MatchEnd より後。脱落と同時に届く約束（4-F）に反する")
		}
	})

	t.Logf("受信サマリ: %s", summarize(rec))
}

// ── ヘルパ ─────────────────────────────────────────────

// startMatchAndConnect は solo 相当の試合を立て、実WSで1クライアントとして参加し、
// 試合が終わる（MatchEnd 受信 or ctx 期限）まで自動でプレイして受信を記録する。
func startMatchAndConnect(ctx context.Context, t *testing.T, rec *recorder) (*httptest.Server, proto.StoreId) {
	t.Helper()

	const selfId = proto.StoreId("p-1")
	const botCount = 4

	params := game.DefaultParameters()
	// 足切りスケジュールを**時間だけ**短縮する（20秒間隔 → 2秒間隔）。
	//
	// 検証したいのは「契約どおりのメッセージが飛ぶか」であって決着時間ではない。
	// 本番の 120 秒を実時間で待つとテストが2分かかるうえ、CI で回せない。
	// **6ステージ全部を通す**ので、足切り・PersonalResult・MatchEnd・順序はすべて観測できる。
	//
	// 目標生存数は参加人数（5）に合わせる。本番の 75/55/... のままだと
	// aliveCount <= target でスキップされ、足切りが1回も起きない。
	params.Cull.Stages = [game.CullStageCount]game.CullStage{
		{AtMs: 2000, TargetAliveCount: 4},
		{AtMs: 4000, TargetAliveCount: 3},
		{AtMs: 6000, TargetAliveCount: 2},
		{AtMs: 8000, TargetAliveCount: 1},
		{AtMs: 10000, TargetAliveCount: 1},
		{AtMs: 12000, TargetAliveCount: 0}, // 最終＝全店脱落
	}
	if err := params.Validate(); err != nil {
		t.Fatalf("短縮スケジュールが Validate を通らない: %v", err)
	}

	deps := app.DefaultDeps()
	deps.Params = params

	players := []matchmaking.Player{}
	upgraded := make(chan transport.Connection, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := transport.Accept(w, r, transport.AcceptOptions{AllowAll: true})
		if err != nil {
			return
		}
		upgraded <- conn
	})
	srv := httptest.NewServer(mux)

	// クライアント接続（生ワイヤ）
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws"
	cli, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	var serverSide transport.Connection
	select {
	case serverSide = <-upgraded:
	case <-time.After(5 * time.Second):
		t.Fatal("サーバー側の接続確立がタイムアウト")
	}

	players = append(players, matchmaking.Player{Id: selfId, Conn: serverSide, Name: "自分"})
	for i := 0; i < botCount; i++ {
		players = append(players, app.NewBotPlayer(ctx, proto.StoreId(fmt.Sprintf("b-%d", i+1)), bot.DefaultConfig()))
	}

	go app.RunMatch(ctx, deps, players)

	// 受信ループ：記録しつつ、お題が来たら即座に提供して試合を進める。
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = cli.Close(websocket.StatusNormalClosure, "") }()
		for {
			_, data, err := cli.Read(ctx)
			if err != nil {
				return
			}
			var env proto.Envelope
			if json.Unmarshal(data, &env) != nil {
				continue
			}
			rec.add(env.Type, env.Payload)

			switch env.Type {
			case proto.TypeCustomerArrived:
				var c proto.CustomerView
				if json.Unmarshal(env.Payload, &c) != nil {
					continue
				}
				// それらしい所要で提供する（サニティ検証の下限を下回らないように）
				served, _ := json.Marshal(proto.OrderServed{
					CustomerId:      c.CustomerId,
					ElapsedMs:       400 * c.OrderCount,
					MissCount:       1,
					ClientTimestamp: time.Now().UnixMilli(),
				})
				out, _ := json.Marshal(proto.Envelope{Type: proto.TypeOrderServed, Payload: served})
				_ = cli.Write(ctx, websocket.MessageText, out)
			case proto.TypeMatchEnd:
				return
			}
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("試合が終わらないままタイムアウト")
	}
	return srv, selfId
}

func assertZeroOnWireJSON(t *testing.T, raw json.RawMessage, keys ...string) {
	t.Helper()
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue // 省略されているのも「値を入れていない」
		}
		switch x := v.(type) {
		case float64:
			if x != 0 {
				t.Errorf("廃止フィールド %q に値が入っている: %v", k, x)
			}
		case string:
			if x != "" {
				t.Errorf("廃止フィールド %q に値が入っている: %q", k, x)
			}
		}
	}
}

// jsonKeyIsArray は「そのキーが JSON 配列で出ているか」を見る（null でないこと）。
func jsonKeyIsArray(t *testing.T, raw json.RawMessage, key string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(v)), "[")
}

func indexOf(seq []string, typ string) int { return indexOfFrom(seq, typ, 0) }

func indexOfFrom(seq []string, typ string, from int) int {
	for i := from; i < len(seq); i++ {
		if seq[i] == typ {
			return i
		}
	}
	return -1
}

func summarize(rec *recorder) string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	keys := make([]string, 0, len(rec.byTyp))
	for k := range rec.byTyp {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, len(rec.byTyp[k])))
	}
	return strings.Join(parts, " ")
}
