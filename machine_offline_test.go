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
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
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
			m, err := lab.NewMachineWithSeed(0, 77, false)
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
				a, err = lab.NewMachineWithSeed(0, 77, false)
			} else {
				a, err = lab.NewUnoptimizedMachineWithSeed(0, 77, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if optimal {
				b, err = lab.NewMachineWithSeed(0, 77, false)
			} else {
				b, err = lab.NewUnoptimizedMachineWithSeed(0, 77, false)
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

type offlineFaultPRNG struct {
	core.PRNG
	snapshots, restores   int
	snapshotAt, restoreAt int
	fault                 error
}

func (p *offlineFaultPRNG) Snapshot() ([]byte, error) {
	p.snapshots++
	if p.snapshots == p.snapshotAt {
		return nil, p.fault
	}
	return p.PRNG.Snapshot()
}
func (p *offlineFaultPRNG) Restore(b []byte) error {
	p.restores++
	if p.restores == p.restoreAt {
		return p.fault
	}
	return p.PRNG.Restore(b)
}

type offlinePanicLogic struct{}

func (offlinePanicLogic) GetResult(*buf.SpinRequest, *slot.Game) *buf.SpinResult { panic("game fault") }

func TestSpinOfflineSnapshotRestoreFaults(t *testing.T) {
	for _, phase := range []string{"snapshot-before", "snapshot-after", "restore-seed", "restore-stream", "panic", "panic-restore"} {
		t.Run(phase, func(t *testing.T) {
			lab, e := NewAuto(core.Default(), Configs(testManifestConfigFS(t)), Logics(demo_logic.Logics), WithOptimalFS(testManifestFS(t, false)))
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = lab.Close() }()
			m, e := lab.NewMachineWithSeed(0, 77, false)
			if e != nil {
				t.Fatal(e)
			}
			fault := errors.New("injected PRNG failure")
			p := &offlineFaultPRNG{PRNG: m.core.PRNG, fault: fault}
			m.core.PRNG = p
			switch phase {
			case "snapshot-before":
				p.snapshotAt = 1
			case "snapshot-after":
				p.snapshotAt = 2
			case "restore-seed":
				p.restoreAt = 1
			case "restore-stream":
				p.restoreAt = 2
			case "panic", "panic-restore":
				if phase == "panic-restore" {
					p.restoreAt = 2
				}
				reg := slot.NewLogicRegistry()
				if e = reg.Register(m.gh.GameSetting.LogicKey, func(*slot.Game) (slot.GameLogic, error) { return offlinePanicLogic{}, nil }); e != nil {
					t.Fatal(e)
				}
				m.gh, e = slot.NewGame(m.gh.GameSetting, reg, m.core, false)
				if e != nil {
					t.Fatal(e)
				}
			}
			if phase == "panic" || phase == "panic-restore" {
				// Even a panic must restore the sampler's post-selection stream.
				control, e := lab.NewMachineWithSeed(0, 77, false)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = control.optimal.PickSeed(0, control.core); e != nil {
					t.Fatal(e)
				}
				want, _ := control.SnapshotCore()
				func() {
					defer func() {
						caught := recover()
						if caught == nil {
							t.Fatal("panic swallowed")
						}
						if phase == "panic-restore" {
							cause, ok := caught.(error)
							if !ok || !errors.Is(cause, fault) {
								t.Fatal("panic lost restore error", caught)
							}
						}
					}()
					_, _ = m.SpinOffline(0, 1)
				}()
				if phase == "panic-restore" {
					return
				}
				got, _ := m.SnapshotCore()
				if p.restores != 2 || !bytes.Equal(got, want) {
					t.Fatal("panic lost selector RNG")
				}
				return
			}
			r, e := m.SpinOffline(0, 1)
			if r != nil || !errors.Is(e, fault) {
				t.Fatal("error lost or fallback", r, e)
			}
			if phase == "restore-seed" && p.restores != 2 {
				t.Fatal("did not restore selector on failed seed restore")
			}
		})
	}
}

func TestSpinOfflineValidationAndOptimalError(t *testing.T) {
	lab := newOneGameLab(t, core.Default())
	m, err := lab.NewMachineWithSeed(1, 77, false)
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
	om, err := optLab.NewMachineWithSeed(0, 77, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := optLab.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := om.SpinOffline(0, 1); err == nil {
		t.Fatal("closed artifact silently fell back")
	}
}
