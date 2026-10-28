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

package optimalrt

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/klauspost/compress/zstd"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/sampler"
	"github.com/zintix-labs/problab/spec"
)

const testSeedLen = 8

// testPair 回傳一對 gacha（zstd JSON）與 seed bank：n 筆、每筆 testSeedLen 位元組，第 i 筆全是 byte(i+1)。
func testPair(t *testing.T, n int) ([]byte, []byte) {
	t.Helper()
	weights := make([]float64, n)
	bank := make([]byte, 0, n*testSeedLen)
	for i := range weights {
		weights[i] = float64(i + 1)
		bank = append(bank, bytes.Repeat([]byte{byte(i + 1)}, testSeedLen)...)
	}
	return testGacha(t, sampler.BuildAliasTableF64(weights), testSeedLen), bank
}

func testGacha(t *testing.T, table *sampler.AliasTableF64, seedLen int) []byte {
	t.Helper()
	raw, err := json.Marshal(legacyGacha{Picker: table, SeedLen: seedLen})
	if err != nil {
		t.Fatal(err)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = enc.Close() }()
	return enc.EncodeAll(raw, nil)
}

func testSetting(n int) *spec.GameSetting {
	gs := &spec.GameSetting{OptimalSetting: spec.OptimalSetting{UseOptimal: true}}
	for i := range n {
		gs.BetUnits = append(gs.BetUnits, 10*(i+1))
		gs.OptimalSetting.Gachas = append(gs.OptimalSetting.Gachas, filepath.ToSlash(filepath.Join("m", string(rune('0'+i)), "gacha.json.zst")))
		gs.OptimalSetting.SeedBank = append(gs.OptimalSetting.SeedBank, filepath.ToSlash(filepath.Join("m", string(rune('0'+i)), "seed_bank.bin")))
	}
	return gs
}

func testFS(t *testing.T, gs *spec.GameSetting, sizes ...int) fstest.MapFS {
	t.Helper()
	files := fstest.MapFS{}
	for i, n := range sizes {
		gacha, bank := testPair(t, n)
		files[gs.OptimalSetting.Gachas[i]] = &fstest.MapFile{Data: gacha}
		files[gs.OptimalSetting.SeedBank[i]] = &fstest.MapFile{Data: bank}
	}
	return files
}

// countingSource 記錄 OpenBinary 的呼叫與 closeFn 的釋放次數。
type countingSource struct {
	artifactSource
	opens  int
	closes int
}

func (s *countingSource) OpenBinary(name string, size int64) ([]byte, func() error, string, error) {
	data, closeFn, backend, err := s.artifactSource.OpenBinary(name, size)
	if err != nil {
		return nil, nil, "", err
	}
	s.opens++
	return data, func() error { s.closes++; return closeFn() }, backend, nil
}

func pickAll(t *testing.T, a *Artifact, betMode int) [][]byte {
	t.Helper()
	rng, err := core.Default().New(core.EncodeInt64Seed(9))
	if err != nil {
		t.Fatal(err)
	}
	c := core.New(rng)
	var out [][]byte
	for range 200 {
		seed, err := a.PickStart(betMode, c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, append([]byte(nil), seed...))
	}
	return out
}

// 目錄來源在支援的平台上以 mmap 載入 seed bank，FS 來源讀入記憶體；兩者抽到的 seed 相同。
func TestLegacyArtifactBackends(t *testing.T) {
	gs := testSetting(2)
	files := testFS(t, gs, 3, 5)
	memory, err := loadLegacyArtifact(fsSource{fsys: files}, "k", testSeedLen, nil, gs)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for name, f := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := newDirSource(root)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := loadLegacyArtifact(dir, "k", testSeedLen, nil, gs)
	if err != nil {
		t.Fatal(err)
	}
	want := "memory"
	if mmapAvailable {
		want = "mmap"
	}
	if memory.Backend() != "memory" || mapped.Backend() != want {
		t.Fatalf("backends %q and %q, want memory and %q", memory.Backend(), mapped.Backend(), want)
	}
	for mode := range 2 {
		got, exp := pickAll(t, mapped, mode), pickAll(t, memory, mode)
		for i := range exp {
			if !bytes.Equal(got[i], exp[i]) {
				t.Fatalf("mode %d pick %d differs between backends", mode, i)
			}
		}
	}
	if err := mapped.Close(); err != nil || !mapped.Closed() {
		t.Fatalf("close: %v", err)
	}
}

// Size × SeedLen 溢位時回傳錯誤，不開啟 seed bank。
func TestLegacyArtifactRejectsOverflowingBank(t *testing.T) {
	seedLen := math.MaxInt/2 + 1
	gs := testSetting(1)
	files := fstest.MapFS{
		gs.OptimalSetting.Gachas[0]:   &fstest.MapFile{Data: testGacha(t, sampler.BuildAliasTableF64([]float64{1, 1}), seedLen)},
		gs.OptimalSetting.SeedBank[0]: &fstest.MapFile{Data: []byte{1}},
	}
	source := &countingSource{artifactSource: fsSource{fsys: files}}
	_, err := loadLegacyArtifact(source, "k", seedLen, nil, gs)
	if err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("error %v, want an overflow", err)
	}
	if source.opens != 0 {
		t.Fatal("the seed bank was opened")
	}
}

// 後一個 bet mode 載入失敗時，已載入的 mode 恰好釋放一次。
func TestLegacyArtifactReleasesLoadedModesOnFailure(t *testing.T) {
	gs := testSetting(2)
	files := testFS(t, gs, 3, 5)
	files[gs.OptimalSetting.SeedBank[1]].Data = files[gs.OptimalSetting.SeedBank[1]].Data[1:]
	source := &countingSource{artifactSource: fsSource{fsys: files}}
	if _, err := loadLegacyArtifact(source, "k", testSeedLen, nil, gs); err == nil {
		t.Fatal("a truncated seed bank was accepted")
	}
	if source.opens != 1 || source.closes != 1 {
		t.Fatalf("opens %d closes %d, want 1 and 1", source.opens, source.closes)
	}
}

// 首筆檢查收到每個 mode 的第一筆快照；失敗時回傳錯誤並釋放該 mode 與已載入的 mode。
func TestLegacyArtifactChecksFirstSnapshot(t *testing.T) {
	gs := testSetting(2)
	files := testFS(t, gs, 3, 5)
	var seen [][]byte
	check := func(snapshot []byte) error {
		seen = append(seen, append([]byte(nil), snapshot...))
		return nil
	}
	if _, err := loadLegacyArtifact(fsSource{fsys: files}, "k", testSeedLen, check, gs); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || !bytes.Equal(seen[0], bytes.Repeat([]byte{1}, testSeedLen)) || !bytes.Equal(seen[1], seen[0]) {
		t.Fatalf("checked snapshots %v", seen)
	}

	reject := errors.New("format mismatch")
	calls := 0
	source := &countingSource{artifactSource: fsSource{fsys: files}}
	_, err := loadLegacyArtifact(source, "k", testSeedLen, func([]byte) error {
		calls++
		if calls == 2 {
			return reject
		}
		return nil
	}, gs)
	if !errors.Is(err, reject) || !strings.Contains(err.Error(), "first snapshot") {
		t.Fatalf("error %v", err)
	}
	if source.opens != 2 || source.closes != 2 {
		t.Fatalf("opens %d closes %d, want 2 and 2", source.opens, source.closes)
	}
}

// modesSetting 回傳一個 bet mode 的 modes 設定：start 抽 a、入口 3 抽 b、入口 7 照常使用 RNG。
func modesSetting() (*spec.GameSetting, fstest.MapFS) {
	gs := &spec.GameSetting{BetUnits: []int{10}, OptimalSetting: spec.OptimalSetting{UseOptimal: true, Modes: spec.OptimalModes{{
		Start: &spec.GachaSlot{Dist: "a/gacha.json.zst", Bank: "a/seed_bank.bin"},
		After: map[int]*spec.GachaSlot{3: {Dist: "b/gacha.json.zst", Bank: "b/seed_bank.bin"}, 7: {}},
	}}}}
	return gs, fstest.MapFS{}
}

func addPair(t *testing.T, files fstest.MapFS, dir string, n int) {
	t.Helper()
	gacha, bank := testPair(t, n)
	files[dir+"/gacha.json.zst"] = &fstest.MapFile{Data: gacha}
	files[dir+"/seed_bank.bin"] = &fstest.MapFile{Data: bank}
}

// 抽庫的位置各載入一份並經過首筆檢查；照常使用 RNG 的位置不開檔。
func TestModesArtifactLoadsSeededSlots(t *testing.T) {
	gs, files := modesSetting()
	addPair(t, files, "a", 3)
	addPair(t, files, "b", 5)
	source := &countingSource{artifactSource: fsSource{fsys: files}}
	checks := 0
	a, err := loadModesArtifact(source, "k", testSeedLen, func([]byte) error { checks++; return nil }, gs)
	if err != nil {
		t.Fatal(err)
	}
	if source.opens != 2 || checks != 2 || a.Backend() != "memory" {
		t.Fatalf("opens %d checks %d backend %s", source.opens, checks, a.Backend())
	}
	if !a.StartSeeded(0) {
		t.Fatal("start is not seeded")
	}
	if seeded, ok := a.Entry(0, 3); !seeded || !ok {
		t.Fatal("entry 3")
	}
	if seeded, ok := a.Entry(0, 7); seeded || !ok {
		t.Fatal("entry 7")
	}
	if _, ok := a.Entry(0, 0); ok {
		t.Fatal("entry 0 is configured")
	}
	if err := a.Close(); err != nil || source.closes != 2 {
		t.Fatalf("close %v closes %d", err, source.closes)
	}
}

// 載入 after 中途失敗時，以相反順序釋放已載入的 start。
func TestModesArtifactReleasesOnFailure(t *testing.T) {
	gs, files := modesSetting()
	addPair(t, files, "a", 3)
	addPair(t, files, "b", 5)
	files["b/seed_bank.bin"].Data = files["b/seed_bank.bin"].Data[1:]
	source := &countingSource{artifactSource: fsSource{fsys: files}}
	_, err := loadModesArtifact(source, "k", testSeedLen, nil, gs)
	if err == nil || !strings.Contains(err.Error(), "bet mode 0 after[3]") {
		t.Fatalf("error %v", err)
	}
	if source.opens != 1 || source.closes != 1 {
		t.Fatalf("opens %d closes %d", source.opens, source.closes)
	}
}

// 所有位置都照常使用 RNG 時不開任何檔案，Artifact 仍然存在，backend 為 none。
func TestModesArtifactWithoutGachas(t *testing.T) {
	gs := &spec.GameSetting{BetUnits: []int{10, 20}, OptimalSetting: spec.OptimalSetting{UseOptimal: true, Modes: spec.OptimalModes{
		{Start: &spec.GachaSlot{}, After: map[int]*spec.GachaSlot{1: {}}},
		{Start: &spec.GachaSlot{}},
	}}}
	source := &countingSource{artifactSource: fsSource{fsys: fstest.MapFS{}}}
	a, err := loadModesArtifact(source, "k", testSeedLen, nil, gs)
	if err != nil || a == nil || a.Backend() != "none" || a.ModeCount() != 2 || source.opens != 0 {
		t.Fatalf("artifact %v backend %q err %v opens %d", a, a.Backend(), err, source.opens)
	}
	if a.StartSeeded(0) || !a.Offers(0, []int{1}) || a.Offers(1, []int{1}) {
		t.Fatal("routes")
	}
}

func modesNaturalSetting() *spec.GameSetting {
	return &spec.GameSetting{BetUnits: []int{10}, OptimalSetting: spec.OptimalSetting{UseOptimal: true, Modes: spec.OptimalModes{{Start: &spec.GachaSlot{}}}}}
}
