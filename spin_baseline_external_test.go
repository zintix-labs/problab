// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab_test

// U0 playerexp case. playerexp imports problab, so the case lives in the
// external test package with its own data file and pinned constant. Recording
// uses the same switch as TestSpinBaseline: PROBLAB_RECORD_SPIN_BASELINE=u0.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/playerexp"
	"github.com/zintix-labs/problab/playerexp/strategies"
	"github.com/zintix-labs/problab/sdk/buf"
)

// spinU0PlayerexpSHA256 pins u0/playerexp.json.
const spinU0PlayerexpSHA256 = "ddfcbf21a90f0ebac261a28410e9d468c99263a342a6ee4a10b5484653952063"

const (
	spinPlayerexpDir      = "testdata/spin_baseline"
	spinPlayerexpFile     = "u0/playerexp.json"
	spinPlayerexpManifest = "u0-playerexp.sha256"
)

// spinCallLog records every strategy and reporter callback in call order.
// playerexp runs players sequentially on the Run goroutine, so no lock is needed.
type spinCallLog struct{ lines []string }

func (l *spinCallLog) add(format string, a ...any) { l.lines = append(l.lines, fmt.Sprintf(format, a...)) }

func (l *spinCallLog) Report(e playerexp.ProgressEvent) {
	l.add("report|%s|%d|%s|%d|%d", e.Kind, e.GroupIndex, e.GroupName, e.Completed, e.Total)
}

type spinRecordingFactory struct {
	name  string
	inner playerexp.StrategyFactory
	log   *spinCallLog
}

func (f spinRecordingFactory) NewStrategy(initial int) playerexp.Strategy {
	f.log.add("new|%s|%d", f.name, initial)
	return spinRecordingStrategy{name: f.name, inner: f.inner.NewStrategy(initial), log: f.log}
}

type spinRecordingStrategy struct {
	name  string
	inner playerexp.Strategy
	log   *spinCallLog
}

func (s spinRecordingStrategy) FirstSpin(st playerexp.PlayerState) playerexp.Decision {
	d := s.inner.FirstSpin(st)
	s.log.add("first|%s|%+v|%+v", s.name, st, d)
	return d
}

// Next records the full borrowed result the strategy receives, including the
// start and after RNG snapshots, before the next spin overwrites it.
func (s spinRecordingStrategy) Next(st playerexp.PlayerState, r *buf.SpinResult) playerexp.Decision {
	res, err := dto.NewSpinResultDTO(r)
	if err != nil {
		panic(err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(raw)
	d := s.inner.Next(st, r)
	s.log.add("next|%s|%+v|%s|%+v", s.name, st, hex.EncodeToString(sum[:]), d)
	return d
}

type spinPlayerexpRecord struct {
	Report     any    `json:"report"`
	CallCount  int    `json:"call_count"`
	CallDigest string `json:"call_digest"`
	FirstCalls []string `json:"first_calls"`
}

func spinPlayerexpOutput(t *testing.T) []byte {
	t.Helper()
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	c, err := playerexp.ParseConfig([]byte(`game_id: 0
seed: 77
analysis:
  - name: flat
    player: {players: 12, init_bet: 300, strategy: flat}
    win_buckets: [0, 1, 2, 5]
  - name: double
    player: {players: 8, init_bet: 300, strategy: double}
    win_buckets: [0, 1, 2, 5]
`))
	if err != nil {
		t.Fatal(err)
	}
	log := &spinCallLog{}
	rg := playerexp.NewStrategyRegistry()
	for name, f := range map[string]playerexp.StrategyFactory{
		"flat":   strategies.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 200, SatisfiedRatio: 3},
		"double": strategies.Monotone{BetMode: 0, BetMult: 2, MaxSpins: 200, SatisfiedRatio: 3},
	} {
		if err := rg.Register(0, name, spinRecordingFactory{name: name, inner: f, log: log}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := playerexp.Run(context.Background(), c, playerexp.Options{Lab: lab, Strategies: rg, Reporter: log})
	if err != nil || r.Status != playerexp.Completed {
		t.Fatalf("playerexp run: status=%v err=%v", r.Status, err)
	}
	// Keep only integer and non-numeric report fields; derived floats are not compared.
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		t.Fatal(err)
	}
	digest := []byte(nil)
	for _, line := range log.lines {
		next := sha256.Sum256(append(append([]byte(nil), digest...), line...))
		digest = next[:]
	}
	first := log.lines
	if len(first) > 5 {
		first = first[:5]
	}
	out, err := json.MarshalIndent(spinPlayerexpRecord{
		Report: spinExternalKeepInts(tree), CallCount: len(log.lines),
		CallDigest: hex.EncodeToString(digest), FirstCalls: first,
	}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func spinExternalKeepInts(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if kept := spinExternalKeepInts(val); kept != nil {
				out[k] = kept
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, val := range x {
			out = append(out, spinExternalKeepInts(val))
		}
		return out
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return x
		}
		return nil
	default:
		return x
	}
}

func TestSpinBaselinePlayerexp(t *testing.T) {
	mode := os.Getenv("PROBLAB_RECORD_SPIN_BASELINE")
	if mode == "" && testing.Short() {
		t.Skip("consumer baseline comparison is skipped under -short")
	}
	path := filepath.Join(spinPlayerexpDir, spinPlayerexpFile)
	switch mode {
	case "u0":
		first, second := spinPlayerexpOutput(t), spinPlayerexpOutput(t)
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
		manifest := fmt.Sprintf("%x  playerexp.json\n", sum)
		if err := os.WriteFile(filepath.Join(spinPlayerexpDir, spinPlayerexpManifest), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded U0 playerexp: SHA-256=%x", sum)
	case "":
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(want))
		manifest, err := os.ReadFile(filepath.Join(spinPlayerexpDir, spinPlayerexpManifest))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(manifest), sum+" ") {
			t.Fatalf("%s does not match %s; the baseline was modified", path, spinPlayerexpManifest)
		}
		if sum != spinU0PlayerexpSHA256 {
			t.Fatalf("%s SHA-256=%s does not match the pinned constant %q", path, sum, spinU0PlayerexpSHA256)
		}
		if got := spinPlayerexpOutput(t); !bytes.Equal(got, want) {
			t.Fatalf("playerexp output differs from U0\n got=%s", got)
		}
	default:
		t.Fatalf("unknown PROBLAB_RECORD_SPIN_BASELINE=%q", mode)
	}
}
