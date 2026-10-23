// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package playerexp_test

// B0 playerexp case: the consumer baseline recorded before results are
// assembled into whole games. It is a temporary acceptance tool and is removed
// after the change is accepted. Recording uses the same switch as the root
// package: PROBLAB_B0=record.

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
	"github.com/zintix-labs/problab/sdk/tag"
)

// b0PlayerexpSHA256 pins b0/playerexp.json.
const b0PlayerexpSHA256 = "9c542919f2de24dc91381a934d7d56ce10cf76b4512d41ae91eb258312b35274"

const (
	b0PlayerexpDir      = "testdata/game_baseline"
	b0PlayerexpFile     = "b0/playerexp.json"
	b0PlayerexpManifest = "b0-playerexp.sha256"
)

// b0CallLog records every strategy and reporter callback in call order.
// playerexp runs players sequentially on the Run goroutine, so no lock is needed.
type b0CallLog struct{ lines []string }

func (l *b0CallLog) add(format string, a ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, a...))
}

func (l *b0CallLog) Report(e playerexp.ProgressEvent) {
	l.add("report|%s|%d|%s|%d|%d", e.Kind, e.GroupIndex, e.GroupName, e.Completed, e.Total)
}

type b0RecordingFactory struct {
	name  string
	inner playerexp.StrategyFactory
	log   *b0CallLog
}

func (f b0RecordingFactory) NewStrategy(initial int) playerexp.Strategy {
	f.log.add("new|%s|%d", f.name, initial)
	return b0RecordingStrategy{name: f.name, inner: f.inner.NewStrategy(initial), log: f.log}
}

type b0RecordingStrategy struct {
	name  string
	inner playerexp.Strategy
	log   *b0CallLog
}

func (s b0RecordingStrategy) FirstSpin(st playerexp.PlayerState) playerexp.Decision {
	d := s.inner.FirstSpin(st)
	s.log.add("first|%s|%+v|%+v", s.name, st, d)
	return d
}

// Next records the complete borrowed result the strategy receives: the DTO
// (game modes, acts, details, screens and Extend) plus the raw start and after
// RNG snapshots, before the next spin overwrites it.
func (s b0RecordingStrategy) Next(st playerexp.PlayerState, r *buf.SpinResult) playerexp.Decision {
	res, err := dto.NewSpinResultDTO(r)
	if err != nil {
		panic(err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(raw)
	snaps := "nil-state"
	if r.State != nil {
		snaps = hex.EncodeToString(r.State.StartCoreSnap) + "/" + hex.EncodeToString(r.State.AfterCoreSnap)
	}
	d := s.inner.Next(st, r)
	s.log.add("next|%s|%+v|%s|%s|%+v", s.name, st, hex.EncodeToString(sum[:]), snaps, d)
	return d
}

type b0PlayerexpRecord struct {
	Report     json.RawMessage `json:"report"`
	CallCount  int             `json:"call_count"`
	CallDigest string          `json:"call_digest"`
	FirstCalls []string        `json:"first_calls"`
}

func b0PlayerexpOutput(t *testing.T) []byte {
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
    events:
      - {event: fg_big, win_range: [2, null], tags: [fg, big]}
      - {event: fg, win_range: [0, null], tags: [fg]}
      - {event: win, win_range: [0, null], tags: [win]}
      - {event: rest, win_range: [0, null]}
  - name: double
    player: {players: 8, init_bet: 300, strategy: double}
    win_buckets: [0, 1, 2, 5]
    events:
      - {event: big, win_range: [1, null], tags: [big]}
      - {event: rest, win_range: [0, null]}
`))
	if err != nil {
		t.Fatal(err)
	}
	log := &b0CallLog{}
	rg := playerexp.NewStrategyRegistry()
	for name, f := range map[string]playerexp.StrategyFactory{
		"flat":   strategies.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 200, SatisfiedRatio: 3},
		"double": strategies.Monotone{BetMode: 0, BetMult: 2, MaxSpins: 200, SatisfiedRatio: 3},
	} {
		if err := rg.Register(0, name, b0RecordingFactory{name: name, inner: f, log: log}); err != nil {
			t.Fatal(err)
		}
	}
	tags := tag.GameTagCatalog{0: {
		"fg":  func(sr *buf.SpinResult) bool { return sr.GameModeCount > 1 },
		"win": func(sr *buf.SpinResult) bool { return sr.TotalWin > 0 },
		"big": func(sr *buf.SpinResult) bool { return sr.TotalWin >= 3*sr.Bet },
	}}
	r, err := playerexp.Run(context.Background(), c, playerexp.Options{Lab: lab, Strategies: rg, Tags: tags, Reporter: log})
	if err != nil || r.Status != playerexp.Completed {
		t.Fatalf("playerexp run: status=%v err=%v", r.Status, err)
	}
	// The complete report, floats included: the statistics are not changed.
	report, err := json.Marshal(r)
	if err != nil {
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
	out, err := json.MarshalIndent(b0PlayerexpRecord{
		Report: report, CallCount: len(log.lines),
		CallDigest: hex.EncodeToString(digest), FirstCalls: first,
	}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func TestGameBaselinePlayerexp(t *testing.T) {
	mode := os.Getenv("PROBLAB_B0")
	if mode == "" && testing.Short() {
		t.Skip("consumer baseline comparison is skipped under -short")
	}
	path := filepath.Join(b0PlayerexpDir, b0PlayerexpFile)
	switch mode {
	case "record":
		first, second := b0PlayerexpOutput(t), b0PlayerexpOutput(t)
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
		if err := os.WriteFile(filepath.Join(b0PlayerexpDir, b0PlayerexpManifest), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded B0 playerexp: SHA-256=%x", sum)
	case "":
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(want))
		manifest, err := os.ReadFile(filepath.Join(b0PlayerexpDir, b0PlayerexpManifest))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(manifest), sum+" ") {
			t.Fatalf("%s does not match %s; the baseline was modified", path, b0PlayerexpManifest)
		}
		if sum != b0PlayerexpSHA256 {
			t.Fatalf("%s SHA-256=%s does not match the pinned constant %q", path, sum, b0PlayerexpSHA256)
		}
		if got := b0PlayerexpOutput(t); !bytes.Equal(got, want) {
			t.Fatalf("playerexp output differs from B0\n got=%s", got)
		}
	default:
		t.Fatalf("unknown PROBLAB_B0=%q", mode)
	}
}
