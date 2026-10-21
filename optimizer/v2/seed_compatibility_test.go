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
	"io"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

// The fixture bank is an existing bank (16 records) collected with
// Int64Seed(4127483647). The values below are its known result and form a
// standing compatibility contract: the current code must restore and replay the
// existing bank to the same samples, and a fresh collection under the same
// integer seed must produce the same samples. Never re-capture them from current
// code; a mismatch means the bank format, seed encoding, RNG restore or game
// results changed.
const (
	fixtureBankPath     = "testdata/seed_bank_1700000000_s2.bin"
	fixtureBankRootSeed = 4127483647
	// fixtureBankSnapshotsDigest is the SHA-256 of the 16 sample snapshots concatenated in canonical order.
	fixtureBankSnapshotsDigest = "11c842fc692da2883e46d5b817750faa4f59447fd3b5ee81966a9a88a070eb12"
)

// fixtureBankTotalWins are the raw integer wins of the 16 samples in canonical order.
var fixtureBankTotalWins = []int64{20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 210, 10, 10, 0, 65}

type stableReplaySourceEvidence struct {
	State                  CollectionReplaySourceState `json:"state"`
	EndReason              CollectionReplayEndReason   `json:"end_reason"`
	TotalRecords           uint64                      `json:"total_records"`
	Records                uint64                      `json:"records"`
	Accepted               uint64                      `json:"accepted"`
	Duplicates             uint64                      `json:"duplicates"`
	Unmatched              uint64                      `json:"unmatched"`
	Rejected               uint64                      `json:"rejected"`
	StreamCursor           uint64                      `json:"stream_cursor"`
	StreamCursorRecognized bool                        `json:"stream_cursor_recognized"`
}

type stableReplayEvidence struct {
	ReplaySources    []stableReplaySourceEvidence `json:"replay_sources"`
	Classes          []CollectionClassEvidence    `json:"classes"`
	ReplayRecords    uint64                       `json:"replay_records"`
	ReplayDuplicates uint64                       `json:"replay_duplicates"`
	ReplayAccepted   uint64                       `json:"replay_accepted"`
	FreshSpins       uint64                       `json:"fresh_spins"`
	FreshAccepted    uint64                       `json:"fresh_accepted"`
}

func collectionSnapshotsDigest(t *testing.T, collected CollectedProblem) string {
	t.Helper()
	hash := sha256.New()
	if err := visitCanonicalCollectionSamples(collected, func(_, _ int, sample CollectedSample) error {
		_, err := hash.Write(sample.Snapshot)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func collectionTotalWins(t *testing.T, collected CollectedProblem) []int64 {
	t.Helper()
	var wins []int64
	if err := visitCanonicalCollectionSamples(collected, func(_, _ int, sample CollectedSample) error {
		wins = append(wins, sample.TotalWin)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return wins
}

// requireFixtureBankSamples asserts that collected holds exactly the fixture bank samples.
func requireFixtureBankSamples(t *testing.T, collected CollectedProblem) {
	t.Helper()
	if got := collectionSnapshotsDigest(t, collected); got != fixtureBankSnapshotsDigest {
		t.Fatalf("snapshot digest=%s want fixture bank %s", got, fixtureBankSnapshotsDigest)
	}
	if got := collectionTotalWins(t, collected); !reflect.DeepEqual(got, fixtureBankTotalWins) {
		t.Fatalf("total wins=%v want fixture bank %v", got, fixtureBankTotalWins)
	}
}

func fixtureBankClassEvidence(replay, fresh uint64) []CollectionClassEvidence {
	return []CollectionClassEvidence{{Name: "all_outcomes", Requested: 16, ReplayAccepted: replay, FreshAccepted: fresh, Accepted: 16}}
}

func projectStableReplayEvidence(evidence CollectionEvidence) stableReplayEvidence {
	sources := make([]stableReplaySourceEvidence, len(evidence.ReplaySources))
	for i, source := range evidence.ReplaySources {
		sources[i] = stableReplaySourceEvidence{
			State: source.State, EndReason: source.EndReason,
			TotalRecords: source.TotalRecords, Records: source.Records,
			Accepted: source.Accepted, Duplicates: source.Duplicates,
			Unmatched: source.Unmatched, Rejected: source.Rejected,
			StreamCursor: source.StreamCursor, StreamCursorRecognized: source.StreamCursorRecognized,
		}
	}
	return stableReplayEvidence{
		ReplaySources: sources,
		Classes:       append([]CollectionClassEvidence(nil), evidence.Classes...),
		ReplayRecords: evidence.ReplayRecords, ReplayDuplicates: evidence.ReplayDuplicates,
		ReplayAccepted: evidence.ReplayAccepted, FreshSpins: evidence.FreshSpins,
		FreshAccepted: evidence.FreshAccepted,
	}
}

func collectFixtureBankFresh(t *testing.T, probe bool) CollectedProblem {
	t.Helper()
	lab := newCollectionFactoryLab(t, core.Default())
	t.Cleanup(func() { _ = lab.Close() })
	plan := collectionFixturePlan(fixtureBankRootSeed, 2, 16, 16, 4)
	if probe {
		plan.Plan.ID = "probe"
		if _, diagnostic, err := validateRuntimeTarget(lab, plan); err != nil || diagnostic.StopsRun() {
			t.Fatalf("validateRuntimeTarget: diagnostic=%+v err=%v", diagnostic, err)
		}
	}
	collected, diagnostics, err := NewCollector(lab).Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("Collect: diagnostics=%+v err=%v", diagnostics, err)
	}
	return collected
}

// A fresh collection under the bank's integer root seed reproduces the bank samples.
func TestFreshCollectionReproducesFixtureBank(t *testing.T) {
	collected := collectFixtureBankFresh(t, false)
	requireFixtureBankSamples(t, collected)
	want := stableReplayEvidence{
		ReplaySources: []stableReplaySourceEvidence{},
		Classes:       fixtureBankClassEvidence(0, 16),
		FreshSpins:    16, FreshAccepted: 16,
	}
	if got := projectStableReplayEvidence(collected.Evidence); !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence=%+v want %+v", got, want)
	}
}

// The root seed probe before collection must not consume or perturb collection RNG.
func TestRootSeedProbeDoesNotPerturbCollection(t *testing.T) {
	requireFixtureBankSamples(t, collectFixtureBankFresh(t, true))
}

func replayFixtureBank(t *testing.T, seed SeedSpec) CollectedProblem {
	t.Helper()
	lab := newCollectionFactoryLab(t, core.Default())
	t.Cleanup(func() { _ = lab.Close() })
	workingDirectory, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	plan := collectionFixturePlan(fixtureBankRootSeed, 1, 16, 16, 4)
	plan.Plan.Seed = seed
	plan.Plan.Collection.CollectedSeed = []string{fixtureBankPath}
	collector := NewCollector(lab)
	collector.WorkingDirectory = workingDirectory
	collected, diagnostics, err := collector.Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("replay fixture bank: diagnostics=%+v err=%v", diagnostics, err)
	}
	return collected
}

// requireFixtureBankReplay asserts that all 16 records are accepted by replay, no
// fresh spins run, and the replayed samples are the known ones.
func requireFixtureBankReplay(t *testing.T, collected CollectedProblem) {
	t.Helper()
	requireFixtureBankSamples(t, collected)
	want := stableReplayEvidence{
		ReplaySources: []stableReplaySourceEvidence{{
			State: CollectionReplayUsed, EndReason: CollectionReplayEndQuotasFull,
			TotalRecords: 16, Records: 16, Accepted: 16,
			StreamCursor: 2, StreamCursorRecognized: true,
		}},
		Classes:       fixtureBankClassEvidence(16, 0),
		ReplayRecords: 16, ReplayAccepted: 16,
	}
	if got := projectStableReplayEvidence(collected.Evidence); !reflect.DeepEqual(got, want) {
		t.Fatalf("replay evidence=%+v want %+v", got, want)
	}
}

func TestFixtureBankReplayKnownResult(t *testing.T) {
	requireFixtureBankReplay(t, replayFixtureBank(t, Int64Seed(fixtureBankRootSeed)))
}

// Replaying existing records does not use the plan root seed: a UTF-8 seed
// yields the same result.
func TestExistingBankReplaysUnderUTF8Seed(t *testing.T) {
	seed, err := UTF8Seed("bank-root-is-not-used-for-restored-records")
	if err != nil {
		t.Fatal(err)
	}
	requireFixtureBankReplay(t, replayFixtureBank(t, seed))
}

type recordingNewSeedFactory struct {
	base core.PRNGFactory
	mu   sync.Mutex
	seen [][]byte
}

func (f *recordingNewSeedFactory) New(seed []byte) (core.PRNG, error) {
	f.mu.Lock()
	f.seen = append(f.seen, append([]byte(nil), seed...))
	f.mu.Unlock()
	return f.base.New(seed)
}

func (f *recordingNewSeedFactory) GenerateSeed(entropy io.Reader) ([]byte, error) {
	return f.base.GenerateSeed(entropy)
}

func (f *recordingNewSeedFactory) DeriveSeed(parent []byte, stream core.StreamID) ([]byte, error) {
	return f.base.DeriveSeed(parent, stream)
}

func (f *recordingNewSeedFactory) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = nil
}

func (f *recordingNewSeedFactory) seeds() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([][]byte, len(f.seen))
	for i := range f.seen {
		result[i] = append([]byte(nil), f.seen[i]...)
	}
	return result
}

func TestVerifyReplayMachineUsesSameSeedBytesAsCollection(t *testing.T) {
	factory := &recordingNewSeedFactory{base: core.Default()}
	lab := newCollectionFactoryLab(t, factory)
	defer func() { _ = lab.Close() }()
	seed, err := UTF8Seed("shared-root-material")
	if err != nil {
		t.Fatal(err)
	}

	fixtureMachine, err := lab.NewMachine(spec.GID(1), problab.MachineConfig{Seed: problab.SeedBytes(seed.Bytes()), Unoptimized: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixtureMachine.SnapshotCore()
	if err != nil {
		t.Fatal(err)
	}
	spin := fixtureMachine.SpinInternal(0)
	win := float64(spin.TotalWin) / float64(spin.Bet)
	factory.reset()

	plan := collectionFixturePlan(1, 1, 1, 1, 1)
	plan.Plan.Seed = seed
	if _, err := newReplayMachine(NewCollector(lab), plan); err != nil {
		t.Fatal(err)
	}
	compiled := runtimeReplayCompiledFixture(1, spin.Bet)
	compiled.Prepared.Plan.Plan.Seed = seed
	mode, err := MaterializeMode(0, spin.Bet, []MaterializedSample{{
		ClassID: "all_outcomes", BucketIndex: 0, Win: win, Snapshot: snapshot, Probability: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := replayMaterializedSnapshots(context.Background(), lab, compiled, mode); err != nil {
		t.Fatal(err)
	}

	seen := factory.seeds()
	if len(seen) != 2 {
		t.Fatalf("factory New calls=%d seeds=%x", len(seen), seen)
	}
	for i, got := range seen {
		if !bytes.Equal(got, seed.Bytes()) {
			t.Fatalf("New call %d seed=%x want=%x", i, got, seed.Bytes())
		}
	}
}
