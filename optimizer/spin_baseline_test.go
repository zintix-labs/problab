// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package optimizer

// U0 legacy optimizer case, recorded before SpinInternal is replaced by
// SpinUnsafe. This is a temporary acceptance tool and is removed after the
// migration is accepted.
//
// Record: PROBLAB_RECORD_SPIN_BASELINE=u0 go test -run TestSpinBaselineLegacyOptimizer .

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/spec"
)

// spinU0LegacySHA256 pins testdata/spin_baseline/u0/legacy.json.
const spinU0LegacySHA256 = "98688e58d0c2c3db18bba0939cad232f1305fd8d298637e405d23fd8c536a411"

const spinLegacyDir = "testdata/spin_baseline"

// spinLegacyOutput runs the legacy collection loop for both demo games and
// records every collected sample (class, win, pre-spin Core snapshot).
func spinLegacyOutput(t *testing.T) []byte {
	t.Helper()
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	if err := NewRegisterTags(nil); err != nil {
		t.Fatal(err)
	}
	tagger, err := GetTagger(bg, fg)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{}
	for _, gid := range []spec.GID{0, 1} {
		tuner := &Tuner{
			tager: tagger,
			Classes: []*Class{
				{name: "bg", collect: 24, minWin: 0, maxWin: 1e9, tagMask: tagger.Mask(bg)},
				{name: "any", collect: 24, minWin: 0, maxWin: 1e9},
			},
		}
		if err := tuner.collect(gid, 0, lab, fmt.Appendf(nil, "u0-legacy-%d", gid)); err != nil {
			t.Fatalf("legacy collect game %d: %v", gid, err)
		}
		for _, c := range tuner.Classes {
			for _, s := range c.samps {
				lines = append(lines, fmt.Sprintf("g%d|%s|%x|%s", gid, s.CName, s.Win, hex.EncodeToString(s.CoreSnap)))
			}
		}
	}
	raw, err := json.MarshalIndent(map[string]any{"schema": "problab-spin-baseline/v1", "samples": lines}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func TestSpinBaselineLegacyOptimizer(t *testing.T) {
	mode := os.Getenv("PROBLAB_RECORD_SPIN_BASELINE")
	if mode == "" && testing.Short() {
		t.Skip("consumer baseline comparison is skipped under -short")
	}
	path := filepath.Join(spinLegacyDir, "u0", "legacy.json")
	manifestPath := filepath.Join(spinLegacyDir, "u0-legacy.sha256")
	switch mode {
	case "u0":
		first, second := spinLegacyOutput(t), spinLegacyOutput(t)
		if !bytes.Equal(first, second) {
			t.Fatal("two recordings on the same commit differ; the baseline is not reproducible")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, first, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(first)
		if err := os.WriteFile(manifestPath, fmt.Appendf(nil, "%x  legacy.json\n", sum), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded U0 legacy optimizer: SHA-256=%x", sum)
	case "":
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(want))
		manifest, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(manifest), sum+" ") {
			t.Fatal("legacy.json does not match u0-legacy.sha256; the baseline was modified")
		}
		if sum != spinU0LegacySHA256 {
			t.Fatalf("legacy.json SHA-256=%s does not match the pinned constant %q", sum, spinU0LegacySHA256)
		}
		if got := spinLegacyOutput(t); !bytes.Equal(got, want) {
			t.Fatalf("legacy optimizer output differs from U0\n got=%s", got)
		}
	default:
		t.Fatalf("unknown PROBLAB_RECORD_SPIN_BASELINE=%q", mode)
	}
}
