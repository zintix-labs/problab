package problab

// 臨時基準（分段執行重構驗收用，驗收後刪除）：錄製單段遊戲經 SpinUnsafe、Spin、
// Simulator 與 DevSimulator 的完整輸出、呼叫後的 RNG 位置與錯誤訊息。
// PROBLAB_RUN4A_BASELINE=update 時寫入 testdata，否則比對。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

const run4aBaselinePath = "testdata/run4a_baseline.json"

type b0Game struct {
	name    string
	optimal bool
	gid     spec.GID
}

var b0Games = []b0Game{{"normal", false, 0}, {"cascade", false, 1}, {"optimal", true, 0}}

// b0Run 是一串執行的紀錄：每次執行的結果雜湊與呼叫後的 RNG 位置雜湊。
type b0Run struct {
	Results []string
	Cores   []string
	Final   string
}

type b0Baseline struct {
	Unsafe    map[string]b0Run
	Spin      map[string]b0Run
	Errors    map[string]string
	Simulator map[string]json.RawMessage
	DevSim    map[string]json.RawMessage
}

func b0Hash(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:12])
}

func b0JSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func b0Err(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

// b0Lab 建立 deterministic 的 demo lab；optimal 時改用測試 manifest。
func b0Lab(t *testing.T, optimal bool, opts ...ProblabOption) *Problab {
	t.Helper()
	cfg := Configs(demo_configs.FS)
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

// b0ProdMachine 以可重現的 entropy 建立 production 機台（每次呼叫各自一份 lab）。
func b0ProdMachine(t *testing.T, g b0Game) *Machine {
	t.Helper()
	f, err := core.NewChaCha20Factory(newDetReader("b0-reseed"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Configs(demo_configs.FS)
	opts := []ProblabOption{WithSeedEntropy(newDetReader("b0-seed"))}
	if g.optimal {
		cfg = Configs(testManifestConfigFS(t))
		opts = append(opts, WithOptimalFS(testManifestFS(t, false)))
	}
	lab, err := NewAuto(f, cfg, Logics(demo_logic.Logics), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	m, err := newProductionMachineForTest(lab, g.gid)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func b0Machine(t *testing.T, lab *Problab, gid spec.GID, seed int64) *Machine {
	t.Helper()
	m, err := lab.NewMachine(gid, MachineConfig{Seed: SeedInt64(seed)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (r *b0Run) add(t *testing.T, result []byte, m *Machine) {
	t.Helper()
	r.Results = append(r.Results, b0Hash(result))
	r.Cores = append(r.Cores, b0Hash(coreState(t, m)))
}

func (r *b0Run) finish(t *testing.T, m *Machine) {
	t.Helper()
	r.Final = corefmt.EncodeBase64URL(coreState(t, m))
}

func b0Unsafe(t *testing.T, lab *Problab, g b0Game, capture bool) b0Run {
	t.Helper()
	m := b0Machine(t, lab, g.gid, 2026)
	var run b0Run
	for i := 0; i < 300; i++ {
		sr := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1 + i%3, CaptureSnapshots: capture})
		if sr == nil {
			t.Fatal("nil result")
		}
		d, err := dto.NewSpinResultDTO(sr)
		if err != nil {
			t.Fatal(err)
		}
		raw := b0JSON(t, d)
		raw = append(raw, fmt.Sprintf("|cycle=%d|end=%v", sr.Cycle, sr.IsGameEnd)...)
		run.add(t, raw, m)
	}
	run.finish(t, m)
	return run
}

func b0Request(m *Machine, mult int) *dto.SpinRequest {
	return &dto.SpinRequest{GameName: m.gameName, GameId: m.gameId, Bet: m.BetUnits[0] * mult, BetMode: 0, BetMult: mult}
}

// b0Spin 執行 200 次新局，再以其中 40 局的起始快照回放。
func b0Spin(t *testing.T, m *Machine) b0Run {
	t.Helper()
	var run b0Run
	var starts []string
	for i := 0; i < 200; i++ {
		res, err := m.Spin(b0Request(m, 1+i%3))
		if err != nil {
			t.Fatal(err)
		}
		run.add(t, b0JSON(t, res), m)
		if i%5 == 0 {
			starts = append(starts, res.State.StartCoreSnapB64U)
		}
	}
	for i, s := range starts {
		req := b0Request(m, 1+i%3)
		req.StartState = &dto.StartState{StartCoreSnapB64U: s}
		res, err := m.Spin(req)
		if err != nil {
			t.Fatal(err)
		}
		run.add(t, b0JSON(t, res), m)
	}
	run.finish(t, m)
	return run
}

func b0Errors(t *testing.T) map[string]string {
	t.Helper()
	lab := b0Lab(t, false)
	m := b0Machine(t, lab, 0, 77)
	first, err := m.Spin(b0Request(m, 1))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*dto.SpinRequest){
		"gid":           func(r *dto.SpinRequest) { r.GameId = 9 },
		"name":          func(r *dto.SpinRequest) { r.GameName = "x" },
		"betmode":       func(r *dto.SpinRequest) { r.BetMode = 5 },
		"bet":           func(r *dto.SpinRequest) { r.Bet++ },
		"snap/decode":   func(r *dto.SpinRequest) { r.StartState = &dto.StartState{StartCoreSnapB64U: "!!"} },
		"snap/rejected": func(r *dto.SpinRequest) { r.StartState = &dto.StartState{StartCoreSnapB64U: "AAAA"} },
		"checkpoint/decode": func(r *dto.SpinRequest) {
			r.StartState = &dto.StartState{StartCoreSnapB64U: first.State.StartCoreSnapB64U, Checkpoint: json.RawMessage(`{"a":1}`)}
		},
	}
	out := map[string]string{}
	for name, mutate := range cases {
		before := b0Hash(coreState(t, m))
		req := b0Request(m, 1)
		mutate(req)
		_, err := m.Spin(req)
		out[name] = b0Err(err)
		if b0Hash(coreState(t, m)) != before {
			out[name] += " [rng moved]"
		}
	}
	return out
}

func b0Simulator(t *testing.T, lab *Problab, g b0Game, seed int64) json.RawMessage {
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
			out[i] = b0Hash(coreState(t, m))
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
	return b0JSON(t, rec)
}

func b0DevSim(t *testing.T, lab *Problab, g b0Game, seed int64) json.RawMessage {
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
	return b0JSON(t, map[string]any{
		"spins": b0Hash(b0JSON(t, spins)), "restore": b0Hash(b0JSON(t, rr)),
		"spins_after": spins.After, "spins_rtp": spins.Rtp, "sim": sim,
		"machine": b0Hash(coreState(t, d.m)),
	})
}

func b0RecordAll(t *testing.T) b0Baseline {
	t.Helper()
	b := b0Baseline{
		Unsafe: map[string]b0Run{}, Spin: map[string]b0Run{},
		Simulator: map[string]json.RawMessage{}, DevSim: map[string]json.RawMessage{},
	}
	for _, g := range b0Games {
		lab := b0Lab(t, g.optimal)
		for _, capture := range []bool{false, true} {
			b.Unsafe[fmt.Sprintf("%s/capture=%v", g.name, capture)] = b0Unsafe(t, lab, g, capture)
		}
		b.Spin[g.name+"/deterministic"] = b0Spin(t, b0Machine(t, lab, g.gid, 2026))
		b.Spin[g.name+"/production"] = b0Spin(t, b0ProdMachine(t, g))
		b.Simulator[g.name] = b0Simulator(t, lab, g, 2026)
		b.DevSim[g.name] = b0DevSim(t, lab, g, 2026)
	}
	b.Errors = b0Errors(t)
	return b
}

func b0Canon(t *testing.T, b b0Baseline) b0Baseline {
	t.Helper()
	var out b0Baseline
	if err := json.Unmarshal(b0JSON(t, b), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func b0CompareRun(t *testing.T, kind, key string, g, w b0Run) {
	t.Helper()
	if reflect.DeepEqual(g, w) {
		return
	}
	for i := range w.Results {
		if i >= len(g.Results) || g.Results[i] != w.Results[i] || g.Cores[i] != w.Cores[i] {
			t.Errorf("%s %s: first difference at call %d", kind, key, i)
			return
		}
	}
	t.Errorf("%s %s: differ (lengths %d/%d, final equal %v)", kind, key, len(g.Results), len(w.Results), g.Final == w.Final)
}

func TestRun4aBaseline(t *testing.T) {
	got := b0RecordAll(t)
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
	var want b0Baseline
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got, want = b0Canon(t, got), b0Canon(t, want)
	for _, sec := range []struct {
		kind      string
		got, want map[string]b0Run
	}{{"unsafe", got.Unsafe, want.Unsafe}, {"spin", got.Spin, want.Spin}} {
		keys := make([]string, 0, len(sec.want))
		for k := range sec.want {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b0CompareRun(t, sec.kind, k, sec.got[k], sec.want[k])
		}
	}
	for k, w := range want.Simulator {
		if string(got.Simulator[k]) != string(w) {
			t.Errorf("simulator %s differ", k)
		}
	}
	for k, w := range want.DevSim {
		if string(got.DevSim[k]) != string(w) {
			t.Errorf("devsim %s differ", k)
		}
	}
	if !reflect.DeepEqual(got.Errors, want.Errors) {
		t.Errorf("errors differ:\n got %v\nwant %v", got.Errors, want.Errors)
	}
}
