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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/internal/optimalrt"
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

// TestFileArtifactWriterPublishesVerifiedManifest verifies an atomic publication
// contains every mode payload and a self-consistent, digest-checked manifest.
func TestFileArtifactWriterPublishesVerifiedManifest(t *testing.T) {
	root := t.TempDir()
	mode0 := mustMaterializeTestMode(t, 0, 10, byte(10))
	mode1 := mustMaterializeTestMode(t, 1, 20, byte(20))
	writer := FileArtifactWriter{Directory: root}

	if _, err := writer.PublishMode(context.Background(), spec.GID(7), "pcg32/test-v1", []int{10, 20}, mode0); err != nil {
		t.Fatalf("PublishMode(mode=0): %v", err)
	}
	published, err := writer.PublishMode(context.Background(), spec.GID(7), "pcg32/test-v1", []int{10, 20}, mode1)
	if err != nil {
		t.Fatalf("PublishMode(mode=1): %v", err)
	}
	target := filepath.Join(root, "game_7")
	if published.ManifestPath != filepath.Join(target, "manifest.json") {
		t.Fatalf("manifest path=%q", published.ManifestPath)
	}
	if len(published.Paths) != 8 {
		t.Fatalf("published sidecar/payload path count=%d want=8", len(published.Paths))
	}
	for _, path := range published.Paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("published payload %q: %v", path, err)
		}
	}

	raw, err := os.ReadFile(published.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest optimalrt.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("validate manifest: %v", err)
	}
	if manifest.ArtifactID != published.ArtifactID || len(manifest.Modes) != 2 {
		t.Fatalf("manifest identity/modes mismatch: %+v published=%+v", manifest, published)
	}
	for modeIndex, mode := range manifest.Modes {
		if mode.BetUnit != (modeIndex+1)*10 || mode.Size != 2 || mode.SeedLen != 3 || mode.SeedCount != 2 {
			t.Fatalf("unexpected mode[%d] descriptor: %+v", modeIndex, mode)
		}
		for _, ref := range []optimalrt.FileRef{mode.Prob, mode.Aliases, mode.SeedBank} {
			data, err := os.ReadFile(filepath.Join(target, ref.Path))
			if err != nil {
				t.Fatalf("read %q: %v", ref.Path, err)
			}
			digest := sha256.Sum256(data)
			if int64(len(data)) != ref.Size || hex.EncodeToString(digest[:]) != ref.SHA256 {
				t.Fatalf("manifest reference does not match %q", ref.Path)
			}
		}
		descriptorBytes, err := os.ReadFile(filepath.Join(target, "mode_"+string(rune('0'+modeIndex))+".json"))
		if err != nil {
			t.Fatalf("read mode[%d] descriptor: %v", modeIndex, err)
		}
		var descriptor optimalrt.ManifestMode
		if err := json.Unmarshal(descriptorBytes, &descriptor); err != nil {
			t.Fatalf("parse mode[%d] descriptor: %v", modeIndex, err)
		}
		if descriptor != mode {
			t.Fatalf("mode[%d] descriptor differs from manifest: got=%+v want=%+v", modeIndex, descriptor, mode)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if !slices.Equal(entryNames(entries), []string{"game_7", "game_7.pending"}) {
		t.Fatalf("unexpected publication root entries: %v", entryNames(entries))
	}
}

// TestFileArtifactWriterStagesOneModeAndPublishesOnlyWhenComplete locks the
// cross-Run contract requested by cmd/opt: modes may arrive in any order, the
// first successful mode is durable without exposing a manifest, and the final
// missing mode publishes one complete, runtime-loadable Artifact v1 bundle.
func TestFileArtifactWriterStagesOneModeAndPublishesOnlyWhenComplete(t *testing.T) {
	root := t.TempDir()
	writer := FileArtifactWriter{Directory: root}
	betUnits := []int{10, 20}

	mode1 := mustMaterializeTestMode(t, 1, 20, byte(20))
	pending, err := writer.PublishMode(context.Background(), spec.GID(7), "pcg32/test-v1", betUnits, mode1)
	if err != nil {
		t.Fatalf("PublishMode(mode=1): %v", err)
	}
	if pending.Complete || pending.ManifestPath != "" || pending.ArtifactID != "" {
		t.Fatalf("first mode unexpectedly published a manifest: %+v", pending)
	}
	if !slices.Equal(pending.StagedModes, []int{1}) || !slices.Equal(pending.MissingModes, []int{0}) {
		t.Fatalf("first mode readiness = staged %v missing %v", pending.StagedModes, pending.MissingModes)
	}
	if len(pending.Paths) != 4 {
		t.Fatalf("pending mode path count=%d want=4", len(pending.Paths))
	}
	for _, path := range pending.Paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("pending path %q: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "game_7")); !os.IsNotExist(err) {
		t.Fatalf("runtime-visible game directory exists before all modes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pending.StagingDirectory, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("pending directory contains a manifest: %v", err)
	}

	mode0 := mustMaterializeTestMode(t, 0, 10, byte(10))
	published, err := writer.PublishMode(context.Background(), spec.GID(7), "pcg32/test-v1", betUnits, mode0)
	if err != nil {
		t.Fatalf("PublishMode(mode=0): %v", err)
	}
	if !published.Complete || published.ManifestPath == "" || published.ArtifactID == "" {
		t.Fatalf("complete mode set did not publish a manifest: %+v", published)
	}
	if !slices.Equal(published.StagedModes, []int{0, 1}) || len(published.MissingModes) != 0 {
		t.Fatalf("complete readiness = staged %v missing %v", published.StagedModes, published.MissingModes)
	}
	if len(published.Paths) != 8 {
		t.Fatalf("published payload path count=%d want=8", len(published.Paths))
	}
	manifestBytes, err := os.ReadFile(published.ManifestPath)
	if err != nil {
		t.Fatalf("read published manifest: %v", err)
	}
	var manifest optimalrt.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("parse published manifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("validate published manifest: %v", err)
	}
	if len(manifest.Modes) != 2 || manifest.Modes[0].BetUnit != 10 || manifest.Modes[1].BetUnit != 20 {
		t.Fatalf("published modes are not in runtime order: %+v", manifest.Modes)
	}
	if _, err := os.Stat(filepath.Join(published.StagingDirectory, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("retained pending directory gained a manifest: %v", err)
	}
}

// TestFileArtifactWriterRepublishesOneChangedModeWithVerifiedSiblings proves
// each mode may be produced by a different RunPlan. Replacing mode 0 leaves the
// previously verified mode 1 bytes intact and immediately publishes a new full
// manifest because the pending set remains complete across Runs.
func TestFileArtifactWriterRepublishesOneChangedModeWithVerifiedSiblings(t *testing.T) {
	root := t.TempDir()
	writer := FileArtifactWriter{Directory: root}
	betUnits := []int{10, 20}
	if _, err := writer.PublishMode(context.Background(), spec.GID(9), "pcg32/test-v1", betUnits, mustMaterializeTestMode(t, 0, 10, byte(1))); err != nil {
		t.Fatalf("stage initial mode 0: %v", err)
	}
	first, err := writer.PublishMode(context.Background(), spec.GID(9), "pcg32/test-v1", betUnits, mustMaterializeTestMode(t, 1, 20, byte(2)))
	if err != nil {
		t.Fatalf("stage initial mode 1: %v", err)
	}
	mode1Before, err := os.ReadFile(filepath.Join(root, "game_9", "seed_bank_1.bin"))
	if err != nil {
		t.Fatalf("read initial mode 1: %v", err)
	}

	second, err := writer.PublishMode(context.Background(), spec.GID(9), "pcg32/test-v1", betUnits, mustMaterializeTestMode(t, 0, 10, byte(9)))
	if err != nil {
		t.Fatalf("replace mode 0: %v", err)
	}
	if !second.Complete || second.ArtifactID == first.ArtifactID {
		t.Fatalf("single-mode replacement did not publish a new complete bundle: first=%+v second=%+v", first, second)
	}
	mode1After, err := os.ReadFile(filepath.Join(root, "game_9", "seed_bank_1.bin"))
	if err != nil {
		t.Fatalf("read replacement mode 1: %v", err)
	}
	if !sameBytes(mode1After, mode1Before) {
		t.Fatal("replacing mode 0 changed the staged mode 1 payload")
	}
}

// TestFileArtifactWriterRejectsIncompatiblePendingRuntimeMetadata protects the
// final manifest from combining modes produced for different runtime snapshot
// formats or bet-unit layouts. Per-mode optimizer settings may differ, but
// these runtime representation facts may not.
func TestFileArtifactWriterRejectsIncompatiblePendingRuntimeMetadata(t *testing.T) {
	root := t.TempDir()
	writer := FileArtifactWriter{Directory: root}
	if _, err := writer.PublishMode(
		context.Background(), spec.GID(11), "pcg32/test-v1", []int{10, 20}, mustMaterializeTestMode(t, 0, 10, byte(1)),
	); err != nil {
		t.Fatalf("stage initial mode: %v", err)
	}

	_, err := writer.PublishMode(
		context.Background(), spec.GID(11), "pcg32/test-v2", []int{10, 20}, mustMaterializeTestMode(t, 1, 20, byte(2)),
	)
	if err == nil || !strings.Contains(err.Error(), "pending directory") || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("snapshot-format mismatch error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "game_11")); !os.IsNotExist(err) {
		t.Fatalf("incompatible mode unexpectedly published a bundle: %v", err)
	}
}

// TestFileArtifactWriterRollsBackFailedReplacement injects failure precisely
// after the old target has moved to backup. PublishMode must restore the complete
// old directory while retaining the newly staged mode for an explicit retry.
func TestFileArtifactWriterRollsBackFailedReplacement(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "game_11")
	initialWriter := FileArtifactWriter{Directory: root}
	initial, err := initialWriter.PublishMode(
		context.Background(), spec.GID(11), "pcg32/original", []int{10}, mustMaterializeTestMode(t, 0, 10, byte(1)),
	)
	if err != nil {
		t.Fatalf("initial Publish: %v", err)
	}
	before := snapshotArtifactDirectory(t, target)

	injected := false
	failingWriter := FileArtifactWriter{
		Directory: root,
		renamePath: func(oldPath, newPath string) error {
			if !injected && oldPath != target && newPath == target {
				injected = true
				return errors.New("injected install failure")
			}
			return os.Rename(oldPath, newPath)
		},
	}
	_, err = failingWriter.PublishMode(
		context.Background(), spec.GID(11), "pcg32/original", []int{10}, mustMaterializeTestMode(t, 0, 10, byte(8)),
	)
	if err == nil || !strings.Contains(err.Error(), "current bundle restored") {
		t.Fatalf("replacement error=%v, want successful rollback diagnostic", err)
	}
	if !injected {
		t.Fatal("replacement did not reach injected install failure")
	}
	after := snapshotArtifactDirectory(t, target)
	if len(after) != len(before) {
		t.Fatalf("rolled-back file count=%d want=%d", len(after), len(before))
	}
	for name, want := range before {
		if got, ok := after[name]; !ok || !sameBytes(got, want) {
			t.Fatalf("rolled-back file %q changed or disappeared", name)
		}
	}
	retained, err := os.ReadFile(initial.ManifestPath)
	if err != nil || !sameBytes(retained, before["manifest.json"]) {
		t.Fatalf("old manifest was not restored exactly: err=%v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read publication root: %v", err)
	}
	if !slices.Equal(entryNames(entries), []string{"game_11", "game_11.pending"}) {
		t.Fatalf("rollback leaked transaction temporaries or lock entries: %v", entryNames(entries))
	}
}

// TestFileArtifactWriterRejectsModeOutsideRuntimeLayout ensures a single mode
// cannot be staged under an index that the game's complete manifest cannot use.
func TestFileArtifactWriterRejectsModeOutsideRuntimeLayout(t *testing.T) {
	mode := mustMaterializeTestMode(t, 1, 10, byte(1))
	_, err := (FileArtifactWriter{Directory: t.TempDir()}).PublishMode(
		context.Background(), spec.GID(1), "pcg32/test-v1", []int{10}, mode,
	)
	if err == nil || !strings.Contains(err.Error(), "outside runtime range") {
		t.Fatalf("PublishMode error=%v, want runtime-range rejection", err)
	}
}

func TestGachaArtifactWriterPublishesLegacyRuntimePackage(t *testing.T) {
	root := t.TempDir()
	writer := GachaArtifactWriter{Directory: root}
	mode0 := mustMaterializeTestMode(t, 0, 10, byte(10))
	mode1 := mustMaterializeTestMode(t, 1, 20, byte(20))

	pending, err := writer.PublishMode(context.Background(), spec.GID(7), "pcg32/test-v1", []int{10, 20}, mode1)
	if err != nil {
		t.Fatalf("PublishMode(mode=1): %v", err)
	}
	if pending.Complete || !slices.Equal(pending.Formats, []OutputFormat{OutputOptimalGacha}) ||
		!slices.Equal(pending.StagedModes, []int{1}) || !slices.Equal(pending.MissingModes, []int{0}) {
		t.Fatalf("pending legacy package=%+v", pending)
	}
	if len(pending.Paths) != 2 {
		t.Fatalf("pending paths=%v", pending.Paths)
	}

	published, err := writer.PublishMode(context.Background(), spec.GID(7), "pcg32/test-v1", []int{10, 20}, mode0)
	if err != nil {
		t.Fatalf("PublishMode(mode=0): %v", err)
	}
	if !published.Complete || published.ManifestPath != "" || published.ArtifactID == "" ||
		!slices.Equal(published.StagedModes, []int{0, 1}) || len(published.MissingModes) != 0 {
		t.Fatalf("published legacy package=%+v", published)
	}
	target := filepath.Join(root, "game_7")
	if len(published.Paths) != 4 {
		t.Fatalf("published paths=%v", published.Paths)
	}
	for _, path := range published.Paths {
		if !strings.HasPrefix(path, target+string(os.PathSeparator)) {
			t.Fatalf("published path %q is outside %q", path, target)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("published path %q: %v", path, err)
		}
	}
	for modeIndex, want := range []MaterializedMode{mode0, mode1} {
		got, err := verifyLegacyModeDirectory(target, modeIndex, false)
		if err != nil {
			t.Fatalf("verify legacy mode %d: %v", modeIndex, err)
		}
		if got.SeedLen != len(want.Samples[0].Snapshot) || got.Picker.Size != want.Picker.Size ||
			!slices.Equal(got.Picker.Prob, want.Picker.Prob) || !slices.Equal(got.Picker.Aliases, want.Picker.Aliases) {
			t.Fatalf("legacy mode %d picker differs: got=%+v want=%+v", modeIndex, got, want.Picker)
		}
		bank, err := os.ReadFile(filepath.Join(target, legacyModeFileNames(modeIndex).seedBank))
		if err != nil || !sameBytes(bank, want.SeedBank) {
			t.Fatalf("legacy mode %d seed bank mismatch: err=%v", modeIndex, err)
		}
	}
}

func TestOutputPublisherWritesRequestedFormatsIntoSeparatePackages(t *testing.T) {
	root := t.TempDir()
	publisher := newOutputPublisher(OutputOptions{
		Format:    []OutputFormat{OutputOptimalGacha, OutputOptimalArtifactV1},
		Directory: root,
	})
	mode := mustMaterializeTestMode(t, 0, 10, byte(10))
	published, err := publisher.PublishMode(context.Background(), spec.GID(9), "pcg32/test-v1", []int{10}, mode)
	if err != nil {
		t.Fatalf("PublishMode: %v", err)
	}
	if !published.Complete || !slices.Equal(published.Formats, []OutputFormat{OutputOptimalGacha, OutputOptimalArtifactV1}) {
		t.Fatalf("published=%+v", published)
	}
	wantManifest := filepath.Join(root, "artifact_v1", "game_9", "manifest.json")
	if published.ManifestPath != wantManifest {
		t.Fatalf("manifest path=%q want=%q", published.ManifestPath, wantManifest)
	}
	if len(published.Paths) != 6 {
		t.Fatalf("payload paths=%v, want 2 legacy + 4 Artifact v1", published.Paths)
	}
	for _, path := range []string{
		wantManifest,
		filepath.Join(root, "gacha", "game_9", "gacha_0.json.zst"),
		filepath.Join(root, "gacha", "game_9", "seed_bank_0.bin"),
		filepath.Join(root, "artifact_v1", "game_9", "prob_0.bin"),
		filepath.Join(root, "artifact_v1", "game_9", "aliases_0.bin"),
		filepath.Join(root, "artifact_v1", "game_9", "seed_bank_0.bin"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected output %q: %v", path, err)
		}
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

// sameBytes performs the exact byte comparison needed for seed-bank and manifest
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
// published bundle. Rollback tests compare these snapshots to prove that a
// failed replacement restores content, not merely a directory with the same
// name or a parseable manifest.
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
// failures reveal any staging directory that escaped atomic cleanup.
func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i := range entries {
		names[i] = entries[i].Name()
	}
	return names
}
