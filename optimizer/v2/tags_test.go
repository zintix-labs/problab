// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/tag"
	"github.com/zintix-labs/problab/spec"
)

func TestIndependentTunersFreezeTagsAcrossFreshReplayAndBothVerifiers(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "tag-isolation")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	var tuners [2]*Tuner
	for i := range tuners {
		value := i == 0
		defs := map[spec.GID]map[string]tag.IsTag{1: {"shared": func(*buf.SpinResult) bool { return value }}}
		tuners[i] = rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected, OutputRGSOptimized, OutputOptimalArtifactV1},
			WithCollectionTags(defs), WithReporter(auditReporterFunc(func(e StageEvent) {
				if e.State == "started" && strings.HasPrefix(e.Stage, "prepare[") {
					arrived <- struct{}{}
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
			})))
		// Mutate both levels of the caller's maps after construction.
		defs[1]["shared"] = func(*buf.SpinResult) bool { return !value }
		delete(defs, 1)
		plan := &tuners[i].config.Plans[0]
		intent := tuners[i].config.Intents[plan.Intent]
		if value {
			intent.Classes[0].Collect.Tags.Matches = []string{"shared"}
		} else {
			intent.Classes[0].Collect.Tags.Mismatches = []string{"shared"}
			plan.Collection.CollectedSeed = []string{base.Report.Collection.Bank.Path}
		}
		tuners[i].config.Intents[plan.Intent] = intent
	}
	type outcome struct {
		index  int
		result RunResult
		err    error
	}
	out := make(chan outcome, 2)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	for i, tuner := range tuners {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r, e := tuner.Run(ctx, RunRequest{PlanID: base.Report.Plan.Plan.ID})
			out <- outcome{i, r, e}
		}()
	}
	for range 2 {
		select {
		case <-arrived:
		case early := <-out:
			t.Fatalf("run ended before barrier: %+v", early)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(release)
	for range 2 {
		select {
		case got := <-out:
			if got.err != nil || got.result.Status != StatusOptimal || !got.result.Report.Verification.Pass {
				t.Fatalf("run %d: err=%v status=%s diagnostics=%v", got.index, got.err, got.result.Status, got.result.Diagnostics)
			}
			for _, r := range got.result.Report.RGSExports {
				if r.State != "COMPLETED" {
					t.Fatalf("RGS: %+v", r)
				}
			}
			if got.index == 1 && got.result.Report.Collection.Evidence.ReplayAccepted != 256 {
				t.Fatal("bank replay path not exercised")
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestCollectionRegistryHasNoReservedNames(t *testing.T) {
	empty := tag.GameTagCatalog{1: {}}
	for _, name := range []string{"bg", "fg"} {
		if _, err := empty.CompileBy(1, name); err == nil {
			t.Fatalf("implicit tag %s", name)
		}
	}
	catalog := tag.GameTagCatalog{1: {
		"bg": func(*buf.SpinResult) bool { return false },
		"fg": func(*buf.SpinResult) bool { return true },
	}}
	tagger, err := catalog.CompileBy(1, "bg", "fg")
	if err != nil {
		t.Fatal(err)
	}
	fg, err := tagger.Want("fg")
	if err != nil {
		t.Fatal(err)
	}
	bm := tagger.NewMasks()
	tagger.Tag(bm, nil)
	if !tagger.Equal(bm, fg) {
		t.Fatal("game predicates were overridden")
	}
}

func TestCollectionTagValidationAndGameIsolation(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "tag-config")
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected})
	for _, bad := range []map[string]tag.IsTag{{"": func(*buf.SpinResult) bool { return true }}, {"nil": nil}} {
		if err := WithCollectionTags(map[spec.GID]map[string]tag.IsTag{1: bad})(tuner); err == nil {
			t.Fatal("accepted invalid definitions")
		}
	}
	defs := map[spec.GID]map[string]tag.IsTag{2: {"game2": func(*buf.SpinResult) bool { return true }}}
	if err := WithCollectionTags(defs)(tuner); err != nil {
		t.Fatal(err)
	}
	plan := base.Report.Plan
	plan.Intent.Classes[0].Collect.Tags.Matches = []string{"game2"}
	_, diags, err := tuner.collector.Collect(context.Background(), plan, 0)
	if err != nil || !diags.StopsRun() || !strings.Contains(diags[0].Message, "game2") {
		t.Fatalf("cross-game tag resolved: %v %v", diags, err)
	}
	if err := WithCollectionTags(nil)(tuner); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedTagsSurviveSubsequentCollectorConfiguration(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "frozen-tag")
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected})
	plan := base.Report.Plan
	plan.Intent.Classes[0].Collect.Tags.Matches = []string{"custom"}
	tuner.collector.GameTags = map[spec.GID]map[string]tag.IsTag{1: {"custom": func(*buf.SpinResult) bool { return true }}}
	collected, diags, err := tuner.collector.Collect(context.Background(), plan, 0)
	if err != nil || diags.StopsRun() {
		t.Fatalf("collect: %v %v", diags, err)
	}
	tuner.collector.GameTags[1]["custom"] = func(*buf.SpinResult) bool { return false }
	prepared, diags, err := PrepareProblem(plan, collected)
	if err != nil || diags.StopsRun() {
		t.Fatalf("prepare: %v %v", diags, err)
	}
	tagger, predicates, err := prepared.replayTags()
	if err != nil || tagger != collected.tags.tagger {
		t.Fatal("run-local predicates changed")
	}
	bm := tagger.NewMasks()
	tagger.Tag(bm, nil)
	if !tagger.Equal(bm, predicates[0].match) {
		t.Fatal("run-local predicates changed")
	}
	prepared.tags = nil
	if _, _, err := prepared.replayTags(); err == nil {
		t.Fatal("custom tags without runtime context must fail closed")
	}
}

// TestCollectWithMoreThan64TagsMatchesOracle collects with 70 referenced tags
// (two mask words) and checks every sample's Class against first-match
// classification computed by calling each predicate directly on a replay.
func TestCollectWithMoreThan64TagsMatchesOracle(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	const n = 70
	defs := map[string]tag.IsTag{}
	names := make([]string, n)
	// Neighbouring tags share a value so that a two-tag Match on t64,t65 can hold.
	predicate := func(i int, sr *buf.SpinResult) bool { return (sr.TotalWin/sr.Bet+i/2+sr.GameModeCount)%4 == 0 }
	for i := range n {
		names[i] = fmt.Sprintf("t%02d", i)
		defs[names[i]] = func(sr *buf.SpinResult) bool { return predicate(i, sr) }
	}
	all := ClosedInterval{0, 1e9}
	classes := []ClassIntent{
		{Name: "second_word", Weight: 1, Collect: CollectIntent{Samples: 1000, WinRange: all, Tags: TagFilters{Matches: []string{"t64", "t65"}}}},
		{Name: "cross_word", Weight: 1, Collect: CollectIntent{Samples: 1000, WinRange: all, Tags: TagFilters{Matches: []string{"t02"}, Mismatches: []string{"t64"}}}},
		{Name: "not_last", Weight: 1, Collect: CollectIntent{Samples: 1000, WinRange: all, Tags: TagFilters{Mismatches: []string{"t69"}}}},
		{Name: "rest", Weight: 1, Collect: CollectIntent{Samples: 1000, WinRange: all}},
		// Never reached (rest accepts everything first); it makes the run reference all 70 names.
		{Name: "unreached", Weight: 1, Collect: CollectIntent{Samples: 1000, WinRange: all, Tags: TagFilters{Mismatches: names}}},
	}
	plan := ResolvedPlan{
		Plan: RunPlan{
			Target:     Target{Game: spec.GID(1), BetModes: []int{0}},
			Seed:       Int64Seed(4127483647),
			Collection: CollectionOptions{Workers: 1, BatchSize: 64, MaxSpins: 300},
			Intent:     "wide-tags",
		},
		Intent: MathIntent{Classes: classes},
	}
	collector := NewCollector(lab)
	collector.GameTags = tag.GameTagCatalog{1: defs}
	collector.WorkingDirectory = t.TempDir()
	collected, _, err := collector.Collect(context.Background(), plan, 0)
	if err != nil {
		t.Fatal(err)
	}
	if collected.tags == nil || collected.tags.tagger.Len() != n {
		t.Fatalf("compiled tagger does not reference all %d tags", n)
	}
	machine, err := newOptimizerMachine(lab, spec.GID(1), plan.Plan.Seed.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	has := func(sr *buf.SpinResult, name string) bool {
		var i int
		_, _ = fmt.Sscanf(name, "t%d", &i)
		return predicate(i, sr)
	}
	oracle := func(sr *buf.SpinResult) string {
		for _, class := range classes {
			ok := true
			for _, name := range class.Collect.Tags.Matches {
				ok = ok && has(sr, name)
			}
			for _, name := range class.Collect.Tags.Mismatches {
				ok = ok && !has(sr, name)
			}
			if ok {
				return class.Name
			}
		}
		return ""
	}
	seen := map[string]int{}
	for _, class := range collected.Classes {
		for _, sample := range class.Samples {
			if err := machine.RestoreCore(sample.Snapshot); err != nil {
				t.Fatal(err)
			}
			sr := machine.SpinUnsafe(problab.SpinInput{BetMode: 0, BetMult: 1})
			if want := oracle(sr); want != sample.ClassID {
				t.Fatalf("sample classified as %q, oracle says %q", sample.ClassID, want)
			}
			seen[sample.ClassID]++
		}
	}
	for _, name := range []string{"second_word", "cross_word", "not_last", "rest"} {
		if seen[name] == 0 {
			t.Fatalf("fixture never exercised Class %q: %v", name, seen)
		}
	}
}

// TestCollectTagErrorClassification keeps definition errors as general errors
// (checked first) and reference errors as configuration diagnostics.
func TestCollectTagErrorClassification(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	ok := func(*buf.SpinResult) bool { return true }
	plan := func(matches ...string) ResolvedPlan {
		p := collectionFixturePlan(4127483647, 1, 1, 1, 1)
		p.Intent.Classes[0].Collect.Tags.Matches = matches
		return p
	}
	for _, c := range []struct {
		name     string
		tags     tag.GameTagCatalog
		plan     ResolvedPlan
		wantErr  string
		wantDiag string
	}{
		{"no tags, no game", nil, plan(), "", ""},
		{"no tags, invalid definition", tag.GameTagCatalog{1: {"a": ok, "broken": nil}}, plan(), `register collection tags: tag "broken" predicate is nil`, ""},
		{"referenced, no game", tag.GameTagCatalog{2: {"a": ok}}, plan("a"), "", "compile class tags: tag 0 not found: a"},
		{"referenced, missing name", tag.GameTagCatalog{1: {"a": ok}}, plan("a", "nope"), "", "compile class tags: tag 1 not found: nope"},
		{"invalid definition before reference", tag.GameTagCatalog{1: {"": ok}}, plan("nope"), "register collection tags: tag name is empty", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			collector := NewCollector(lab)
			collector.GameTags = c.tags
			collector.WorkingDirectory = t.TempDir()
			_, diags, err := collector.Collect(context.Background(), c.plan, 0)
			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			gotDiag := ""
			if diags.StopsRun() {
				gotDiag = diags[0].Message
			}
			if gotErr != c.wantErr || gotDiag != c.wantDiag {
				t.Fatalf("err=%q diag=%q, want err=%q diag=%q", gotErr, gotDiag, c.wantErr, c.wantDiag)
			}
		})
	}
}

// BenchmarkCollectWithTags measures fresh collection with tag-filtered classes;
// every spin is evaluated against match and mismatch conditions. One op is one spin.
func BenchmarkCollectWithTags(b *testing.B) {
	lab, err := demo.NewProbLab()
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	collector := NewCollector(lab)
	collector.GameTags = map[spec.GID]map[string]tag.IsTag{1: {
		"fg":  func(sr *buf.SpinResult) bool { return sr.GameModeCount > 1 },
		"win": func(sr *buf.SpinResult) bool { return sr.TotalWin > 0 },
		"big": func(sr *buf.SpinResult) bool { return sr.TotalWin >= 3*sr.Bet },
	}}
	collector.WorkingDirectory = b.TempDir()
	spins := uint64(max(b.N, 1))
	all := ClosedInterval{0, 1e9}
	plan := ResolvedPlan{
		Plan: RunPlan{
			Target:     Target{Game: spec.GID(1), BetModes: []int{0}},
			Seed:       Int64Seed(4127483647),
			Collection: CollectionOptions{Workers: 1, BatchSize: 1 << 20, MaxSpins: spins},
			Intent:     "bench",
		},
		Intent: MathIntent{Classes: []ClassIntent{
			{Name: "big_fg", Weight: 1, Collect: CollectIntent{Samples: spins, WinRange: all, Tags: TagFilters{Matches: []string{"big", "fg"}}}},
			{Name: "win_not_fg", Weight: 1, Collect: CollectIntent{Samples: spins, WinRange: all, Tags: TagFilters{Matches: []string{"win"}, Mismatches: []string{"fg"}}}},
			{Name: "rest", Weight: 1, Collect: CollectIntent{Samples: spins, WinRange: all}},
		}},
	}
	b.ResetTimer()
	if _, _, err := collector.Collect(context.Background(), plan, 0); err != nil {
		b.Fatal(err)
	}
}
