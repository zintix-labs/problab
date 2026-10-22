package playerexp

import (
	"testing"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/tag"
	"github.com/zintix-labs/problab/spec"
)

// 暫時的配置量測：只量 BenchmarkRunWithEventTags 同一份設定的準備期。
func TestAllocProbeCompile(t *testing.T) {
	c, err := ParseConfig([]byte(`game_id: 0
seed: 7
analysis:
  - name: g
    player: {players: 20, init_bet: 300, strategy: s}
    win_buckets: [0, 1, 2, 5]
    events:
      - {event: fg_big, win_range: [2, null], tags: [fg, big]}
      - {event: win, win_range: [0, null], tags: [win]}
      - {event: rest, win_range: [0, null]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if c, err = normalizeConfig(c); err != nil {
		t.Fatal(err)
	}
	o := Options{Strategies: NewStrategyRegistry(), Tags: map[spec.GID]map[string]tag.IsTag{0: {
		"fg":  func(sr *buf.SpinResult) bool { return sr.GameModeCount > 1 },
		"win": func(sr *buf.SpinResult) bool { return sr.TotalWin > 0 },
		"big": func(sr *buf.SpinResult) bool { return sr.TotalWin >= 3*sr.Bet },
	}}}
	if err := o.Strategies.Register(0, "s", fixedFactory(1)); err != nil {
		t.Fatal(err)
	}
	n := testing.AllocsPerRun(200, func() {
		if _, err := compile(c, o, []int{1}); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("compile allocs per run: %v", n)
}
