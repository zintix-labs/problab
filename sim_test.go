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
