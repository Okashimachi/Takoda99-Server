package main

import (
	"bytes"
	"io"
	"math/rand"
	"strings"
	"testing"

	"takoda99/internal/game"
	"takoda99/internal/sim"
)

// シミュレータ本体のテストは internal/sim にある。ここは CLI 層（フラグ解釈とレポート）だけ。

func simulateForTest(t *testing.T, stores int, p sim.Profile, seed int64) runResult {
	t.Helper()
	return runResult{
		Result: sim.Simulate(sim.Config{
			Params:   game.DefaultParameters(),
			Stores:   stores,
			Profile:  p,
			Rng:      rand.New(rand.NewSource(seed)),
			MaxTicks: 20000,
		}),
		seed: seed,
	}
}

// 膠着した試行はエラーで返すこと（気付かず素通りさせない）。
func TestRun_StalledIsAnError(t *testing.T) {
	err := run(io.Discard, 20, "normal", 1, 1, 1 /*maxTicks*/, true, 120, 180)
	if err == nil {
		t.Fatal("max-ticks=1 なのに膠着が報告されない")
	}
	if !strings.Contains(err.Error(), "膠着") {
		t.Fatalf("膠着以外のエラーになった: %v", err)
	}
}

func TestRun_RejectsBadFlags(t *testing.T) {
	if err := run(io.Discard, 1, "normal", 1, 1, 100, true, 120, 180); err == nil {
		t.Error("--stores=1 が弾かれない")
	}
	if err := run(io.Discard, 20, "fast", 1, 1, 100, true, 120, 180); err == nil {
		t.Error("未知の --profile が弾かれない")
	}
	if err := run(io.Discard, 20, "normal", 0, 1, 100, true, 120, 180); err == nil {
		t.Error("--runs=0 が弾かれない")
	}
	if err := run(io.Discard, 20, "normal", 1, 1, 0, true, 120, 180); err == nil {
		t.Error("--max-ticks=0 が弾かれない")
	}
}

func TestRun_SucceedsOnNormalRun(t *testing.T) {
	var buf bytes.Buffer
	if err := run(&buf, 20, "normal", 2, 7, 20000, true, 120, 180); err != nil {
		t.Fatalf("正常な試行が失敗した: %v", err)
	}
	if !strings.Contains(buf.String(), "2 runs") {
		t.Errorf("サマリが出ていない:\n%s", buf.String())
	}
}

func TestReportRun_ShowsBalanceMaterial(t *testing.T) {
	r := simulateForTest(t, 20, sim.ProfileNormal, 2)

	var buf bytes.Buffer
	reportRun(&buf, r, 1)
	out := buf.String()

	for _, want := range []string{"決着", "最終フェーズ", "最終heatLevel", "優勝", "脱落内訳", "生存数の推移"} {
		if !strings.Contains(out, want) {
			t.Errorf("レポートに %q が無い:\n%s", want, out)
		}
	}
}

func TestReportSummary_FlagsOutOfTargetRuns(t *testing.T) {
	results := []runResult{
		{Result: sim.Result{Profile: sim.ProfileNormal, Stores: 99, ElapsedMs: 30000, Ticks: 200}},
		{Result: sim.Result{Profile: sim.ProfileNormal, Stores: 99, ElapsedMs: 150000, Ticks: 1000}},
	}
	var buf bytes.Buffer
	reportSummary(&buf, results, 120, 180)
	out := buf.String()

	if !strings.Contains(out, "1/2") {
		t.Errorf("目安内の件数が 1/2 になっていない:\n%s", out)
	}
	if !strings.Contains(out, "❌") {
		t.Errorf("目安を外れたのに ❌ が出ていない:\n%s", out)
	}
}

func TestAliveDeciles_IsMonotonicAndDeduped(t *testing.T) {
	r := runResult{Result: sim.Result{
		Stores: 10,
		AliveCurve: []sim.AlivePoint{
			{Tick: 0, Alive: 10}, {Tick: 5, Alive: 7}, {Tick: 9, Alive: 3}, {Tick: 12, Alive: 1},
		},
	}}
	got := aliveDeciles(r)
	if len(got) == 0 {
		t.Fatal("生存数の推移が空")
	}
	seen := map[int]bool{}
	prevAlive := r.Stores + 1
	for _, s := range got {
		if seen[s.Tick] {
			t.Fatalf("同じ tick が重複している: %d", s.Tick)
		}
		seen[s.Tick] = true
		if s.Alive > prevAlive {
			t.Fatalf("生存数が増えている: %d → %d", prevAlive, s.Alive)
		}
		prevAlive = s.Alive
	}
	if got[len(got)-1].Alive != 1 {
		t.Fatalf("最後が生存1店で終わっていない: %+v", got[len(got)-1])
	}
}
