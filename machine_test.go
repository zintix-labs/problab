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
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// stickyStateLogic 包住 demo 邏輯，但每次都把結果的 State 換成同一個物件，且不清除
// 其中的 RNG 快照，並在 Checkpoint 寫入呼叫次數。用來驗證 CaptureSnapshots 為 false
// 時，Machine 只清除前一次留下的 RNG 快照，不碰遊戲輸出的 Checkpoint。
type stickyStateLogic struct {
	inner slot.GameLogic
	state buf.SpinState
	calls int
}

func (l *stickyStateLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	sr := l.inner.GetResult(r, g)
	l.calls++
	l.state.Checkpoint = l.calls
	sr.State = &l.state
	return sr
}

func withStickyStateLogic(t testing.TB, m *Machine) *stickyStateLogic {
	t.Helper()
	key := m.gh.GameSetting.LogicKey
	logic := &stickyStateLogic{}
	reg := slot.NewLogicRegistry()
	if err := reg.Register(key, func(g *slot.Game) (slot.GameLogic, error) {
		inner, err := demo_logic.Logics.Build(key, g)
		logic.inner = inner
		return logic, err
	}); err != nil {
		t.Fatal(err)
	}
	gh, err := slot.NewGame(m.gh.GameSetting, reg, m.core, m.gh.OmitExtend())
	if err != nil {
		t.Fatal(err)
	}
	m.gh = gh
	return logic
}

func TestSpinUnsafeWithoutCaptureClearsOnlyRNGSnapshots(t *testing.T) {
	for _, optimal := range []bool{false, true} {
		t.Run(fmt.Sprint("optimal=", optimal), func(t *testing.T) {
			m := faultMachine(t, optimal)
			logic := withStickyStateLogic(t, m)

			captured := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: true})
			if captured.State != &logic.state || len(logic.state.StartCoreSnap) == 0 || len(logic.state.AfterCoreSnap) == 0 {
				t.Fatal("capturing call did not write snapshots into the game's State")
			}

			plain := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1})
			if plain.State != &logic.state {
				t.Fatal("result State was replaced")
			}
			if plain.State.StartCoreSnap != nil || plain.State.AfterCoreSnap != nil {
				t.Fatal("non-capturing call kept the previous RNG snapshots")
			}
			if plain.State.Checkpoint != 2 {
				t.Fatalf("non-capturing call changed the game's Checkpoint: %v", plain.State.Checkpoint)
			}
		})
	}
}

func TestSpinUnsafeCaptureReusesOwnedBuffers(t *testing.T) {
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
				r := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: true})
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

func TestSpinUnsafeCaptureParity(t *testing.T) {
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
				raw := b.SpinUnsafe(SpinInput{BetMode: mode, BetMult: mult, CaptureSnapshots: true})
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

// SpinUnsafe 不驗證輸入；抽 seed 失敗屬於內部執行故障：panic，不得靜默改用原始 RNG。
func TestSpinUnsafeClosedArtifactPanics(t *testing.T) {
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
	for _, capture := range []bool{false, true} {
		if p := catchPanic(func() { om.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: capture}) }); p == nil {
			t.Fatalf("capture=%t: closed artifact silently fell back", capture)
		}
	}
}

// 遊戲收到的請求契約：每個執行入口交給遊戲的請求內容。
//
// 探測遊戲包住 demo 的真實邏輯：先記錄收到的請求內容，再交給原邏輯執行，因此
// optimal 開啟時同樣可用。

// probeCheckpoint 是測試用的 checkpoint 型別。
type probeCheckpoint struct {
	Stage int `json:"stage"`
}

var registerProbeCheckpoint sync.Once

// probeRequest 是遊戲收到的請求內容的深拷貝。
type probeRequest struct {
	UID        string
	GameName   string
	GameId     spec.GID
	Bet        int
	BetMode    int
	BetMult    int
	Cycle      int
	Choice     int
	HasChoice  bool
	HasStart   bool   // StartState 是否非 nil
	StartSnap  []byte // StartState.StartCoreSnap 的複製
	Checkpoint any    // *probeCheckpoint 時複製為值
}

type probeLogic struct {
	inner slot.GameLogic
	log   *[]probeRequest
}

func (p *probeLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	rec := probeRequest{
		UID: r.UID, GameName: r.GameName, GameId: r.GameId,
		Bet: r.Bet, BetMode: r.BetMode, BetMult: r.BetMult,
		Cycle: r.Cycle, Choice: r.Choice, HasChoice: r.HasChoice,
	}
	if r.StartState != nil {
		rec.HasStart = true
		if r.StartState.StartCoreSnap != nil {
			rec.StartSnap = append([]byte{}, r.StartState.StartCoreSnap...)
		}
		if cp, ok := r.StartState.Checkpoint.(*probeCheckpoint); ok {
			rec.Checkpoint = *cp
		} else {
			rec.Checkpoint = r.StartState.Checkpoint
		}
	}
	*p.log = append(*p.log, rec)
	return p.inner.GetResult(r, g)
}

// probeRegistry 以探測邏輯包住 demo 的 demo_normal 與 demo_cascade。
func probeRegistry(t testing.TB, log *[]probeRequest) *slot.LogicRegistry {
	t.Helper()
	reg := slot.NewLogicRegistry()
	for _, key := range []spec.LogicKey{"demo_normal", "demo_cascade"} {
		if err := reg.Register(key, func(g *slot.Game) (slot.GameLogic, error) {
			inner, err := demo_logic.Logics.Build(key, g)
			if err != nil {
				return nil, err
			}
			return &probeLogic{inner: inner, log: log}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	registerProbeCheckpoint.Do(func() {
		if err := dto.RegisterCheckpoint[probeCheckpoint]("demo_cascade"); err != nil {
			t.Fatal(err)
		}
	})
	return reg
}

func probeOffLab(t testing.TB, log *[]probeRequest) *Problab {
	t.Helper()
	files := fstest.MapFS{}
	for _, name := range []string{"game_0_demonormal.yaml", "game_1_democascade.yaml"} {
		raw, err := fs.ReadFile(demo_configs.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: raw}
	}
	lab, err := NewAuto(core.Default(), Configs(files), Logics(probeRegistry(t, log)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

func probeManifestLab(t testing.TB, log *[]probeRequest) *Problab {
	t.Helper()
	lab, err := NewAuto(core.Default(), Configs(testManifestConfigFS(t)), Logics(probeRegistry(t, log)), WithOptimalFS(testManifestFS(t, false)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

func probeLast(t testing.TB, log []probeRequest) probeRequest {
	t.Helper()
	if len(log) == 0 {
		t.Fatal("game was not called")
	}
	return log[len(log)-1]
}

func requireProbe(t testing.TB, step string, got, want probeRequest) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: game received\n got=%+v\nwant=%+v", step, got, want)
	}
}

func TestGameReceivesRequestPerEntry(t *testing.T) {
	var log []probeRequest
	lab := probeOffLab(t, &log)
	m, err := lab.NewMachine(1, MachineConfig{Seed: SeedInt64(42)})
	if err != nil {
		t.Fatal(err)
	}
	unit := m.BetUnits[0]

	// 1. Spin 不帶快照：遊戲收到 DTO 的值，StartState 為 nil。
	plain := &dto.SpinRequest{UID: "u1", GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1}
	first, err := m.Spin(plain)
	if err != nil {
		t.Fatal(err)
	}
	requireProbe(t, "spin", probeLast(t, log), probeRequest{
		UID: "u1", GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1,
	})

	// 2. Spin 帶外部快照與 checkpoint，並帶選擇欄位：全部原樣交給遊戲。
	snap, err := corefmt.DecodeBase64URL(first.State.StartCoreSnapB64U)
	if err != nil {
		t.Fatal(err)
	}
	replay := &dto.SpinRequest{
		UID: "u2", GameName: m.gameName, GameId: m.gameId, Bet: unit * 2, BetMode: 0, BetMult: 2,
		Cycle: 2, Choice: 1, HasChoice: true,
		StartState: &dto.StartState{StartCoreSnapB64U: first.State.StartCoreSnapB64U, Checkpoint: json.RawMessage(`{"stage":3}`)},
	}
	if _, err := m.Spin(replay); err != nil {
		t.Fatal(err)
	}
	requireProbe(t, "spin-replay", probeLast(t, log), probeRequest{
		UID: "u2", GameName: m.gameName, GameId: m.gameId, Bet: unit * 2, BetMode: 0, BetMult: 2,
		Cycle: 2, Choice: 1, HasChoice: true,
		HasStart: true, StartSnap: snap, Checkpoint: probeCheckpoint{Stage: 3},
	})

	// 3. 緊接不擷取快照的 SpinUnsafe：不帶上一段的任何殘留。
	m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1})
	requireProbe(t, "unsafe-after-replay", probeLast(t, log), probeRequest{
		GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1,
	})

	// 4. 再帶外部快照與 checkpoint 的 Spin，緊接擷取快照、倍數 3 的 SpinUnsafe：同樣不帶殘留。
	if _, err := m.Spin(replay); err != nil {
		t.Fatal(err)
	}
	m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 3, CaptureSnapshots: true})
	requireProbe(t, "unsafe-capture-after-replay", probeLast(t, log), probeRequest{
		GameName: m.gameName, GameId: m.gameId, Bet: unit * 3, BetMode: 0, BetMult: 3,
	})

	// 5. 兩種 SpinUnsafe 交錯後再 Spin：Spin 不帶本機入口的殘留。
	m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1})
	if _, err := m.Spin(plain); err != nil {
		t.Fatal(err)
	}
	requireProbe(t, "spin-after-internal", probeLast(t, log), probeRequest{
		UID: "u1", GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1,
	})
	if len(log) != 7 {
		t.Fatalf("game calls=%d, want 7", len(log))
	}
}

func TestGameReceivesOptimalStartPerEntry(t *testing.T) {
	var log []probeRequest
	lab := probeManifestLab(t, &log)
	m, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(42)})
	if err != nil {
		t.Fatal(err)
	}
	unit := m.BetUnits[0]

	// Spin 抽庫：抽出的 seed 填進 StartState 交給遊戲，checkpoint 為 nil。
	res, err := m.Spin(&dto.SpinRequest{UID: "u1", GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := corefmt.DecodeBase64URL(res.State.StartCoreSnapB64U)
	if err != nil {
		t.Fatal(err)
	}
	got := probeLast(t, log)
	requireProbe(t, "spin-optimal", got, probeRequest{
		UID: "u1", GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1,
		HasStart: true, StartSnap: seed,
	})

	// SpinUnsafe 兩種快照模式抽庫：不把 seed 交給遊戲。
	m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1})
	requireProbe(t, "unsafe-optimal", probeLast(t, log), probeRequest{
		GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1,
	})
	sr := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 2, CaptureSnapshots: true})
	requireProbe(t, "unsafe-capture-optimal", probeLast(t, log), probeRequest{
		GameName: m.gameName, GameId: m.gameId, Bet: unit * 2, BetMode: 0, BetMult: 2,
	})
	// 擷取快照的結果仍記錄抽出的 seed 作為起始快照（不經由請求交給遊戲）；
	// 值由 U0 逐位元保護，這裡只確認存在。
	if len(sr.State.StartCoreSnap) == 0 {
		t.Fatal("offline optimal start snapshot missing")
	}
}
