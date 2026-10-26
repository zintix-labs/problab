package problab

// 臨時基準（分段執行 4b 驗收用，驗收後刪除）：錄製單段遊戲經 Simulator 與 DevSimulator
// 的完整報表與呼叫後的 RNG 位置。PROBLAB_RUN4B_BASELINE=update 時寫入 testdata，否則比對。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

const run4bBaselinePath = "testdata/run4b_baseline.json"

type b1Game struct {
	name    string
	optimal bool
	gid     spec.GID
}

var b1Games = []b1Game{{"normal", false, 0}, {"cascade", false, 1}, {"optimal", true, 0}}

type b1Baseline struct {
	Simulator map[string]json.RawMessage
	DevSim    map[string]json.RawMessage
}

func b1Hash(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:12])
}

func b1JSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// b1Lab 建立 deterministic 的 demo lab；optimal 時改用測試 manifest。
func b1Lab(t *testing.T, optimal bool) *Problab {
	t.Helper()
	cfg := Configs(demo_configs.FS)
	var opts []ProblabOption
	if optimal {
		cfg = Configs(testManifestConfigFS(t))
		opts = append(opts, WithOptimalFS(testManifestFS(t, false)))
	}
	lab, err := NewAuto(core.Default(), cfg, Logics(demo_logic.Logics), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

// b1Simulator 錄製 deterministic 的 Simulator 路徑：Sim、3 worker 的 SimMP（每個 worker
// 固定使用自己的機台與 Recorder）、單 worker 的 SimPlayers。
func b1Simulator(t *testing.T, lab *Problab, g b1Game, seed int64) json.RawMessage {
	t.Helper()
	newSim := func() *Simulator {
		s, err := lab.NewSimulator(g.gid, MachineConfig{Seed: SeedInt64(seed)})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	cores := func(s *Simulator) []string {
		out := make([]string, len(s.mBuf))
		for i, m := range s.mBuf {
			out[i] = b1Hash(coreState(t, m))
		}
		return out
	}
	rec := map[string]any{}
	s := newSim()
	st, _, err := s.Sim(0, 30000, false)
	if err != nil {
		t.Fatal(err)
	}
	rec["sim"], rec["sim_core"] = st, cores(s)
	s = newSim()
	st, _, err = s.SimMP(0, 20000, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	rec["simmp"], rec["simmp_core"] = st, cores(s)
	s = newSim()
	st, est, _, err := s.SimPlayers(1, 50, 100, 0, 500, false)
	if err != nil {
		t.Fatal(err)
	}
	rec["players"], rec["players_est"], rec["players_core"] = st, est, cores(s)
	return b1JSON(t, rec)
}

func b1DevSim(t *testing.T, lab *Problab, g b1Game, seed int64) json.RawMessage {
	t.Helper()
	d, err := lab.NewDevSimulator(g.gid, seed)
	if err != nil {
		t.Fatal(err)
	}
	spins, err := d.Spins(0, 1500)
	if err != nil {
		t.Fatal(err)
	}
	rr, err := d.RestoreSpins(spins.Before, 0, 1500)
	if err != nil {
		t.Fatal(err)
	}
	sim, err := d.Sim(0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	rsim, err := d.RestoreSim(sim.Before, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	return b1JSON(t, map[string]any{
		"spins": b1Hash(b1JSON(t, spins)), "restore": b1Hash(b1JSON(t, rr)),
		"spins_after": spins.After, "spins_rtp": spins.Rtp, "sim": sim, "restore_sim": rsim,
		"machine": b1Hash(coreState(t, d.m)),
	})
}

func b1RecordAll(t *testing.T) b1Baseline {
	t.Helper()
	b := b1Baseline{Simulator: map[string]json.RawMessage{}, DevSim: map[string]json.RawMessage{}}
	for _, g := range b1Games {
		lab := b1Lab(t, g.optimal)
		b.Simulator[g.name] = b1Simulator(t, lab, g, 2026)
		b.DevSim[g.name] = b1DevSim(t, lab, g, 2026)
	}
	return b
}

func TestRun4bBaseline(t *testing.T) {
	got := b1RecordAll(t)
	if os.Getenv("PROBLAB_RUN4B_BASELINE") == "update" {
		raw, err := json.MarshalIndent(got, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(run4bBaselinePath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(run4bBaselinePath)
	if err != nil {
		t.Fatal(err)
	}
	var want b1Baseline
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	// 兩邊都經同一種 JSON 正規化後比對。
	for _, sec := range []struct {
		kind      string
		got, want map[string]json.RawMessage
	}{{"simulator", got.Simulator, want.Simulator}, {"devsim", got.DevSim, want.DevSim}} {
		if len(sec.got) != len(sec.want) {
			t.Errorf("%s: %d entries, want %d", sec.kind, len(sec.got), len(sec.want))
		}
		for k, w := range sec.want {
			if string(compactJSON(t, sec.got[k])) != string(compactJSON(t, w)) {
				t.Errorf("%s %s differ", sec.kind, k)
			}
		}
	}
}

// compactJSON 以 any 解碼再編碼，使縮排與鍵的順序一致。
func compactJSON(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return b1JSON(t, v)
}
