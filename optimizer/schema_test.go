// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package optimizer

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/sampler"
)

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

// 每個 bet mode 各自發布到 game_<gid>/mode_<betMode>/ 底下，互不覆蓋；既有的發布目錄不被改寫。
func TestSaveKeepsBetModesApart(t *testing.T) {
	t.Chdir(t.TempDir())
	g0, bank0 := testSaveInput(t, 1, 2)
	g1, bank1 := testSaveInput(t, 3, 4, 5)
	s0, err := new(Tuner).Save(7, 0, g0, bank0)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := new(Tuner).Save(7, 1, g1, bank1)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(s0.Dir) != filepath.Join("build", "optimizer", "game_7", "mode_0") ||
		filepath.Dir(s1.Dir) != filepath.Join("build", "optimizer", "game_7", "mode_1") {
		t.Fatalf("directories %s and %s", s0.Dir, s1.Dir)
	}
	for _, c := range []struct {
		saved SavedGacha
		bank  []byte
	}{{s0, bank0}, {s1, bank1}} {
		got, err := os.ReadFile(c.saved.SeedBank)
		if err != nil || !bytes.Equal(got, c.bank) {
			t.Fatalf("%s: seed bank differs: %v", c.saved.Dir, err)
		}
	}

	// 同一個 bet mode 換一份內容：新開目錄，舊目錄原樣保留。
	g2, bank2 := testSaveInput(t, 6, 7)
	s2, err := new(Tuner).Save(7, 0, g2, bank2)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Dir == s0.Dir || s2.Reused {
		t.Fatal("different content reused the published directory")
	}
	if got, err := os.ReadFile(s0.SeedBank); err != nil || !bytes.Equal(got, bank0) {
		t.Fatalf("the earlier publication changed: %v", err)
	}
}

// 同一份內容重跑時沿用既有目錄。
func TestSaveReusesSameContent(t *testing.T) {
	t.Chdir(t.TempDir())
	gacha, bank := testSaveInput(t, 1, 2)
	first, err := new(Tuner).Save(3, 0, gacha, bank)
	if err != nil {
		t.Fatal(err)
	}
	again, err := new(Tuner).Save(3, 0, gacha, bank)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reused || !again.Reused || again.Dir != first.Dir || again.ID != first.ID {
		t.Fatalf("first %+v again %+v", first, again)
	}
}

// Save 的輸出可由 WithOptimalDir 直接載入。
func TestSaveOutputLoads(t *testing.T) {
	t.Chdir(t.TempDir())
	gacha, bank := testSaveInput(t, 1, 2, 3)
	saved, err := new(Tuner).Save(0, 0, gacha, bank)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("build", "optimizer")
	rel := func(path string) string {
		r, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		return filepath.ToSlash(r)
	}
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	block := "optimal_setting:\n  use_optimal: true\n  gachas: [" + rel(saved.Gacha) + "]\n  seed_bank: [" + rel(saved.SeedBank) + "]\n\n"
	cfg := regexp.MustCompile(`(?s)optimal_setting:.*?\n\n`).ReplaceAllLiteral(raw, []byte(block))
	lab, err := problab.NewAuto(core.Default(), problab.Configs(fstest.MapFS{"game.yaml": &fstest.MapFile{Data: cfg}}),
		problab.Logics(demo_logic.Logics), problab.WithOptimalDir(root))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	m, err := lab.NewMachine(0, problab.MachineConfig{Seed: problab.SeedInt64(5)})
	if err != nil {
		t.Fatal(err)
	}
	if m.SpinUnsafe(problab.SpinInput{BetMode: 0, BetMult: 1}) == nil {
		t.Fatal("optimal spin returned nil")
	}
}

// 發布失敗時不留下暫存目錄，也不影響已發布的目錄。
func TestSaveFailureLeavesNoStaging(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test cannot write to")
	}
	t.Chdir(t.TempDir())
	gacha, bank := testSaveInput(t, 1, 2)
	published, err := new(Tuner).Save(3, 0, gacha, bank)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(published.Dir)
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	other, otherBank := testSaveInput(t, 7, 8, 9)
	if _, err := new(Tuner).Save(3, 0, other, otherBank); err == nil {
		t.Fatal("Save succeeded in a read-only directory")
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || strings.HasPrefix(entries[0].Name(), ".") {
		t.Fatalf("entries after a failed Save: %v", entries)
	}
	if got, err := os.ReadFile(published.SeedBank); err != nil || !bytes.Equal(got, bank) {
		t.Fatalf("the published pair changed: %v", err)
	}
}
