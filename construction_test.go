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
	"io"
	"io/fs"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// seedRecordingFactory 記錄每次 New 收到的 seed material。
type seedRecordingFactory struct {
	core.PRNGFactory
	seeds [][]byte
}

func (f *seedRecordingFactory) New(seed []byte) (core.PRNG, error) {
	f.seeds = append(f.seeds, append([]byte(nil), seed...))
	return f.PRNGFactory.New(seed)
}

func newRecordingLab(t *testing.T) (*Problab, *seedRecordingFactory, *detReader) {
	t.Helper()
	f := &seedRecordingFactory{PRNGFactory: core.Default()}
	entropy := newDetReader("api-test")
	lab := newOneGameLab(t, f, WithSeedEntropy(entropy))
	f.seeds = nil
	return lab, f, entropy
}

// U4：Seed{} 才會觸發 GenerateSeed；明確提供的空 material 是合法 seed。
func TestSeedSemantics(t *testing.T) {
	lab, f, entropy := newRecordingLab(t)
	defer func() { _ = lab.Close() }()

	before := entropy.read
	if _, err := lab.NewMachine(1, MachineConfig{}); err != nil {
		t.Fatal(err)
	}
	if entropy.read == before {
		t.Fatal("Seed{} did not generate a seed")
	}
	for _, seed := range []Seed{SeedBytes(nil), SeedBytes([]byte{}), SeedString("")} {
		f.seeds = nil
		before = entropy.read
		if _, err := lab.NewMachine(1, MachineConfig{Seed: seed}); err != nil {
			t.Fatal(err)
		}
		if entropy.read != before {
			t.Fatal("explicit empty seed triggered GenerateSeed")
		}
		if len(f.seeds) != 1 || len(f.seeds[0]) != 0 {
			t.Fatalf("explicit empty seed material=%v", f.seeds)
		}
	}
	f.seeds = nil
	if _, err := lab.NewMachine(1, MachineConfig{Seed: SeedInt64(-7)}); err != nil {
		t.Fatal(err)
	}
	if len(f.seeds) != 1 || !bytes.Equal(f.seeds[0], core.EncodeInt64Seed(-7)) {
		t.Fatal("SeedInt64 did not use the canonical int64 encoding")
	}
	f.seeds = nil
	if _, err := lab.NewSimulator(1, MachineConfig{Seed: SeedString("utf8-種子")}); err != nil {
		t.Fatal(err)
	}
	if len(f.seeds) != 1 || string(f.seeds[0]) != "utf8-種子" {
		t.Fatal("SeedString did not use the unmodified UTF-8 bytes")
	}
}

// U5：建構函式在呼叫時就複製傳入的 slice；NewMachine 不保留 cfg 的參照。
func TestConstructionOwnership(t *testing.T) {
	lab, _, _ := newRecordingLab(t)
	defer func() { _ = lab.Close() }()
	material := []byte("owned-seed")
	cfg := MachineConfig{Seed: SeedBytes(material)}
	material[0] ^= 0xff
	got, err := lab.NewMachine(1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want, err := lab.NewMachine(1, MachineConfig{Seed: SeedString("owned-seed")})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := got.SnapshotCore()
	b, _ := want.SnapshotCore()
	if !bytes.Equal(a, b) {
		t.Fatal("mutating the caller slice after SeedBytes changed the Machine")
	}

	gs, err := lab.cat.GameSettingById(1)
	if err != nil {
		t.Fatal(err)
	}
	raw := yamlOfSetting(t, lab, 1)
	source := SettingFromYAML(raw)
	for i := range raw {
		raw[i] = 0
	}
	m, err := lab.NewMachine(1, MachineConfig{Seed: SeedInt64(3), Setting: source})
	if err != nil {
		t.Fatalf("mutating the caller slice after SettingFromYAML broke the source: %v", err)
	}
	if m.gameName != gs.GameName {
		t.Fatal("custom setting resolved to a different game")
	}
}

func yamlOfSetting(t *testing.T, lab *Problab, id spec.GID) []byte {
	t.Helper()
	ent, ok := lab.cat.GetByID(id)
	if !ok {
		t.Fatal("missing entry")
	}
	src, ok := lab.cat.Cfg().GetFS(ent.ConfigName)
	if !ok {
		t.Fatal("missing config source")
	}
	raw, err := fs.ReadFile(src, ent.ConfigName)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// U6：自訂設定一律經過 spec 解析與 init，並驗證 game id。
func TestSettingSourceValidation(t *testing.T) {
	lab, _, _ := newRecordingLab(t)
	defer func() { _ = lab.Close() }()
	raw := yamlOfSetting(t, lab, 1)
	m, err := lab.NewMachine(1, MachineConfig{Seed: SeedInt64(1), Setting: SettingFromYAML(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if m.gh.GameSetting.GameModeSettings[0].ScreenSetting.ScreenSize == 0 {
		t.Fatal("custom setting was not initialized")
	}
	_, err = lab.NewMachine(0, MachineConfig{Seed: SeedInt64(1), Setting: SettingFromYAML(raw)})
	e, ok := errs.AsErr(err)
	if !ok || e.ErrLv != errs.Warn {
		t.Fatalf("game id mismatch error=%v, want Warn", err)
	}
	if _, err := lab.NewSimulator(0, MachineConfig{Seed: SeedInt64(1), Setting: SettingFromYAML(raw)}); err == nil {
		t.Fatal("Simulator accepted a game id mismatch")
	}
}

// countingFS 計數 Open 與 ReadFile 次數。
type countingFS struct {
	fs.FS
	reads atomic.Int64
}

func (c *countingFS) Open(name string) (fs.File, error) {
	c.reads.Add(1)
	return c.FS.Open(name)
}

func (c *countingFS) ReadFile(name string) ([]byte, error) {
	c.reads.Add(1)
	f, err := c.FS.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// U7：Unoptimized 不讀取任何 optimal 分布檔與 bank，也不修改 catalog。
func TestUnoptimizedDoesNotTouchOptimalFiles(t *testing.T) {
	optimalFS := &countingFS{FS: testOptimalFS(t, false)}
	lab, err := New(core.Default(), Configs(testOptimalConfigFS(t)), Logics(demo_logic.Logics),
		WithOptimalFS(optimalFS), WithSeedEntropy(newDetReader("u7")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	if err := lab.RegisterAll(); err != nil {
		t.Fatal(err)
	}
	lab.Freeze()
	raw, err := fs.ReadFile(testOptimalConfigFS(t), "game.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []MachineConfig{
		{Seed: SeedInt64(5), Unoptimized: true},
		{Seed: SeedInt64(5), Unoptimized: true, Setting: SettingFromYAML(raw)},
	} {
		m, err := lab.NewMachine(0, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if m.optimal != nil {
			t.Fatal("Unoptimized Machine still uses an optimal artifact")
		}
	}
	if n := optimalFS.reads.Load(); n != 0 {
		t.Fatalf("Unoptimized read optimal files %d times", n)
	}
	gs, err := lab.cat.GameSettingById(0)
	if err != nil {
		t.Fatal(err)
	}
	if !gs.OptimalSetting.UseOptimal {
		t.Fatal("Unoptimized modified the catalog setting")
	}
	m, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(5)})
	if err != nil {
		t.Fatal(err)
	}
	if m.optimal == nil || optimalFS.reads.Load() == 0 {
		t.Fatal("control: optimized Machine did not load its artifact")
	}
}

// U8：Machine 一律保留 Extend；Simulator 內部的 Machine 一律省略。
func TestProductOmitExtendPolicy(t *testing.T) {
	lab, _, _ := newRecordingLab(t)
	defer func() { _ = lab.Close() }()
	m, err := lab.NewMachine(1, MachineConfig{Seed: SeedInt64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if m.OmitExtend() || m.rngMode != rngDeterministic {
		t.Fatal("NewMachine must be deterministic and keep Extend")
	}
	s, err := lab.NewSimulator(1, MachineConfig{Seed: SeedInt64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if !s.mBuf[0].OmitExtend() || s.mBuf[0].rngMode != rngDeterministic {
		t.Fatal("NewSimulator must be deterministic and omit Extend")
	}
}

// R1：公開 API 不能建立單台 production Machine。
func TestPublicAPICannotBuildProductionMachine(t *testing.T) {
	machineType := reflect.TypeOf((*Machine)(nil))
	var builders []string
	pt := reflect.TypeOf((*Problab)(nil))
	for i := range pt.NumMethod() {
		method := pt.Method(i)
		for j := range method.Type.NumOut() {
			if method.Type.Out(j) == machineType {
				builders = append(builders, method.Name)
			}
		}
	}
	if len(builders) != 1 || builders[0] != "NewMachine" {
		t.Fatalf("exported Machine builders=%v, want only NewMachine", builders)
	}
	lab, _, _ := newRecordingLab(t)
	defer func() { _ = lab.Close() }()
	for _, cfg := range []MachineConfig{{}, {Seed: SeedInt64(9)}, {Unoptimized: true}} {
		m, err := lab.NewMachine(1, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if m.rngMode != rngDeterministic {
			t.Fatalf("NewMachine(%+v) built a production Machine", cfg)
		}
	}
}

// probeOmitLogic 在 build 當下記錄 gh.OmitExtend()，用來證明旗標在遊戲邏輯
// 建出之前就已設定（techspec P2、U1）。
type probeOmitLogic struct{}

func (probeOmitLogic) GetResult(r *buf.SpinRequest, gh *slot.Game) *buf.SpinResult {
	sr := gh.StartNewSpin(r)
	sr.End()
	return sr
}

func probeOmitSetting(t testing.TB) *spec.GameSetting {
	t.Helper()
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "logic_key: demo_normal", "logic_key: probe_omit", 1)
	gs, err := spec.GetGameSettingByYAML([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return gs
}

func probeOmitRegistry(t testing.TB, seen *[]bool) *slot.LogicRegistry {
	t.Helper()
	reg := slot.NewLogicRegistry()
	if err := slot.GameRegister("probe_omit", func(gh *slot.Game) (slot.GameLogic, error) {
		*seen = append(*seen, gh.OmitExtend())
		return probeOmitLogic{}, nil
	}, reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

// U1：gh.OmitExtend() 在 build 時即可讀到，值等於建構政策；涵蓋 slot.NewGame 與
// Machine 的私有建構核心。
func TestOmitExtendVisibleAtBuild(t *testing.T) {
	gs := probeOmitSetting(t)
	for _, omit := range []bool{true, false} {
		var seen []bool
		reg := probeOmitRegistry(t, &seen)
		rng, err := core.Default().New(core.EncodeInt64Seed(1))
		if err != nil {
			t.Fatal(err)
		}
		gh, err := slot.NewGame(gs, reg, core.New(rng), omit)
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != omit || gh.OmitExtend() != omit {
			t.Fatalf("slot.NewGame(omitExtend=%v): seen at build=%v, after=%v", omit, seen, gh.OmitExtend())
		}

		seen = nil
		m, err := newMachine(gs, reg, core.Default(), core.EncodeInt64Seed(1), omit, nil, rngDeterministic)
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != omit || m.OmitExtend() != omit {
			t.Fatalf("Machine(omitExtend=%v): seen at build=%v, after=%v", omit, seen, m.OmitExtend())
		}
	}
}

// 私有建構核心拒絕 production 搭配省略 Extend（techspec §5.1、C7）。
func TestProductionMachineMustKeepExtend(t *testing.T) {
	gs := probeOmitSetting(t)
	var seen []bool
	_, err := newMachine(gs, probeOmitRegistry(t, &seen), core.Default(), core.EncodeInt64Seed(1), true, nil, rngProduction)
	if err == nil {
		t.Fatal("production machine with omitExtend=true was accepted")
	}
	if len(seen) != 0 {
		t.Fatal("rejected machine still built its game logic")
	}
}

// U2：fixture 遊戲每轉 4 次 YieldResult，觸發 GMR pool 擴容；擴容後新建的 GMR
// 也必須遵守省略政策，且跨轉 Reset 後不變。
func TestOmitExtendAppliesToGrownGameModeResults(t *testing.T) {
	lab, err := newFixtureLab(t)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	gs, err := lab.cat.GameSettingById(fixtureGameID)
	if err != nil {
		t.Fatal(err)
	}
	for _, omit := range []bool{true, false} {
		m, err := newMachine(gs, lab.reg, lab.cf, core.EncodeInt64Seed(7), omit, nil, rngDeterministic)
		if err != nil {
			t.Fatal(err)
		}
		before := fixtureSnapshotCalls.Load()
		for spin := range 20 {
			sr := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1})
			if len(sr.GameModeList) < fixtureBaseYield {
				t.Fatalf("spin %d: only %d mode results; pool growth not exercised", spin, len(sr.GameModeList))
			}
			for gi, gmr := range sr.GameModeList {
				for ai, act := range gmr.ActResults {
					if act.ActType != "screen" {
						continue
					}
					if (act.ExtendResult == nil) != omit {
						t.Fatalf("omitExtend=%v spin %d gmr %d act %d: ExtendResult=%v", omit, spin, gi, ai, act.ExtendResult)
					}
				}
			}
		}
		calls := fixtureSnapshotCalls.Load() - before
		if omit && calls != 0 {
			t.Fatalf("omitExtend=true: Snapshot called %d times", calls)
		}
		if !omit && calls == 0 {
			t.Fatal("omitExtend=false: Snapshot was never called")
		}
	}
}
