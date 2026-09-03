// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Command regenerate-demo-optimal rebuilds a local-only runtime example using
// snapshots produced by the current default PRNG. The generated Artifact is
// ignored by Git and is a loader/runtime fixture, not a production bundle.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing/fstest"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/sdk/core"
)

const (
	demoSnapshotCount = 200
	demoSourceSpins   = 1_000_000
)

func main() {
	root, err := repositoryRoot()
	if err != nil {
		panic(err)
	}
	dir := filepath.Join(root, "demo", "optimal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	factory := core.Default()
	bank, seedLen, sourceRTP, bankRTP, err := collectRepresentativeSnapshots(factory)
	if err != nil {
		panic(err)
	}
	prob := make([]byte, demoSnapshotCount*8)
	aliases := make([]byte, demoSnapshotCount*4)
	for i := range demoSnapshotCount {
		binary.LittleEndian.PutUint64(prob[i*8:], math.Float64bits(1))
		binary.LittleEndian.PutUint32(aliases[i*4:], uint32(i))
	}
	mustWrite(filepath.Join(dir, "prob_0.bin"), prob)
	mustWrite(filepath.Join(dir, "aliases_0.bin"), aliases)
	seedBankPath := filepath.Join(dir, "seed_bank_0.bin")
	mustWrite(seedBankPath, bank)

	mode := optimalrt.ManifestMode{
		BetUnit:   40,
		Size:      demoSnapshotCount,
		SeedLen:   seedLen,
		SeedCount: demoSnapshotCount,
		Prob:      fileRef("prob_0.bin", prob),
		Aliases:   fileRef("aliases_0.bin", aliases),
		SeedBank:  fileRef("seed_bank_0.bin", bank),
	}
	format := core.SnapshotFormatOfPRNG(mustNew(factory))
	manifest := optimalrt.Manifest{
		SchemaVersion:  optimalrt.ManifestSchemaV1,
		SnapshotFormat: format,
		Modes:          []optimalrt.ManifestMode{mode},
	}
	manifest.ArtifactID = artifactID(format, manifest.Modes)
	if err := manifest.Validate(); err != nil {
		panic(err)
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		panic(err)
	}
	mustWrite(filepath.Join(dir, "manifest.json"), append(raw, '\n'))
	fmt.Printf(
		"regenerated %d %s snapshots (%d bytes each); source RTP %.6f, bank RTP %.6f\n",
		demoSnapshotCount, format, seedLen, sourceRTP, bankRTP,
	)
}

type collectedSnapshot struct {
	state []byte
	win   int
}

func collectRepresentativeSnapshots(factory core.PRNGFactory) ([]byte, int, float64, float64, error) {
	raw, err := demo_configs.FS.ReadFile("game_0_demonormal.yaml")
	if err != nil {
		return nil, 0, 0, 0, err
	}
	lab, err := problab.NewAuto(
		factory,
		problab.Configs(fstest.MapFS{"game.yaml": &fstest.MapFile{Data: raw}}),
		problab.Logics(demo_logic.Logics),
		problab.WithSeedEntropy(bytes.NewReader(make([]byte, 32))),
	)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	defer func() { _ = lab.Close() }()
	machine, err := lab.NewMachineWithSeedBytes(0, core.EncodeInt64Seed(0), true)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	samples := make([]collectedSnapshot, demoSourceSpins)
	totalWin := int64(0)
	for i := range samples {
		snapshot, err := machine.SnapshotCore()
		if err != nil {
			return nil, 0, 0, 0, err
		}
		result := machine.SpinInternal(0)
		samples[i] = collectedSnapshot{state: snapshot, win: result.TotalWin}
		totalWin += int64(result.TotalWin)
	}
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].win < samples[j].win })

	seedLen := len(samples[0].state)
	bank := make([]byte, 0, demoSnapshotCount*seedLen)
	bankWin := int64(0)
	for bin := range demoSnapshotCount {
		start := bin * len(samples) / demoSnapshotCount
		end := (bin + 1) * len(samples) / demoSnapshotCount
		binWin := int64(0)
		for _, sample := range samples[start:end] {
			binWin += int64(sample.win)
		}
		best := start
		bestDistance := absScaledDistance(samples[start].win, binWin, end-start)
		for i := start + 1; i < end; i++ {
			distance := absScaledDistance(samples[i].win, binWin, end-start)
			if distance < bestDistance {
				best = i
				bestDistance = distance
			}
		}
		if len(samples[best].state) != seedLen {
			return nil, 0, 0, 0, fmt.Errorf("default PRNG snapshot size is not fixed")
		}
		bank = append(bank, samples[best].state...)
		bankWin += int64(samples[best].win)
	}
	return bank, seedLen,
		float64(totalWin) / float64(demoSourceSpins*40),
		float64(bankWin) / float64(demoSnapshotCount*40), nil
}

func absScaledDistance(win int, sum int64, count int) int64 {
	distance := int64(win)*int64(count) - sum
	if distance < 0 {
		return -distance
	}
	return distance
}

func repositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repository root not found")
		}
		dir = parent
	}
}

func mustNew(factory core.PRNGFactory) core.PRNG {
	rng, err := factory.New(core.EncodeInt64Seed(0))
	if err != nil {
		panic(err)
	}
	return rng
}

func fileRef(name string, data []byte) optimalrt.FileRef {
	digest := sha256.Sum256(data)
	return optimalrt.FileRef{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
}

func artifactID(snapshotFormat string, modes []optimalrt.ManifestMode) string {
	raw, _ := json.Marshal(struct {
		SnapshotFormat string                   `json:"snapshot_format"`
		Modes          []optimalrt.ManifestMode `json:"modes"`
	}{snapshotFormat, modes})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func mustWrite(path string, data []byte) {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		panic(err)
	}
}
