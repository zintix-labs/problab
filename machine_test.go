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
	GameName   string
	GameId     spec.GID
	Bet        int
	BetMode    int
	BetMult    int
	Cycle      int
	Choice     int
	ChooseNext []int // 複製；空時為 nil
	Checkpoint any   // *probeCheckpoint 時複製為值
}

type probeLogic struct {
	inner slot.GameLogic
	log   *[]probeRequest
}

func (p *probeLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	rec := probeRequest{
		GameName: r.GameName, GameId: r.GameId,
		Bet: r.Bet, BetMode: r.BetMode, BetMult: r.BetMult,
		Cycle: r.Cycle, Choice: r.Choice, ChooseNext: append([]int(nil), r.ChooseNext...),
		Checkpoint: r.Checkpoint,
	}
	if cp, ok := r.Checkpoint.(*probeCheckpoint); ok {
		rec.Checkpoint = *cp
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

	// 1. Spin 不帶快照：遊戲收到 DTO 的值，沒有選項與 checkpoint。
	plain := &dto.SpinRequest{GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1}
	first, err := m.Spin(plain)
	if err != nil {
		t.Fatal(err)
	}
	requireProbe(t, "spin", probeLast(t, log), probeRequest{
		GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1,
	})

	// 2. Spin 帶外部快照、checkpoint 與續段欄位：續段欄位與 checkpoint 原樣交給遊戲，
	//    RNG 快照不交給遊戲。
	one := 1
	replay := &dto.SpinRequest{
		GameName: m.gameName, GameId: m.gameId, Bet: unit * 2, BetMode: 0, BetMult: 2,
		Cycle: 2, Choice: &one, ChooseNext: []int{5, 6},
		StartState: &dto.StartState{StartCoreSnapB64U: first.State.StartCoreSnapB64U, Checkpoint: json.RawMessage(`{"stage":3}`)},
	}
	if _, err := m.Spin(replay); err != nil {
		t.Fatal(err)
	}
	requireProbe(t, "spin-replay", probeLast(t, log), probeRequest{
		GameName: m.gameName, GameId: m.gameId, Bet: unit * 2, BetMode: 0, BetMult: 2,
		Cycle: 2, Choice: 1, ChooseNext: []int{5, 6}, Checkpoint: probeCheckpoint{Stage: 3},
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
		GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1,
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

	// Spin 抽庫：抽出的 seed 不交給遊戲，只記錄為結果的起始快照。
	res, err := m.Spin(&dto.SpinRequest{GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := corefmt.DecodeBase64URL(res.State.StartCoreSnapB64U)
	if err != nil {
		t.Fatal(err)
	}
	if len(seed) == 0 {
		t.Fatal("optimal start snapshot missing")
	}
	requireProbe(t, "spin-optimal", probeLast(t, log), probeRequest{
		GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1,
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

// cycleLogic 包住 demo 邏輯：在結果寫入自己的 Cycle（模擬遊戲寫入引擎擁有的欄位），並保留
// 回傳的結果，讓測試在入口返回後讀取引擎最後寫入的值；nilResult 為 true 時回傳 nil。
type cycleLogic struct {
	inner     slot.GameLogic
	last      *buf.SpinResult
	nilResult bool
}

func (l *cycleLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	if l.nilResult {
		l.last = nil
		return nil
	}
	sr := l.inner.GetResult(r, g)
	sr.Cycle = 99
	l.last = sr
	return sr
}

func withCycleLogic(t testing.TB, m *Machine) *cycleLogic {
	t.Helper()
	key := m.gh.GameSetting.LogicKey
	logic := &cycleLogic{}
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

// 結果的 Cycle 由引擎在遊戲執行後覆寫為請求的 Cycle（SpinUnsafe 未指定時為 0）；遊戲寫入的值
// 不保留，DTO 在 Cycle 非 0 時輸出 cycle，nil 結果原樣回傳。
func TestResultCycleIsFilledByEngine(t *testing.T) {
	for _, optimal := range []bool{false, true} {
		t.Run(fmt.Sprintf("optimal=%v", optimal), func(t *testing.T) {
			var log []probeRequest
			lab := probeOffLab(t, &log)
			if optimal {
				lab = probeManifestLab(t, &log)
			}
			m, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(42)})
			if err != nil {
				t.Fatal(err)
			}
			if (m.optimal != nil) != optimal {
				t.Fatalf("machine optimal=%v, want %v", m.optimal != nil, optimal)
			}
			l := withCycleLogic(t, m)
			unsafe := func(step string, capture bool) {
				t.Helper()
				sr := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: capture})
				if sr == nil || sr != l.last {
					t.Fatalf("%s: SpinUnsafe returned %p, the game returned %p", step, sr, l.last)
				}
				if sr.Cycle != 0 {
					t.Fatalf("%s: SpinUnsafe result cycle %d, want 0", step, sr.Cycle)
				}
			}
			unsafe("unsafe", false)
			unsafe("unsafe-capture", true)

			zero := 0
			req := &dto.SpinRequest{GameName: m.gameName, GameId: m.gameId, Bet: m.BetUnits[0], BetMode: 0, BetMult: 1,
				Cycle: 3, Choice: &zero, ChooseNext: []int{9}}
			if optimal {
				// optimal 開啟時，續段只能以指定快照回放；未指定快照時在執行前拒絕。
				if _, err := m.Spin(req); !errors.Is(err, ErrContinuationUnsupported) {
					t.Fatalf("optimal continuation without snapshot: err %v", err)
				}
				req.StartState = &dto.StartState{StartCoreSnapB64U: corefmt.EncodeBase64URL(coreState(t, m))}
			}
			res, err := m.Spin(req)
			if err != nil {
				t.Fatal(err)
			}
			if l.last == nil {
				t.Fatal("Spin: the game was not called")
			}
			if l.last.Cycle != 3 {
				t.Fatalf("Spin: result cycle %d, want the request cycle 3", l.last.Cycle)
			}
			raw, err := json.Marshal(res)
			if err != nil {
				t.Fatal(err)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(raw, &top); err != nil {
				t.Fatal(err)
			}
			if string(top["cycle"]) != "3" || string(top["choice"]) != "0" {
				t.Fatalf("DTO must output cycle 3 and choice 0: %s", raw)
			}
			// Spin 之後的 SpinUnsafe 不帶 Spin 的段號。
			unsafe("unsafe-after-spin", false)

			l.nilResult = true
			if sr := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}); sr != nil {
				t.Fatalf("a nil game result must be returned as nil, got %p", sr)
			}
			if sr := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: true}); sr != nil {
				t.Fatalf("a nil game result must be returned as nil with capture, got %p", sr)
			}
		})
	}
}

// 跨 Machine 接手：以第 0 段結果的 choose_next 與 cp 組出續段請求，送到另一台位置相同的
// Machine，結果與原機台繼續執行相同；結果回帶 bet_id、cycle 與 choice。
func TestDemoChoiceTakeoverAcrossMachines(t *testing.T) {
	lab := demoChoiceLab(t)
	a, b := demoChoiceMachine(t, lab, 21), demoChoiceMachine(t, lab, 99)
	seg0 := spinTriggered(t, a)
	if seg0.BetID != "bet-1" || seg0.Choice != nil || len(seg0.ChooseNext) != 2 || len(seg0.State.Checkpoint) == 0 {
		t.Fatalf("segment 0: %+v", seg0)
	}
	if err := b.RestoreCore(coreState(t, a)); err != nil {
		t.Fatal(err)
	}
	cont := choiceRequest(a, 1, intp(1), seg0.ChooseNext, seg0.State.Checkpoint, "")
	resA, err := a.Spin(cont)
	if err != nil {
		t.Fatal(err)
	}
	resB, err := b.Spin(cont)
	if err != nil {
		t.Fatal(err)
	}
	if mustJSONString(t, resA) != mustJSONString(t, resB) {
		t.Fatal("the continuation differs across machines")
	}
	if !resA.IsGameEnd || resA.Cycle != 1 || resA.Choice == nil || *resA.Choice != 1 || resA.BetID != "bet-1" {
		t.Fatalf("continuation: %+v", resA)
	}

	// 逐段回放：第 1 段以自己的起始快照、choice，以及第 0 段的 choose_next 與 cp 重送。
	c := demoChoiceMachine(t, lab, 5)
	replay := choiceRequest(c, 1, intp(1), seg0.ChooseNext, seg0.State.Checkpoint, resA.State.StartCoreSnapB64U)
	before := coreState(t, c)
	got, err := c.Spin(replay)
	if err != nil {
		t.Fatal(err)
	}
	if mustJSONString(t, got) != mustJSONString(t, resA) {
		t.Fatal("replaying segment 1 differs")
	}
	requireCore(t, c, before)
	// 第 0 段同樣可以回放。
	if got, err := c.Spin(choiceRequest(c, 0, nil, nil, nil, seg0.State.StartCoreSnapB64U)); err != nil || mustJSONString(t, got) != mustJSONString(t, seg0) {
		t.Fatalf("replaying segment 0 differs: %v", err)
	}
}

// optimal 開啟的機台：以起始快照重播第 0 段時跳過抽庫，得到相同的結果，機台恢復到執行前。
func TestOptimalSegmentReplaySkipsDraw(t *testing.T) {
	m := faultMachine(t, true)
	req := &dto.SpinRequest{GameName: m.gameName, GameId: m.gameId, Bet: m.BetUnits[0], BetMode: 0, BetMult: 1}
	first, err := m.Spin(req)
	if err != nil {
		t.Fatal(err)
	}
	before := coreState(t, m)
	req.StartState = &dto.StartState{StartCoreSnapB64U: first.State.StartCoreSnapB64U}
	again, err := m.Spin(req)
	if err != nil {
		t.Fatal(err)
	}
	if mustJSONString(t, again) != mustJSONString(t, first) {
		t.Fatal("replay differs")
	}
	requireCore(t, m, before)
	snap, _ := corefmt.DecodeBase64URL(first.State.StartCoreSnapB64U)
	if sr := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, StartSnap: snap, CaptureSnapshots: true}); sr == nil || sr.TotalWin != first.TotalWin {
		t.Fatal("SpinUnsafe replay with StartSnap differs")
	}
	requireCore(t, m, before)
}
