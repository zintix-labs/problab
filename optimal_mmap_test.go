// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/core"
)

func TestMMapOptimalMatchesMemoryAndIsShared(t *testing.T) {
	source := testManifestFS(t, false)
	dir := materializeTestFS(t, source)
	config := testManifestConfigFS(t)

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

	memoryMachine, err := memoryLab.NewMachineWithSeed(0, 5150, true)
	if err != nil {
		t.Fatalf("memory machine: %v", err)
	}
	mmapMachine1, err := mmapLab.NewMachineWithSeed(0, 5150, true)
	if err != nil {
		t.Fatalf("mmap machine 1: %v", err)
	}
	mmapMachine2, err := mmapLab.NewMachineWithSeed(0, 5151, true)
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
		want, err := json.Marshal(memoryMachine.SpinInternal(0))
		if err != nil {
			t.Fatalf("marshal memory result: %v", err)
		}
		got, err := json.Marshal(mmapMachine1.SpinInternal(0))
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
