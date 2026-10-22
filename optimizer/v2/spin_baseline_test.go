// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package v2

// U0 optimizer v2 cases, recorded before SpinInternal is replaced by
// SpinUnsafe. They cover every optimizer v2 call site: fresh collection,
// Collection Bank replay, runtime-replay verification and RGS replay. This is
// a temporary acceptance tool and is removed after the migration is accepted.
//
// Record: PROBLAB_RECORD_SPIN_BASELINE=u0 go test -run TestSpinBaselineOptimizer .

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/spec"
)

// spinU0OptimizerSHA256 pins testdata/spin_baseline/u0/cases.json.
const spinU0OptimizerSHA256 = "65fc470421d1ec1ddb1e97c5c972a9be30d9adfee3e56e4b814a5db7a89864fb"

const (
	spinOptDir    = "testdata/spin_baseline"
	spinOptSeed   = int64(4127483647)
	spinOptFirsts = 3
)

type spinOptRun struct {
	Count  int      `json:"count"`
	Digest string   `json:"digest"`
	First  []string `json:"first"`
}

type spinOptAcc struct {
	sum []byte
	run spinOptRun
}

func (a *spinOptAcc) add(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	next := sha256.Sum256(append(append([]byte(nil), a.sum...), line...))
	a.sum = next[:]
	a.run.Count++
	if len(a.run.First) < spinOptFirsts {
		a.run.First = append(a.run.First, line)
	}
}

func (a *spinOptAcc) finish() *spinOptRun {
	a.run.Digest = hex.EncodeToString(a.sum)
	return &a.run
}

func spinOptSamples(acc *spinOptAcc, collected CollectedProblem) {
	acc.add("spins=%d next=%d snaplen=%d", collected.Spins, collected.NextStreamOrdinal, collected.SnapshotLength)
	ev := collected.Evidence
	acc.add("evidence fresh_spins=%d fresh_accepted=%d replay_records=%d replay_accepted=%d replay_duplicates=%d",
		ev.FreshSpins, ev.FreshAccepted, ev.ReplayRecords, ev.ReplayAccepted, ev.ReplayDuplicates)
	for ci, class := range collected.Classes {
		for _, s := range class.Samples {
			acc.add("c%d|%s|%d|%x|%d|%s", ci, s.ClassID, s.TotalWin, s.Win, s.Sequence, hex.EncodeToString(s.Snapshot))
		}
	}
}

func spinOptLab(t testing.TB) *problab.Problab {
	t.Helper()
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatalf("construct demo Problab: %v", err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

func spinOptCollect(t testing.TB, lab *problab.Problab) CollectedProblem {
	t.Helper()
	collected, diagnostics, err := NewCollector(lab).Collect(context.Background(), collectionFixturePlan(spinOptSeed, 2, 64, 64, 16), 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("fixture collection diagnostics=%+v err=%v", diagnostics, err)
	}
	return collected
}

func spinOptBetUnit(t testing.TB, lab *problab.Problab) int {
	t.Helper()
	m, err := lab.NewMachine(spec.GID(1), problab.MachineConfig{Seed: problab.SeedInt64(spinOptSeed), Unoptimized: true})
	if err != nil {
		t.Fatal(err)
	}
	return m.BetUnits[0]
}

type spinOptCase struct {
	key string
	run func(t testing.TB) *spinOptRun
}

func spinOptCases() []spinOptCase {
	return []spinOptCase{
		// collect: two workers, every spin accepted, so samples follow both worker streams.
		{"collect", func(t testing.TB) *spinOptRun {
			acc := &spinOptAcc{}
			spinOptSamples(acc, spinOptCollect(t, spinOptLab(t)))
			return acc.finish()
		}},
		// collect-workers: runs each fresh-collection worker exactly as Collect wires it,
		// on a machine owned by the test, and records the worker machine's final RNG so
		// extra consumption after the last accepted sample is visible.
		{"collect-workers", func(t testing.TB) *spinOptRun {
			lab := spinOptLab(t)
			plan := collectionFixturePlan(spinOptSeed, 2, 64, 64, 16)
			registry, err := newCollectionTagRegistry(nil)
			if err != nil {
				t.Fatal(err)
			}
			tagger, predicates, err := compileTagPredicates(plan.Intent.Classes, registry)
			if err != nil {
				t.Fatal(err)
			}
			workers := plan.Plan.Collection.Workers
			quotas := partitionQuotas([]uint64{plan.Intent.Classes[0].Collect.Samples}, workers)
			acc := &spinOptAcc{}
			for worker := range workers {
				seed, err := optimizerWorkerSeed(lab, plan.Plan.Seed.Bytes(), uint64(worker))
				if err != nil {
					t.Fatal(err)
				}
				machine, err := newOptimizerMachine(lab, plan.Plan.Target.Game, seed)
				if err != nil {
					t.Fatal(err)
				}
				progresses := make(chan collectionWorkerProgress, 1024)
				result := collectWorker(context.Background(), worker, machine, 0, plan.Intent.Classes, predicates, tagger,
					quotas[worker], partitionShare(plan.Plan.Collection.MaxSpins, workers, worker), plan.Plan.Collection.BatchSize, progresses)
				if result.err != nil {
					t.Fatal(result.err)
				}
				core, err := machine.SnapshotCore()
				if err != nil {
					t.Fatal(err)
				}
				acc.add("w%d|spins=%d|samples=%d|progress=%d|core=%s", worker, result.spins, len(result.samples), len(progresses), hex.EncodeToString(core))
				for _, s := range result.samples {
					acc.add("w%d|%d|%d|%x|%s", worker, s.classIndex, s.sample.TotalWin, s.sample.Win, hex.EncodeToString(s.sample.Snapshot))
				}
			}
			return acc.finish()
		}},
		// bank-replay: replay a written Collection Bank, then fill the remaining deficit freshly.
		{"bank-replay", func(t testing.TB) *spinOptRun {
			lab := spinOptLab(t)
			collected := spinOptCollect(t, lab)
			dir := t.TempDir()
			bankPath := filepath.Join(dir, "u0.bin")
			if _, err := (collectionBankWriter{}).Write(context.Background(), bankPath, collected, false, false); err != nil {
				t.Fatal(err)
			}
			plan := collectionFixturePlan(spinOptSeed, 2, 96, 32, 16)
			plan.Plan.Collection.CollectedSeed = []string{bankPath}
			collector := NewCollector(lab)
			collector.WorkingDirectory = dir
			replayed, diagnostics, err := collector.Collect(context.Background(), plan, 0)
			if err != nil || diagnostics.StopsRun() {
				t.Fatalf("replay collection diagnostics=%+v err=%v", diagnostics, err)
			}
			acc := &spinOptAcc{}
			spinOptSamples(acc, replayed)
			return acc.finish()
		}},
		// verify: runtime replay of materialized snapshots through a raw machine.
		{"verify", func(t testing.TB) *spinOptRun {
			lab := spinOptLab(t)
			collected := spinOptCollect(t, lab)
			samples := make([]MaterializedSample, 0, 8)
			for _, s := range collected.Classes[0].Samples[:8] {
				samples = append(samples, MaterializedSample{ClassID: s.ClassID, Win: s.Win, Snapshot: s.Snapshot, Probability: 0.125})
			}
			bet := spinOptBetUnit(t, lab)
			mode, err := MaterializeMode(0, bet, samples)
			if err != nil {
				t.Fatal(err)
			}
			replayed, check, err := replayMaterializedSnapshots(context.Background(), lab, runtimeReplayCompiledFixture(spinOptSeed, bet), mode)
			if err != nil {
				t.Fatal(err)
			}
			acc := &spinOptAcc{}
			acc.add("check|%t|%s", check.Pass, check.Actual)
			for _, s := range replayed.Samples {
				acc.add("sample|%s|%x|%s", s.ClassID, s.Win, hex.EncodeToString(s.Snapshot))
			}
			return acc.finish()
		}},
		// rgs: RGS replay of collected snapshots; records each full result and the machine RNG after it.
		{"rgs", func(t testing.TB) *spinOptRun {
			lab := spinOptLab(t)
			collected := spinOptCollect(t, lab)
			replay, err := newRGSReplay(lab, collectionFixturePlan(spinOptSeed, 2, 64, 64, 16), spinOptBetUnit(t, lab))
			if err != nil {
				t.Fatal(err)
			}
			acc := &spinOptAcc{}
			for _, s := range collected.Classes[0].Samples[:16] {
				result, err := replay.spin(context.Background(), s.Snapshot, s.Win)
				if err != nil {
					t.Fatal(err)
				}
				res, err := dto.NewSpinResultDTO(result)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(res)
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(raw)
				core, err := replay.machine.SnapshotCore()
				if err != nil {
					t.Fatal(err)
				}
				acc.add("%x|%s", sum, hex.EncodeToString(core))
			}
			return acc.finish()
		}},
	}
}

func TestSpinBaselineOptimizer(t *testing.T) {
	mode := os.Getenv("PROBLAB_RECORD_SPIN_BASELINE")
	if mode == "" && testing.Short() {
		t.Skip("consumer baseline comparison is skipped under -short")
	}
	cases := spinOptCases()
	recordAll := func() []byte {
		runs := map[string]*spinOptRun{}
		for _, c := range cases {
			runs[c.key] = c.run(t)
		}
		raw, err := json.MarshalIndent(map[string]any{"schema": "problab-spin-baseline/v1", "cases": runs}, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		return append(raw, '\n')
	}
	path := filepath.Join(spinOptDir, "u0", "cases.json")
	manifestPath := filepath.Join(spinOptDir, "u0.sha256")
	switch mode {
	case "u0":
		first, second := recordAll(), recordAll()
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
		if err := os.WriteFile(manifestPath, fmt.Appendf(nil, "%x  cases.json\n", sum), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded U0 optimizer: %d cases, SHA-256=%x", len(cases), sum)
	case "":
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read U0: %v", err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(raw))
		manifest, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(manifest), sum+" ") {
			t.Fatal("cases.json does not match u0.sha256; the baseline was modified")
		}
		if sum != spinU0OptimizerSHA256 {
			t.Fatalf("cases.json SHA-256=%s does not match the pinned constant %q", sum, spinU0OptimizerSHA256)
		}
		var file struct {
			Cases map[string]*spinOptRun `json:"cases"`
		}
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, c := range cases {
			keys = append(keys, c.key)
		}
		var want []string
		for k := range file.Cases {
			want = append(want, k)
		}
		sort.Strings(keys)
		sort.Strings(want)
		if !reflect.DeepEqual(keys, want) {
			t.Fatalf("case keys differ from U0: got=%v want=%v", keys, want)
		}
		for _, c := range cases {
			t.Run(c.key, func(t *testing.T) {
				if got := c.run(t); !reflect.DeepEqual(got, file.Cases[c.key]) {
					t.Fatalf("differs from U0\n got=%+v\nwant=%+v", got, file.Cases[c.key])
				}
			})
		}
	default:
		t.Fatalf("unknown PROBLAB_RECORD_SPIN_BASELINE=%q", mode)
	}
}
