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
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/klauspost/compress/zstd"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/sampler"
	"github.com/zintix-labs/problab/spec"
)

func TestOptimalMachineRequiresSource(t *testing.T) {
	_, err := NewAuto(
		core.Default(),
		Configs(testLegacyConfigFS(t)),
		Logics(demo_logic.Logics),
	)
	if err == nil {
		t.Fatal("expected NewAuto to fail fast without an optimal source")
	}
}

func TestDefaultChaCha20OptimalArtifact(t *testing.T) {
	lab, err := NewAuto(
		core.Default(),
		Configs(testManifestConfigFS(t)),
		Logics(demo_logic.Logics),
		WithOptimalFS(testManifestFS(t, false)),
	)
	if err != nil {
		t.Fatalf("NewAuto with compatible artifact: %v", err)
	}
	defer func() { _ = lab.Close() }()
	machine, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(99)})
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if machine.optimal == nil || machine.optimal.Backend() != "memory" {
		t.Fatal("fixture did not load its manifest memory artifact")
	}
	if result := machine.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}); result == nil {
		t.Fatal("fixture optimal spin returned nil")
	}
}

func TestExplicitPCG64LoadsLegacyCompatibleArtifact(t *testing.T) {
	lab, err := NewAuto(
		core.PCG64(),
		Configs(testManifestConfigFS(t)),
		Logics(demo_logic.Logics),
		WithOptimalFS(testPCGManifestFS(t)),
	)
	if err != nil {
		t.Fatalf("NewAuto with explicit PCG64 artifact: %v", err)
	}
	defer func() { _ = lab.Close() }()
	machine, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(832)})
	if err != nil {
		t.Fatal(err)
	}
	if result := machine.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}); result == nil {
		t.Fatal("PCG64 artifact replay returned nil")
	}
}

func TestOptimalArtifactRejectsBothAlgorithmMismatchDirections(t *testing.T) {
	if _, err := NewAuto(
		core.Default(), Configs(testManifestConfigFS(t)), Logics(demo_logic.Logics),
		WithOptimalFS(testPCGManifestFS(t)),
	); err == nil {
		t.Fatal("ChaCha20 runtime accepted a PCG64 artifact")
	}
	if _, err := NewAuto(
		core.PCG64(), Configs(testManifestConfigFS(t)), Logics(demo_logic.Logics),
		WithOptimalFS(testManifestFS(t, false)),
	); err == nil {
		t.Fatal("PCG64 runtime accepted a ChaCha20 artifact")
	}
}

func TestLegacyOptimalDeterministic(t *testing.T) {
	lab, err := NewAuto(
		core.Default(),
		Configs(testLegacyConfigFS(t)),
		Logics(demo_logic.Logics),
		WithOptimalFS(testOptimalFS(t)),
	)
	if err != nil {
		t.Fatalf("NewAuto: %v", err)
	}

	m1, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(19)})
	if err != nil {
		t.Fatalf("NewMachine m1: %v", err)
	}
	m2, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(19)})
	if err != nil {
		t.Fatalf("NewMachine m2: %v", err)
	}

	r1, err := json.Marshal(m1.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}))
	if err != nil {
		t.Fatalf("marshal result 1: %v", err)
	}
	r2, err := json.Marshal(m2.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}))
	if err != nil {
		t.Fatalf("marshal result 2: %v", err)
	}
	if !bytes.Equal(r1, r2) {
		t.Fatal("same seed produced different optimal spin results")
	}

	s1, err := m1.SnapshotCore()
	if err != nil {
		t.Fatalf("snapshot m1: %v", err)
	}
	s2, err := m2.SnapshotCore()
	if err != nil {
		t.Fatalf("snapshot m2: %v", err)
	}
	if !bytes.Equal(s1, s2) {
		t.Fatal("same seed produced different final core state")
	}
}

func TestOptimalArtifactSharedAcrossAllMachineBuilders(t *testing.T) {
	lab, err := NewAuto(
		core.Default(),
		Configs(testLegacyConfigFS(t)),
		Logics(demo_logic.Logics),
		WithOptimalFS(testOptimalFS(t)),
	)
	if err != nil {
		t.Fatalf("NewAuto: %v", err)
	}
	if got := lab.optimal.LoadCount(); got != 1 {
		t.Fatalf("preload count=%d, want 1", got)
	}

	m1, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(1)})
	if err != nil {
		t.Fatalf("NewMachine m1: %v", err)
	}
	m2, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(2)})
	if err != nil {
		t.Fatalf("NewMachine m2: %v", err)
	}
	if m1.optimal == nil || m1.optimal != m2.optimal {
		t.Fatal("machines do not share the same immutable artifact")
	}

	sim, err := lab.NewSimulator(0, MachineConfig{Seed: SeedInt64(3)})
	if err != nil {
		t.Fatalf("NewSimulator: %v", err)
	}
	if sim.mBuf[0].optimal != m1.optimal {
		t.Fatal("simulator does not share the Problab artifact")
	}

	rt, err := lab.BuildRuntime(4)
	if err != nil {
		t.Fatalf("BuildRuntime: %v", err)
	}
	rt.Close()
	if got := lab.optimal.LoadCount(); got != 1 {
		t.Fatalf("artifact reloaded by machine builders: count=%d", got)
	}
}

func TestOptimalArtifactConcurrentFirstResolveLoadsOnce(t *testing.T) {
	lab, err := New(
		core.Default(),
		Configs(testLegacyConfigFS(t)),
		Logics(demo_logic.Logics),
		WithOptimalFS(testOptimalFS(t)),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := lab.RegisterAll(); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	lab.Freeze()
	defer func() { _ = lab.Close() }()

	const workers = 32
	artifacts := make(chan *optimalrt.Artifact, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(seed int64) {
			defer wg.Done()
			machine, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(seed)})
			if err != nil {
				errs <- err
				return
			}
			artifacts <- machine.optimal
		}(int64(i + 1))
	}
	wg.Wait()
	close(artifacts)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent machine build: %v", err)
	}
	var first *optimalrt.Artifact
	for artifact := range artifacts {
		if first == nil {
			first = artifact
			continue
		}
		if artifact != first {
			t.Fatal("concurrent first resolve returned different artifacts")
		}
	}
	if got := lab.optimal.LoadCount(); got != 1 {
		t.Fatalf("concurrent first resolve load count=%d, want 1", got)
	}
}

func TestManifestOptimalMatchesLegacyRuntime(t *testing.T) {
	legacyLab, err := NewAuto(
		core.Default(),
		Configs(testLegacyConfigFS(t)),
		Logics(demo_logic.Logics),
		WithOptimalFS(testOptimalFS(t)),
	)
	if err != nil {
		t.Fatalf("legacy NewAuto: %v", err)
	}
	manifestLab, err := NewAuto(
		core.Default(),
		Configs(testManifestConfigFS(t)),
		Logics(demo_logic.Logics),
		WithOptimalFS(testManifestFS(t, false)),
	)
	if err != nil {
		t.Fatalf("manifest NewAuto: %v", err)
	}

	legacy, err := legacyLab.NewMachine(0, MachineConfig{Seed: SeedInt64(832)})
	if err != nil {
		t.Fatalf("legacy machine: %v", err)
	}
	binaryMachine, err := manifestLab.NewMachine(0, MachineConfig{Seed: SeedInt64(832)})
	if err != nil {
		t.Fatalf("manifest machine: %v", err)
	}
	if got := binaryMachine.optimal.Backend(); got != "memory" {
		t.Fatalf("manifest backend=%q, want memory", got)
	}
	if _, err := manifestLab.resolveOptimal(&spec.GameSetting{
		BetUnits: []int{41},
		OptimalSetting: spec.OptimalSetting{
			UseOptimal: true,
			Artifact:   "game0/manifest.json",
		},
	}); err == nil {
		t.Fatal("cached manifest accepted incompatible bet units")
	}

	for i := 0; i < 100; i++ {
		want, err := json.Marshal(legacy.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}))
		if err != nil {
			t.Fatalf("marshal legacy result: %v", err)
		}
		got, err := json.Marshal(binaryMachine.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}))
		if err != nil {
			t.Fatalf("marshal manifest result: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("spin %d differs between legacy and manifest backends", i)
		}
	}
}

func TestManifestOptimalRejectsCorruptFile(t *testing.T) {
	_, err := NewAuto(
		core.Default(),
		Configs(testManifestConfigFS(t)),
		Logics(demo_logic.Logics),
		WithOptimalFS(testManifestFS(t, true)),
	)
	if err == nil {
		t.Fatal("expected corrupt manifest artifact to fail fast")
	}
}

func testOptimalFS(t testing.TB) fs.FS {
	t.Helper()
	snap1 := testDefaultSnapshot(t, 100)
	snap2 := testDefaultSnapshot(t, 200)
	gacha := Gacha{
		Picker:  sampler.BuildAliasTableF64([]float64{1, 1}),
		SeedLen: len(snap1),
	}
	raw, err := json.Marshal(gacha)
	if err != nil {
		t.Fatalf("marshal test gacha: %v", err)
	}
	var compressed bytes.Buffer
	zw, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("create test zstd writer: %v", err)
	}
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("compress test gacha: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close test zstd writer: %v", err)
	}
	bank := append(append([]byte(nil), snap1...), snap2...)
	return fstest.MapFS{
		"gacha_0.json.zst": &fstest.MapFile{Data: compressed.Bytes()},
		"seed_bank_0.bin":  &fstest.MapFile{Data: bank},
	}
}

func testLegacyConfigFS(t testing.TB) fs.FS {
	t.Helper()
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatalf("read demo config: %v", err)
	}
	replacement := "gachas: [gacha_0.json.zst]\n  seed_bank: [seed_bank_0.bin]"
	enabled := enableTestOptimal(t, string(raw))
	updated := strings.Replace(enabled, "artifact: manifest.json", replacement, 1)
	if updated == enabled {
		t.Fatal("test config legacy block was not replaced")
	}
	return fstest.MapFS{"game.yaml": &fstest.MapFile{Data: []byte(updated)}}
}

func enableTestOptimal(t testing.TB, config string) string {
	t.Helper()
	updated := strings.Replace(config, "use_optimal: false", "use_optimal: true", 1)
	if updated == config {
		t.Fatal("test config optimal flag was not enabled")
	}
	return updated
}

func testPCGManifestFS(t testing.TB) fs.FS {
	t.Helper()
	factory := core.PCG64()
	snapshots := make([][]byte, 2)
	for i, seed := range []int64{100, 200} {
		rng, err := factory.New(core.EncodeInt64Seed(seed))
		if err != nil {
			t.Fatal(err)
		}
		snapshots[i], err = rng.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
	}
	table := sampler.BuildAliasTableF64([]float64{1, 1})
	prob := make([]byte, table.Size*8)
	aliases := make([]byte, table.Size*4)
	for i := 0; i < table.Size; i++ {
		binary.LittleEndian.PutUint64(prob[i*8:], math.Float64bits(table.Prob[i]))
		binary.LittleEndian.PutUint32(aliases[i*4:], uint32(table.Aliases[i]))
	}
	bank := append(append([]byte(nil), snapshots[0]...), snapshots[1]...)
	manifest := optimalrt.Manifest{
		SchemaVersion:  optimalrt.ManifestSchemaV1,
		ArtifactID:     "explicit-pcg64-compatibility-fixture",
		SnapshotFormat: "go.math/rand/v2.PCG.MarshalBinary/v1",
		Modes: []optimalrt.ManifestMode{{
			BetUnit: 40, Size: table.Size, SeedLen: len(snapshots[0]), SeedCount: table.Size,
			Prob: testFileRef("prob.bin", prob), Aliases: testFileRef("aliases.bin", aliases),
			SeedBank: testFileRef("seed_bank.bin", bank),
		}},
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{
		"game0/manifest.json": &fstest.MapFile{Data: manifestRaw},
		"game0/prob.bin":      &fstest.MapFile{Data: prob},
		"game0/aliases.bin":   &fstest.MapFile{Data: aliases},
		"game0/seed_bank.bin": &fstest.MapFile{Data: bank},
	}
}

func testDefaultSnapshot(t testing.TB, seed int64) []byte {
	t.Helper()
	rng, err := core.Default().New(core.EncodeInt64Seed(seed))
	if err != nil {
		t.Fatalf("create PRNG: %v", err)
	}
	snapshot, err := rng.Snapshot()
	if err != nil {
		t.Fatalf("snapshot PRNG: %v", err)
	}
	return snapshot
}

func testFileRef(name string, data []byte) optimalrt.FileRef {
	sum := sha256.Sum256(data)
	return optimalrt.FileRef{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

func TestOptimalLifecycleClosesPoolsBeforeArtifact(t *testing.T) {
	lab, err := NewAuto(
		core.Default(),
		Configs(testLegacyConfigFS(t)),
		Logics(demo_logic.Logics),
		WithOptimalFS(testOptimalFS(t)),
	)
	if err != nil {
		t.Fatalf("NewAuto: %v", err)
	}
	machine, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(1)})
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	artifact := machine.optimal

	runtime, err := lab.BuildRuntime(2)
	if err != nil {
		t.Fatalf("BuildRuntime: %v", err)
	}
	runtime.Close()
	for _, id := range runtime.ids {
		if !runtime.pools[id].Closed() {
			t.Fatalf("pool %d was not closed with runtime", id)
		}
	}
	if _, err := runtime.Spin(context.Background(), nil); err == nil {
		t.Fatal("closed runtime accepted a spin")
	}
	if artifact.Closed() {
		t.Fatal("runtime close must not close Problab-owned artifact")
	}

	if err := lab.Close(); err != nil {
		t.Fatalf("Problab.Close: %v", err)
	}
	if err := lab.Close(); err != nil {
		t.Fatalf("second Problab.Close: %v", err)
	}
	if !lab.Closed() || !artifact.Closed() {
		t.Fatal("Problab close did not close its artifact store")
	}
	if _, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(2)}); err == nil {
		t.Fatal("closed Problab created a new machine")
	}
}

// manifest 與 gacha 兩種格式：WithOptimalDir 在支援的平台上以 mmap 載入，結果與 WithOptimalFS
// 相同，同一個 Lab 內只載入一次，Problab.Close 後關閉。
func TestMMapOptimalMatchesMemoryAndIsShared(t *testing.T) {
	for _, fx := range []struct {
		name           string
		source, config fs.FS
	}{
		{"manifest", testManifestFS(t, false), testManifestConfigFS(t)},
		{"gacha", testOptimalFS(t), testLegacyConfigFS(t)},
	} {
		t.Run(fx.name, func(t *testing.T) {
			testMMapOptimalMatchesMemory(t, fx.source, fx.config)
		})
	}
}

func testMMapOptimalMatchesMemory(t *testing.T, source, config fs.FS) {
	dir := materializeTestFS(t, source)

	memoryLab, err := NewAuto(
		core.Default(), Configs(config), Logics(demo_logic.Logics), WithOptimalFS(source),
	)
	if err != nil {
		t.Fatalf("memory NewAuto: %v", err)
	}
	mmapLab, err := NewAuto(
		core.Default(), Configs(config), Logics(demo_logic.Logics), WithOptimalDir(dir),
	)
	if err != nil {
		t.Fatalf("mmap NewAuto: %v", err)
	}

	memoryMachine, err := memoryLab.NewMachine(0, MachineConfig{Seed: SeedInt64(5150)})
	if err != nil {
		t.Fatalf("memory machine: %v", err)
	}
	mmapMachine1, err := mmapLab.NewMachine(0, MachineConfig{Seed: SeedInt64(5150)})
	if err != nil {
		t.Fatalf("mmap machine 1: %v", err)
	}
	mmapMachine2, err := mmapLab.NewMachine(0, MachineConfig{Seed: SeedInt64(5151)})
	if err != nil {
		t.Fatalf("mmap machine 2: %v", err)
	}
	if mmapMachine1.optimal != mmapMachine2.optimal || mmapLab.optimal.LoadCount() != 1 {
		t.Fatal("mmap artifact was not shared exactly once")
	}
	wantBackend := "memory"
	switch runtime.GOOS {
	case "aix", "darwin", "dragonfly", "freebsd", "linux", "netbsd", "openbsd", "solaris":
		wantBackend = "mmap"
	}
	if got := mmapMachine1.optimal.Backend(); got != wantBackend {
		t.Fatalf("backend=%q, want %q", got, wantBackend)
	}

	for i := 0; i < 100; i++ {
		want, err := json.Marshal(memoryMachine.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}))
		if err != nil {
			t.Fatalf("marshal memory result: %v", err)
		}
		got, err := json.Marshal(mmapMachine1.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}))
		if err != nil {
			t.Fatalf("marshal mmap result: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("spin %d differs between memory and mmap", i)
		}
	}
	if err := mmapLab.Close(); err != nil {
		t.Fatalf("mmap Problab.Close: %v", err)
	}
	if !mmapMachine1.optimal.Closed() {
		t.Fatal("mmap artifact was not closed")
	}
}

// seed bank 的快照長度相符、格式卻不屬於 runtime 的 PRNG 時，建構 Lab 就以首筆檢查失敗。
func TestOptimalRejectsForeignSnapshotFormat(t *testing.T) {
	rng, err := core.PCG64().New(core.EncodeInt64Seed(100))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := rng.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	source := testOptimalFS(t).(fstest.MapFS)
	bank := source["seed_bank_0.bin"].Data
	seedLen := len(bank) / 2
	if len(foreign) > seedLen {
		t.Fatalf("PCG snapshot (%d bytes) is longer than the default one (%d)", len(foreign), seedLen)
	}
	padded := append(append([]byte(nil), foreign...), make([]byte, seedLen-len(foreign))...)
	source["seed_bank_0.bin"] = &fstest.MapFile{Data: append(padded, bank[seedLen:]...)}
	_, err = NewAuto(core.Default(), Configs(testLegacyConfigFS(t)), Logics(demo_logic.Logics), WithOptimalFS(source))
	if err == nil || !strings.Contains(err.Error(), "first snapshot") {
		t.Fatalf("NewAuto error %v, want the first-snapshot check", err)
	}
}

func TestOptimalSourcesAreMutuallyExclusive(t *testing.T) {
	_, err := New(
		core.Default(),
		Configs(testManifestConfigFS(t)),
		Logics(demo_logic.Logics),
		WithOptimalFS(testManifestFS(t, false)),
		WithOptimalDir(t.TempDir()),
	)
	if err == nil {
		t.Fatal("expected mutually exclusive optimal source error")
	}
}

func materializeTestFS(t *testing.T, source fs.FS) string {
	t.Helper()
	root := t.TempDir()
	err := fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("materialize artifact FS: %v", err)
	}
	return root
}

// TestUnoptimizedMachinePreservesReplayIdentity locks the collection
// boundary needed by optimizer/v2. Game 0 enables a published Optimal Artifact;
// a normal Machine therefore owns that artifact, while the raw optimizer Machine
// must bypass it and reproduce a spin directly from its pre-spin Core snapshot.
func TestUnoptimizedMachinePreservesReplayIdentity(t *testing.T) {
	lab, err := NewAuto(
		core.Default(), Configs(testManifestConfigFS(t)), Logics(demo_logic.Logics),
		WithOptimalFS(testManifestFS(t, false)),
	)
	if err != nil {
		t.Fatalf("NewAuto: %v", err)
	}
	defer func() { _ = lab.Close() }()

	normal, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(77)})
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if normal.optimal == nil {
		t.Fatal("game 0 fixture did not enable its existing Optimal Artifact")
	}
	raw, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(77), Unoptimized: true})
	if err != nil {
		t.Fatalf("NewMachine(Unoptimized): %v", err)
	}
	if raw.optimal != nil {
		t.Fatal("raw optimizer Machine retained an Optimal Artifact")
	}

	before, err := raw.SnapshotCore()
	if err != nil {
		t.Fatalf("SnapshotCore: %v", err)
	}
	first, err := json.Marshal(raw.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}))
	if err != nil {
		t.Fatalf("marshal first raw spin: %v", err)
	}
	if err := raw.RestoreCore(before); err != nil {
		t.Fatalf("RestoreCore: %v", err)
	}
	second, err := json.Marshal(raw.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}))
	if err != nil {
		t.Fatalf("marshal replayed raw spin: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("pre-spin raw Core snapshot did not reproduce the collected outcome")
	}
}
