// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v2

import (
	"bytes"
	"context"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo"
	legacyoptimizer "github.com/zintix-labs/problab/optimizer"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

// TestCollectWorkersAreDeterministicAndPreserveWorkerZeroSeed locks the public
// stream-partition contract: scheduling cannot affect a fixed Seed/Workers
// pair, worker zero preserves the old sequential seed, and worker one starts at
// SeedMaker's first deterministic sub-seed.
func TestCollectWorkersAreDeterministicAndPreserveWorkerZeroSeed(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatalf("construct demo Problab: %v", err)
	}
	defer func() { _ = lab.Close() }()

	const seed int64 = 918273645
	const (
		workers = 4
		samples = 17
	)
	plan := collectionFixturePlan(seed, workers, samples, 100, 3)
	first, diagnostics, err := NewCollector(lab).Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("first Collect: diagnostics=%+v err=%v", diagnostics, err)
	}
	second, diagnostics, err := NewCollector(lab).Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("second Collect: diagnostics=%+v err=%v", diagnostics, err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same Seed and Workers produced different collected results")
	}
	if first.Spins != samples || len(first.Classes) != 1 || len(first.Classes[0].Samples) != samples {
		t.Fatalf("collected=%+v, want %d accepted spins", first, samples)
	}
	for index, sample := range first.Classes[0].Samples {
		if sample.Sequence != uint64(index) {
			t.Fatalf("sample[%d].Sequence=%d, want %d", index, sample.Sequence, index)
		}
	}

	workerZero, err := lab.NewUnoptimizedMachineWithSeed(spec.GID(1), seed, true)
	if err != nil {
		t.Fatal(err)
	}
	wantWorkerZero, err := workerZero.SnapshotCore()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Classes[0].Samples[0].Snapshot, wantWorkerZero) {
		t.Fatal("worker zero did not retain the original sequential seed")
	}

	seedMaker := problab.NewSeedMaker(seed)
	workerOne, err := lab.NewUnoptimizedMachineWithSeed(spec.GID(1), seedMaker.Next(), true)
	if err != nil {
		t.Fatal(err)
	}
	wantWorkerOne, err := workerOne.SnapshotCore()
	if err != nil {
		t.Fatal(err)
	}
	workerOneOffset := int(partitionShare(samples, workers, 0))
	if !bytes.Equal(first.Classes[0].Samples[workerOneOffset].Snapshot, wantWorkerOne) {
		t.Fatal("worker one did not receive SeedMaker's first deterministic sub-seed")
	}
}

// TestCollectStaticallyPartitionsMaxSpins proves the aggregate hard budget is
// neither multiplied by Workers nor dynamically borrowed between workers.
func TestCollectStaticallyPartitionsMaxSpins(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatalf("construct demo Problab: %v", err)
	}
	defer func() { _ = lab.Close() }()

	plan := collectionFixturePlan(77, 3, 5, 2, 1)
	collected, diagnostics, err := NewCollector(lab).Collect(context.Background(), plan, 0)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if collected.Spins != 2 || len(collected.Classes[0].Samples) != 2 {
		t.Fatalf("spins=%d samples=%d, want the exact aggregate MaxSpins=2", collected.Spins, len(collected.Classes[0].Samples))
	}
	if len(diagnostics) != 1 || diagnostics[0].Code != DiagnosticCollectionInsufficient {
		t.Fatalf("diagnostics=%+v, want CollectionInsufficient", diagnostics)
	}
}

func TestCollectMergePreservesWorkerThenLocalAcceptanceOrder(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatalf("construct demo Problab: %v", err)
	}
	defer func() { _ = lab.Close() }()

	// MaxSpins exactly equals the aggregate request. Rotating the two odd Class
	// remainders gives each worker three requested samples and three spins;
	// assigning every remainder to worker zero would fail spuriously here.
	plan := collectionFixturePlan(2468, 2, 3, 6, 2)
	second := plan.Intent.Classes[0]
	second.Name = "second"
	plan.Intent.Classes[0].Name = "first"
	plan.Intent.Classes = append(plan.Intent.Classes, second)

	collected, diagnostics, err := NewCollector(lab).Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("Collect: diagnostics=%+v err=%v", diagnostics, err)
	}
	if collected.Spins != 6 {
		t.Fatalf("spins=%d, want 6", collected.Spins)
	}
	wantSequences := [][]uint64{{0, 1, 3}, {2, 4, 5}}
	for classIndex, class := range collected.Classes {
		got := make([]uint64, len(class.Samples))
		for i, sample := range class.Samples {
			got[i] = sample.Sequence
		}
		if !reflect.DeepEqual(got, wantSequences[classIndex]) {
			t.Fatalf("class %q sequences=%v, want %v", class.Intent.Name, got, wantSequences[classIndex])
		}
	}
}

// TestCollectSerializesReporterCalls exercises many small progress updates
// from several workers. Reporter remains a synchronous single-caller boundary,
// and its aggregate counters never move backwards even if worker messages
// arrive in a scheduler-dependent order.
func TestCollectSerializesReporterCalls(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatalf("construct demo Problab: %v", err)
	}
	defer func() { _ = lab.Close() }()

	reporter := &concurrencyCheckingReporter{}
	collector := NewCollector(lab)
	collector.Reporter = reporter
	plan := collectionFixturePlan(12345, 4, 64, 128, 1)
	_, diagnostics, err := collector.Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("Collect: diagnostics=%+v err=%v", diagnostics, err)
	}
	if reporter.concurrent.Load() {
		t.Fatal("Reporter.Report was called concurrently")
	}

	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if len(reporter.events) < 2 {
		t.Fatalf("reported %d events, want initial and terminal progress", len(reporter.events))
	}
	for i := 1; i < len(reporter.events); i++ {
		before, after := reporter.events[i-1], reporter.events[i]
		if after.Spins < before.Spins || after.Accepted < before.Accepted {
			t.Fatalf("progress moved backwards: before=%+v after=%+v", before, after)
		}
	}
	last := reporter.events[len(reporter.events)-1]
	if last.Spins != 64 || last.Accepted != 64 || last.Requested != 64 {
		t.Fatalf("terminal progress=%+v", last)
	}
}

func TestCollectResolvesGameScopedCustomTag(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatalf("construct demo Problab: %v", err)
	}
	defer func() { _ = lab.Close() }()

	const gid spec.GID = 1
	isZeroWin := func(result *buf.SpinResult) bool { return result.TotalWin == 0 }
	collector := NewCollector(lab)
	collector.GameTags = map[spec.GID]map[string]legacyoptimizer.IsTag{
		gid: {"custom_tag": isZeroWin},
	}
	plan := collectionFixturePlan(864209753, 1, 32, 10_000, 100)
	plan.Plan.Target.Game = gid
	plan.Intent.Classes[0].Collect.Tags.Matches = []string{"custom_tag"}

	collected, diagnostics, err := collector.Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("Collect: diagnostics=%+v err=%v", diagnostics, err)
	}
	if got := len(collected.Classes[0].Samples); got != 32 {
		t.Fatalf("accepted samples=%d, want 32", got)
	}
	if collected.Spins <= uint64(len(collected.Classes[0].Samples)) {
		t.Fatalf("spins=%d accepted=%d, want evidence that nonmatching outcomes were rejected", collected.Spins, len(collected.Classes[0].Samples))
	}

	replay, err := lab.NewUnoptimizedMachineWithSeed(gid, 1, true)
	if err != nil {
		t.Fatalf("construct replay machine: %v", err)
	}
	for index, sample := range collected.Classes[0].Samples {
		if err := replay.RestoreCore(sample.Snapshot); err != nil {
			t.Fatalf("restore accepted sample[%d]: %v", index, err)
		}
		result := replay.SpinInternal(0)
		if result == nil || !isZeroWin(result) {
			t.Fatalf("accepted sample[%d] does not satisfy custom_tag: %+v", index, result)
		}
	}
}

func TestCollectFailsClearlyOnUnknownTagName(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatalf("construct demo Problab: %v", err)
	}
	defer func() { _ = lab.Close() }()

	plan := collectionFixturePlan(123, 1, 1, 10, 1)
	plan.Intent.Classes[0].Collect.Tags.Matches = []string{"missing_custom_tag"}
	collector := NewCollector(lab)
	collector.GameTags = map[spec.GID]map[string]legacyoptimizer.IsTag{
		plan.Plan.Target.Game: {"different_custom_tag": func(*buf.SpinResult) bool { return true }},
	}

	_, diagnostics, err := collector.Collect(context.Background(), plan, 0)
	if err != nil {
		t.Fatalf("Collect returned an operational error: %v", err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Code != DiagnosticConfigInvalid {
		t.Fatalf("diagnostics=%+v, want one ConfigInvalid diagnostic", diagnostics)
	}
	if !strings.Contains(diagnostics[0].Message, "missing_custom_tag") || !strings.Contains(diagnostics[0].Message, "not found") {
		t.Fatalf("unknown-tag diagnostic=%q, want tag name and not-found cause", diagnostics[0].Message)
	}
}

func TestCollectWithoutGameTagsStillResolvesBuiltins(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatalf("construct demo Problab: %v", err)
	}
	defer func() { _ = lab.Close() }()

	plan := collectionFixturePlan(975312468, 1, 16, 1_000, 20)
	plan.Intent.Classes[0].Collect.Tags.Matches = []string{"bg"}
	withoutOption, diagnostics, err := NewCollector(lab).Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("Collect without GameTags: diagnostics=%+v err=%v", diagnostics, err)
	}
	withEmptySet := NewCollector(lab)
	withEmptySet.GameTags = map[spec.GID]map[string]legacyoptimizer.IsTag{
		plan.Plan.Target.Game: {},
	}
	withOption, diagnostics, err := withEmptySet.Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("Collect with empty game tag set: diagnostics=%+v err=%v", diagnostics, err)
	}
	if !reflect.DeepEqual(withoutOption, withOption) {
		t.Fatal("nil GameTags changed built-in bg collection behavior")
	}

	replay, err := lab.NewUnoptimizedMachineWithSeed(plan.Plan.Target.Game, 1, true)
	if err != nil {
		t.Fatalf("construct replay machine: %v", err)
	}
	for index, sample := range withoutOption.Classes[0].Samples {
		if err := replay.RestoreCore(sample.Snapshot); err != nil {
			t.Fatalf("restore accepted sample[%d]: %v", index, err)
		}
		if _, matched := legacyoptimizer.IsOnlyBg(replay.SpinInternal(0)); !matched {
			t.Fatalf("accepted sample[%d] does not satisfy built-in bg", index)
		}
	}
}

func TestPartitionSharePreservesTotalsAndAssignsRemaindersByWorkerIndex(t *testing.T) {
	for _, test := range []struct {
		total   uint64
		workers int
		want    []uint64
	}{
		{total: 10, workers: 3, want: []uint64{4, 3, 3}},
		{total: 2, workers: 4, want: []uint64{1, 1, 0, 0}},
		{total: 0, workers: 3, want: []uint64{0, 0, 0}},
	} {
		got := make([]uint64, test.workers)
		sum := uint64(0)
		for worker := range test.workers {
			got[worker] = partitionShare(test.total, test.workers, worker)
			sum += got[worker]
		}
		if !reflect.DeepEqual(got, test.want) || sum != test.total {
			t.Fatalf("partitionShare(%d, %d)=%v sum=%d, want %v", test.total, test.workers, got, sum, test.want)
		}
	}
}

func TestPartitionClassQuotasBalancesRemaindersAcrossClasses(t *testing.T) {
	classes := []ClassIntent{
		{Name: "a", Collect: CollectIntent{Samples: 1}},
		{Name: "b", Collect: CollectIntent{Samples: 1}},
		{Name: "c", Collect: CollectIntent{Samples: 3}},
	}
	got := partitionClassQuotas(classes, 2)
	want := [][]uint64{{1, 0, 2}, {0, 1, 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("partitionClassQuotas=%v, want %v", got, want)
	}
	for worker, quotas := range got {
		total := uint64(0)
		for _, quota := range quotas {
			total += quota
		}
		if wantTotal := partitionShare(5, 2, worker); total != wantTotal {
			t.Fatalf("worker %d total quota=%d, want %d", worker, total, wantTotal)
		}
	}
}

type concurrencyCheckingReporter struct {
	active     atomic.Int32
	concurrent atomic.Bool
	mu         sync.Mutex
	events     []StageEvent
}

func (r *concurrencyCheckingReporter) Report(event StageEvent) {
	if r.active.Add(1) != 1 {
		r.concurrent.Store(true)
	}
	runtime.Gosched()
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	r.active.Add(-1)
}

func collectionFixturePlan(seed int64, workers int, samples, maxSpins, batchSize uint64) ResolvedPlan {
	class := ClassIntent{
		Name:   "all_outcomes",
		Weight: 1,
		Collect: CollectIntent{
			Samples: samples, WinRange: ClosedInterval{0, 1e9},
		},
	}
	return ResolvedPlan{
		Plan: RunPlan{
			Target: Target{Game: spec.GID(1), BetModes: []int{0}},
			Seed:   seed,
			Collection: CollectionOptions{
				Workers: workers, BatchSize: batchSize, MaxSpins: maxSpins,
			},
			Intent: "collection-fixture",
		},
		Intent: MathIntent{Classes: []ClassIntent{class}},
	}
}
