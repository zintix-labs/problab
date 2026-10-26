// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package buf_test

import (
	"encoding/json"
	"testing"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

// TestSpinAssemblerWithMachineResults relabels two consecutive real spins of one
// machine as segments 0 and 1. The second spin reuses and overwrites the first
// spin's buffers, so the assembled game proves its copies are independent; real
// results also bring real TmpAct values, Details with stale tails and Extend.
func TestSpinAssemblerWithMachineResults(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	for _, gid := range []spec.GID{0, 1} {
		m, err := lab.NewMachine(gid, problab.MachineConfig{Seed: problab.SeedInt64(7)})
		if err != nil {
			t.Fatal(err)
		}
		if m.OmitExtend() {
			t.Fatal("the test needs Extend")
		}
		a := buf.NewSpinAssembler()
		in := problab.SpinInput{BetMode: 0, BetMult: 1}
		multi, extend := 0, 0
		for i := range 3000 {
			r1 := m.SpinUnsafe(in)
			d1 := toDTO(t, r1)
			r1.Cycle, r1.IsGameEnd = 0, false
			if res, err := a.Add(r1); res != nil || err != nil {
				t.Fatalf("game %d pair %d: first segment returned %v, %v", gid, i, res, err)
			}
			r2 := m.SpinUnsafe(in)
			if r2 != r1 {
				t.Fatal("the machine is expected to reuse its result buffer")
			}
			d2 := toDTO(t, r2)
			r2.Cycle = 1
			res, err := a.Add(r2)
			if err != nil || res == nil || res == r2 {
				t.Fatalf("game %d pair %d: second segment returned %v, %v", gid, i, res, err)
			}
			got := toDTO(t, res)
			want := d1
			want.TotalWin = d1.TotalWin + d2.TotalWin
			want.GameModes = append(append([]dto.GameModeResultDTO(nil), d1.GameModes...), d2.GameModes...)
			want.IsGameEnd = true
			want.Cycle = 1 // 完整局的 Cycle 取最後一段
			if mustJSON(t, got) != mustJSON(t, want) {
				t.Fatalf("game %d pair %d: assembled game differs\n got=%s\nwant=%s", gid, i, mustJSON(t, got), mustJSON(t, want))
			}
			if res.GameModeCount != len(d1.GameModes)+len(d2.GameModes) {
				t.Fatalf("game %d pair %d: mode count %d", gid, i, res.GameModeCount)
			}
			if len(res.GameModeList) > 2 {
				multi++
			}
			for _, g := range res.GameModeList {
				for _, act := range g.ActResults {
					if act.ExtendResult != nil {
						extend++
					}
				}
			}
		}
		if multi == 0 || extend == 0 {
			t.Fatalf("game %d: coverage too small: %d games with more than two modes, %d acts with Extend", gid, multi, extend)
		}
		t.Logf("game %d: %d of 3000 assembled games have more than two modes; %d acts carry Extend", gid, multi, extend)
	}
}

func toDTO(t *testing.T, sr *buf.SpinResult) dto.SpinResult {
	t.Helper()
	res, err := dto.NewSpinResultDTO(sr)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
