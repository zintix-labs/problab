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

func TestAssembleOnePanics(t *testing.T) {
	seg := func(cycle int, end bool) *buf.SpinResult {
		return &buf.SpinResult{Cycle: cycle, IsGameEnd: end, GameModeList: []*buf.GameModeResult{{}}, GameModeCount: 1}
	}
	cases := []struct {
		name string
		sr   *buf.SpinResult
		want string
		is   error
	}{
		{"nil", nil, "simulator: game 3 bet mode 1 round 7: nil spin result", nil},
		{"incomplete", seg(0, false), "simulator: game 3 bet mode 1 round 7: incomplete game result", nil},
		{"cycle", seg(2, true), "simulator: game 3 bet mode 1 round 7: spin assembler: cycle out of order: got cycle 2, want 0", buf.ErrCycleOutOfOrder},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := catchPanic(func() { assembleOne(buf.NewSpinAssembler(), c.sr, 3, 1, 7) })
			err, ok := p.(error)
			if !ok || err.Error() != c.want {
				t.Fatalf("panic %v, want %q", p, c.want)
			}
			if c.is != nil && !errors.Is(err, c.is) {
				t.Fatalf("panic %v does not wrap %v", err, c.is)
			}
		})
	}
	finished := seg(0, true)
	if got := assembleOne(buf.NewSpinAssembler(), finished, 3, 1, 7); got != finished {
		t.Fatal("a finished single segment must pass through")
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
