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
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/zintix-labs/problab/sdk/sampler"
	"github.com/zintix-labs/problab/spec"
)

// TestMaterializeModeAliasRoundTrip proves materialization preserves deterministic
// sample order and reconstructs true outcome marginals rather than alias thresholds.
func TestMaterializeModeAliasRoundTrip(t *testing.T) {
	original := []MaterializedSample{
		{ClassID: "main", BucketIndex: 0, Win: 0, Snapshot: []byte{1, 2, 3}, Probability: 0.05},
		{ClassID: "main", BucketIndex: 1, Win: 1, Snapshot: []byte{4, 5, 6}, Probability: 0.15},
		{ClassID: "bonus", BucketIndex: 0, Win: 7, Snapshot: []byte{7, 8, 9}, Probability: 0.80},
	}

	mode, err := MaterializeMode(0, 40, original)
	if err != nil {
		t.Fatalf("MaterializeMode: %v", err)
	}
	if mode.BetMode != 0 || mode.BetUnit != 40 {
		t.Fatalf("unexpected mode identity: %+v", mode)
	}
	if got, want := mode.SeedBank, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}; !sameBytes(got, want) {
		t.Fatalf("seed bank did not preserve stable sample order: got=%v want=%v", got, want)
	}

	effective, err := EffectiveAliasProbabilities(mode.Picker)
	if err != nil {
		t.Fatalf("EffectiveAliasProbabilities: %v", err)
	}
	for i, want := range []float64{0.05, 0.15, 0.80} {
		if math.Abs(effective[i]-want) > 1e-12 {
			t.Fatalf("effective probability[%d]: got=%.17g want=%.17g", i, effective[i], want)
		}
	}
	if mode.Picker.Prob[0] == effective[0] {
		t.Fatalf("test precondition failed: alias threshold unexpectedly equals outcome probability")
	}

	// The materialized mode owns its snapshots; caller mutation cannot silently
	// change the seed bank or the per-sample replay identity.
	original[0].Snapshot[0] = 99
	if mode.Samples[0].Snapshot[0] != 1 || mode.SeedBank[0] != 1 {
		t.Fatalf("materialization retained caller-owned snapshot memory")
	}
}

// TestMaterializeModeRejectsInvalidSnapshotsAndWeights locks the artifact boundary
// against inputs that cannot form a replayable fixed-width seed bank or distribution.
func TestMaterializeModeRejectsInvalidSnapshotsAndWeights(t *testing.T) {
	valid := func() []MaterializedSample {
		return []MaterializedSample{
			{ClassID: "main", Win: 0, Snapshot: []byte{1, 2}, Probability: 0.4},
			{ClassID: "main", Win: 1, Snapshot: []byte{3, 4}, Probability: 0.6},
		}
	}

	tests := []struct {
		name    string
		mutate  func([]MaterializedSample) []MaterializedSample
		message string
	}{
		{
			name: "empty input",
			mutate: func([]MaterializedSample) []MaterializedSample {
				return nil
			},
			message: "must not be empty",
		},
		{
			name: "empty snapshot",
			mutate: func(samples []MaterializedSample) []MaterializedSample {
				samples[0].Snapshot = nil
				return samples
			},
			message: "snapshot must not be empty",
		},
		{
			name: "unequal snapshot",
			mutate: func(samples []MaterializedSample) []MaterializedSample {
				samples[1].Snapshot = []byte{3}
				return samples
			},
			message: "snapshot length mismatch",
		},
		{
			name: "negative probability",
			mutate: func(samples []MaterializedSample) []MaterializedSample {
				samples[0].Probability = -0.1
				return samples
			},
			message: "probability must be finite and >= 0",
		},
		{
			name: "nonfinite probability",
			mutate: func(samples []MaterializedSample) []MaterializedSample {
				samples[0].Probability = math.NaN()
				return samples
			},
			message: "probability must be finite and >= 0",
		},
		{
			name: "wrong sum",
			mutate: func(samples []MaterializedSample) []MaterializedSample {
				samples[0].Probability = 0.2
				return samples
			},
			message: "probability sum must equal 1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := MaterializeMode(0, 10, test.mutate(valid()))
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error=%v, want substring %q", err, test.message)
			}
		})
	}
}

// TestMaterializeModeNormalizesOnlyProvedEngineScaleResidual protects the
// boundary that failed with a multi-million-outcome production distribution.
// The exported API remains strict, while Tuner's internal path may accept the
// already-proved feasibility residual and must normalize it before aliasing.
func TestMaterializeModeNormalizesOnlyProvedEngineScaleResidual(t *testing.T) {
	samples := []MaterializedSample{
		{ClassID: "main", Win: 0, Snapshot: []byte{1, 2}, Probability: 0.4},
		{ClassID: "main", Win: 1, Snapshot: []byte{3, 4}, Probability: 0.6000000000060967},
	}

	if _, err := MaterializeMode(0, 10, samples); err == nil || !strings.Contains(err.Error(), "within 1.0e-12") {
		t.Fatalf("strict MaterializeMode error=%v, want rejection outside standalone artifact tolerance", err)
	}
	mode, err := materializeModeWithNormalizationTolerance(0, 10, samples, 1e-9)
	if err != nil {
		t.Fatalf("materialize proved near-one distribution: %v", err)
	}
	var normalized compensatedSum
	for _, sample := range mode.Samples {
		normalized.Add(sample.Probability)
	}
	if got := normalized.Value(); math.Abs(got-1) > artifactProbabilityTolerance {
		t.Fatalf("normalized sample sum=%.17g, want 1 within %.1e", got, artifactProbabilityTolerance)
	}
	for i := range mode.Samples {
		if math.Abs(mode.Samples[i].Probability-mode.EffectiveProbabilities[i]) > artifactProbabilityTolerance {
			t.Fatalf("sample[%d] probability %.17g != alias marginal %.17g", i, mode.Samples[i].Probability, mode.EffectiveProbabilities[i])
		}
	}
	if samples[1].Probability != 0.6000000000060967 {
		t.Fatalf("materialization mutated caller probability: %.17g", samples[1].Probability)
	}

	invalid := append([]MaterializedSample(nil), samples...)
	invalid[1].Probability = 0.600001
	if _, err := materializeModeWithNormalizationTolerance(0, 10, invalid, 1e-9); err == nil || !strings.Contains(err.Error(), "probability sum must equal 1") {
		t.Fatalf("materialize materially invalid distribution error=%v, want rejection", err)
	}
}

// TestCompensatedSumRetainsSmallProbabilityMass ensures large distributions do
// not lose low-order probability merely because a large term appeared first.
func TestCompensatedSumRetainsSmallProbabilityMass(t *testing.T) {
	var sum compensatedSum
	sum.Add(1)
	for range 10_000 {
		sum.Add(1e-19)
	}
	want := 1 + 1e-15
	if got := sum.Value(); got != want {
		t.Fatalf("compensated sum=%.17g want=%.17g", got, want)
	}
}

// TestAliasApproximationBoundsTotalVariation prevents a large table from
// hiding material aggregate drift behind individually acceptable row errors.
func TestAliasApproximationBoundsTotalVariation(t *testing.T) {
	expected := []float64{0.25, 0.25, 0.25, 0.25}
	acceptable := []float64{0.2504, 0.2504, 0.2496, 0.2496}
	if err := validateAliasApproximation(expected, acceptable, 1e-3); err != nil {
		t.Fatalf("bounded alias approximation rejected: %v", err)
	}
	material := []float64{0.2506, 0.2506, 0.2494, 0.2494}
	if err := validateAliasApproximation(expected, material, 1e-3); err == nil || !strings.Contains(err.Error(), "total_variation") {
		t.Fatalf("material aggregate alias drift error=%v, want total-variation rejection", err)
	}
}

// TestEffectiveAliasProbabilitiesRejectsMalformedAlias ensures marginal replay
// fails closed when an alias table is incomplete or contains invalid entries.
func TestEffectiveAliasProbabilitiesRejectsMalformedAlias(t *testing.T) {
	tests := []struct {
		name  string
		table *sampler.AliasTableF64
	}{
		{name: "nil", table: nil},
		{name: "empty", table: &sampler.AliasTableF64{}},
		{name: "prob length", table: &sampler.AliasTableF64{Size: 1, Aliases: []int{0}}},
		{name: "alias length", table: &sampler.AliasTableF64{Size: 1, Prob: []float64{1}}},
		{name: "bad probability", table: &sampler.AliasTableF64{Size: 1, Prob: []float64{1.1}, Aliases: []int{0}}},
		{name: "bad alias", table: &sampler.AliasTableF64{Size: 1, Prob: []float64{1}, Aliases: []int{1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := EffectiveAliasProbabilities(test.table); err == nil {
				t.Fatalf("EffectiveAliasProbabilities unexpectedly accepted %+v", test.table)
			}
		})
	}
}

// readTestPair returns the decoded gacha and the seed bank of one published pair.
func readTestPair(t *testing.T, published PublishedArtifact) (legacyTestGacha, []byte) {
	t.Helper()
	if len(published.Paths) != 2 {
		t.Fatalf("published paths=%v", published.Paths)
	}
	compressed, err := os.ReadFile(published.Paths[0])
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var gacha legacyTestGacha
	if err := json.Unmarshal(raw, &gacha); err != nil {
		t.Fatal(err)
	}
	bank, err := os.ReadFile(published.Paths[1])
	if err != nil {
		t.Fatal(err)
	}
	return gacha, bank
}

type legacyTestGacha struct {
	Picker  *sampler.AliasTableF64 `json:"picker"`
	SeedLen int                    `json:"seed_len"`
}

// Each mode is published on its own, beneath game_<gid>/mode_<mode>/, without
// waiting for sibling modes and without touching their publications.
func TestGachaArtifactWriterPublishesEachModeOnItsOwn(t *testing.T) {
	root := t.TempDir()
	writer := GachaArtifactWriter{Directory: root}
	mode0 := mustMaterializeTestMode(t, 0, 10, byte(10))
	mode1 := mustMaterializeTestMode(t, 1, 20, byte(20))

	first, err := writer.PublishMode(context.Background(), spec.GID(7), "pcg32/test-v1", []int{10, 20}, mode1)
	if err != nil {
		t.Fatalf("PublishMode(mode=1): %v", err)
	}
	if !slices.Equal(first.Formats, []OutputFormat{OutputOptimalGacha}) || first.ArtifactID == "" || first.Reused ||
		filepath.Dir(first.Directory) != filepath.Join(root, "game_7", "mode_1") {
		t.Fatalf("mode 1 publication=%+v", first)
	}
	before := snapshotArtifactDirectory(t, first.Directory)

	second, err := writer.PublishMode(context.Background(), spec.GID(7), "pcg32/test-v1", []int{10, 20}, mode0)
	if err != nil {
		t.Fatalf("PublishMode(mode=0): %v", err)
	}
	if filepath.Dir(second.Directory) != filepath.Join(root, "game_7", "mode_0") {
		t.Fatalf("mode 0 publication=%+v", second)
	}
	after := snapshotArtifactDirectory(t, first.Directory)
	if len(after) != len(before) {
		t.Fatal("publishing mode 0 changed mode 1's directory")
	}
	for name, data := range before {
		if !sameBytes(after[name], data) {
			t.Fatalf("publishing mode 0 changed mode 1's %s", name)
		}
	}
	for _, c := range []struct {
		published PublishedArtifact
		want      MaterializedMode
	}{{first, mode1}, {second, mode0}} {
		gacha, bank := readTestPair(t, c.published)
		if gacha.SeedLen != len(c.want.Samples[0].Snapshot) || !slices.Equal(gacha.Picker.Prob, c.want.Picker.Prob) ||
			!slices.Equal(gacha.Picker.Aliases, c.want.Picker.Aliases) || !sameBytes(bank, c.want.SeedBank) {
			t.Fatalf("mode %d pair differs from its materialization", c.want.BetMode)
		}
	}
}

// Publishing the same content again keeps the existing directory.
func TestGachaArtifactWriterReusesSameContent(t *testing.T) {
	writer := GachaArtifactWriter{Directory: t.TempDir()}
	mode := mustMaterializeTestMode(t, 0, 10, byte(10))
	first, err := writer.PublishMode(context.Background(), spec.GID(7), "pcg32/test-v1", []int{10}, mode)
	if err != nil {
		t.Fatal(err)
	}
	again, err := writer.PublishMode(context.Background(), spec.GID(7), "pcg32/test-v1", []int{10}, mode)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Reused || again.Directory != first.Directory || again.ArtifactID != first.ArtifactID {
		t.Fatalf("first=%+v again=%+v", first, again)
	}
}

func TestGachaArtifactWriterRejectsModeOutsideRuntimeLayout(t *testing.T) {
	_, err := (GachaArtifactWriter{Directory: t.TempDir()}).PublishMode(
		context.Background(), spec.GID(7), "pcg32/test-v1", []int{10}, mustMaterializeTestMode(t, 1, 10, byte(10)),
	)
	if err == nil || !strings.Contains(err.Error(), "outside runtime range") {
		t.Fatalf("PublishMode error=%v, want runtime-range rejection", err)
	}
}

// cancelAfterFirstErr reports no error to the first Err call and cancellation
// afterwards, standing in for a Run cancelled while its pair is being written.
type cancelAfterFirstErr struct {
	context.Context
	calls int
}

func (c *cancelAfterFirstErr) Err() error {
	c.calls++
	if c.calls == 1 {
		return nil
	}
	return context.Canceled
}

// Cancellation before writing publishes nothing; cancellation after writing has
// started is reported as success with a complete directory, which a rerun
// reuses.
func TestGachaArtifactWriterCancellation(t *testing.T) {
	root := t.TempDir()
	writer := GachaArtifactWriter{Directory: root}
	mode := mustMaterializeTestMode(t, 0, 10, byte(10))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := writer.PublishMode(cancelled, spec.GID(7), "pcg32/test-v1", []int{10}, mode); !errors.Is(err, context.Canceled) {
		t.Fatalf("PublishMode error=%v, want cancellation", err)
	}
	if _, err := os.Stat(filepath.Join(root, "game_7")); !os.IsNotExist(err) {
		t.Fatalf("a cancelled publication wrote output: %v", err)
	}

	late := &cancelAfterFirstErr{Context: context.Background()}
	published, err := writer.PublishMode(late, spec.GID(7), "pcg32/test-v1", []int{10}, mode)
	if err != nil {
		t.Fatalf("PublishMode after a late cancellation: %v", err)
	}
	if gacha, bank := readTestPair(t, published); gacha.Picker == nil || !sameBytes(bank, mode.SeedBank) {
		t.Fatal("the late-cancelled publication is incomplete")
	}
	again, err := writer.PublishMode(context.Background(), spec.GID(7), "pcg32/test-v1", []int{10}, mode)
	if err != nil || !again.Reused || again.Directory != published.Directory {
		t.Fatalf("rerun=%+v err=%v, want reuse", again, err)
	}
	entries, err := os.ReadDir(filepath.Dir(published.Directory))
	if err != nil || len(entries) != 1 {
		t.Fatalf("mode directory entries=%v err=%v", entryNames(entries), err)
	}
}

func TestOutputPublisherWritesGachaBeneathOutputDirectory(t *testing.T) {
	root := t.TempDir()
	publisher := newOutputPublisher(OutputOptions{Format: []OutputFormat{OutputRGSCollected, OutputOptimalGacha}, Directory: root})
	mode := mustMaterializeTestMode(t, 0, 10, byte(10))
	published, err := publisher.PublishMode(context.Background(), spec.GID(9), "pcg32/test-v1", []int{10}, mode)
	if err != nil {
		t.Fatalf("PublishMode: %v", err)
	}
	if filepath.Dir(published.Directory) != filepath.Join(root, "gacha", "game_9", "mode_0") {
		t.Fatalf("published=%+v", published)
	}
	_, err = newOutputPublisher(OutputOptions{Format: []OutputFormat{OutputRGSCollected}, Directory: root}).
		PublishMode(context.Background(), spec.GID(9), "pcg32/test-v1", []int{10}, mode)
	if err == nil || !strings.Contains(err.Error(), "no native output format") {
		t.Fatalf("publisher without a native format: err=%v", err)
	}
}

// mustMaterializeTestMode builds a minimal valid two-outcome mode and turns fixture
// construction errors into immediate test failures at the caller's location.
func mustMaterializeTestMode(t *testing.T, betMode, betUnit int, prefix byte) MaterializedMode {
	t.Helper()
	mode, err := MaterializeMode(betMode, betUnit, []MaterializedSample{
		{ClassID: "main", BucketIndex: 0, Win: 0, Snapshot: []byte{prefix, 1, 2}, Probability: 0.25},
		{ClassID: "bonus", BucketIndex: 0, Win: 2, Snapshot: []byte{prefix, 3, 4}, Probability: 0.75},
	})
	if err != nil {
		t.Fatalf("MaterializeMode: %v", err)
	}
	return mode
}

// sameBytes performs the exact byte comparison needed for seed-bank
// immutability assertions without introducing an unrelated assertion dependency.
func sameBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// snapshotArtifactDirectory captures the exact regular-file bytes beneath one
// publication directory, so tests can prove it was not rewritten.
func snapshotArtifactDirectory(t *testing.T, directory string) map[string][]byte {
	t.Helper()
	snapshot := make(map[string][]byte)
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("artifact snapshot encountered a non-regular entry")
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(relative)] = data
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot artifact directory %q: %v", directory, err)
	}
	return snapshot
}

// entryNames converts directory entries to stable diagnostic text so publication
// failures reveal any staging directory left behind.
func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i := range entries {
		names[i] = entries[i].Name()
	}
	return names
}
