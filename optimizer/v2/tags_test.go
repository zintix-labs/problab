// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

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
	r, err := newCollectionTagRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bg", "fg"} {
		if _, err := r.BitSet(name); err == nil {
			t.Fatalf("implicit tag %s", name)
		}
	}
	r, err = newCollectionTagRegistry(map[string]tag.IsTag{
		"bg": func(*buf.SpinResult) bool { return false },
		"fg": func(*buf.SpinResult) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	bs, err := r.BitSet("bg", "fg")
	if err != nil {
		t.Fatal(err)
	}
	if bs.Tagging(nil) != 2 {
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
	bs, predicates, err := prepared.replayTags()
	if err != nil || bs != collected.tags.tagger || bs.Tagging(nil) != predicates[0].matchMask {
		t.Fatal("run-local predicates changed")
	}
	prepared.tags = nil
	if _, _, err := prepared.replayTags(); err == nil {
		t.Fatal("custom tags without runtime context must fail closed")
	}
}
