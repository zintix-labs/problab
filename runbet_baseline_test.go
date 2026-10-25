package problab

// 臨時基準（runbet 重構驗收用，驗收後刪除）：錄製 DevSimulator 與 Simulator 的完整輸出、
// 最終 RNG 位置與錯誤訊息。PROBLAB_RUNBET_BASELINE=update 時寫入 testdata，否則比對。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

const runBetBaselinePath = "testdata/runbet_baseline.json"

type rbSpins struct {
	Before, After     string
	Round             int
	Rtp               float64
	TotalBet          int
	TotalWin          int
	BaseWin           int
	FreeWin           int
	ResultsHash       string
	ResultHashes      []string
	MachineAfter      string // 批次後 DevSimulator 機台的 core 快照
	RestoreInput      string // RestoreSpins 使用的輸入（舊版的 Before）
	RestoreReportHash string // RestoreSpins 完整報告（不含 Results）
	RestoreResults    string // RestoreSpins 的 Results 雜湊
	RestoreMachine    string
}

type rbSim struct {
	Report        json.RawMessage
	Restore       json.RawMessage
	RestoreInput  string
	MachineAfter  string
	RestoreMAfter string
}

type rbSimulator struct {
	Sim        json.RawMessage
	SimCore    []string
	SimMP      json.RawMessage
	SimMPCore  []string
	Players    json.RawMessage
	PlayersEst json.RawMessage
	PlayerCore []string
}

type rbBaseline struct {
	Spins     map[string]rbSpins
	Sim       map[string]rbSim
	Simulator map[string]rbSimulator
	Errors    map[string]string
}

type rbGame struct {
	name    string
	optimal bool
	gid     spec.GID
}

var rbGames = []rbGame{{"normal", false, 0}, {"cascade", false, 1}, {"optimal", true, 0}}

func rbLab(t *testing.T, optimal bool) *Problab {
	t.Helper()
	cfg := Configs(demo_configs.FS)
	opts := []ProblabOption{}
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

func rbHash(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:12])
}

func rbJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func rbCore(t *testing.T, m *Machine) string {
	t.Helper()
	return corefmt.EncodeBase64URL(coreState(t, m))
}

func rbErr(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func rbSpinsRecord(t *testing.T, lab *Problab, g rbGame, seed int64) rbSpins {
	t.Helper()
	d, err := lab.NewDevSimulator(g.gid, seed)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := d.Spins(0, 2000)
	if err != nil {
		t.Fatal(err)
	}
	hs := make([]string, len(rep.Results))
	for i, r := range rep.Results {
		hs[i] = rbHash(r)
	}
	rec := rbSpins{
		Before: rep.Before, After: rep.After, Round: rep.Round, Rtp: rep.Rtp,
		TotalBet: rep.TotalBet, TotalWin: rep.TotalWin, BaseWin: rep.BaseWin, FreeWin: rep.FreeWin,
		ResultsHash: rbHash(rep.Results), ResultHashes: hs, MachineAfter: rbCore(t, d.m),
	}
	// RestoreSpins 固定使用舊版的 Before 字串，比對時以同一個輸入重跑。
	rec.RestoreInput = rep.Before
	return rec
}

func rbRestoreSpins(t *testing.T, lab *Problab, g rbGame, seed int64, input string) (DevSpinReport, string) {
	t.Helper()
	d, err := lab.NewDevSimulator(g.gid, seed+1) // 換 seed，確認還原與建構時的 seed 無關
	if err != nil {
		t.Fatal(err)
	}
	rep, err := d.RestoreSpins(input, 0, 2000)
	if err != nil {
		t.Fatal(err)
	}
	return rep, rbCore(t, d.m)
}

func rbHead(r DevSpinReport) DevSpinReport {
	r.Results = nil
	return r
}

func rbSimRecord(t *testing.T, lab *Problab, g rbGame, seed int64) rbSim {
	t.Helper()
	d, err := lab.NewDevSimulator(g.gid, seed)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := d.Sim(0, 20000)
	if err != nil {
		t.Fatal(err)
	}
	rec := rbSim{Report: rbJSON(t, rep), RestoreInput: rep.Before, MachineAfter: rbCore(t, d.sim.mBuf[0])}
	d2, err := lab.NewDevSimulator(g.gid, seed+1)
	if err != nil {
		t.Fatal(err)
	}
	rr, err := d2.RestoreSim(rep.Before, 0, 20000)
	if err != nil {
		t.Fatal(err)
	}
	rec.Restore = rbJSON(t, rr)
	rec.RestoreMAfter = rbCore(t, d2.sim.mBuf[0])
	return rec
}

func rbSimulatorRecord(t *testing.T, lab *Problab, g rbGame, seed int64) rbSimulator {
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
			out[i] = rbCore(t, m)
		}
		return out
	}
	var rec rbSimulator
	s := newSim()
	st, _, err := s.Sim(0, 50000, false)
	if err != nil {
		t.Fatal(err)
	}
	rec.Sim, rec.SimCore = rbJSON(t, st), cores(s)

	s = newSim()
	st, _, err = s.SimMP(0, 20000, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	rec.SimMP, rec.SimMPCore = rbJSON(t, st), cores(s)

	// 單一機台：玩家分配到機台的順序才固定。
	s = newSim()
	st, est, _, err := s.SimPlayers(1, 50, 100, 0, 500, false)
	if err != nil {
		t.Fatal(err)
	}
	rec.Players, rec.PlayersEst, rec.PlayerCore = rbJSON(t, st), rbJSON(t, est), cores(s)
	return rec
}

func rbErrors(t *testing.T, lab *Problab) map[string]string {
	t.Helper()
	d, err := lab.NewDevSimulator(0, 77)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	_, err = d.Spins(5, 10)
	out["spins/betmode"] = rbErr(err)
	_, err = d.Spins(-1, 10)
	out["spins/betmode-neg"] = rbErr(err)
	_, err = d.Spins(0, 0)
	out["spins/round0"] = rbErr(err)
	_, err = d.Spins(0, 5001)
	out["spins/round5001"] = rbErr(err)
	_, err = d.RestoreSpins("!!", 0, 10)
	out["restorespins/decode"] = rbErr(err)
	_, err = d.RestoreSpins("AAAA", 0, 10)
	out["restorespins/core"] = rbErr(err)
	_, err = d.RestoreSpins(corefmt.EncodeBase64URL(coreState(t, d.m)), 0, 0)
	out["restorespins/round0"] = rbErr(err)
	_, err = d.Sim(5, 10)
	out["sim/betmode"] = rbErr(err)
	_, err = d.Sim(0, 0)
	out["sim/round0"] = rbErr(err)
	_, err = d.RestoreSim("!!", 0, 10)
	out["restoresim/decode"] = rbErr(err)
	_, err = d.RestoreSim("AAAA", 0, 10)
	out["restoresim/core"] = rbErr(err)
	return out
}

func rbRecordAll(t *testing.T) rbBaseline {
	t.Helper()
	b := rbBaseline{Spins: map[string]rbSpins{}, Sim: map[string]rbSim{}, Simulator: map[string]rbSimulator{}}
	for _, g := range rbGames {
		lab := rbLab(t, g.optimal)
		for _, seed := range []int64{77, 2026} {
			key := fmt.Sprintf("%s/%d", g.name, seed)
			rec := rbSpinsRecord(t, lab, g, seed)
			rr, rm := rbRestoreSpins(t, lab, g, seed, rec.RestoreInput)
			rec.RestoreReportHash, rec.RestoreResults, rec.RestoreMachine = rbHash(rbHead(rr)), rbHash(rr.Results), rm
			b.Spins[key] = rec
			b.Sim[key] = rbSimRecord(t, lab, g, seed)
			b.Simulator[key] = rbSimulatorRecord(t, lab, g, seed)
		}
	}
	b.Errors = rbErrors(t, rbLab(t, false))
	return b
}

func TestRunBetBaseline(t *testing.T) {
	got := rbRecordAll(t)
	if os.Getenv("PROBLAB_RUNBET_BASELINE") == "update" {
		raw, err := json.MarshalIndent(got, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(runBetBaselinePath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(runBetBaselinePath)
	if err != nil {
		t.Fatal(err)
	}
	var want rbBaseline
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	// 兩邊都經 JSON 往返（Marshal 會壓縮 RawMessage），讓格式一致。
	got, want = rbCanon(t, got), rbCanon(t, want)
	for k, w := range want.Spins {
		g := got.Spins[k]
		if !reflect.DeepEqual(g, w) {
			t.Errorf("spins %s differ:\n got %s\nwant %s", k, rbBrief(g), rbBrief(w))
		}
	}
	for k, w := range want.Sim {
		if !reflect.DeepEqual(got.Sim[k], w) {
			t.Errorf("dev sim %s differ", k)
		}
	}
	for k, w := range want.Simulator {
		if !reflect.DeepEqual(got.Simulator[k], w) {
			t.Errorf("simulator %s differ", k)
		}
	}
	if !reflect.DeepEqual(got.Errors, want.Errors) {
		t.Errorf("errors differ:\n got %v\nwant %v", got.Errors, want.Errors)
	}
}

func rbBrief(s rbSpins) string {
	s.ResultHashes = nil
	return fmt.Sprintf("%+v", s)
}

func rbCanon(t *testing.T, b rbBaseline) rbBaseline {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var out rbBaseline
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
