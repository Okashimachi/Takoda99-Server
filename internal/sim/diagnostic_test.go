package sim

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"takoda99/internal/game"
	"takoda99/internal/odai"
	"takoda99/internal/proto"
)

// TestDiagnostic_CustomerLifecycle は試合を丸ごとシミュレートし、
// 客・お題の配信状態を詳細にログ出力してサーバー側の問題を切り分けるテスト。
//
// チェック項目:
//   1. CustomerView に words が空のまま送られていないか
//   2. 同じ CustomerId が同時に2箇所以上に存在していないか
//   3. restPool が枯渇して客の配信が止まっていないか
//   4. 各店舗に客が偏りすぎていないか
//   5. 試合が正常に終了するか
func TestDiagnostic_CustomerLifecycle(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	sessRng := rand.New(rand.NewSource(rng.Int63()))

	params := game.DefaultParameters()
	params.Matching.ReadyCountdownMs = 0

	const storeCount = 99
	dummies := buildStores(storeCount, ProfileNormal, rng)
	inits := make([]game.PlayerInit, storeCount)
	byId := make(map[game.PlayerId]*dummyStore, storeCount)
	for i, d := range dummies {
		inits[i] = game.PlayerInit{Id: d.id, DisplayName: string(d.id)}
		byId[d.id] = d
	}

	words := odai.NewStaticPool()
	sess := game.NewSession("diag", params, words, sessRng, inits)
	tickMs := params.Session.TickIntervalMs

	// ── トラッキング用の構造 ─────────────────────────────
	type customerEvent struct {
		tick   int
		action string // "arrived" | "left" | "served"
		store  game.PlayerId
		cid    proto.CustomerId
		words  []string
	}

	var events []customerEvent
	activeCustomers := make(map[proto.CustomerId]game.PlayerId) // cid -> 現在いる店
	seenIds := make(map[proto.CustomerId]int)                   // cid -> 登場回数
	emptyWordCount := 0
	duplicateIdCount := 0
	totalArrived := 0
	totalLeft := 0
	totalServed := 0
	rejected := 0

	// 周期的にrestPoolの枯渇を検知するための変数
	ticksWithNoDistribution := 0
	maxTicksWithNoDistribution := 0
	lastDistributionTick := 0

	tick := 0

	handle := func(out []game.Outbound) {
		hadDistribution := false
		for _, o := range out {
			switch m := o.Msg.(type) {
			case proto.CustomerView:
				totalArrived++
				hadDistribution = true

				// チェック1: words が空でないか
				if len(m.Words) == 0 {
					emptyWordCount++
					t.Errorf("[tick=%d] ⚠ CustomerView words が空! cid=%s store=%s",
						tick, m.CustomerId, o.To.PlayerId)
				}

				// チェック2: 同じIDが既にアクティブでないか
				if existingStore, exists := activeCustomers[m.CustomerId]; exists {
					duplicateIdCount++
					t.Errorf("[tick=%d] ⚠ 重複ID検出! cid=%s が store=%s に既にいるのに store=%s にも配信",
						tick, m.CustomerId, existingStore, o.To.PlayerId)
				}
				activeCustomers[m.CustomerId] = o.To.PlayerId
				seenIds[m.CustomerId]++

				events = append(events, customerEvent{
					tick: tick, action: "arrived", store: o.To.PlayerId,
					cid: m.CustomerId, words: m.Words,
				})

				if d := byId[o.To.PlayerId]; d != nil {
					d.arrive(m)
				}

			case proto.CustomerLeft:
				totalLeft++
				delete(activeCustomers, m.CustomerId)
				events = append(events, customerEvent{
					tick: tick, action: "left", store: o.To.PlayerId,
					cid: m.CustomerId,
				})
				if d := byId[o.To.PlayerId]; d != nil {
					d.leave(m.CustomerId)
				}

			case proto.StoreEliminated:
				// 脱落した店の客をactiveCustomersから除去
				for cid, store := range activeCustomers {
					if store == game.PlayerId(m.StoreId) {
						delete(activeCustomers, cid)
					}
				}
				if d := byId[game.PlayerId(m.StoreId)]; d != nil {
					d.alive = false
				}
			}
		}

		if hadDistribution {
			lastDistributionTick = tick
			ticksWithNoDistribution = 0
		}
	}

	// ── 試合開始 ─────────────────────────────────────────
	handle(sess.Start(0))

	t.Logf("=== 診断テスト開始 === 店舗数=%d tickMs=%d", storeCount, tickMs)

	// 30秒ごと（周期的）にサマリーログを出力するための閾値
	logIntervalMs := 10000 // 10秒ごと
	nextLogMs := int64(logIntervalMs)

	maxTicks := 20000
	for tick = 1; tick <= maxTicks; tick++ {
		out := sess.Tick(tickMs)
		handle(out)

		// ダミー店の打鍵を進める
		for _, d := range dummies {
			o, done := d.step(tickMs, rng)
			if !done {
				continue
			}
			totalServed++
			delete(activeCustomers, o.CustomerId)
			events = append(events, customerEvent{
				tick: tick, action: "served", store: d.id, cid: o.CustomerId,
			})
			res := sess.ApplyOrderServed(d.id, o)
			if len(res) == 0 {
				rejected++
			}
			handle(res)
		}

		// 配信が無いtickを計測
		if tick-lastDistributionTick > ticksWithNoDistribution {
			ticksWithNoDistribution = tick - lastDistributionTick
			if ticksWithNoDistribution > maxTicksWithNoDistribution {
				maxTicksWithNoDistribution = ticksWithNoDistribution
			}
		}

		// 周期的にサマリーログ
		elapsed := sess.ElapsedMs()
		if elapsed >= nextLogMs {
			alive := sess.AliveCount()
			t.Logf("[%5.1fs | tick=%d] alive=%d arrived=%d left=%d served=%d rejected=%d active=%d emptyWords=%d dupeId=%d noDistFor=%dticks",
				float64(elapsed)/1000.0, tick, alive,
				totalArrived, totalLeft, totalServed, rejected,
				len(activeCustomers), emptyWordCount, duplicateIdCount,
				ticksWithNoDistribution)

			// チェック3: 長時間配信が無い場合に警告
			if ticksWithNoDistribution > 100 {
				t.Logf("  ⚠ 配信が %d tick (%dms) 止まっている！",
					ticksWithNoDistribution, ticksWithNoDistribution*tickMs)
			}

			nextLogMs += int64(logIntervalMs)
		}

		if sess.State() == game.Finished {
			t.Logf("=== 試合終了 === tick=%d elapsed=%.1fs", tick, float64(sess.ElapsedMs())/1000.0)
			break
		}
	}

	if sess.State() != game.Finished {
		t.Fatalf("⚠ %d tick で決着せず（膠着）", maxTicks)
	}

	// ── 最終サマリー ─────────────────────────────────────
	t.Logf("")
	t.Logf("========== 診断結果サマリー ==========")
	t.Logf("  総来店数 (CustomerView 送信):  %d", totalArrived)
	t.Logf("  総離脱数 (CustomerLeft):       %d", totalLeft)
	t.Logf("  総提供数 (OrderServed):        %d", totalServed)
	t.Logf("  拒否数   (Rejected):           %d", rejected)
	t.Logf("  お題空配信:                    %d", emptyWordCount)
	t.Logf("  ID重複検出:                    %d", duplicateIdCount)
	t.Logf("  ユニークID数:                  %d", len(seenIds))
	t.Logf("  最長配信停止:                  %d ticks (%dms)",
		maxTicksWithNoDistribution, maxTicksWithNoDistribution*tickMs)

	// ID再利用の分析
	reuseCount := 0
	maxReuse := 0
	for _, count := range seenIds {
		if count > 1 {
			reuseCount++
			if count > maxReuse {
				maxReuse = count
			}
		}
	}
	t.Logf("  再利用されたID数:              %d (最大 %d 回)", reuseCount, maxReuse)

	// もっとも再利用された上位10件
	if reuseCount > 0 {
		t.Logf("  --- 再利用上位10件 ---")
		type idCount struct {
			cid   proto.CustomerId
			count int
		}
		var top []idCount
		for cid, count := range seenIds {
			if count > 1 {
				top = append(top, idCount{cid, count})
			}
		}
		// ソート（バブル）
		for i := 0; i < len(top); i++ {
			for j := i + 1; j < len(top); j++ {
				if top[j].count > top[i].count {
					top[i], top[j] = top[j], top[i]
				}
			}
		}
		limit := 10
		if len(top) < limit {
			limit = len(top)
		}
		for i := 0; i < limit; i++ {
			t.Logf("    %s: %d回", top[i].cid, top[i].count)
		}
	}

	// ── アサーション ────────────────────────────────────
	if emptyWordCount > 0 {
		t.Errorf("❌ お題が空の CustomerView が %d 件あります（サーバーバグ）", emptyWordCount)
	}
	if duplicateIdCount > 0 {
		t.Errorf("❌ 同じ CustomerId が同時に複数箇所に存在するケースが %d 件（ID管理バグ）", duplicateIdCount)
	}
	if rejected > 0 {
		t.Errorf("❌ OrderServed が %d 件拒否されています（行列同期ズレ）", rejected)
	}

	// 10秒以上配信が止まると体験上問題
	if maxTicksWithNoDistribution*tickMs > 10000 {
		t.Errorf("❌ 最大 %dms（%d ticks）客の配信が止まっています。restPool枯渇の疑い",
			maxTicksWithNoDistribution*tickMs, maxTicksWithNoDistribution)
	}

	// 再利用分析のサマリーライン
	if reuseCount > 0 {
		t.Logf("⚠ %d 個のIDが再利用されています（クライアントでキャッシュ衝突の可能性）", reuseCount)
	} else {
		t.Logf("✅ IDの再利用なし")
	}
	t.Logf("======================================")

	// ── 時系列イベントのダンプ（最初の50件＋最後の50件）──────
	if len(events) > 0 {
		t.Logf("")
		t.Logf("--- イベント先頭50件 ---")
		limit := 50
		if len(events) < limit {
			limit = len(events)
		}
		for i := 0; i < limit; i++ {
			e := events[i]
			wordStr := ""
			if len(e.words) > 0 {
				wordStr = fmt.Sprintf(" words=[%s]", strings.Join(e.words, ","))
			}
			t.Logf("  [tick=%d] %s cid=%s store=%s%s",
				e.tick, e.action, e.cid, e.store, wordStr)
		}
		if len(events) > 100 {
			t.Logf("  ... （中略 %d 件） ...", len(events)-100)
			t.Logf("--- イベント末尾50件 ---")
			for i := len(events) - 50; i < len(events); i++ {
				e := events[i]
				wordStr := ""
				if len(e.words) > 0 {
					wordStr = fmt.Sprintf(" words=[%s]", strings.Join(e.words, ","))
				}
				t.Logf("  [tick=%d] %s cid=%s store=%s%s",
					e.tick, e.action, e.cid, e.store, wordStr)
			}
		}
	}
}
