// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"math"
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
	machine, err := lab.NewMachineWithSeed(0, 99, true)
	if err != nil {
		t.Fatalf("NewMachineWithSeed: %v", err)
	}
	if machine.optimal == nil || machine.optimal.Backend() != "memory" {
		t.Fatal("fixture did not load its manifest memory artifact")
	}
	if result := machine.SpinInternal(0); result == nil {
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
	machine, err := lab.NewMachineWithSeed(0, 832, true)
	if err != nil {
		t.Fatal(err)
	}
	if result := machine.SpinInternal(0); result == nil {
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

	m1, err := lab.NewMachineWithSeed(0, 19, true)
	if err != nil {
		t.Fatalf("NewMachineWithSeed m1: %v", err)
	}
	m2, err := lab.NewMachineWithSeed(0, 19, true)
	if err != nil {
		t.Fatalf("NewMachineWithSeed m2: %v", err)
	}

	r1, err := json.Marshal(m1.SpinInternal(0))
	if err != nil {
		t.Fatalf("marshal result 1: %v", err)
	}
	r2, err := json.Marshal(m2.SpinInternal(0))
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

	m1, err := lab.NewMachineWithSeed(0, 1, true)
	if err != nil {
		t.Fatalf("NewMachineWithSeed m1: %v", err)
	}
	m2, err := lab.NewMachineWithSeed(0, 2, true)
	if err != nil {
		t.Fatalf("NewMachineWithSeed m2: %v", err)
	}
	if m1.optimal == nil || m1.optimal != m2.optimal {
		t.Fatal("machines do not share the same immutable artifact")
	}

	sim, err := lab.NewSimulatorWithSeed(0, 3)
	if err != nil {
		t.Fatalf("NewSimulatorWithSeed: %v", err)
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
			machine, err := lab.NewMachineWithSeed(0, seed, true)
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

	legacy, err := legacyLab.NewMachineWithSeed(0, 832, true)
	if err != nil {
		t.Fatalf("legacy machine: %v", err)
	}
	binaryMachine, err := manifestLab.NewMachineWithSeed(0, 832, true)
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
		want, err := json.Marshal(legacy.SpinInternal(0))
		if err != nil {
			t.Fatalf("marshal legacy result: %v", err)
		}
		got, err := json.Marshal(binaryMachine.SpinInternal(0))
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
