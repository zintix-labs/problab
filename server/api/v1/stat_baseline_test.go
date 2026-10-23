// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package v1

// B0 server case: the statistics API output recorded before results are
// assembled into whole games. It is a temporary acceptance tool and is removed
// after the change is accepted. Recording uses the same switch as the root
// package: PROBLAB_B0=record.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b0StatSHA256 pins b0/stat.json.
const b0StatSHA256 = "95b3c096171b84ececfdd8defb581e931ad3b6647a958b2d344ee3f9de401b01"

const (
	b0StatDir      = "testdata/game_baseline"
	b0StatFile     = "b0/stat.json"
	b0StatManifest = "b0-stat.sha256"
)

// b0StatInput builds a fixed request: rounds with and without a trigger, and
// free wins only on triggered rounds.
func b0StatInput(t *testing.T) []byte {
	t.Helper()
	const rounds = 60
	in := DistStat{GameName: "b0", BetUnits: []int{40}, BetMode: 0, BetMult: 1, Bet: 40}
	x := uint32(2026)
	for range rounds {
		x = x*1664525 + 1013904223
		base := int(x>>20) % 120
		free, trig := 0, 0
		if x%7 == 0 {
			trig = 1
			free = int(x>>12) % 900
		}
		in.BaseWins = append(in.BaseWins, base)
		in.FreeWins = append(in.FreeWins, free)
		in.Triggers = append(in.Triggers, trig)
		in.TotalWins = append(in.TotalWins, base+free)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func b0StatOutput(t *testing.T) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/stat", bytes.NewReader(b0StatInput(t)))
	rec := httptest.NewRecorder()
	Stat(rec, req)
	out := fmt.Sprintf("status=%d\n%s", rec.Code, rec.Body.Bytes())
	return []byte(out)
}

func TestGameBaselineStat(t *testing.T) {
	mode := os.Getenv("PROBLAB_B0")
	if mode == "" && testing.Short() {
		t.Skip("consumer baseline comparison is skipped under -short")
	}
	path := filepath.Join(b0StatDir, b0StatFile)
	switch mode {
	case "record":
		first, second := b0StatOutput(t), b0StatOutput(t)
		if !bytes.Equal(first, second) {
			t.Fatal("two recordings on the same commit differ; the baseline is not reproducible")
		}
		if !bytes.HasPrefix(first, []byte("status=200\n")) {
			t.Fatalf("stat API failed: %s", first)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, first, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(first)
		manifest := fmt.Sprintf("%x  stat.json\n", sum)
		if err := os.WriteFile(filepath.Join(b0StatDir, b0StatManifest), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded B0 stat: SHA-256=%x", sum)
	case "":
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(want))
		manifest, err := os.ReadFile(filepath.Join(b0StatDir, b0StatManifest))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(manifest), sum+" ") {
			t.Fatalf("%s does not match %s; the baseline was modified", path, b0StatManifest)
		}
		if sum != b0StatSHA256 {
			t.Fatalf("%s SHA-256=%s does not match the pinned constant %q", path, sum, b0StatSHA256)
		}
		if got := b0StatOutput(t); !bytes.Equal(got, want) {
			t.Fatalf("stat output differs from B0\n got=%s", got)
		}
	default:
		t.Fatalf("unknown PROBLAB_B0=%q", mode)
	}
}
