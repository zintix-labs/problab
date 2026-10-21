// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package problab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/sdk/core"
)

type offlineScratchPRNG struct {
	core.PRNG
	scratch []byte
}

func (p *offlineScratchPRNG) Snapshot() ([]byte, error) {
	b, err := p.PRNG.Snapshot()
	p.scratch = append(p.scratch[:0], b...)
	return p.scratch, err
}

func TestSpinOfflineReusesOwnedBuffers(t *testing.T) {
	for _, optimal := range []bool{false, true} {
		t.Run(fmt.Sprint(optimal), func(t *testing.T) {
			lab, err := NewAuto(core.Default(), Configs(testManifestConfigFS(t)), Logics(demo_logic.Logics), WithOptimalFS(testManifestFS(t, false)))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lab.Close() }()
			m, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(77)})
			if err != nil {
				t.Fatal(err)
			}
			if !optimal {
				m.optimal = nil
			}
			p := &offlineScratchPRNG{PRNG: m.core.PRNG}
			m.core.PRNG = p
			var startPtr, afterPtr *byte
			for i := 0; i < 5; i++ {
				r, err := m.SpinOffline(0, 1)
				if err != nil {
					t.Fatal(err)
				}
				start, after := r.State.StartCoreSnap, r.State.AfterCoreSnap
				if len(start) == 0 || len(after) == 0 {
					t.Fatal("missing snapshots")
				}
				if i > 0 && (startPtr != &start[0] || afterPtr != &after[0]) {
					t.Fatal("buffers reallocated")
				}
				startPtr, afterPtr = &start[0], &after[0]
				savedStart, savedAfter := bytes.Clone(start), bytes.Clone(after)
				// Scratch mutation must not corrupt either borrowed result snapshot.
				for j := range p.scratch {
					p.scratch[j] ^= 0xff
				}
				if !bytes.Equal(start, savedStart) || !bytes.Equal(after, savedAfter) {
					t.Fatal("PRNG scratch aliases result")
				}
				if startPtr == afterPtr {
					t.Fatal("start and after alias")
				}
			}
		})
	}
}

func TestSpinOfflineParity(t *testing.T) {
	for _, optimal := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "optimal"}[optimal], func(t *testing.T) {
			cfg, err := fs.ReadFile(testManifestConfigFS(t), "game.yaml")
			if err != nil {
				t.Fatal(err)
			}
			cfg = []byte(strings.Replace(string(cfg), "bet_units : [40]", "bet_units : [40, 80]", 1))
			artifacts := testManifestFS(t, false).(fstest.MapFS)
			var manifest optimalrt.Manifest
			if err = json.Unmarshal(artifacts["game0/manifest.json"].Data, &manifest); err != nil {
				t.Fatal(err)
			}
			second := manifest.Modes[0]
			second.BetUnit = 80
			manifest.Modes = append(manifest.Modes, second)
			artifacts["game0/manifest.json"].Data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			lab, err := NewAuto(core.Default(), Configs(fstest.MapFS{"game.yaml": &fstest.MapFile{Data: cfg}}), Logics(demo_logic.Logics), WithOptimalFS(artifacts))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lab.Close() }()
			var a, b *Machine
			if optimal {
				a, err = lab.NewMachine(0, MachineConfig{Seed: SeedInt64(77)})
			} else {
				a, err = lab.NewMachine(0, MachineConfig{Seed: SeedInt64(77), Unoptimized: true})
			}
			if err != nil {
				t.Fatal(err)
			}
			if optimal {
				b, err = lab.NewMachine(0, MachineConfig{Seed: SeedInt64(77)})
			} else {
				b, err = lab.NewMachine(0, MachineConfig{Seed: SeedInt64(77), Unoptimized: true})
			}
			if err != nil {
				t.Fatal(err)
			}
			if a.UsesOptimal() != optimal {
				t.Fatal("source mismatch")
			}
			if len(a.BetUnits) != 2 {
				t.Fatal("multi-mode fixture was not applied")
			}
			for i := 0; i < 60; i++ {
				mode := i % len(a.BetUnits)
				mult := 1 + i%3
				r, err := a.Spin(&dto.SpinRequest{GameId: a.gameId, GameName: a.gameName, BetMode: mode, BetMult: mult, Bet: a.BetUnits[mode] * mult})
				if err != nil {
					t.Fatal(err)
				}
				raw, err := b.SpinOffline(mode, mult)
				if err != nil {
					t.Fatal(err)
				}
				r2, err := dto.NewSpinResultDTO(raw)
				if err != nil {
					t.Fatal(err)
				}
				x, _ := json.Marshal(r)
				y, _ := json.Marshal(r2)
				if !bytes.Equal(x, y) {
					t.Fatalf("spin %d DTO mismatch", i)
				}
				sx, _ := a.SnapshotCore()
				sy, _ := b.SnapshotCore()
				if !bytes.Equal(sx, sy) {
					t.Fatalf("spin %d RNG mismatch", i)
				}
			}
		})
	}
}

func TestSpinOfflineValidationAndOptimalError(t *testing.T) {
	lab := newOneGameLab(t, core.Default())
	m, err := lab.NewMachine(1, MachineConfig{Seed: SeedInt64(77)})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := m.SnapshotCore()
	for _, b := range [][2]int{{-1, 1}, {len(m.BetUnits), 1}, {0, 0}, {0, -1}, {0, int(^uint(0) >> 1)}} {
		if _, err := m.SpinOffline(b[0], b[1]); err == nil {
			t.Fatalf("accepted %v", b)
		}
	}
	after, _ := m.SnapshotCore()
	if !bytes.Equal(before, after) {
		t.Fatal("invalid bet consumed RNG")
	}
	m.rngMode = rngProduction
	if _, err := m.SpinOffline(0, 1); err == nil {
		t.Fatal("accepted production")
	}
	optLab, err := NewAuto(core.Default(), Configs(testManifestConfigFS(t)), Logics(demo_logic.Logics), WithOptimalFS(testManifestFS(t, false)))
	if err != nil {
		t.Fatal(err)
	}
	om, err := optLab.NewMachine(0, MachineConfig{Seed: SeedInt64(77)})
	if err != nil {
		t.Fatal(err)
	}
	if err := optLab.Close(); err != nil {
		t.Fatal(err)
	}
	// 抽 seed 失敗屬於內部執行故障：panic，不得靜默改用原始 RNG。
	if p := catchPanic(func() { _, _ = om.SpinOffline(0, 1) }); p == nil {
		t.Fatal("closed artifact silently fell back")
	}
}
