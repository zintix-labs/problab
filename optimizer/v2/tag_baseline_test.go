// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

// T0 optimizer tag baseline, recorded before tag resolution moves to
// GameTagCatalog without changing production code. It covers tag-filtered
// collection (one and two workers), Collection Bank replay, runtime-replay
// verification, RGS validation and the tag error classification. This is a
// temporary acceptance tool and is removed after the migration is accepted.
//
// Record: PROBLAB_RECORD_TAG_BASELINE=t0 go test -run TestTagBaselineOptimizer .

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
	"sync"
	"testing"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/tag"
	"github.com/zintix-labs/problab/spec"
)

// tagT0OptimizerSHA256 pins testdata/tag_baseline/t0/cases.json.
const tagT0OptimizerSHA256 = "f319ce2f20b0a94a8907801862c8c96e43916a665b0cf5a4038eb89c1e54e691"

const (
	tagT0Dir  = "testdata/tag_baseline"
	tagT0Seed = int64(4127483647)
)

// tagCallLog records every predicate call in order. Only single-flow cases
// compare it; multi-worker cases compare results only.
type tagCallLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *tagCallLog) wrap(name string, fn func(*buf.SpinResult) bool) tag.IsTag {
	return func(sr *buf.SpinResult) bool {
		v := fn(sr)
		l.mu.Lock()
		l.calls = append(l.calls, fmt.Sprintf("%s=%t", name, v))
		l.mu.Unlock()
		return v
	}
}

func (l *tagCallLog) digest() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	sum := sha256.Sum256([]byte(strings.Join(l.calls, ",")))
	return fmt.Sprintf("%d:%x", len(l.calls), sum)
}

// tagT0Catalog defines game 1 tags. "unused" is never referenced so a change
// that evaluates every definition would show up in the call log.
func tagT0Catalog(log *tagCallLog) map[spec.GID]map[string]tag.IsTag {
	return map[spec.GID]map[string]tag.IsTag{1: {
		"fg":     log.wrap("fg", func(sr *buf.SpinResult) bool { return sr.GameModeCount > 1 }),
		"win":    log.wrap("win", func(sr *buf.SpinResult) bool { return sr.TotalWin > 0 }),
		"big":    log.wrap("big", func(sr *buf.SpinResult) bool { return sr.TotalWin >= 3*sr.Bet }),
		"bg":     log.wrap("bg", func(sr *buf.SpinResult) bool { return sr.GameModeCount == 1 }),
		"unused": log.wrap("unused", func(*buf.SpinResult) bool { return true }),
	}}
}

// tagT0Plan uses four classes in a non-alphabetical tag order: match only,
// match plus mismatch, mismatch only, and an unfiltered catch-all, so
// first-match ownership is exercised.
func tagT0Plan(workers int, samples, maxSpins uint64) ResolvedPlan {
	all := ClosedInterval{0, 1e9}
	classes := []ClassIntent{
		{Name: "big_fg", Weight: 1, Collect: CollectIntent{Samples: samples, WinRange: all, Tags: TagFilters{Matches: []string{"big", "fg"}}}},
		{Name: "win_not_fg", Weight: 1, Collect: CollectIntent{Samples: samples, WinRange: all, Tags: TagFilters{Matches: []string{"win"}, Mismatches: []string{"fg"}}}},
		{Name: "no_win", Weight: 1, Collect: CollectIntent{Samples: samples, WinRange: all, Tags: TagFilters{Mismatches: []string{"win"}}}},
		{Name: "rest", Weight: 1, Collect: CollectIntent{Samples: samples, WinRange: all}},
	}
	return ResolvedPlan{
		Plan: RunPlan{
			Target: Target{Game: spec.GID(1), BetModes: []int{0}},
			Seed:   Int64Seed(tagT0Seed),
			Collection: CollectionOptions{
				Workers: workers, BatchSize: 32, MaxSpins: maxSpins,
			},
			Intent: "tag-baseline",
		},
		Intent: MathIntent{Classes: classes},
	}
}

type tagT0Run struct {
	Count  int      `json:"count"`
	Digest string   `json:"digest"`
	First  []string `json:"first"`
}

type tagT0Acc struct {
	sum []byte
	run tagT0Run
}

func (a *tagT0Acc) add(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	next := sha256.Sum256(append(append([]byte(nil), a.sum...), line...))
	a.sum = next[:]
	a.run.Count++
	if len(a.run.First) < 3 {
		a.run.First = append(a.run.First, line)
	}
}

func (a *tagT0Acc) finish() *tagT0Run {
	a.run.Digest = hex.EncodeToString(a.sum)
	return &a.run
}

func tagT0Lab(t testing.TB) *problab.Problab {
	t.Helper()
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

func tagT0Collect(t testing.TB, lab *problab.Problab, log *tagCallLog, plan ResolvedPlan, dir string) (CollectedProblem, Diagnostics) {
	t.Helper()
	c := NewCollector(lab)
	c.GameTags = tagT0Catalog(log)
	c.WorkingDirectory = dir
	collected, diagnostics, err := c.Collect(context.Background(), plan, 0)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return collected, diagnostics
}

func tagT0Samples(acc *tagT0Acc, collected CollectedProblem, diagnostics Diagnostics) {
	acc.add("spins=%d fresh=%d replay_accepted=%d", collected.Spins, collected.Evidence.FreshSpins, collected.Evidence.ReplayAccepted)
	for _, d := range diagnostics {
		acc.add("diag|%s|%s", d.Code, d.Message)
	}
	for ci, class := range collected.Classes {
		for _, s := range class.Samples {
			acc.add("c%d|%s|%d|%x|%d|%s", ci, s.ClassID, s.TotalWin, s.Win, s.Sequence, hex.EncodeToString(s.Snapshot))
		}
	}
}

// tagT0Compiled builds the replay model from a tagged collection without LP
// rows, so verification and RGS validation use the collection's tag predicates.
func tagT0Compiled(t testing.TB, lab *problab.Problab, plan ResolvedPlan, collected CollectedProblem) CompiledModel {
	t.Helper()
	m, err := lab.NewMachine(spec.GID(1), problab.MachineConfig{Seed: problab.SeedInt64(tagT0Seed), Unoptimized: true})
	if err != nil {
		t.Fatal(err)
	}
	classes := make([]PreparedClass, len(plan.Intent.Classes))
	for i, c := range plan.Intent.Classes {
		classes[i] = PreparedClass{ID: c.Name, Index: i, Probability: 0.25, Buckets: []PreparedBucket{{Index: 0}}}
	}
	return CompiledModel{Prepared: PreparedProblem{
		Plan: plan, Game: spec.GID(1), BetMode: 0, BetUnit: m.BetUnits[0],
		Classes: classes, tags: collected.tags,
	}}
}

func tagT0Materialized(collected CollectedProblem) []MaterializedSample {
	var samples []MaterializedSample
	for _, class := range collected.Classes {
		for _, s := range class.Samples[:min(4, len(class.Samples))] {
			samples = append(samples, MaterializedSample{ClassID: s.ClassID, Win: s.Win, Snapshot: s.Snapshot})
		}
	}
	for i := range samples {
		samples[i].Probability = 1 / float64(len(samples))
	}
	return samples
}

type tagT0Case struct {
	key string
	run func(t testing.TB) *tagT0Run
}

func tagT0Cases() []tagT0Case {
	return []tagT0Case{
		{"collect-1w", func(t testing.TB) *tagT0Run {
			log := &tagCallLog{}
			collected, diagnostics := tagT0Collect(t, tagT0Lab(t), log, tagT0Plan(1, 6, 600), t.TempDir())
			acc := &tagT0Acc{}
			tagT0Samples(acc, collected, diagnostics)
			acc.add("calls|%s", log.digest())
			return acc.finish()
		}},
		{"collect-2w", func(t testing.TB) *tagT0Run {
			log := &tagCallLog{}
			collected, diagnostics := tagT0Collect(t, tagT0Lab(t), log, tagT0Plan(2, 6, 600), t.TempDir())
			acc := &tagT0Acc{}
			tagT0Samples(acc, collected, diagnostics)
			return acc.finish()
		}},
		{"bank-replay", func(t testing.TB) *tagT0Run {
			lab := tagT0Lab(t)
			dir := t.TempDir()
			collected, _ := tagT0Collect(t, lab, &tagCallLog{}, tagT0Plan(1, 6, 600), dir)
			bankPath := filepath.Join(dir, "t0.bin")
			if _, err := (collectionBankWriter{}).Write(context.Background(), bankPath, collected, true, false); err != nil {
				t.Fatal(err)
			}
			plan := tagT0Plan(1, 8, 200)
			plan.Plan.Collection.CollectedSeed = []string{bankPath}
			log := &tagCallLog{}
			replayed, diagnostics := tagT0Collect(t, lab, log, plan, dir)
			acc := &tagT0Acc{}
			tagT0Samples(acc, replayed, diagnostics)
			acc.add("calls|%s", log.digest())
			return acc.finish()
		}},
		{"verify", func(t testing.TB) *tagT0Run {
			lab := tagT0Lab(t)
			log := &tagCallLog{}
			plan := tagT0Plan(1, 6, 600)
			collected, _ := tagT0Collect(t, lab, log, plan, t.TempDir())
			compiled := tagT0Compiled(t, lab, plan, collected)
			mode, err := MaterializeMode(0, compiled.Prepared.BetUnit, tagT0Materialized(collected))
			if err != nil {
				t.Fatal(err)
			}
			before := log.digest()
			log.mu.Lock()
			log.calls = nil
			log.mu.Unlock()
			replayed, check, err := replayMaterializedSnapshots(context.Background(), lab, compiled, mode)
			if err != nil {
				t.Fatal(err)
			}
			acc := &tagT0Acc{}
			acc.add("collect-calls|%s", before)
			acc.add("check|%t|%s", check.Pass, check.Actual)
			for _, s := range replayed.Samples {
				acc.add("sample|%s|%x|%s", s.ClassID, s.Win, hex.EncodeToString(s.Snapshot))
			}
			acc.add("calls|%s", log.digest())
			return acc.finish()
		}},
		{"rgs", func(t testing.TB) *tagT0Run {
			lab := tagT0Lab(t)
			log := &tagCallLog{}
			plan := tagT0Plan(1, 6, 600)
			collected, _ := tagT0Collect(t, lab, log, plan, t.TempDir())
			compiled := tagT0Compiled(t, lab, plan, collected)
			samples := tagT0Materialized(collected)
			p := make([]float64, len(samples))
			for i := range p {
				p[i] = samples[i].Probability
			}
			rows, err := optimizedRGSRows(compiled, samples, p)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := newRGSReplay(lab, plan, compiled.Prepared.BetUnit)
			if err != nil {
				t.Fatal(err)
			}
			log.mu.Lock()
			log.calls = nil
			log.mu.Unlock()
			acc := &tagT0Acc{}
			if err := rows(func(row rgsSourceRow) error {
				result, err := replay.spin(context.Background(), row.snapshot, row.row.WinMultiplier)
				if err != nil {
					return err
				}
				res, err := dto.NewSpinResultDTO(result)
				if err != nil {
					return err
				}
				raw, err := json.Marshal(res)
				if err != nil {
					return err
				}
				acc.add("row|%d|%s|%x|validate=%v", row.row.RecordIndex, row.row.ClassName, sha256.Sum256(raw), row.validate(result))
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			acc.add("calls|%s", log.digest())
			return acc.finish()
		}},
		{"errors", func(t testing.TB) *tagT0Run {
			lab := tagT0Lab(t)
			acc := &tagT0Acc{}
			record := func(name string, tags map[spec.GID]map[string]tag.IsTag, plan ResolvedPlan) {
				c := NewCollector(lab)
				c.GameTags = tags
				c.WorkingDirectory = t.TempDir()
				_, diagnostics, err := c.Collect(context.Background(), plan, 0)
				msgs := []string{}
				for _, d := range diagnostics {
					msgs = append(msgs, string(d.Code)+":"+d.Message)
				}
				acc.add("%s|err=%v|diag=%s", name, err, strings.Join(msgs, ";"))
			}
			ok := func(*buf.SpinResult) bool { return true }
			noTags := tagT0Plan(1, 1, 1)
			for i := range noTags.Intent.Classes {
				noTags.Intent.Classes[i].Collect.Tags = TagFilters{}
			}
			record("invalid-def-unreferenced", map[spec.GID]map[string]tag.IsTag{1: {"fg": ok, "broken": nil}}, noTags)
			record("empty-name-unreferenced", map[spec.GID]map[string]tag.IsTag{1: {"": ok}}, noTags)
			record("no-game-unreferenced", nil, noTags)
			record("no-game-referenced", map[spec.GID]map[string]tag.IsTag{0: {"fg": ok}}, tagT0Plan(1, 1, 1))
			missing := tagT0Plan(1, 1, 1)
			missing.Intent.Classes[1].Collect.Tags.Matches = []string{"nope"}
			record("name-missing", tagT0Catalog(&tagCallLog{}), missing)
			// Tuner validates every injected game's definitions.
			for _, tc := range []struct {
				name string
				tags map[spec.GID]map[string]tag.IsTag
			}{
				{"tuner-nil-predicate", map[spec.GID]map[string]tag.IsTag{1: {"broken": nil}}},
				{"tuner-empty-name", map[spec.GID]map[string]tag.IsTag{1: {"": ok}}},
				{"tuner-valid", map[spec.GID]map[string]tag.IsTag{1: {"fg": ok}}},
			} {
				tuner := &Tuner{collector: NewCollector(lab)}
				err := WithCollectionTags(tc.tags)(tuner)
				acc.add("%s|err=%v", tc.name, err)
			}
			return acc.finish()
		}},
	}
}

func TestTagBaselineOptimizer(t *testing.T) {
	mode := os.Getenv("PROBLAB_RECORD_TAG_BASELINE")
	if mode == "" && testing.Short() {
		t.Skip("tag baseline comparison is skipped under -short")
	}
	cases := tagT0Cases()
	recordAll := func() []byte {
		runs := map[string]*tagT0Run{}
		for _, c := range cases {
			runs[c.key] = c.run(t)
		}
		raw, err := json.MarshalIndent(map[string]any{"schema": "problab-tag-baseline/v1", "cases": runs}, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		return append(raw, '\n')
	}
	path := filepath.Join(tagT0Dir, "t0", "cases.json")
	manifestPath := filepath.Join(tagT0Dir, "t0.sha256")
	switch mode {
	case "t0":
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
		t.Logf("recorded T0 optimizer: %d cases, SHA-256=%x", len(cases), sum)
	case "":
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read T0: %v", err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(raw))
		manifest, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(manifest), sum+" ") {
			t.Fatal("cases.json does not match t0.sha256; the baseline was modified")
		}
		if sum != tagT0OptimizerSHA256 {
			t.Fatalf("cases.json SHA-256=%s does not match the pinned constant %q", sum, tagT0OptimizerSHA256)
		}
		var file struct {
			Cases map[string]*tagT0Run `json:"cases"`
		}
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatal(err)
		}
		var keys, want []string
		for _, c := range cases {
			keys = append(keys, c.key)
		}
		for k := range file.Cases {
			want = append(want, k)
		}
		sort.Strings(keys)
		sort.Strings(want)
		if !reflect.DeepEqual(keys, want) {
			t.Fatalf("case keys differ from T0: got=%v want=%v", keys, want)
		}
		for _, c := range cases {
			t.Run(c.key, func(t *testing.T) {
				if got := c.run(t); !reflect.DeepEqual(got, file.Cases[c.key]) {
					t.Fatalf("differs from T0\n got=%+v\nwant=%+v", got, file.Cases[c.key])
				}
			})
		}
	default:
		t.Fatalf("unknown PROBLAB_RECORD_TAG_BASELINE=%q", mode)
	}
}
