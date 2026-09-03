// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package optimizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/sampler"
)

func TestSaveModePublishesManifestOnlyWhenBundleComplete(t *testing.T) {
	t.Chdir(t.TempDir())
	rng1, err := core.Default().New(core.EncodeInt64Seed(11))
	if err != nil {
		t.Fatalf("PRNG 1: %v", err)
	}
	snap1, err := rng1.Snapshot()
	if err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}
	rng2, err := core.Default().New(core.EncodeInt64Seed(22))
	if err != nil {
		t.Fatalf("PRNG 2: %v", err)
	}
	snap2, err := rng2.Snapshot()
	if err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}
	bank := append(append([]byte(nil), snap1...), snap2...)
	gacha := &Gacha{Picker: sampler.BuildAliasTableF64([]float64{1, 3}), SeedLen: len(snap1)}
	tuner := new(Tuner)
	betUnits := []int{10, 20}
	format := core.SnapshotFormatOfPRNG(rng1)

	if err := tuner.SaveMode(7, 0, betUnits, format, gacha, bank); err != nil {
		t.Fatalf("SaveMode 0: %v", err)
	}
	manifestPath := filepath.Join("build", "optimizer", "game_7", "manifest.json")
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("manifest published before all modes were present: %v", err)
	}
	if err := tuner.SaveMode(7, 1, betUnits, format, gacha, bank); err != nil {
		t.Fatalf("SaveMode 1: %v", err)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest optimalrt.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("invalid published manifest: %v", err)
	}
	if len(manifest.Modes) != 2 || manifest.Modes[0].BetUnit != 10 || manifest.Modes[1].BetUnit != 20 {
		t.Fatalf("unexpected manifest modes: %+v", manifest.Modes)
	}
}
