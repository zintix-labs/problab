// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package optimizer

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func testSaveInput(t *testing.T, seeds ...int64) (*Gacha, []byte) {
	t.Helper()
	var bank []byte
	seedLen := 0
	for _, seed := range seeds {
		rng, err := core.Default().New(core.EncodeInt64Seed(seed))
		if err != nil {
			t.Fatal(err)
		}
		snap, err := rng.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		seedLen = len(snap)
		bank = append(bank, snap...)
	}
	weights := make([]float64, len(seeds))
	for i := range weights {
		weights[i] = float64(i + 1)
	}
	return &Gacha{Picker: sampler.BuildAliasTableF64(weights), SeedLen: seedLen}, bank
}

// Save 以「暫存檔再改名」替換輸出：已開啟的舊檔仍讀得到舊內容，沒有被截斷或原地改寫。
func TestSaveReplacesFilesWithoutTruncating(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("renaming over an open file is not supported on windows")
	}
	t.Chdir(t.TempDir())
	gacha, oldBank := testSaveInput(t, 1, 2)
	if err := new(Tuner).Save(3, gacha, oldBank); err != nil {
		t.Fatal(err)
	}
	bankPath := filepath.Join("build", "optimizer", "seed_bank_3.bin")
	old, err := os.Open(bankPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()

	gacha, newBank := testSaveInput(t, 5, 6)
	if err := new(Tuner).Save(3, gacha, newBank); err != nil {
		t.Fatal(err)
	}
	stale, err := io.ReadAll(old)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stale, oldBank) {
		t.Fatal("the open seed bank was rewritten in place")
	}
	current, err := os.ReadFile(bankPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, newBank) {
		t.Fatal("the seed bank path does not hold the new content")
	}
}

// 某個檔案替換失敗時，該檔案保持原內容，輸出目錄不留暫存檔。
func TestSaveFailureKeepsExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test cannot write to")
	}
	t.Chdir(t.TempDir())
	gacha, bank := testSaveInput(t, 1, 2)
	if err := new(Tuner).Save(3, gacha, bank); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join("build", "optimizer")
	gachaPath := filepath.Join(out, "gacha_3.json.zst")
	before, err := os.ReadFile(gachaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(out, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(out, 0o755) })

	other, otherBank := testSaveInput(t, 7, 8, 9)
	if err := new(Tuner).Save(3, other, otherBank); err == nil {
		t.Fatal("Save succeeded in a read-only directory")
	}
	after, err := os.ReadFile(gachaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("a failed Save changed the existing gacha")
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			t.Fatalf("temporary file %s was left behind", entry.Name())
		}
	}
}
