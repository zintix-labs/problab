package problab

import (
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
	"github.com/zintix-labs/problab/stats"
)

// callCountLogic 包住遊戲邏輯，以 atomic 計數 GetResult 的呼叫次數，作為 worker 是否仍在
// 執行的證據。
type callCountLogic struct {
	inner slot.GameLogic
	calls *atomic.Int64
}

func (l *callCountLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	l.calls.Add(1)
	return l.inner.GetResult(r, g)
}

// callCountChoiceLab 建立 demo_choice 的 Lab，所有機台共用同一個呼叫計數。
func callCountChoiceLab(t testing.TB) (*Problab, *atomic.Int64) {
	t.Helper()
	calls := new(atomic.Int64)
	reg := slot.NewLogicRegistry()
	for _, key := range []string{"demo_normal", "demo_cascade", "demo_choice"} {
		if err := reg.Register(spec.LogicKey(key), func(g *slot.Game) (slot.GameLogic, error) {
			inner, err := demo_logic.Logics.Build(spec.LogicKey(key), g)
			return &callCountLogic{inner: inner, calls: calls}, err
		}); err != nil {
			t.Fatal(err)
		}
	}
	lab, err := NewAuto(core.Default(), Configs(demo_configs.FS, demo_configs.StagedFS), Logics(reg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab, calls
}

func choiceSimulator(t testing.TB, lab *Problab, seed int64) *Simulator {
	t.Helper()
	s, err := lab.NewSimulator(demoChoiceID, MachineConfig{Seed: SeedInt64(seed)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func choiceDevSimulator(t testing.TB, lab *Problab, seed int64) *DevSimulator {
	t.Helper()
	d, err := lab.NewDevSimulator(demoChoiceID, seed)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func requireWarn(t *testing.T, step string, err, target error) {
	t.Helper()
	var e *errs.E
	if !errors.Is(err, target) || !errors.As(err, &e) || e.ErrLv != errs.Warn {
		t.Fatalf("%s: err %v, want a Warn wrapping %v", step, err, target)
	}
}

func TestBetConfigResolve(t *testing.T) {
	for _, c := range []struct {
		in   BetConfig
		want betCfg
	}{
		{BetConfig{}, betCfg{ChoosePlayer, DefaultMaxSegments}},
		{BetConfig{Choose: ChooseFirst}, betCfg{ChooseFirst, DefaultMaxSegments}},
		{BetConfig{MaxSegments: 30}, betCfg{ChoosePlayer, 30}},
		{BetConfig{Choose: ChooseLast, MaxSegments: 2}, betCfg{ChooseLast, 2}},
	} {
		if got, err := c.in.resolve(); err != nil || got != c.want {
			t.Fatalf("resolve(%+v) = %+v, %v", c.in, got, err)
		}
	}
	for _, bad := range []BetConfig{{Choose: 3}, {MaxSegments: -1}} {
		if _, err := bad.resolve(); err == nil {
			t.Fatalf("resolve(%+v) accepted", bad)
		}
	}
}

// SetCfg 從預設值開始整份取代；失敗時保留原設定；不動機台、RNG 與統計；reset 不清除設定。
func TestSimulatorSetCfg(t *testing.T) {
	s := choiceSimulator(t, demoChoiceLab(t), 3)
	before := coreState(t, s.mBuf[0])
	steps := []struct {
		in   BetConfig
		want betCfg
	}{
		{BetConfig{Choose: ChooseFirst}, betCfg{ChooseFirst, 10}},
		{BetConfig{MaxSegments: 30}, betCfg{ChoosePlayer, 30}},
		{BetConfig{}, betCfg{ChoosePlayer, 10}},
		{BetConfig{Choose: ChooseLast, MaxSegments: 4}, betCfg{ChooseLast, 4}},
	}
	for _, st := range steps {
		if err := s.SetCfg(st.in); err != nil || s.cfg != st.want {
			t.Fatalf("SetCfg(%+v): cfg %+v, err %v", st.in, s.cfg, err)
		}
	}
	for _, bad := range []BetConfig{{Choose: 9}, {MaxSegments: -2}} {
		err := s.SetCfg(bad)
		var e *errs.E
		if !errors.As(err, &e) || e.ErrLv != errs.Warn || s.cfg != (betCfg{ChooseLast, 4}) {
			t.Fatalf("SetCfg(%+v): err %v, cfg %+v", bad, err, s.cfg)
		}
	}
	requireCore(t, s.mBuf[0], before)
	if _, _, err := s.Sim(0, 50, false); err != nil {
		t.Fatal(err)
	}
	if s.cfg != (betCfg{ChooseLast, 4}) {
		t.Fatal("a run cleared the settings")
	}

	d := choiceDevSimulator(t, demoChoiceLab(t), 3)
	if err := d.SetCfg(BetConfig{Choose: ChooseFirst, MaxSegments: 5}); err != nil || d.cfg != d.sim.cfg || d.cfg != (betCfg{ChooseFirst, 5}) {
		t.Fatalf("DevSimulator.SetCfg: %+v %+v %v", d.cfg, d.sim.cfg, err)
	}
	if err := d.SetCfg(BetConfig{MaxSegments: -1}); err == nil || d.cfg != (betCfg{ChooseFirst, 5}) || d.sim.cfg != d.cfg {
		t.Fatal("a rejected DevSimulator.SetCfg changed the settings")
	}
}

// 未指定選擇方式：每個入口都回傳 Warn 的 ErrChooseModeRequired，不 panic，也不回傳報表。
func TestChoiceGameRequiresChooseMode(t *testing.T) {
	lab := demoChoiceLab(t)
	s := choiceSimulator(t, lab, 5)
	st, _, err := s.Sim(0, 2000, false)
	requireWarn(t, "Sim", err, ErrChooseModeRequired)
	if st != nil || !strings.Contains(err.Error(), "set choose mode first or last") {
		t.Fatalf("Sim: report %v, err %v", st, err)
	}
	st, _, err = s.SimMP(0, 2000, 3, false)
	requireWarn(t, "SimMP", err, ErrChooseModeRequired)
	if st != nil || strings.Count(err.Error(), "simulation stopped") != 1 {
		t.Fatalf("SimMP: report %v, err %v", st, err)
	}
	st, est, _, err := s.SimPlayers(2, 20, 100, 0, 200, false)
	requireWarn(t, "SimPlayers", err, ErrChooseModeRequired)
	if st != nil || est != nil {
		t.Fatal("SimPlayers returned reports after stopping")
	}
	_, err = choiceDevSimulator(t, lab, 5).Spins(0, 2000)
	requireWarn(t, "Spins", err, ErrChooseModeRequired)
}

// 段數上限不足：Warn 的 ErrSegmentLimit，訊息帶上限並說明不是遊戲故障。
func TestChoiceGameSegmentLimit(t *testing.T) {
	lab := demoChoiceLab(t)
	s := choiceSimulator(t, lab, 5)
	if err := s.SetCfg(BetConfig{Choose: ChooseFirst, MaxSegments: 1}); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Sim(0, 2000, false)
	requireWarn(t, "Sim", err, ErrSegmentLimit)
	if msg := err.Error(); !strings.Contains(msg, "not a game fault") || !strings.Contains(msg, "segment limit reached: 1") {
		t.Fatalf("message %q", msg)
	}
	d := choiceDevSimulator(t, lab, 5)
	if err := d.SetCfg(BetConfig{Choose: ChooseLast, MaxSegments: 1}); err != nil {
		t.Fatal(err)
	}
	_, err = d.Spins(0, 2000)
	requireWarn(t, "Spins", err, ErrSegmentLimit)
}

// 指定 first、last：一筆下注只計一次投注與一局，報表標示選擇方式。
func TestChoiceGameFullGameStatistics(t *testing.T) {
	lab := demoChoiceLab(t)
	for _, mode := range []ChooseMode{ChooseFirst, ChooseLast} {
		s := choiceSimulator(t, lab, 9)
		if err := s.SetCfg(BetConfig{Choose: mode}); err != nil {
			t.Fatal(err)
		}
		st, _, err := s.Sim(0, 3000, false)
		if err != nil {
			t.Fatal(err)
		}
		sum := st.Summary
		if sum.Rounds != 3000 || sum.TotalBet != 3000*s.gs.BetUnits[0] || sum.Choose != mode.String() || sum.Trigger == 0 {
			t.Fatalf("%v: %+v", mode, sum)
		}
		mp, _, err := s.SimMP(0, 1000, 3, false)
		if err != nil || mp.Summary.Rounds != 3000 || mp.Summary.Choose != mode.String() {
			t.Fatalf("%v SimMP: %v %v", mode, mp, err)
		}
		pl, est, _, err := s.SimPlayers(2, 10, 100, 0, 100, false)
		if err != nil || est == nil || pl.Summary.Choose != mode.String() {
			t.Fatalf("%v SimPlayers: %v %v", mode, pl, err)
		}

		d := choiceDevSimulator(t, lab, 9)
		if err := d.SetCfg(BetConfig{Choose: mode}); err != nil {
			t.Fatal(err)
		}
		rep, err := d.Spins(0, 1500)
		if err != nil {
			t.Fatal(err)
		}
		multi := 0
		for _, r := range rep.Results {
			if r.Cycle > 0 {
				multi++
			}
		}
		if rep.Round != 1500 || len(rep.Results) != 1500 || rep.TotalBet != 1500*s.gs.BetUnits[0] || rep.Choose != mode.String() || multi == 0 {
			t.Fatalf("%v Spins: round %d results %d bet %d choose %q multi %d", mode, rep.Round, len(rep.Results), rep.TotalBet, rep.Choose, multi)
		}
		again, err := d.RestoreSpins(rep.Before, 0, 1500)
		if err != nil || mustJSONString(t, again) != mustJSONString(t, rep) {
			t.Fatalf("%v: RestoreSpins differs: %v", mode, err)
		}
		sim, err := d.Sim(0, 2000)
		if err != nil || sim.Stat.Summary.Choose != mode.String() {
			t.Fatalf("%v DevSim.Sim: %v", mode, err)
		}
	}
}

// 單段遊戲指定 first：報表除選擇方式外與未指定時相同。
func TestSingleSegmentGameWithChooseMode(t *testing.T) {
	lab := demoChoiceLab(t)
	report := func(c BetConfig) *stats.StatReport {
		s, err := lab.NewSimulator(0, MachineConfig{Seed: SeedInt64(4)})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetCfg(c); err != nil {
			t.Fatal(err)
		}
		st, _, err := s.Sim(0, 3000, false)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	plain, first := report(BetConfig{}), report(BetConfig{Choose: ChooseFirst})
	if plain.Summary.Choose != "" || first.Summary.Choose != "first" {
		t.Fatalf("labels %q %q", plain.Summary.Choose, first.Summary.Choose)
	}
	raw, err := json.Marshal(plain)
	if err != nil || strings.Contains(string(raw), `"Choose"`) {
		t.Fatalf("an unspecified report outputs Choose: %s", raw)
	}
	first.Summary.Choose = ""
	if mustJSONString(t, plain) != mustJSONString(t, first) {
		t.Fatal("first changed a single-segment report")
	}
}

// 多 worker 停止：玩家數超過 jobs 的緩衝時，派工端不阻塞；返回後沒有 worker 繼續執行；
// 失敗後同一個 Simulator 的下一次執行沒有殘留狀態。
func TestSimPlayersStopsAllWorkers(t *testing.T) {
	lab, calls := callCountChoiceLab(t)
	s := choiceSimulator(t, lab, 13)
	type out struct {
		st  *stats.StatReport
		est *stats.EstimatorPlayers
		err error
	}
	done := make(chan out, 1)
	go func() {
		st, est, _, err := s.SimPlayers(4, 3000, 100, 0, 50, false)
		done <- out{st, est, err}
	}()
	var o out
	select {
	case o = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("SimPlayers did not return after a worker stopped")
	}
	requireWarn(t, "SimPlayers", o.err, ErrChooseModeRequired)
	if o.st != nil || o.est != nil {
		t.Fatal("SimPlayers returned reports after stopping")
	}
	n := calls.Load()
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != n {
		t.Fatal("a worker kept running after SimPlayers returned")
	}

	// 下一次執行：沒有殘留的停止訊號與統計；RNG 從失敗後的位置繼續。
	if err := s.SetCfg(BetConfig{Choose: ChooseFirst}); err != nil {
		t.Fatal(err)
	}
	st, est, _, err := s.SimPlayers(4, 30, 100, 0, 50, false)
	if err != nil || st == nil || est == nil {
		t.Fatalf("the next run: %v", err)
	}
	if st.Summary.Rounds > 30*50 || st.Summary.TotalBet != st.Summary.Rounds*s.gs.BetUnits[0] {
		t.Fatalf("the next run: %+v", st.Summary)
	}
}

// 單 worker：失敗後的下一次執行等於「從同一個執行前位置開始」的全新 Simulator。
func TestSimAfterStopContinuesFromPosition(t *testing.T) {
	lab := demoChoiceLab(t)
	s := choiceSimulator(t, lab, 17)
	if _, _, err := s.Sim(0, 2000, false); !errors.Is(err, ErrChooseModeRequired) {
		t.Fatalf("Sim: %v", err)
	}
	pos := coreState(t, s.mBuf[0])
	if err := s.SetCfg(BetConfig{Choose: ChooseFirst}); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Sim(0, 1000, false)
	if err != nil {
		t.Fatal(err)
	}
	fresh := choiceSimulator(t, lab, 99)
	if err := fresh.mBuf[0].RestoreCore(pos); err != nil {
		t.Fatal(err)
	}
	if err := fresh.SetCfg(BetConfig{Choose: ChooseFirst}); err != nil {
		t.Fatal(err)
	}
	want, _, err := fresh.Sim(0, 1000, false)
	if err != nil {
		t.Fatal(err)
	}
	if mustJSONString(t, got) != mustJSONString(t, want) {
		t.Fatal("the run after a stop differs from a fresh run at the same position")
	}
}
