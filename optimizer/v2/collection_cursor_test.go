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
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/core"
)

func TestParseCollectionBankStreamCursorAndPathContract(t *testing.T) {
	tests := []struct {
		path       string
		cursor     uint64
		recognized bool
		distinct   bool
	}{
		{path: "/tmp/seed_bank_100_s4.bin", cursor: 4, recognized: true},
		{path: "seed_bank_200_s8.distinct.bin", cursor: 8, recognized: true, distinct: true},
		{path: "seed_bank_999999999999999999999999999_s12.bin", cursor: 12, recognized: true},
		{path: "seed_bank_100.bin"},
		{path: "seed_bank__s4.bin"},
		{path: "seed_bank_100_s.bin"},
		{path: "seed_bank_100_s-1.bin"},
		{path: "seed_bank_100_s18446744073709551616.bin"},
		{path: "seed_bank_100_s4.partial.bin"},
		{path: "distinct_seed_bank_100_s4.bin"},
	}
	for _, test := range tests {
		got := parseCollectionBankStreamCursor(test.path)
		if got.Cursor != test.cursor || got.Recognized != test.recognized || got.Distinct != test.distinct {
			t.Fatalf("parseCollectionBankStreamCursor(%q)=%+v", test.path, got)
		}
	}

	parsed, maximum := parseConfiguredCollectionStreamCursors([]string{
		"seed_bank_100_s4.bin", "seed_bank_200_s8.distinct.bin", "legacy.bin",
	})
	if maximum != 8 || len(parsed) != 3 || parsed[2].Recognized {
		t.Fatalf("configured cursors=%+v maximum=%d", parsed, maximum)
	}
	parsed, maximum = parseConfiguredCollectionStreamCursors(nil)
	if len(parsed) != 0 || maximum != 0 {
		t.Fatalf("empty configured cursors=%+v maximum=%d", parsed, maximum)
	}

	workingDirectory := t.TempDir()
	plan := collectionFixturePlan(1, 1, 1, 1, 1)
	plan.Plan.Output.Directory = "build/optimizer"
	normal, err := collectionBankPath(workingDirectory, plan, 2, 1788754321, 12, false)
	if err != nil {
		t.Fatal(err)
	}
	distinct, err := collectionBankPath(workingDirectory, plan, 2, 1788754321, 12, true)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(normal) != "seed_bank_1788754321_s12.bin" || filepath.Base(distinct) != "seed_bank_1788754321_s12.distinct.bin" {
		t.Fatalf("normal=%q distinct=%q", normal, distinct)
	}
	if _, err := collectionBankPath(workingDirectory, plan, 2, -1, 12, false); err == nil {
		t.Fatal("negative timestamp was accepted")
	}
}

func TestCollectionAuditIsClassLocalOrderedAndClassifiesOrigins(t *testing.T) {
	original := CollectedProblem{
		SnapshotLength:    1,
		NextStreamOrdinal: 7,
		Evidence:          CollectionEvidence{ReplayAccepted: 2},
		Classes: []CollectedClass{
			{Intent: ClassIntent{Name: "first"}, Samples: []CollectedSample{
				{ClassID: "first", Win: 1, Snapshot: []byte{1}, Sequence: 0},
				{ClassID: "first", Win: 1, Snapshot: []byte{1}, Sequence: 1},
				{ClassID: "first", Win: 2, Snapshot: []byte{1}, Sequence: 3},
				{ClassID: "first", Win: 2, Snapshot: []byte{2}, Sequence: 4},
				{ClassID: "first", Win: 2, Snapshot: []byte{2}, Sequence: 5},
			}},
			{Intent: ClassIntent{Name: "second"}, Samples: []CollectedSample{
				{ClassID: "second", Win: 99, Snapshot: []byte{1}, Sequence: 2},
			}},
		},
	}
	before := cloneCollectedProblemForTest(original)
	audit, recovery, err := auditCollectedReplayIdentities(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, before) {
		t.Fatal("audit mutated the original collection")
	}
	if audit.Records != 6 || audit.UniqueRecords != 3 || audit.Duplicates != 3 || audit.DuplicateRate != 0.5 {
		t.Fatalf("audit=%+v", audit)
	}
	wantOrigins := []CollectionDuplicateOriginReport{
		{Origin: CollectionDuplicateReplayFresh, Duplicates: 1},
		{Origin: CollectionDuplicateFreshFresh, Duplicates: 1},
		{Origin: CollectionDuplicateReplayReplay, Duplicates: 1},
	}
	if !reflect.DeepEqual(audit.Origins, wantOrigins) || len(audit.Classes) != 1 || !reflect.DeepEqual(audit.Classes[0].Origins, wantOrigins) {
		t.Fatalf("origin reports=%+v classes=%+v", audit.Origins, audit.Classes)
	}
	if got := recovery.Classes[0].Samples; len(got) != 2 || got[0].Sequence != 0 || got[1].Sequence != 4 {
		t.Fatalf("first Class recovery samples=%+v", got)
	}
	if got := recovery.Classes[1].Samples; len(got) != 1 || !bytes.Equal(got[0].Snapshot, []byte{1}) {
		t.Fatalf("cross-Class occurrence was removed: %+v", got)
	}
	if recovery.NextStreamOrdinal != original.NextStreamOrdinal || !reflect.DeepEqual(recovery.Evidence, original.Evidence) {
		t.Fatalf("recovery metadata changed: %+v", recovery)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := auditCollectedReplayIdentities(cancelled, original); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled audit error=%v", err)
	}
}

func TestCollectionAuditAllowsEqualPayoutWithDifferentSnapshots(t *testing.T) {
	collected := CollectedProblem{
		SnapshotLength: 1,
		Classes: []CollectedClass{{Intent: ClassIntent{Name: "same-win"}, Samples: []CollectedSample{
			{ClassID: "same-win", Win: 10, Snapshot: []byte{1}, Sequence: 0},
			{ClassID: "same-win", Win: 10, Snapshot: []byte{2}, Sequence: 1},
		}}},
	}
	audit, recovery, err := auditCollectedReplayIdentities(context.Background(), collected)
	if err != nil || audit.Duplicates != 0 || audit.Records != 2 || audit.UniqueRecords != 2 || len(recovery.Classes[0].Samples) != 2 {
		t.Fatalf("audit=%+v recovery=%+v err=%v", audit, recovery, err)
	}
}

func TestCollectionRunReportValidatesCleanAndDistinctBankContracts(t *testing.T) {
	directory := t.TempDir()
	collected := CollectedProblem{
		SnapshotLength: 1, Spins: 2, NextStreamOrdinal: 2,
		Classes: []CollectedClass{{Intent: ClassIntent{Name: "all", Collect: CollectIntent{Samples: 2}}, Samples: []CollectedSample{
			{ClassID: "all", Snapshot: []byte{1}, Sequence: 0},
			{ClassID: "all", Snapshot: []byte{1}, Sequence: 1},
		}}},
		Evidence: CollectionEvidence{
			FreshSpins: 2, FreshAccepted: 2,
			Classes: []CollectionClassEvidence{{Name: "all", Requested: 2, FreshAccepted: 2, Accepted: 2}},
		},
	}
	audit, recovery, err := auditCollectedReplayIdentities(context.Background(), collected)
	if err != nil || audit.Duplicates != 1 {
		t.Fatalf("audit=%+v err=%v", audit, err)
	}
	distinctPath := filepath.Join(directory, "seed_bank_1_s2.distinct.bin")
	bank, err := (collectionBankWriter{}).Write(context.Background(), distinctPath, recovery, true, true)
	if err != nil {
		t.Fatal(err)
	}
	report, err := buildCollectionRunReport(collected, audit, bank)
	if err != nil {
		t.Fatal(err)
	}
	if report.Accepted != 2 || report.Bank.SeedCount != 1 || !report.Bank.Distinct || report.DuplicateAudit.Duplicates != 1 {
		t.Fatalf("distinct report=%+v", report)
	}

	badCount := bank
	badCount.SeedCount = 2
	badCount.Bytes = 2
	if _, err := buildCollectionRunReport(collected, audit, badCount); err == nil {
		t.Fatal("distinct seed-count mismatch was accepted")
	}
	badVariant := bank
	badVariant.Distinct = false
	if _, err := buildCollectionRunReport(collected, audit, badVariant); err == nil {
		t.Fatal("path/report distinct variant mismatch was accepted")
	}
	badCursor := bank
	badCursor.NextStreamOrdinal = 3
	if _, err := buildCollectionRunReport(collected, audit, badCursor); err == nil {
		t.Fatal("bank/report cursor mismatch was accepted")
	}

	clean := collected
	clean.Classes = []CollectedClass{{Intent: collected.Classes[0].Intent, Samples: []CollectedSample{
		{ClassID: "all", Snapshot: []byte{1}, Sequence: 0},
		{ClassID: "all", Snapshot: []byte{2}, Sequence: 1},
	}}}
	cleanAudit, _, err := auditCollectedReplayIdentities(context.Background(), clean)
	if err != nil {
		t.Fatal(err)
	}
	cleanPath := filepath.Join(directory, "seed_bank_2_s2.bin")
	cleanBank, err := (collectionBankWriter{}).Write(context.Background(), cleanPath, clean, false, false)
	if err != nil {
		t.Fatal(err)
	}
	cleanReport, err := buildCollectionRunReport(clean, cleanAudit, cleanBank)
	if err != nil || cleanReport.DuplicateAudit.Records != 2 || cleanReport.DuplicateAudit.UniqueRecords != 2 || cleanReport.DuplicateAudit.Duplicates != 0 {
		t.Fatalf("clean report=%+v err=%v", cleanReport, err)
	}
}

func TestCollectCursorZeroPreservesExistingWorkerSeedMapping(t *testing.T) {
	factory := &recordingPRNGFactory{base: core.Default()}
	lab := newCollectionFactoryLab(t, factory)
	defer func() { _ = lab.Close() }()
	factory.reset()

	plan := collectionFixturePlan(918273645, 4, 8, 8, 2)
	collected, diagnostics, err := NewCollector(lab).Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("Collect diagnostics=%+v err=%v", diagnostics, err)
	}
	want := []core.StreamID{
		{Domain: "optimizer/v2/worker", Index: 0},
		{Domain: "optimizer/v2/worker", Index: 1},
		{Domain: "optimizer/v2/worker", Index: 2},
	}
	if got := factory.deriveCalls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DeriveSeed calls=%+v want=%+v", got, want)
	}
	if collected.NextStreamOrdinal != 4 {
		t.Fatalf("next stream ordinal=%d want=4", collected.NextStreamOrdinal)
	}
	root := plan.Plan.Seed.Bytes()
	seed0, err := optimizerWorkerSeed(lab, root, 0)
	if err != nil || !bytes.Equal(seed0, root) || &seed0[0] == &root[0] {
		t.Fatalf("ordinal zero seed=%x root=%x err=%v", seed0, root, err)
	}
}

func TestCollectTopUpStartsAtMaximumConfiguredCursor(t *testing.T) {
	factory := &recordingPRNGFactory{base: core.Default()}
	lab := newCollectionFactoryLab(t, factory)
	defer func() { _ = lab.Close() }()
	factory.reset()

	directory := t.TempDir()
	plan := collectionFixturePlan(771122, 4, 4, 4, 1)
	plan.Plan.Collection.CollectedSeed = []string{
		filepath.Join(directory, "missing_1", "seed_bank_100_s4.bin"),
		filepath.Join(directory, "missing_2", "seed_bank_200_s8.distinct.bin"),
		filepath.Join(directory, "legacy.bin"),
	}
	recorder := &recordingReporter{}
	collector := NewCollector(lab)
	collector.WorkingDirectory = directory
	collector.Reporter = recorder
	collected, diagnostics, err := collector.Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("Collect diagnostics=%+v err=%v", diagnostics, err)
	}
	wantCalls := []core.StreamID{
		{Domain: "optimizer/v2/worker", Index: 7},
		{Domain: "optimizer/v2/worker", Index: 8},
		{Domain: "optimizer/v2/worker", Index: 9},
		{Domain: "optimizer/v2/worker", Index: 10},
	}
	if got := factory.deriveCalls(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("DeriveSeed calls=%+v want=%+v", got, wantCalls)
	}
	if collected.NextStreamOrdinal != 12 {
		t.Fatalf("next stream ordinal=%d want=12", collected.NextStreamOrdinal)
	}
	wantCursors := []struct {
		cursor     uint64
		recognized bool
	}{{4, true}, {8, true}, {0, false}}
	for i, want := range wantCursors {
		got := collected.Evidence.ReplaySources[i]
		if got.StreamCursor != want.cursor || got.StreamCursorRecognized != want.recognized {
			t.Fatalf("source[%d]=%+v", i, got)
		}
	}
	warnings := 0
	for _, event := range recorder.events {
		if event.Stage == "collection-replay-cursor" && event.State == "warning" {
			warnings++
			if event.Path != plan.Plan.Collection.CollectedSeed[2] || !strings.Contains(event.Message, "best-effort fallback") {
				t.Fatalf("legacy cursor event=%+v", event)
			}
		}
	}
	if warnings != 1 {
		t.Fatalf("legacy cursor warnings=%d want=1", warnings)
	}
}

func TestCollectReplayOnlyDoesNotAdvanceCursor(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	directory := t.TempDir()
	baselinePlan := collectionFixturePlan(9988, 1, 1, 1, 1)
	baseline, diagnostics, err := NewCollector(lab).Collect(context.Background(), baselinePlan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("baseline diagnostics=%+v err=%v", diagnostics, err)
	}
	bankPath := filepath.Join(directory, "seed_bank_100_s9.bin")
	if _, err := (collectionBankWriter{}).Write(context.Background(), bankPath, baseline, false, false); err != nil {
		t.Fatal(err)
	}
	plan := collectionFixturePlan(9988, 4, 1, 100, 1)
	plan.Plan.Collection.CollectedSeed = []string{bankPath}
	collector := NewCollector(lab)
	collector.WorkingDirectory = directory
	collected, diagnostics, err := collector.Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("replay-only diagnostics=%+v err=%v", diagnostics, err)
	}
	if collected.Spins != 0 || collected.NextStreamOrdinal != 9 {
		t.Fatalf("replay-only collection spins=%d next=%d", collected.Spins, collected.NextStreamOrdinal)
	}
}

func TestCollectFreshFanOutReservesWorkersWhenNothingIsAccepted(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	plan := collectionFixturePlan(123, 4, 1, 1, 1)
	plan.Intent.Classes[0].Collect.WinRange = ClosedInterval{1e100, 1e100}
	collected, diagnostics, err := NewCollector(lab).Collect(context.Background(), plan, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Code != DiagnosticCollectionInsufficient || collected.Evidence.FreshAccepted != 0 || collected.Spins != 1 {
		t.Fatalf("collection=%+v diagnostics=%+v", collected, diagnostics)
	}
	if collected.NextStreamOrdinal != 4 {
		t.Fatalf("zero-acceptance next stream ordinal=%d want=4", collected.NextStreamOrdinal)
	}
}

func TestTopUpWithPreviousBankProducesNoDuplicates(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	output := t.TempDir()
	tuner := newCollectionStageTuner(lab, output, &recordingReporter{}, 100)
	plan1 := collectionStagePlan(556677, 4, 8, 8, output)
	report1 := RunReport{}
	first, diagnostics, err := tuner.collectStage(context.Background(), plan1, 0, &report1)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("Run 1 diagnostics=%+v err=%v", diagnostics, err)
	}
	if report1.Collection == nil || filepath.Base(report1.Collection.Bank.Path) != "seed_bank_100_s4.bin" {
		t.Fatalf("Run 1 report=%+v", report1.Collection)
	}

	plan2 := collectionStagePlan(556677, 4, 16, 8, output)
	plan2.Plan.Collection.CollectedSeed = []string{report1.Collection.Bank.Path}
	report2 := RunReport{}
	second, diagnostics, err := tuner.collectStage(context.Background(), plan2, 0, &report2)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("Run 2 diagnostics=%+v err=%v", diagnostics, err)
	}
	if report2.Collection == nil || report2.Collection.DuplicateAudit.Duplicates != 0 || report2.Collection.Bank.Distinct || filepath.Base(report2.Collection.Bank.Path) != "seed_bank_100_s8.bin" {
		t.Fatalf("Run 2 report=%+v", report2.Collection)
	}
	firstIdentities := make(map[string]struct{}, len(first.Classes[0].Samples))
	for _, sample := range first.Classes[0].Samples {
		firstIdentities[string(sample.Snapshot)] = struct{}{}
	}
	for _, sample := range second.Classes[0].Samples {
		if sample.Sequence < second.Evidence.ReplayAccepted {
			continue
		}
		if _, exists := firstIdentities[string(sample.Snapshot)]; exists {
			t.Fatal("cursor-aware fresh collection repeated a Run 1 snapshot")
		}
	}
}

func TestLegacyBankTopUpProducesDistinctBankThenConverges(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	output := t.TempDir()
	recorder := &recordingReporter{}
	tuner := newCollectionStageTuner(lab, output, recorder, 200)

	plan1 := collectionStagePlan(778899, 4, 8, 8, output)
	report1 := RunReport{}
	if _, diagnostics, err := tuner.collectStage(context.Background(), plan1, 0, &report1); err != nil || diagnostics.StopsRun() {
		t.Fatalf("Run 1 diagnostics=%+v err=%v", diagnostics, err)
	}
	legacyPath := filepath.Join(output, "legacy_seed_bank.bin")
	copyFileForTest(t, report1.Collection.Bank.Path, legacyPath)

	plan2 := collectionStagePlan(778899, 4, 16, 8, output)
	plan2.Plan.Collection.CollectedSeed = []string{legacyPath}
	report2 := RunReport{}
	_, diagnostics, err := tuner.collectStage(context.Background(), plan2, 0, &report2)
	if err != nil {
		t.Fatalf("Run 2 operational error: %v", err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Code != DiagnosticDuplicateReplayIdentity {
		t.Fatalf("Run 2 diagnostics=%+v", diagnostics)
	}
	if report2.Collection == nil || !report2.Collection.Bank.Distinct || !report2.Collection.Bank.Partial || report2.Collection.Bank.NextStreamOrdinal != 4 || filepath.Base(report2.Collection.Bank.Path) != "seed_bank_200_s4.distinct.bin" {
		t.Fatalf("Run 2 report=%+v", report2.Collection)
	}
	if report2.Collection.Accepted != 16 || report2.Collection.DuplicateAudit.Duplicates != 8 || report2.Collection.Bank.SeedCount != 8 {
		t.Fatalf("Run 2 counts=%+v", report2.Collection)
	}
	if !reflect.DeepEqual(report2.Collection.DuplicateAudit.Origins, []CollectionDuplicateOriginReport{{Origin: CollectionDuplicateReplayFresh, Duplicates: 8}}) {
		t.Fatalf("Run 2 origins=%+v", report2.Collection.DuplicateAudit.Origins)
	}

	plan3 := collectionStagePlan(778899, 4, 16, 8, output)
	plan3.Plan.Collection.CollectedSeed = []string{report2.Collection.Bank.Path}
	report3 := RunReport{}
	_, diagnostics, err = tuner.collectStage(context.Background(), plan3, 0, &report3)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("Run 3 diagnostics=%+v err=%v", diagnostics, err)
	}
	if report3.Collection == nil || report3.Collection.DuplicateAudit.Duplicates != 0 || report3.Collection.Bank.Distinct || report3.Collection.Bank.NextStreamOrdinal != 8 || filepath.Base(report3.Collection.Bank.Path) != "seed_bank_200_s8.bin" {
		t.Fatalf("Run 3 report=%+v", report3.Collection)
	}
	legacyWarning := false
	for _, event := range recorder.events {
		if event.Stage == "collection-replay-cursor" && event.Path == legacyPath {
			legacyWarning = true
		}
	}
	if !legacyWarning {
		t.Fatal("legacy top-up emitted no cursor warning")
	}
}

func TestTunerDuplicateCollectionWritesOnlyDistinctBankAndStops(t *testing.T) {
	lab := newCollectionFactoryLab(t, &collidingPRNGFactory{base: core.Default()})
	defer func() { _ = lab.Close() }()
	output := t.TempDir()
	recorder := &recordingReporter{}
	tuner := newCollectionStageTuner(lab, output, recorder, 300)
	plan := collectionStagePlan(1234, 2, 2, 2, output)
	report := RunReport{}
	collected, diagnostics, err := tuner.collectStage(context.Background(), plan, 0, &report)
	if err != nil {
		t.Fatal(err)
	}
	if len(collected.Classes[0].Samples) != 2 || !bytes.Equal(collected.Classes[0].Samples[0].Snapshot, collected.Classes[0].Samples[1].Snapshot) {
		t.Fatal("fresh collection was deduplicated before the post-collection audit")
	}
	if len(diagnostics) != 1 || diagnostics[0].Code != DiagnosticDuplicateReplayIdentity || !diagnostics.StopsRun() {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	if report.Collection == nil || !report.Collection.Bank.Distinct || !report.Collection.Bank.Partial || report.Collection.Accepted != 2 || report.Collection.Bank.SeedCount != 1 || report.Collection.Bank.NextStreamOrdinal != 2 {
		t.Fatalf("collection report=%+v", report.Collection)
	}
	if !reflect.DeepEqual(report.Collection.DuplicateAudit.Origins, []CollectionDuplicateOriginReport{{Origin: CollectionDuplicateFreshFresh, Duplicates: 1}}) {
		t.Fatalf("duplicate audit=%+v", report.Collection.DuplicateAudit)
	}
	if !strings.Contains(diagnostics[0].Message, report.Collection.Bank.Path) || !strings.Contains(diagnostics[0].Message, "stopped before Prepare") || len(diagnostics[0].Causes) != 1 || !strings.Contains(diagnostics[0].Causes[0].Summary, "provenance is investigative evidence only") {
		t.Fatalf("duplicate diagnostic=%+v", diagnostics[0])
	}
	entries, err := os.ReadDir(filepath.Dir(report.Collection.Bank.Path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "seed_bank_300_s2.distinct.bin" || entries[1].Name() != "seed_bank_300_s2.distinct.parquet" {
		t.Fatalf("collection bank entries=%v", entries)
	}
	assertDescriptorMatchesBank(t, *report.Collection)
	assertDuplicateEventOrder(t, recorder.events, true)
}

func TestTunerRunStopsBeforePrepareOnDuplicateCollection(t *testing.T) {
	lab := newCollectionFactoryLab(t, &collidingPRNGFactory{base: core.Default()})
	defer func() { _ = lab.Close() }()
	output := t.TempDir()
	disabled := false
	config := Config{
		Version: ConfigVersion,
		Plans: []RunPlan{{
			ID: "duplicate-gate", Target: Target{Game: 1, BetModes: []int{0}},
			Engine: EngineIntentLPV2, Intent: "duplicate-gate", Seed: Int64Seed(24680),
			Collection:         CollectionOptions{Workers: 2, BatchSize: 1, MaxSpins: 2},
			CandidateSelection: CandidateSelectionOptions{Evaluator: "none", MaxCandidates: 1},
			Output:             OutputOptions{Format: []OutputFormat{OutputOptimalArtifactV1}, Directory: output},
		}},
		Intents: map[string]MathIntent{"duplicate-gate": {
			Overall: OverallIntent{CV: NumericRange{Min: 0, Max: 100}},
			Classes: []ClassIntent{{
				Name: "all", Weight: ClassWeightBase,
				Collect: CollectIntent{Samples: 2, WinRange: ClosedInterval{0, 1e9}},
				Design: ClassDesign{
					Exp: 1, Median: ClosedInterval{0, 1e9},
					Subjective: SubjectiveIntent{Intent: &disabled},
				},
			}},
		}},
		EngineOptions: DefaultEngineOptions(),
	}
	recorder := &recordingReporter{}
	tuner, err := NewTuner(config, lab, WithReporter(recorder))
	if err != nil {
		t.Fatal(err)
	}
	tuner.now = func() time.Time { return time.Unix(350, 0) }
	result, err := tuner.Run(context.Background(), RunRequest{PlanID: "duplicate-gate"})
	if err != nil {
		t.Fatalf("Tuner.Run operational error: %v", err)
	}
	if result.Status != StatusInfeasibleSupport || len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != DiagnosticDuplicateReplayIdentity {
		t.Fatalf("result status=%s diagnostics=%+v", result.Status, result.Diagnostics)
	}
	if result.Report.Collection == nil || !result.Report.Collection.Bank.Distinct || filepath.Base(result.Report.Collection.Bank.Path) != "seed_bank_350_s2.distinct.bin" {
		t.Fatalf("collection report=%+v", result.Report.Collection)
	}
	for _, event := range recorder.events {
		if strings.HasPrefix(event.Stage, "prepare[") || strings.HasPrefix(event.Stage, "compile[") || strings.HasPrefix(event.Stage, "solve[") {
			t.Fatalf("downstream stage ran after duplicate collection: %+v", event)
		}
	}
}

func TestTunerDistinctWriteFailurePreservesWarningWithoutSavedBank(t *testing.T) {
	lab := newCollectionFactoryLab(t, &collidingPRNGFactory{base: core.Default()})
	defer func() { _ = lab.Close() }()
	output := t.TempDir()
	recorder := &recordingReporter{}
	tuner := newCollectionStageTuner(lab, output, recorder, 400)
	tuner.collectionBankWriter.renamePath = func(_, _ string) error { return errors.New("rename blocked") }
	plan := collectionStagePlan(4321, 2, 2, 2, output)
	report := RunReport{}
	_, diagnostics, err := tuner.collectStage(context.Background(), plan, 0, &report)
	if err == nil {
		t.Fatal("distinct write failure was ignored")
	}
	wantPath := filepath.Join(output, "collected", "game_1", "mode_0", "seed_bank_400_s2.distinct.bin")
	for _, want := range []string{"duplicate-recovery", "game 1", "mode 0", wantPath, "1 duplicates", "rename blocked"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error=%q want substring=%q", err, want)
		}
	}
	if len(diagnostics) != 0 || report.Collection != nil {
		t.Fatalf("failure returned diagnostics=%+v report=%+v", diagnostics, report.Collection)
	}
	if _, statErr := os.Stat(wantPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed target exists: %v", statErr)
	}
	assertDuplicateEventOrder(t, recorder.events, false)
}

func TestTunerPreservesCollectionInsufficientBeforeDuplicateDiagnostic(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	output := t.TempDir()
	baselinePlan := collectionStagePlan(9191, 1, 1, 1, output)
	baseline, baselineDiagnostics, err := NewCollector(lab).Collect(context.Background(), baselinePlan, 0)
	if err != nil || baselineDiagnostics.StopsRun() {
		t.Fatalf("baseline diagnostics=%+v err=%v", baselineDiagnostics, err)
	}
	legacyPath := filepath.Join(output, "legacy.bin")
	if _, err := (collectionBankWriter{}).Write(context.Background(), legacyPath, baseline, false, false); err != nil {
		t.Fatal(err)
	}
	plan := collectionStagePlan(9191, 1, 3, 1, output)
	plan.Plan.Collection.CollectedSeed = []string{legacyPath}
	tuner := newCollectionStageTuner(lab, output, &recordingReporter{}, 500)
	report := RunReport{}
	_, diagnostics, err := tuner.collectStage(context.Background(), plan, 0, &report)
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 2 || diagnostics[0].Code != DiagnosticCollectionInsufficient || diagnostics[1].Code != DiagnosticDuplicateReplayIdentity {
		t.Fatalf("diagnostic order=%+v", diagnostics)
	}
	if report.Collection == nil || report.Collection.Accepted != 2 || report.Collection.DuplicateAudit.Duplicates != 1 || report.Collection.Bank.SeedCount != 1 {
		t.Fatalf("collection report=%+v", report.Collection)
	}
}

type recordingPRNGFactory struct {
	base  core.PRNGFactory
	mu    sync.Mutex
	calls []core.StreamID
}

func (f *recordingPRNGFactory) New(seed []byte) (core.PRNG, error) {
	return f.base.New(seed)
}

func (f *recordingPRNGFactory) GenerateSeed(entropy io.Reader) ([]byte, error) {
	return f.base.GenerateSeed(entropy)
}

func (f *recordingPRNGFactory) DeriveSeed(parent []byte, stream core.StreamID) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, stream)
	f.mu.Unlock()
	return f.base.DeriveSeed(parent, stream)
}

func (f *recordingPRNGFactory) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *recordingPRNGFactory) deriveCalls() []core.StreamID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.StreamID(nil), f.calls...)
}

type collidingPRNGFactory struct {
	base core.PRNGFactory
}

func (f *collidingPRNGFactory) New(seed []byte) (core.PRNG, error) {
	return f.base.New(seed)
}

func (f *collidingPRNGFactory) GenerateSeed(entropy io.Reader) ([]byte, error) {
	return f.base.GenerateSeed(entropy)
}

func (f *collidingPRNGFactory) DeriveSeed(parent []byte, stream core.StreamID) ([]byte, error) {
	if stream.Domain == "" {
		return nil, errors.New("empty stream domain")
	}
	return append([]byte(nil), parent...), nil
}

func newCollectionFactoryLab(t *testing.T, factory core.PRNGFactory) *problab.Problab {
	t.Helper()
	lab, err := problab.NewAuto(
		factory,
		problab.Configs(demo_configs.FS),
		problab.Logics(demo_logic.Logics),
	)
	if err != nil {
		t.Fatalf("construct Problab: %v", err)
	}
	return lab
}

func collectionStagePlan(seed int64, workers int, samples, maxSpins uint64, output string) ResolvedPlan {
	plan := collectionFixturePlan(seed, workers, samples, maxSpins, 1)
	plan.Plan.Output.Directory = output
	return plan
}

func newCollectionStageTuner(lab *problab.Problab, workingDirectory string, reporter Reporter, unix int64) *Tuner {
	collector := NewCollector(lab)
	collector.WorkingDirectory = workingDirectory
	collector.Reporter = reporter
	return &Tuner{
		lab: lab, collector: collector, reporter: reporter,
		workingDirectory:     workingDirectory,
		now:                  func() time.Time { return time.Unix(unix, 0) },
		collectionBankWriter: collectionBankWriter{},
	}
}

func cloneCollectedProblemForTest(collected CollectedProblem) CollectedProblem {
	cloned := collected
	cloned.Evidence = cloneCollectionEvidence(collected.Evidence)
	cloned.Classes = make([]CollectedClass, len(collected.Classes))
	for i, class := range collected.Classes {
		cloned.Classes[i] = CollectedClass{Intent: cloneClassIntent(class.Intent), Samples: make([]CollectedSample, len(class.Samples))}
		for j, sample := range class.Samples {
			cloned.Classes[i].Samples[j] = sample
			cloned.Classes[i].Samples[j].Snapshot = append([]byte(nil), sample.Snapshot...)
		}
	}
	return cloned
}

func copyFileForTest(t *testing.T, source, destination string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertDuplicateEventOrder(t *testing.T, events []StageEvent, expectCompleted bool) {
	t.Helper()
	warningIndex, completedIndex := -1, -1
	for i, event := range events {
		switch event.Stage {
		case "collection-duplicates":
			if event.State != "warning" || event.Duplicates != 1 || !event.Distinct || event.Path == "" {
				t.Fatalf("duplicate warning=%+v", event)
			}
			warningIndex = i
		case "collection-bank":
			if event.State == "completed" {
				completedIndex = i
			}
		}
	}
	if warningIndex < 0 {
		t.Fatal("duplicate warning was not emitted")
	}
	if expectCompleted {
		if completedIndex <= warningIndex {
			t.Fatalf("event order warning=%d completed=%d", warningIndex, completedIndex)
		}
	} else if completedIndex >= 0 {
		t.Fatalf("failed write emitted bank completion at %d", completedIndex)
	}
}

func TestCollectRejectsLogicalCursorOverflowBeforeFreshFanOut(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	directory := t.TempDir()
	plan := collectionFixturePlan(1, 2, 1, 1, 1)
	plan.Plan.Collection.CollectedSeed = []string{filepath.Join(directory, "seed_bank_1_s18446744073709551615.bin")}
	collector := NewCollector(lab)
	collector.WorkingDirectory = directory
	_, diagnostics, err := collector.Collect(context.Background(), plan, 0)
	if err == nil || diagnostics.StopsRun() || !strings.Contains(err.Error(), "logical stream cursor") || !strings.Contains(err.Error(), "overflows uint64") {
		t.Fatalf("overflow diagnostics=%+v err=%v", diagnostics, err)
	}
}

func TestCollectionDuplicateRateZeroForEmptyAudit(t *testing.T) {
	audit, recovery, err := auditCollectedReplayIdentities(context.Background(), CollectedProblem{SnapshotLength: 1})
	if err != nil || audit.Records != 0 || audit.UniqueRecords != 0 || audit.Duplicates != 0 || audit.DuplicateRate != 0 || len(recovery.Classes) != 0 {
		t.Fatalf("empty audit=%+v recovery=%+v err=%v", audit, recovery, err)
	}
	if math.IsNaN(audit.DuplicateRate) {
		t.Fatal("empty duplicate rate is NaN")
	}
}
