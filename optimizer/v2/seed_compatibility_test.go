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
	"encoding/json"
	"io"
	"path/filepath"
	"sync"
	"testing"

	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

const (
	preSeedSpecCollectionSnapshotsGolden = "11c842fc692da2883e46d5b817750faa4f59447fd3b5ee81966a9a88a070eb12"
	preSeedSpecCollectionEvidenceGolden  = "b11eb29110bb6b5c3856e04bf56b7ec984933b395834b4f9f92513112d6d0c26"
	preSeedSpecReplayEvidenceGolden      = "2bf29b8780452b8caa744262c35b97d93696f5bfee30248d06aed99d834f8183"
	preSeedSpecBankFixture               = "testdata/seed_bank_1700000000_s2.bin"
)

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

func stableJSONDigest(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
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

func TestCollectionDigestMatchesPreChangeGolden(t *testing.T) {
	lab := newCollectionFactoryLab(t, core.Default())
	defer func() { _ = lab.Close() }()
	plan := collectionFixturePlan(4127483647, 2, 16, 16, 4)

	collected, diagnostics, err := NewCollector(lab).Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("Collect: diagnostics=%+v err=%v", diagnostics, err)
	}
	if got := collectionSnapshotsDigest(t, collected); got != preSeedSpecCollectionSnapshotsGolden {
		t.Fatalf("snapshot digest=%s want pre-change golden %s", got, preSeedSpecCollectionSnapshotsGolden)
	}
	if got := stableJSONDigest(t, collected.Evidence); got != preSeedSpecCollectionEvidenceGolden {
		t.Fatalf("evidence digest=%s want pre-change golden %s", got, preSeedSpecCollectionEvidenceGolden)
	}
}

func TestRootSeedProbeDoesNotPerturbCollection(t *testing.T) {
	lab := newCollectionFactoryLab(t, core.Default())
	defer func() { _ = lab.Close() }()
	plan := collectionFixturePlan(4127483647, 2, 16, 16, 4)
	plan.Plan.ID = "probe-golden"
	if _, diagnostic, err := validateRuntimeTarget(lab, plan); err != nil || diagnostic.StopsRun() {
		t.Fatalf("validateRuntimeTarget: diagnostic=%+v err=%v", diagnostic, err)
	}
	collected, diagnostics, err := NewCollector(lab).Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("Collect after probe: diagnostics=%+v err=%v", diagnostics, err)
	}
	if got := collectionSnapshotsDigest(t, collected); got != preSeedSpecCollectionSnapshotsGolden {
		t.Fatalf("post-probe snapshot digest=%s want %s", got, preSeedSpecCollectionSnapshotsGolden)
	}
}

func replayPreSeedSpecFixture(t *testing.T, seed SeedSpec) CollectedProblem {
	t.Helper()
	lab := newCollectionFactoryLab(t, core.Default())
	t.Cleanup(func() { _ = lab.Close() })
	workingDirectory, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	plan := collectionFixturePlan(4127483647, 1, 16, 16, 4)
	plan.Plan.Seed = seed
	plan.Plan.Collection.CollectedSeed = []string{preSeedSpecBankFixture}
	collector := NewCollector(lab)
	collector.WorkingDirectory = workingDirectory
	collected, diagnostics, err := collector.Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("replay fixture: diagnostics=%+v err=%v", diagnostics, err)
	}
	return collected
}

func TestFixtureBankReplayMatchesPreChangeGolden(t *testing.T) {
	collected := replayPreSeedSpecFixture(t, Int64Seed(4127483647))
	if got := collectionSnapshotsDigest(t, collected); got != preSeedSpecCollectionSnapshotsGolden {
		t.Fatalf("replay snapshot digest=%s want pre-change golden %s", got, preSeedSpecCollectionSnapshotsGolden)
	}
	if got := stableJSONDigest(t, projectStableReplayEvidence(collected.Evidence)); got != preSeedSpecReplayEvidenceGolden {
		t.Fatalf("stable replay evidence digest=%s want pre-change golden %s", got, preSeedSpecReplayEvidenceGolden)
	}
}

func TestExistingBankReplaysUnderUTF8Seed(t *testing.T) {
	seed, err := UTF8Seed("bank-root-is-not-used-for-restored-records")
	if err != nil {
		t.Fatal(err)
	}
	collected := replayPreSeedSpecFixture(t, seed)
	if collected.Evidence.ReplayAccepted != 16 || collected.Evidence.FreshAccepted != 0 {
		t.Fatalf("replay evidence=%+v", collected.Evidence)
	}
	if got := collectionSnapshotsDigest(t, collected); got != preSeedSpecCollectionSnapshotsGolden {
		t.Fatalf("UTF-8 replay snapshot digest=%s want G3 %s", got, preSeedSpecCollectionSnapshotsGolden)
	}
	if got := stableJSONDigest(t, projectStableReplayEvidence(collected.Evidence)); got != preSeedSpecReplayEvidenceGolden {
		t.Fatalf("UTF-8 stable replay evidence digest=%s want G3 %s", got, preSeedSpecReplayEvidenceGolden)
	}
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

	fixtureMachine, err := lab.NewUnoptimizedMachineWithSeedBytes(spec.GID(1), seed.Bytes(), true)
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
