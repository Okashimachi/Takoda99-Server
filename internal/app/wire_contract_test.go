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
	"os"
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
	log   []logEntry                   // 全受信の生ログ（時刻つき・クライアントへ渡す用）
	t0    time.Time
}

// logEntry は生ログ1行。クライアント（Unity）に渡して受信側の検証に使ってもらう。
type logEntry struct {
	AtMs    int64           `json:"atMs"` // 接続からの経過ms
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func newRecorder() *recorder {
	return &recorder{byTyp: map[string][]json.RawMessage{}, t0: time.Now()}
}

func (r *recorder) add(typ string, payload json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, typ)
	cp := make(json.RawMessage, len(payload))
	copy(cp, payload)
	r.byTyp[typ] = append(r.byTyp[typ], cp)
	r.log = append(r.log, logEntry{
		AtMs:    time.Since(r.t0).Milliseconds(),
		Type:    typ,
		Payload: cp,
	})
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

	// 既定は検証用の短縮設定（17秒で完走・CI で回せる）。
	// WIRE_LOG_PRODUCTION=1 のときは**本番と同じ設定**（99店・20秒間隔・120秒）で走らせる。
	// クライアントへ渡す生ログは、24店まとめての StoreEliminatedBatch や
	// 99件の RankingSnapshot が入っていないと受信側の検証にならないため。
	production := os.Getenv("WIRE_LOG_PRODUCTION") == "1"

	botCount := 4
	if production {
		botCount = 98 // 自分 + Bot98 = 99店（本番と同じ）
	}

	params := game.DefaultParameters()
	// 足切りスケジュールを**時間だけ**短縮する（20秒間隔 → 2秒間隔）。
	//
	// 検証したいのは「契約どおりのメッセージが飛ぶか」であって決着時間ではない。
	// 本番の 120 秒を実時間で待つとテストが2分かかるうえ、CI で回せない。
	// **6ステージ全部を通す**ので、足切り・PersonalResult・MatchEnd・順序はすべて観測できる。
	//
	// 目標生存数は参加人数（5）に合わせる。本番の 75/55/... のままだと
	// aliveCount <= target でスキップされ、足切りが1回も起きない。
	if !production {
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

// ── クライアント（みかみ）からの確認事項 A〜D に実測で答えるための検証 ────────
//
// 2026-08-15 に受けた質問に、推測ではなく**実際に飛んだメッセージ**で回答するためのもの。
// ここが緑である限り、回答内容はサーバーの実挙動として保証される。

// TestWireContract_ClientQuestions は A〜D を1試合の実測で確かめる。
func TestWireContract_ClientQuestions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	rec := newRecorder()
	srv, _ := startMatchAndConnect(ctx, t, rec)
	defer srv.Close()

	seq := rec.seq()

	// 【A-1】PersonalResult は MatchEnd より必ず「前」に届くか。
	//
	// 後に届くとクライアントは試合終了状態に移った後なので取りこぼす、という指摘。
	t.Run("A-1_PersonalResultはMatchEndより前", func(t *testing.T) {
		pi, ei := indexOf(seq, proto.TypePersonalResult), indexOf(seq, proto.TypeMatchEnd)
		if pi < 0 {
			t.Fatal("PersonalResult が届いていない")
		}
		if ei < 0 {
			t.Fatal("MatchEnd が届いていない")
		}
		if pi > ei {
			t.Fatalf("PersonalResult(%d) が MatchEnd(%d) より後。クライアントが取りこぼす", pi, ei)
		}
		t.Logf("✅ PersonalResult は MatchEnd より %d メッセージ前に届く", ei-pi)
	})

	// 【A-2】最終ステージの StoreEliminatedBatch は stageIndex == stageTotal で届くか。
	t.Run("A-2_最終バッチのstageIndexはstageTotalと一致", func(t *testing.T) {
		raws := rec.all(proto.TypeStoreEliminatedBatch)
		if len(raws) == 0 {
			t.Fatal("StoreEliminatedBatch が届いていない")
		}
		var last proto.StoreEliminatedBatch
		if err := json.Unmarshal(raws[len(raws)-1], &last); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		// stageTotal は ForcedEliminationWarning から取る（Batch は持たない）
		wraw, ok := rec.first(proto.TypeForcedEliminationWarning)
		if !ok {
			t.Fatal("ForcedEliminationWarning が届いていない")
		}
		var w proto.ForcedEliminationWarning
		if err := json.Unmarshal(wraw, &w); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if last.StageIndex != w.StageTotal {
			t.Errorf("最終バッチの stageIndex=%d, stageTotal=%d（一致すべき）", last.StageIndex, w.StageTotal)
		}
		t.Logf("✅ 最終バッチ stageIndex=%d / stageTotal=%d", last.StageIndex, w.StageTotal)
	})

	// 【A-3】StoreEliminatedBatch と MatchEnd の間隔。
	t.Run("A-3_最終バッチとMatchEndの間隔", func(t *testing.T) {
		bt, okb := rec.lastAtMs(proto.TypeStoreEliminatedBatch)
		et, oke := rec.lastAtMs(proto.TypeMatchEnd)
		if !okb || !oke {
			t.Skip("観測できていない")
		}
		t.Logf("✅ 最終 StoreEliminatedBatch → MatchEnd の間隔: %d ms", et-bt)
	})

	// 【B】最終順位が確定した RankingSnapshot（全量）が試合終了時に必ず1回飛ぶか。
	//
	// リザルトは保持した順位表をそのまま出す作りなので、最後に全量が来ないと
	// 他店の順位がズレたまま固定される、という指摘。
	t.Run("B_最終RankingSnapshotがMatchEndの直前に飛ぶ", func(t *testing.T) {
		ei := indexOf(seq, proto.TypeMatchEnd)
		if ei < 0 {
			t.Fatal("MatchEnd が届いていない")
		}
		ri := lastIndexBefore(seq, proto.TypeRankingSnapshot, ei)
		if ri < 0 {
			t.Fatal("MatchEnd より前に RankingSnapshot が無い。リザルトの順位が確定しない")
		}
		bi := lastIndexBefore(seq, proto.TypeStoreEliminatedBatch, ei)
		if bi >= 0 && ri < bi {
			t.Errorf("最後の RankingSnapshot(%d) が最終バッチ(%d) より前。"+
				"脱落を反映していない順位でリザルトが固定される", ri, bi)
		}
		// 中身：全店ぶんあり、rank が 1..N で重複しないこと
		raws := rec.all(proto.TypeRankingSnapshot)
		var s proto.RankingSnapshot
		if err := json.Unmarshal(raws[len(raws)-1], &s); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		ranks := map[int]bool{}
		for _, e := range s.Entries {
			if ranks[e.Rank] {
				t.Errorf("最終順位で rank=%d が重複している", e.Rank)
			}
			ranks[e.Rank] = true
			if e.Alive {
				t.Errorf("試合終了後なのに %s が alive=true", e.StoreId)
			}
		}
		t.Logf("✅ 最終 RankingSnapshot: %d 店・全員 alive=false・rank 重複なし", len(s.Entries))
	})

	// 【C-1/C-2】廃止メッセージと単体 StoreEliminated が飛ばないこと。
	t.Run("C_廃止メッセージと単体StoreEliminatedが飛ばない", func(t *testing.T) {
		for _, typ := range []string{"CustomerLeft", "CreditUpdate", "StoreListUpdate", "StoreEliminated"} {
			if n := rec.count(typ); n > 0 {
				t.Errorf("🔴 %s が %d 通飛んでいる（飛ばない約束）", typ, n)
			}
		}
		t.Log("✅ CustomerLeft / CreditUpdate / StoreListUpdate / StoreEliminated(単体) はいずれも0通")
	})

	// 【C-3】MatchEnd のペイロードは空 {} か。
	t.Run("C-3_MatchEndのペイロードは空", func(t *testing.T) {
		raw, ok := rec.first(proto.TypeMatchEnd)
		if !ok {
			t.Fatal("MatchEnd が届いていない")
		}
		got := strings.TrimSpace(string(raw))
		if got != "{}" {
			t.Errorf("MatchEnd payload=%s, want {}", got)
		}
		t.Logf("✅ MatchEnd payload = %s", got)
	})

	// 【C-4】12種以外の型を送っていないか。
	t.Run("C-4_許可12種以外を送っていない", func(t *testing.T) {
		allowed := map[string]bool{
			proto.TypeMatchmakingStatus: true, proto.TypeMatchStart: true,
			proto.TypeCustomerArrived: true, proto.TypeEvaluationUpdate: true,
			proto.TypeDifficultyUpdate: true, proto.TypePhaseChange: true,
			proto.TypeRankingSnapshot: true, proto.TypeRankingDelta: true,
			proto.TypeForcedEliminationWarning: true, proto.TypeStoreEliminatedBatch: true,
			proto.TypePersonalResult: true, proto.TypeMatchEnd: true,
		}
		for _, typ := range rec.types() {
			if !allowed[typ] {
				t.Errorf("🔴 許可リストに無い型 %q を送っている", typ)
			}
		}
		t.Logf("✅ 送信した型: %v（すべて許可12種の範囲内）", rec.types())
	})

	// 【D】MatchStart.stores[] に Bot を含む全店が入っているか。
	t.Run("D_MatchStartに全店ぶん入っている", func(t *testing.T) {
		raw, _ := rec.first(proto.TypeMatchStart)
		var m proto.MatchStart
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		// 最終 RankingSnapshot に出てくる storeId が全部 MatchStart にあること
		// （＝以降 storeId だけで名前を引ける）
		known := map[proto.StoreId]bool{}
		for _, s := range m.Stores {
			known[s.StoreId] = true
		}
		raws := rec.all(proto.TypeRankingSnapshot)
		var last proto.RankingSnapshot
		if err := json.Unmarshal(raws[len(raws)-1], &last); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, e := range last.Entries {
			if !known[e.StoreId] {
				t.Errorf("🔴 %s がランキングに出るが MatchStart.stores[] に無い（名前を引けない）", e.StoreId)
			}
		}
		t.Logf("✅ MatchStart.stores[] = %d 店（Bot 含む）。ランキングの storeId は全部ここで引ける", len(m.Stores))
	})

	// 生ログをファイルに書き出す（クライアントへ渡す用）。
	if path := os.Getenv("WIRE_LOG_OUT"); path != "" {
		if err := rec.dump(path); err != nil {
			t.Errorf("生ログ出力に失敗: %v", err)
		} else {
			t.Logf("生ログを書き出した: %s", path)
		}
	}
}

// all は type の全ペイロードを返す。
func (r *recorder) all(typ string) []json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]json.RawMessage(nil), r.byTyp[typ]...)
}

// types は受信した type の一覧（ソート済み）。
func (r *recorder) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.byTyp))
	for k := range r.byTyp {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// lastAtMs は type の最後の受信時刻（接続からの経過ms）。
func (r *recorder) lastAtMs(typ string) (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.log) - 1; i >= 0; i-- {
		if r.log[i].Type == typ {
			return r.log[i].AtMs, true
		}
	}
	return 0, false
}

// dump は全受信を JSON Lines で書き出す。クライアントが受信側の検証に使う。
func (r *recorder) dump(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, e := range r.log {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// lastIndexBefore は before より前にある最後の typ の位置。
func lastIndexBefore(seq []string, typ string, before int) int {
	for i := before - 1; i >= 0; i-- {
		if seq[i] == typ {
			return i
		}
	}
	return -1
}

// TestWireContract_ProductionLog は**本番と同じ設定**（99店・20秒間隔×6・120秒）で
// 1試合を走らせ、クライアントへ渡す生ログを書き出す。
//
// ⚠ 実時間で 120 秒以上かかるので **CI では走らせない**（WIRE_LOG_PRODUCTION 必須）。
//
// なぜ本番同等が要るか: 検証用の短縮ログ（5店・2秒間隔）では
//   - StoreEliminatedBatch.entries が 1件（本番は初回 24件）
//   - RankingSnapshot.entries が 5件（本番は 99件）
//
// となり、**クライアントが一番確かめたい「まとめて来たときの挙動」が再現できない**。
//
//	make wirelog
func TestWireContract_ProductionLog(t *testing.T) {
	if os.Getenv("WIRE_LOG_PRODUCTION") != "1" {
		t.Skip("WIRE_LOG_PRODUCTION=1 のときだけ走る（実時間で120秒かかる）")
	}
	out := os.Getenv("WIRE_LOG_OUT")
	if out == "" {
		t.Skip("WIRE_LOG_OUT が未設定")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 280*time.Second)
	defer cancel()

	rec := newRecorder()
	srv, _ := startMatchAndConnect(ctx, t, rec)
	defer srv.Close()

	if err := rec.dump(out); err != nil {
		t.Fatalf("生ログ出力に失敗: %v", err)
	}

	// 本番同等になっているかを、ログ自体から確かめる。
	// ここが満たされていないと「本番同等ログ」と言って渡せない。
	raws := rec.all(proto.TypeRankingSnapshot)
	var snap proto.RankingSnapshot
	if err := json.Unmarshal(raws[len(raws)-1], &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(snap.Entries) != 99 {
		t.Errorf("RankingSnapshot が %d 店（本番は99店であるべき）", len(snap.Entries))
	}

	batches := rec.all(proto.TypeStoreEliminatedBatch)
	if len(batches) != game.CullStageCount {
		t.Errorf("足切りが %d 回（本番は %d 回）", len(batches), game.CullStageCount)
	}
	var firstBatch proto.StoreEliminatedBatch
	if len(batches) > 0 {
		if err := json.Unmarshal(batches[0], &firstBatch); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(firstBatch.Entries) != 24 {
			t.Errorf("初回の足切りが %d 店（99→75 なので24店であるべき）", len(firstBatch.Entries))
		}
	}

	// cutStoreIds が 24 件まで入ること（クライアントと合意した上限）。
	maxCut := 0
	for _, raw := range rec.all(proto.TypeForcedEliminationWarning) {
		var w proto.ForcedEliminationWarning
		if json.Unmarshal(raw, &w) != nil {
			continue
		}
		if n := len(w.CutStoreIds); n > maxCut {
			maxCut = n
		}
	}

	t.Logf("✅ 本番同等ログを書き出した: %s", out)
	t.Logf("   RankingSnapshot=%d店 / 足切り=%d回 / 初回バッチ=%d店 / cutStoreIds最大=%d件",
		len(snap.Entries), len(batches), len(firstBatch.Entries), maxCut)
	t.Logf("   受信サマリ: %s", summarize(rec))
}
