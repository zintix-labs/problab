// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
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

// playBet：完成的局直接交出；player 停在選擇點時回報 ErrChooseModeRequired 並 Reset，
// 下一局可以直接開始；其他錯誤原樣交出。
func TestPlayBet(t *testing.T) {
	in := SpinInput{BetMode: 0, BetMult: 1}
	br, _, _ := chainRunner(t, 2)
	res, err := playBet(br, &in, ChoosePlayer)
	if res != nil || !errors.Is(err, ErrChooseModeRequired) || !isSettingError(err) {
		t.Fatalf("player at a choice point: %v, %v", res, err)
	}
	if br.state != runnerIdle {
		t.Fatalf("state %d after ErrChooseModeRequired", br.state)
	}
	if res, err := playBet(br, &in, ChooseFirst); err != nil || !res.IsGameEnd {
		t.Fatalf("first: %v, %v", res, err)
	}
	limited, _, _ := chainRunner(t, 3, WithMaxSegments(2))
	if _, err := playBet(limited, &in, ChooseLast); !errors.Is(err, ErrSegmentLimit) || !isSettingError(err) || limited.state != runnerIdle {
		t.Fatalf("limit: %v, state %d", err, limited.state)
	}
	rejected, logic, _ := chainRunner(t, 2)
	logic.nilAt = 0
	if _, err := playBet(rejected, &in, ChooseFirst); !errors.Is(err, ErrGameRejected) || isSettingError(err) {
		t.Fatalf("game rejection: %v", err)
	}
}

// simFault 補上遊戲、投注模式與局號，並保留原始錯誤。
func TestSimFault(t *testing.T) {
	_, cause := buf.NewSpinAssembler().Add(&buf.SpinResult{Cycle: 2, IsGameEnd: true, GameModeList: []*buf.GameModeResult{{}}, GameModeCount: 1})
	err := simFault(3, 1, 7, cause)
	if want := "simulator: game 3 bet mode 1 round 7: spin assembler: cycle out of order: got cycle 2, want 0"; err.Error() != want {
		t.Fatalf("simFault = %q, want %q", err, want)
	}
	if !errors.Is(err, buf.ErrCycleOutOfOrder) {
		t.Fatalf("simFault %v does not wrap %v", err, buf.ErrCycleOutOfOrder)
	}
}

// brokenResultLogic 包住 demo_normal：kind 為 "incomplete" 時結果不結束，為 "nil" 時回傳 nil。
type brokenResultLogic struct {
	inner slot.GameLogic
	kind  string
}

func (l *brokenResultLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	sr := l.inner.GetResult(r, g)
	if l.kind == "nil" {
		return nil
	}
	sr.IsGameEnd = false
	return sr
}

func brokenResultSimulator(t testing.TB, kind string) *Simulator {
	t.Helper()
	s, err := brokenResultLab(t, kind).NewSimulator(0, MachineConfig{Seed: SeedInt64(42)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// brokenResultLab 建立只有 game 0 的 Lab，遊戲邏輯為 brokenResultLogic。
func brokenResultLab(t testing.TB, kind string) *Problab {
	t.Helper()
	reg := slot.NewLogicRegistry()
	if err := reg.Register("demo_normal", func(g *slot.Game) (slot.GameLogic, error) {
		inner, err := demo_logic.Logics.Build("demo_normal", g)
		return &brokenResultLogic{inner: inner, kind: kind}, err
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lab, err := NewAuto(core.Default(), Configs(fstest.MapFS{"game.yaml": &fstest.MapFile{Data: raw}}), Logics(reg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

// Sim runs on the calling goroutine, so its panic can be recovered here.
func TestSimPanicsOnBrokenResults(t *testing.T) {
	for _, c := range []struct{ kind, want string }{
		{"incomplete", "game returned an inconsistent segment: IsGameEnd=false, len(ChooseNext)=0"},
		{"nil", "simulator: game 0 bet mode 0 round 0: game rejected the request"},
	} {
		s := brokenResultSimulator(t, c.kind)
		p := catchPanic(func() { _, _, _ = s.Sim(0, 10, false) })
		if err, ok := p.(error); !ok || err.Error() != c.want {
			t.Fatalf("%s: panic %v, want %q", c.kind, p, c.want)
		}
	}
}

const simWorkerPanicEnv = "PROBLAB_SIM_WORKER_PANIC"

// SimMP and SimPlayers panic on worker goroutines, which a test cannot recover
// without changing production code. The test re-runs itself in a subprocess and
// asserts that the subprocess exits with the expected panic.
func TestSimWorkersPanicOnBrokenResults(t *testing.T) {
	if run := os.Getenv(simWorkerPanicEnv); run != "" {
		entry, kind, _ := strings.Cut(run, "/")
		s := brokenResultSimulator(t, kind)
		switch entry {
		case "simmp":
			_, _, _ = s.SimMP(0, 10, 2, false)
		case "simplayers":
			_, _, _, _ = s.SimPlayers(2, 4, 50, 0, 10, false)
		}
		t.Fatal("the simulator returned instead of panicking")
	}
	for _, entry := range []string{"simmp", "simplayers"} {
		for _, c := range []struct{ kind, want string }{
			{"incomplete", "game returned an inconsistent segment: IsGameEnd=false, len(ChooseNext)=0"},
			{"nil", "simulator: game 0 bet mode 0 round 0: game rejected the request"},
		} {
			t.Run(fmt.Sprintf("%s/%s", entry, c.kind), func(t *testing.T) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestSimWorkersPanicOnBrokenResults$", "-test.count=1")
				cmd.Env = append(os.Environ(), simWorkerPanicEnv+"="+entry+"/"+c.kind)
				out, err := cmd.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() == 0 {
					t.Fatalf("subprocess err=%v, want a non-zero exit\n%s", err, out)
				}
				if !strings.Contains(string(out), "panic: "+c.want) {
					t.Fatalf("subprocess output lacks %q\n%s", "panic: "+c.want, out)
				}
			})
		}
	}
}

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
	for _, key := range []string{"demo_normal", "demo_cascade", "demo_choice", "demo_entry"} {
		if err := reg.Register(spec.LogicKey(key), func(g *slot.Game) (slot.GameLogic, error) {
			inner, err := demo_logic.Logics.Build(spec.LogicKey(key), g)
			return &callCountLogic{inner: inner, calls: calls}, err
		}); err != nil {
			t.Fatal(err)
		}
	}
	lab, err := NewAuto(core.Default(), Configs(demo_configs.FS), Logics(reg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab, calls
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
	requireWarnIs(t, "SimPlayers", o.err, ErrChooseModeRequired)
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

// SetCfg 從預設值開始整份取代；失敗時保留原設定；不動機台、RNG 與統計；reset 不清除設定。
func TestSimulatorSetCfg(t *testing.T) {
	s := choiceSimulator(t, demoChoiceLab(t), 3)
	before := coreState(t, s.mBuf[0])
	for _, st := range []struct {
		in   BetConfig
		want betCfg
	}{
		{BetConfig{Choose: ChooseFirst}, betCfg{ChooseFirst, 10}},
		{BetConfig{MaxSegments: 30}, betCfg{ChoosePlayer, 30}},
		{BetConfig{}, betCfg{ChoosePlayer, 10}},
		{BetConfig{Choose: ChooseLast, MaxSegments: 4}, betCfg{ChooseLast, 4}},
	} {
		if err := s.SetCfg(st.in); err != nil || s.cfg != st.want {
			t.Fatalf("SetCfg(%+v): cfg %+v, err %v", st.in, s.cfg, err)
		}
	}
	for _, bad := range []BetConfig{{Choose: 9}, {MaxSegments: -2}} {
		var e *errs.E
		if err := s.SetCfg(bad); !errors.As(err, &e) || e.ErrLv != errs.Warn || s.cfg != (betCfg{ChooseLast, 4}) {
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
}

// 預設的 demo 設定包含選擇類遊戲；未指定選擇方式時，每個入口都回傳 Warn 的
// ErrChooseModeRequired，不 panic，也不回傳報表。
func TestChoiceGameRequiresChooseMode(t *testing.T) {
	lab := demoChoiceLab(t)
	if !slices.Contains(lab.IDs(), demoChoiceID) {
		t.Fatal("the default demo configs do not include the choice game")
	}
	s := choiceSimulator(t, lab, 5)
	st, _, err := s.Sim(0, 2000, false)
	requireWarnIs(t, "Sim", err, ErrChooseModeRequired)
	if st != nil || !strings.Contains(err.Error(), "set choose mode first or last") {
		t.Fatalf("Sim: report %v, err %v", st, err)
	}
	st, _, err = s.SimMP(0, 2000, 3, false)
	requireWarnIs(t, "SimMP", err, ErrChooseModeRequired)
	if st != nil || strings.Count(err.Error(), "simulation stopped") != 1 {
		t.Fatalf("SimMP: report %v, err %v", st, err)
	}
	st, est, _, err := s.SimPlayers(2, 20, 100, 0, 200, false)
	requireWarnIs(t, "SimPlayers", err, ErrChooseModeRequired)
	if st != nil || est != nil {
		t.Fatal("SimPlayers returned reports after stopping")
	}
}

// 段數上限不足：Warn 的 ErrSegmentLimit，訊息帶上限並說明不是遊戲故障。
func TestChoiceGameSegmentLimit(t *testing.T) {
	s := choiceSimulator(t, demoChoiceLab(t), 5)
	if err := s.SetCfg(BetConfig{Choose: ChooseFirst, MaxSegments: 1}); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Sim(0, 2000, false)
	requireWarnIs(t, "Sim", err, ErrSegmentLimit)
	if msg := err.Error(); !strings.Contains(msg, "not a game fault") || !strings.Contains(msg, "segment limit reached: 1") {
		t.Fatalf("message %q", msg)
	}
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
	}
}
