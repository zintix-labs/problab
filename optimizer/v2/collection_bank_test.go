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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zintix-labs/problab/demo"
)

func TestResolveRunPathAndCollectionBankPath(t *testing.T) {
	workingDirectory := filepath.Join(t.TempDir(), "startup", "work")
	resolved, err := resolveRunPath(workingDirectory, "../banks/one.bin")
	if err != nil {
		t.Fatal(err)
	}
	wantResolved := filepath.Clean(filepath.Join(workingDirectory, "../banks/one.bin"))
	if resolved != wantResolved || !filepath.IsAbs(resolved) {
		t.Fatalf("resolved=%q want=%q", resolved, wantResolved)
	}
	absolute := filepath.Join(t.TempDir(), "absolute", "two.bin")
	if got, err := resolveRunPath(workingDirectory, absolute); err != nil || got != filepath.Clean(absolute) {
		t.Fatalf("absolute resolve=%q err=%v", got, err)
	}

	plan := ResolvedPlan{Plan: RunPlan{Target: Target{Game: 2026}, Output: OutputOptions{Directory: "build/optimizer"}}}
	path, err := collectionBankPath(workingDirectory, plan, 2, 1788754321, 4, false)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(workingDirectory, "build", "optimizer", "collected", "game_2026", "mode_2", "seed_bank_1788754321_s4.bin")
	if path != wantPath {
		t.Fatalf("collection bank path=%q want=%q", path, wantPath)
	}
	if _, err := collectionBankPath(workingDirectory, plan, 2, -1, 4, false); err == nil {
		t.Fatal("negative timestamp was accepted")
	}
}

func TestReplayCollectionRecordsUsesTypedNormalStopsAndPreservesErrors(t *testing.T) {
	input := []byte{1, 2, 3, 4, 5, 6}
	var indexes []uint64
	records, end, err := replayCollectionRecords(context.Background(), bytes.NewReader(input), 3, 2, func(index uint64, snapshot []byte) (replayRecordAction, error) {
		indexes = append(indexes, index)
		return replayRecordContinue, nil
	})
	if err != nil || records != 3 || end != replayLoopExhausted || !reflect.DeepEqual(indexes, []uint64{0, 1, 2}) {
		t.Fatalf("exhausted records=%d end=%q indexes=%v err=%v", records, end, indexes, err)
	}
	records, end, err = replayCollectionRecords(context.Background(), bytes.NewReader(input), 3, 2, func(index uint64, snapshot []byte) (replayRecordAction, error) {
		return replayRecordStopQuotasFull, nil
	})
	if err != nil || records != 1 || end != replayLoopQuotasFull {
		t.Fatalf("quota stop records=%d end=%q err=%v", records, end, err)
	}
	sentinel := errors.New("visitor failed")
	_, _, err = replayCollectionRecords(context.Background(), bytes.NewReader(input), 3, 2, func(index uint64, snapshot []byte) (replayRecordAction, error) {
		return replayRecordContinue, sentinel
	})
	if err != sentinel {
		t.Fatalf("visitor error=%v want exact sentinel", err)
	}
	if _, _, err := replayCollectionRecords(context.Background(), bytes.NewReader(input[:1]), 1, 2, func(uint64, []byte) (replayRecordAction, error) {
		return replayRecordContinue, nil
	}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short reader error=%v", err)
	}
}

func TestCollectionBankWriterUsesClassThenSequenceOrderAndDigest(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nested", "seed_bank_1.bin")
	collected := CollectedProblem{
		SnapshotLength: 1,
		Classes: []CollectedClass{
			{Intent: ClassIntent{Name: "first"}, Samples: []CollectedSample{
				{ClassID: "first", Win: 99, Snapshot: []byte{2}, Sequence: 2},
				{ClassID: "first", Win: 1, Snapshot: []byte{4}, Sequence: 4},
			}},
			{Intent: ClassIntent{Name: "second"}, Samples: []CollectedSample{
				{ClassID: "second", Win: 0, Snapshot: []byte{1}, Sequence: 1},
			}},
		},
	}
	report, err := (collectionBankWriter{}).Write(context.Background(), path, collected, false, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{2, 4, 1}
	if !bytes.Equal(raw, want) {
		t.Fatalf("bank bytes=%v want=%v", raw, want)
	}
	digest := sha256.Sum256(want)
	if report.Path != path || report.SeedCount != 3 || report.SeedLength != 1 || report.Bytes != 3 || report.Partial || report.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("bank report=%+v", report)
	}

	bad := collected
	bad.Classes = append([]CollectedClass(nil), collected.Classes...)
	bad.Classes[0].Samples = append([]CollectedSample(nil), collected.Classes[0].Samples...)
	bad.Classes[0].Samples[1].Sequence = 2
	if _, err := (collectionBankWriter{}).Write(context.Background(), filepath.Join(directory, "bad.bin"), bad, false, false); err == nil {
		t.Fatal("non-increasing Class Sequence was accepted")
	}
}

func TestCollectionBankWriterWritesZeroBytePartialAndPreservesExistingOnRenameFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed_bank_2.bin")
	empty := CollectedProblem{SnapshotLength: 7}
	report, err := (collectionBankWriter{}).Write(context.Background(), path, empty, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.SeedCount != 0 || report.Bytes != 0 || report.SeedLength != 7 || !report.Partial || report.SHA256 != hex.EncodeToString(sha256.New().Sum(nil)) {
		t.Fatalf("empty bank report=%+v", report)
	}
	old := []byte("existing-bank")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	writer := collectionBankWriter{renamePath: func(oldPath, newPath string) error { return errors.New("rename blocked") }}
	if _, err := writer.Write(context.Background(), path, CollectedProblem{
		SnapshotLength: 1,
		Classes:        []CollectedClass{{Intent: ClassIntent{Name: "a"}, Samples: []CollectedSample{{ClassID: "a", Snapshot: []byte{9}}}}},
	}, false, false); err == nil {
		t.Fatal("rename failure was ignored")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, old) {
		t.Fatalf("existing bank changed after rename failure: %q", got)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".seed_bank-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files after rename failure=%v err=%v", matches, err)
	}
	replacement := CollectedProblem{
		SnapshotLength: 1,
		Classes:        []CollectedClass{{Intent: ClassIntent{Name: "a"}, Samples: []CollectedSample{{ClassID: "a", Snapshot: []byte{9}}}}},
	}
	if _, err := (collectionBankWriter{}).Write(context.Background(), path, replacement, false, false); err != nil {
		t.Fatalf("atomic replacement: %v", err)
	}
	got, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(got, []byte{9}) {
		t.Fatalf("replacement bytes=%v err=%v", got, err)
	}
}

func TestReplayDeduplicatesBankThenFreshCollectionFillsOnlyDeficit(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()

	const seed int64 = 884422
	fixture := collectionFixturePlan(seed, 1, 1, 1, 1)
	baseline, diagnostics, err := NewCollector(lab).Collect(context.Background(), fixture, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("baseline collection diagnostics=%+v err=%v", diagnostics, err)
	}
	sample := baseline.Classes[0].Samples[0]
	bankCollection := CollectedProblem{
		SnapshotLength: baseline.SnapshotLength,
		Classes: []CollectedClass{{Intent: fixture.Intent.Classes[0], Samples: []CollectedSample{
			{ClassID: sample.ClassID, Win: sample.Win, Snapshot: append([]byte(nil), sample.Snapshot...), Sequence: 0},
			{ClassID: sample.ClassID, Win: sample.Win, Snapshot: append([]byte(nil), sample.Snapshot...), Sequence: 1},
		}}},
	}
	directory := t.TempDir()
	bankPath := filepath.Join(directory, "duplicate.bin")
	if _, err := (collectionBankWriter{}).Write(context.Background(), bankPath, bankCollection, false, false); err != nil {
		t.Fatal(err)
	}

	plan := collectionFixturePlan(seed, 1, 2, 1, 1)
	plan.Plan.Collection.CollectedSeed = []string{bankPath}
	collector := NewCollector(lab)
	collector.WorkingDirectory = directory
	collected, diagnostics, err := collector.Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("replay collection diagnostics=%+v err=%v", diagnostics, err)
	}
	if collected.Spins != 1 || collected.Evidence.FreshSpins != 1 || collected.Evidence.ReplayAccepted != 1 || collected.Evidence.ReplayDuplicates != 1 || collected.Evidence.FreshAccepted != 1 {
		t.Fatalf("collection evidence=%+v spins=%d", collected.Evidence, collected.Spins)
	}
	if got := collected.Evidence.ReplaySources[0]; got.State != CollectionReplayUsed || got.EndReason != CollectionReplayEndExhausted || got.Records != 2 || got.Accepted != 1 || got.Duplicates != 1 {
		t.Fatalf("source report=%+v", got)
	}
	if len(collected.Classes[0].Samples) != 2 || collected.Classes[0].Samples[0].Sequence != 0 || collected.Classes[0].Samples[1].Sequence != 1 {
		t.Fatalf("collected samples=%+v", collected.Classes[0].Samples)
	}
	if !bytes.Equal(collected.Classes[0].Samples[0].Snapshot, collected.Classes[0].Samples[1].Snapshot) {
		t.Fatal("fresh collection unexpectedly deduplicated against replay")
	}
}

func TestReplaySkipsMissingThenStopsOpeningSourcesWhenQuotasFill(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	directory := t.TempDir()
	fixture := collectionFixturePlan(998877, 1, 2, 2, 1)
	baseline, diagnostics, err := NewCollector(lab).Collect(context.Background(), fixture, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("baseline collection diagnostics=%+v err=%v", diagnostics, err)
	}
	bankPath := filepath.Join(directory, "valid.bin")
	if _, err := (collectionBankWriter{}).Write(context.Background(), bankPath, baseline, false, false); err != nil {
		t.Fatal(err)
	}
	plan := collectionFixturePlan(998877, 1, 1, 1, 1)
	plan.Plan.Collection.CollectedSeed = []string{"missing.bin", "valid.bin", "never-opened.bin"}
	recorder := &recordingReporter{}
	collector := NewCollector(lab)
	collector.WorkingDirectory = directory
	collector.Reporter = recorder
	collected, diagnostics, err := collector.Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("replay collection diagnostics=%+v err=%v", diagnostics, err)
	}
	if collected.Spins != 0 || len(collected.Evidence.ReplaySources) != 3 {
		t.Fatalf("collection=%+v", collected)
	}
	first, second, third := collected.Evidence.ReplaySources[0], collected.Evidence.ReplaySources[1], collected.Evidence.ReplaySources[2]
	if first.State != CollectionReplaySkipped || first.EndReason != CollectionReplayEndSourceSkipped || first.Warning == "" {
		t.Fatalf("missing source report=%+v", first)
	}
	if second.State != CollectionReplayUsed || second.EndReason != CollectionReplayEndQuotasFull || second.Records != 1 || second.TotalRecords != 2 {
		t.Fatalf("quota source report=%+v", second)
	}
	if third.State != CollectionReplayNotOpened || third.EndReason != CollectionReplayEndQuotasFull || third.Warning != "" {
		t.Fatalf("unopened source report=%+v", third)
	}
	infoEvents := 0
	for _, event := range recorder.events {
		if event.Stage == "collection-replay" && event.State == "info" {
			infoEvents++
			if event.RemainingSources != 1 {
				t.Fatalf("remaining source event=%+v", event)
			}
		}
		if event.Stage == "collection-replay" && event.Path == third.ResolvedPath && event.State != "info" {
			t.Fatalf("unopened source received lifecycle event: %+v", event)
		}
	}
	if infoEvents != 1 {
		t.Fatalf("quota-full info events=%d want=1", infoEvents)
	}
}

func TestTunerSavesPartialCollectionBankBeforeCollectionInsufficient(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	disabled := false
	outputDirectory := t.TempDir()
	config := Config{
		Version: ConfigVersion,
		Plans: []RunPlan{{
			ID: "partial-bank", Target: Target{Game: 1, BetModes: []int{0}}, Engine: EngineIntentLPV2,
			Intent: "partial-bank", Seed: Int64Seed(77),
			Collection:         CollectionOptions{Workers: 1, BatchSize: 1, MaxSpins: 1},
			CandidateSelection: CandidateSelectionOptions{Evaluator: "none", MaxCandidates: 1},
			Output:             OutputOptions{Format: []OutputFormat{OutputOptimalArtifactV1}, Directory: outputDirectory},
		}},
		Intents: map[string]MathIntent{"partial-bank": {
			Overall: OverallIntent{CV: NumericRange{Min: 0, Max: 100}},
			Classes: []ClassIntent{{
				Name: "all", Weight: ClassWeightBase,
				Collect: CollectIntent{Samples: 2, WinRange: ClosedInterval{0, 1e9}},
				Design:  ClassDesign{Exp: 1, Median: ClosedInterval{0, 1e9}, Subjective: SubjectiveIntent{Intent: &disabled}},
			}},
		}},
		EngineOptions: DefaultEngineOptions(),
	}
	recorder := &recordingReporter{}
	tuner, err := NewTuner(config, lab, WithReporter(recorder))
	if err != nil {
		t.Fatal(err)
	}
	const savedAt = int64(1788754321)
	tuner.now = func() time.Time { return time.Unix(savedAt, 0) }
	result, err := tuner.Run(context.Background(), RunRequest{PlanID: "partial-bank"})
	if err != nil {
		t.Fatalf("Tuner.Run operational error: %v", err)
	}
	if result.Status != StatusInfeasibleSupport || len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != DiagnosticCollectionInsufficient {
		t.Fatalf("result status=%s diagnostics=%+v", result.Status, result.Diagnostics)
	}
	if result.Report.Collection == nil || !result.Report.Collection.Bank.Partial || result.Report.Collection.Bank.SeedCount != 1 {
		t.Fatalf("collection report=%+v", result.Report.Collection)
	}
	wantPath := filepath.Join(outputDirectory, "collected", "game_1", "mode_0", "seed_bank_1788754321_s1.bin")
	if result.Report.Collection.Bank.Path != wantPath {
		t.Fatalf("bank path=%q want=%q", result.Report.Collection.Bank.Path, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("partial bank was not saved: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(wantPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(wantPath) {
		t.Fatalf("Collection Bank directory contains latest/manifest/temp sidecars: %v", entries)
	}
	bankEvent := -1
	collectFinished := -1
	for i, event := range recorder.events {
		if event.Stage == "collection-bank" && event.State == "completed" {
			bankEvent = i
		}
		if event.Stage == "collect[mode=0]" && event.State == "failed" {
			collectFinished = i
		}
		if strings.HasPrefix(event.Stage, "prepare[") {
			t.Fatalf("Prepare ran after CollectionInsufficient: %+v", event)
		}
	}
	if bankEvent < 0 || collectFinished < 0 || bankEvent > collectFinished {
		t.Fatalf("event order does not save before terminal collection diagnostic: %+v", recorder.events)
	}
}
