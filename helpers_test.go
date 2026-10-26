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
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/sampler"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
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

func faultMachine(t testing.TB, optimal bool) *Machine {
	t.Helper()
	m, err := faultLab(t, core.Default(), optimal).NewMachine(0, MachineConfig{Seed: SeedInt64(77)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func catchPanic(f func()) (p any) {
	defer func() { p = recover() }()
	f()
	return nil
}

func testManifestConfigFS(t testing.TB) fs.FS {
	t.Helper()
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatalf("read demo config: %v", err)
	}
	enabled := enableTestOptimal(t, string(raw))
	updated := strings.Replace(enabled, "artifact: manifest.json", "artifact: game0/manifest.json", 1)
	if updated == enabled {
		t.Fatal("test config manifest path was not replaced")
	}
	return fstest.MapFS{"game.yaml": &fstest.MapFile{Data: []byte(updated)}}
}

func testManifestFS(t testing.TB, corrupt bool) fs.FS {
	t.Helper()
	snap1 := testDefaultSnapshot(t, 100)
	snap2 := testDefaultSnapshot(t, 200)
	table := sampler.BuildAliasTableF64([]float64{1, 1})
	prob := make([]byte, table.Size*8)
	aliases := make([]byte, table.Size*4)
	for i := 0; i < table.Size; i++ {
		binary.LittleEndian.PutUint64(prob[i*8:], math.Float64bits(table.Prob[i]))
		binary.LittleEndian.PutUint32(aliases[i*4:], uint32(table.Aliases[i]))
	}
	bank := append(append([]byte(nil), snap1...), snap2...)
	manifest := optimalrt.Manifest{
		SchemaVersion:  optimalrt.ManifestSchemaV1,
		ArtifactID:     "test-artifact",
		SnapshotFormat: "problab.chacha20.rfc8439/state-v1",
		Modes: []optimalrt.ManifestMode{{
			BetUnit:   40,
			Size:      table.Size,
			SeedLen:   len(snap1),
			SeedCount: table.Size,
			Prob:      testFileRef("prob.bin", prob),
			Aliases:   testFileRef("aliases.bin", aliases),
			SeedBank:  testFileRef("seed_bank.bin", bank),
		}},
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if corrupt {
		prob = append([]byte(nil), prob...)
		prob[0] ^= 0xff
	}
	return fstest.MapFS{
		"game0/manifest.json": &fstest.MapFile{Data: manifestRaw},
		"game0/prob.bin":      &fstest.MapFile{Data: prob},
		"game0/aliases.bin":   &fstest.MapFile{Data: aliases},
		"game0/seed_bank.bin": &fstest.MapFile{Data: bank},
	}
}

func newOneGameLab(t testing.TB, factory core.PRNGFactory, opts ...ProblabOption) *Problab {
	t.Helper()
	lab, err := newOneGameLabE(factory, opts...)
	if err != nil {
		t.Fatalf("new one-game Problab: %v", err)
	}
	return lab
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

// fixture 遊戲（techspec §6.3）：
//   - ext 的 Snapshot() 不檢查 isSim，永遠回傳深拷貝，並計數被呼叫次數；
//   - 每轉對 BG mode 做 4 次 YieldResult，觸發 GMR pool 擴容（poolSize = 3）；
//   - 每個 screen act 都附上 ext，讓 Snapshot 路徑被大量執行。
//
// 它只用來驗證「引擎省略 Snapshot() 呼叫」這條路徑；demo 遊戲因為自行檢查
// isSim，驗證不到它。
const (
	fixtureGameID    spec.GID = 900
	fixtureGameName           = "fixture_noguard"
	fixtureLogicKey           = spec.LogicKey("fixture_noguard")
	fixtureBaseYield          = 4
)

var fixtureSnapshotCalls atomic.Int64

var fixtureLogics = func() *slot.LogicRegistry {
	reg := slot.NewLogicRegistry()
	if err := slot.GameRegister(fixtureLogicKey, buildFixtureGame, reg); err != nil {
		panic(err)
	}
	return reg
}()

type fixtureFixed struct {
	FreeRound int    `yaml:"free_round"`
	DemoB     []int  `yaml:"demo_b"`
	DemoC     string `yaml:"demo_c"`
}

type fixtureGame struct {
	fixed       fixtureFixed
	symbolTypes []spec.SymbolType
	ext         *fixtureExt
}

type fixtureExt struct {
	Round        int   `json:"round"`
	ScatterCount int   `json:"scatters"`
	ScatterHits  []int `json:"hits"`
}

func (e *fixtureExt) Reset() {
	e.Round = 0
	e.ScatterCount = 0
	e.ScatterHits = e.ScatterHits[:0]
}

// Snapshot 刻意不檢查 isSim；計數是測試觀察用，不影響遊戲狀態與 RNG。
func (e *fixtureExt) Snapshot() any {
	fixtureSnapshotCalls.Add(1)
	hits := make([]int, len(e.ScatterHits))
	copy(hits, e.ScatterHits)
	return &fixtureExt{Round: e.Round, ScatterCount: e.ScatterCount, ScatterHits: hits}
}

func buildFixtureGame(gh *slot.Game) (slot.GameLogic, error) {
	g := &fixtureGame{ext: &fixtureExt{ScatterHits: make([]int, 0, 16)}}
	if err := spec.DecodeFixed(gh.GameSetting, &g.fixed); err != nil {
		return nil, err
	}
	g.symbolTypes = gh.GameSetting.GameModeSettings[0].SymbolSetting.SymbolTypes
	return g, nil
}

func (g *fixtureGame) GetResult(r *buf.SpinRequest, gh *slot.Game) *buf.SpinResult {
	sr := gh.StartNewSpin(r)
	triggered := false
	for i := range fixtureBaseYield {
		base := g.round(r.BetMult, gh, 0, 1, i)
		if i == 0 && base.Trigger != 0 {
			triggered = true
		}
		sr.AppendModeResult(base)
	}
	if triggered {
		sr.AppendModeResult(g.round(r.BetMult, gh, 1, g.fixed.FreeRound, 0))
	}
	sr.End()
	return sr
}

func (g *fixtureGame) round(betMult int, gh *slot.Game, modeIndex, rounds, tag int) *buf.GameModeResult {
	mode := gh.GameModeHandlerList[modeIndex]
	gmr := mode.GameModeResult
	for i := range rounds {
		g.ext.Reset()
		g.ext.Round = tag*100 + i
		screen := mode.ScreenGenerator.GenScreen()
		for idx, symbol := range screen {
			if g.symbolTypes[symbol] == spec.SymbolTypeScatter {
				g.ext.ScatterCount++
				g.ext.ScatterHits = append(g.ext.ScatterHits, idx)
			}
		}
		gmr.AddAct(buf.FinishAct, "screen", screen, g.ext)
		mode.ScreenCalculator.CalcScreen(betMult, screen, gmr)
		if gmr.GetTmpWin() > 0 {
			gmr.AddAct(buf.FinishAct, "win", nil, g.ext)
		}
		if modeIndex == 0 && g.ext.ScatterCount > 2 {
			gmr.Trigger = 1
		}
		gmr.FinishRound()
	}
	return mode.YieldResult()
}

// fixtureConfigYAML 以 demo_0 的設定為基礎，只替換遊戲身分與 logic key。
func fixtureConfigYAML(t testing.TB) []byte {
	t.Helper()
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatalf("read demo config: %v", err)
	}
	text := string(raw)
	for _, pair := range [][2]string{
		{"game_id: 0", "game_id: 900"},
		{"game_name: demo_normal", "game_name: " + fixtureGameName},
		{"logic_key: demo_normal", "logic_key: " + string(fixtureLogicKey)},
	} {
		next := strings.Replace(text, pair[0], pair[1], 1)
		if next == text {
			t.Fatalf("fixture config replacement failed: %q", pair[0])
		}
		text = next
	}
	return []byte(text)
}

// newFixtureLab 建立只含 fixture 遊戲的凍結 Lab（ChaCha20、無 optimal）。
func newFixtureLab(t testing.TB) (*Problab, error) {
	t.Helper()
	f, err := core.NewChaCha20Factory(newDetReader("fixture-reseed"))
	if err != nil {
		return nil, err
	}
	cfgs := Configs(fstest.MapFS{"fixture.yaml": &fstest.MapFile{Data: fixtureConfigYAML(t)}})
	return NewAuto(f, cfgs, Logics(fixtureLogics), WithSeedEntropy(newDetReader("fixture-seed")))
}

// newProductionMachineForTest 只存在於測試，不屬於公開 API（PRD 0012 §3.2、
// techspec §5.5）。它經私有建構核心逐步重現舊 NewMachine(id, false) 的建立流程：
// 凍結檢查 → catalog 取設定 → resolveOptimal → generateSeed → production、保留
// Extend。用途只限於保留原有的 RNG 與 entropy 順序斷言。
func newProductionMachineForTest(p *Problab, id spec.GID) (*Machine, error) {
	b, err := p.resolveBuild(id, MachineConfig{}, "Machine")
	if err != nil {
		return nil, err
	}
	return newMachine(b.gs, p.reg, p.cf, b.seed, false, b.optimal, rngProduction)
}

// detReader 是無限長、可重現的 reader：第 n 個 32 位元組區塊為
// SHA-256(label || n)。它同時計數被讀取的位元組數。
type detReader struct {
	label   string
	counter uint64
	pending []byte
	read    int64
}

func newDetReader(label string) *detReader { return &detReader{label: label} }

func (r *detReader) Read(dst []byte) (int, error) {
	n := 0
	for n < len(dst) {
		if len(r.pending) == 0 {
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s#%d", r.label, r.counter)))
			r.counter++
			r.pending = sum[:]
		}
		c := copy(dst[n:], r.pending)
		r.pending = r.pending[c:]
		n += c
	}
	r.read += int64(n)
	return n, nil
}

func requireWarnIs(t testing.TB, step string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: err %v, want %v", step, err, target)
	}
	if e, ok := err.(*errs.E); !ok || e.ErrLv != errs.Warn {
		t.Fatalf("%s: err %T %v, want *errs.E at Warn", step, err, err)
	}
}

func intp(v int) *int { return &v }

// demo_choice（game 2）：BG 觸發後二選一的分段遊戲，供分段相關的測試共用。

const demoChoiceID spec.GID = 2

func demoChoiceLab(t testing.TB) *Problab {
	t.Helper()
	lab, err := NewAuto(core.Default(), Configs(demo_configs.FS), Logics(demo_logic.Logics))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

func demoChoiceMachine(t testing.TB, lab *Problab, seed int64) *Machine {
	t.Helper()
	m, err := lab.NewMachine(demoChoiceID, MachineConfig{Seed: SeedInt64(seed)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func choiceRequest(m *Machine, cycle int, choice *int, next []int, cp json.RawMessage, start string) *dto.SpinRequest {
	req := &dto.SpinRequest{BetID: "bet-1", GameName: m.gameName, GameId: m.gameId, Bet: m.BetUnits[0], BetMode: 0, BetMult: 1,
		Cycle: cycle, Choice: choice, ChooseNext: next}
	if len(cp) != 0 || start != "" {
		req.StartState = &dto.StartState{StartCoreSnapB64U: start, Checkpoint: cp}
	}
	return req
}

// spinTriggered 以 Spin 反覆開局，直到第 0 段停在選擇點。
func spinTriggered(t *testing.T, m *Machine) dto.SpinResult {
	t.Helper()
	for range 5000 {
		res, err := m.Spin(choiceRequest(m, 0, nil, nil, nil, ""))
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsGameEnd {
			return res
		}
	}
	t.Fatal("the choice game never triggered")
	return dto.SpinResult{}
}

func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
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
