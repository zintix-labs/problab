package playerexp_test

// 臨時基準（分段執行重構驗收用，驗收後刪除）：錄製 playerexp 在 demo 遊戲上的完整報告。
// PROBLAB_RUN4A_BASELINE=update 時寫入 testdata，否則比對。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/playerexp"
	"github.com/zintix-labs/problab/playerexp/strategies"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/tag"
	"github.com/zintix-labs/problab/spec"
)

const run4aBaselinePath = "testdata/run4a_baseline.json"

func run4aReports(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	out := map[string]json.RawMessage{}
	for _, gid := range []spec.GID{0, 1} {
		c, err := playerexp.ParseConfig(fmt.Appendf(nil, `game_id: %d
seed: 7
analysis:
  - name: g
    player: {players: 30, init_bet: 300, strategy: flat}
    win_buckets: [0, 1, 2, 5]
    events:
      - {event: fg_big, win_range: [2, null], tags: [fg, big]}
      - {event: win, win_range: [0, null], tags: [win]}
      - {event: rest, win_range: [0, null]}
`, gid))
		if err != nil {
			t.Fatal(err)
		}
		rg := playerexp.NewStrategyRegistry()
		if err := rg.Register(gid, "flat", strategies.Monotone{BetMode: 0, BetMult: 2, MaxSpins: 300, SatisfiedRatio: 3}); err != nil {
			t.Fatal(err)
		}
		tags := map[spec.GID]map[string]tag.IsTag{gid: {
			"fg":  func(sr *buf.SpinResult) bool { return sr.GameModeCount > 1 },
			"win": func(sr *buf.SpinResult) bool { return sr.TotalWin > 0 },
			"big": func(sr *buf.SpinResult) bool { return sr.TotalWin >= 3*sr.Bet },
		}}
		r, err := playerexp.Run(context.Background(), c, playerexp.Options{Lab: lab, Strategies: rg, Tags: tags})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		out[fmt.Sprint(gid)] = raw
	}
	return out
}

func TestRun4aBaseline(t *testing.T) {
	got := run4aReports(t)
	if os.Getenv("PROBLAB_RUN4A_BASELINE") == "update" {
		raw, err := json.MarshalIndent(got, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(run4aBaselinePath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(run4aBaselinePath)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]json.RawMessage
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for k, w := range want {
		var a, b any
		if json.Unmarshal(got[k], &a) != nil || json.Unmarshal(w, &b) != nil {
			t.Fatalf("game %s: invalid json", k)
		}
		ga, _ := json.Marshal(a)
		wb, _ := json.Marshal(b)
		if string(ga) != string(wb) {
			t.Errorf("playerexp report for game %s differs", k)
		}
	}
}
