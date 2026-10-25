// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
)

// runBet 把 nil 與未結束的結果回報為錯誤，不 panic；完成的單段局直接交出。
func TestRunBetErrors(t *testing.T) {
	in := SpinInput{BetMode: 0, BetMult: 1}
	for _, c := range []struct {
		kind string
		want error
	}{{"nil", errNilSpinResult}, {"incomplete", errIncompleteGame}} {
		m := brokenResultSimulator(t, c.kind).mBuf[0]
		if res, err := runBet(m, buf.NewSpinAssembler(), in); res != nil || !errors.Is(err, c.want) {
			t.Fatalf("%s: runBet = %v, %v; want nil, %v", c.kind, res, err, c.want)
		}
	}
	m := faultMachine(t, false)
	res, err := runBet(m, buf.NewSpinAssembler(), in)
	if err != nil || res == nil || !res.IsGameEnd {
		t.Fatalf("finished game: runBet = %v, %v", res, err)
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
	s, err := lab.NewSimulator(0, MachineConfig{Seed: SeedInt64(42)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Sim runs on the calling goroutine, so its panic can be recovered here.
func TestSimPanicsOnBrokenResults(t *testing.T) {
	for _, c := range []struct{ kind, want string }{
		{"incomplete", "simulator: game 0 bet mode 0 round 0: incomplete game result"},
		{"nil", "simulator: game 0 bet mode 0 round 0: nil spin result"},
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
			{"incomplete", "simulator: game 0 bet mode 0 round 0: incomplete game result"},
			{"nil", "simulator: game 0 bet mode 0 round 0: nil spin result"},
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
