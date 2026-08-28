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
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/sdk/sampler"
	"github.com/zintix-labs/problab/spec"
)

const artifactProbabilityTolerance = 1e-12

// MaterializedSample is one immutable row in the runtime outcome table.
// Probability is the outcome probability established by the mathematical
// model, not the threshold stored in an alias-table column. Snapshot contains
// the exact pre-spin PRNG state that reproduces this outcome.
type MaterializedSample struct {
	ClassID     string
	BucketIndex int
	Win         float64
	Snapshot    []byte
	Probability float64
}

// MaterializedMode is the verified in-memory representation of one bet mode.
// Samples, Picker, and SeedBank share the same stable positional order.
// EffectiveProbabilities records the actual outcome probabilities reconstructed
// from Picker, which intentionally differ from Picker.Prob for aliased columns.
type MaterializedMode struct {
	BetMode                int
	BetUnit                int
	Samples                []MaterializedSample
	Picker                 *sampler.AliasTableF64
	SeedBank               []byte
	EffectiveProbabilities []float64
}

// MaterializeMode converts weighted samples into the exact structures consumed
// by the production runtime. It validates the mathematical boundary before
// calling the alias builder, preserves caller order, owns copies of all
// snapshots, and verifies the alias table by reconstructing outcome probability.
// Reconstructing probability is essential: Picker.Prob is a per-column branch
// threshold and must never be interpreted as an outcome's marginal probability.
func MaterializeMode(betMode, betUnit int, samples []MaterializedSample) (MaterializedMode, error) {
	return materializeModeWithNormalizationTolerance(betMode, betUnit, samples, artifactProbabilityTolerance)
}

// materializeModeWithNormalizationTolerance is the model-to-artifact boundary
// used by Tuner. A backend-approved solution can differ from exact
// normalization by the configured semantic feasibility tolerance, and
// expanding c*p/n over millions of outcomes introduces another small floating
// summation residual. This helper accepts only a near-one input within that
// explicit tolerance, deterministically normalizes it once, and permits only
// an alias approximation whose worst outcome drift and total variation both
// remain within the same solver-proved allowance. The reconstructed runtime
// marginals then become the artifact truth and are replayed against every hard
// semantic constraint before publication.
//
// Keeping this separate from exported MaterializeMode preserves the latter's
// strict standalone contract: callers without a proved optimizer witness do
// not receive the Engine's wider model-boundary allowance.
func materializeModeWithNormalizationTolerance(
	betMode, betUnit int,
	samples []MaterializedSample,
	normalizationTolerance float64,
) (MaterializedMode, error) {
	if betMode < 0 {
		return MaterializedMode{}, fmt.Errorf("materialize mode: bet mode must be >= 0: %d", betMode)
	}
	if betUnit <= 0 {
		return MaterializedMode{}, fmt.Errorf("materialize mode %d: bet unit must be > 0", betMode)
	}
	if len(samples) == 0 {
		return MaterializedMode{}, fmt.Errorf("materialize mode %d: samples must not be empty", betMode)
	}
	if !isFinite(normalizationTolerance) || normalizationTolerance <= 0 {
		return MaterializedMode{}, fmt.Errorf("materialize mode %d: normalization tolerance must be finite and > 0", betMode)
	}

	seedLen := len(samples[0].Snapshot)
	if seedLen == 0 {
		return MaterializedMode{}, fmt.Errorf("materialize mode %d: sample[0] snapshot must not be empty", betMode)
	}
	if len(samples) > int(^uint(0)>>1)/seedLen {
		return MaterializedMode{}, fmt.Errorf("materialize mode %d: seed bank dimensions overflow", betMode)
	}

	owned := make([]MaterializedSample, len(samples))
	weights := make([]float64, len(samples))
	seedBank := make([]byte, 0, len(samples)*seedLen)
	var probabilitySum compensatedSum
	for i, sample := range samples {
		if strings.TrimSpace(sample.ClassID) == "" {
			return MaterializedMode{}, fmt.Errorf("materialize mode %d: sample[%d] class id is required", betMode, i)
		}
		if !isFinite(sample.Win) || sample.Win < 0 {
			return MaterializedMode{}, fmt.Errorf("materialize mode %d: sample[%d] win must be finite and >= 0", betMode, i)
		}
		if len(sample.Snapshot) == 0 {
			return MaterializedMode{}, fmt.Errorf("materialize mode %d: sample[%d] snapshot must not be empty", betMode, i)
		}
		if len(sample.Snapshot) != seedLen {
			return MaterializedMode{}, fmt.Errorf(
				"materialize mode %d: sample[%d] snapshot length mismatch: got=%d want=%d",
				betMode, i, len(sample.Snapshot), seedLen,
			)
		}
		if !isFinite(sample.Probability) || sample.Probability < 0 {
			return MaterializedMode{}, fmt.Errorf("materialize mode %d: sample[%d] probability must be finite and >= 0", betMode, i)
		}

		owned[i] = sample
		owned[i].Snapshot = append([]byte(nil), sample.Snapshot...)
		weights[i] = sample.Probability
		seedBank = append(seedBank, sample.Snapshot...)
		probabilitySum.Add(sample.Probability)
	}
	sum := probabilitySum.Value()
	if !isFinite(sum) || math.Abs(sum-1) > scaledTolerance(normalizationTolerance, sum, 1) {
		return MaterializedMode{}, fmt.Errorf("materialize mode %d: probability sum must equal 1 within %.1e: got=%.17g", betMode, normalizationTolerance, sum)
	}
	if sum <= 0 {
		return MaterializedMode{}, fmt.Errorf("materialize mode %d: probability sum must be > 0", betMode)
	}

	// Normalize an accepted near-one sum once so that both the model-facing
	// samples and the alias verification use precisely the same probability.
	for i := range weights {
		weights[i] /= sum
		owned[i].Probability = weights[i]
	}
	picker := sampler.BuildAliasTableF64(weights)
	effective, err := EffectiveAliasProbabilities(picker)
	if err != nil {
		return MaterializedMode{}, fmt.Errorf("materialize mode %d: verify alias table: %w", betMode, err)
	}
	if err := validateAliasApproximation(weights, effective, normalizationTolerance); err != nil {
		return MaterializedMode{}, fmt.Errorf("materialize mode %d: %w", betMode, err)
	}
	// The alias table is the runtime distribution and therefore the artifact
	// truth. Once its bounded approximation has been accepted, store the
	// reconstructed marginals in the redundant sample annotations so later
	// publication checks never compare runtime truth with pre-alias inputs.
	for i := range owned {
		owned[i].Probability = effective[i]
	}

	return MaterializedMode{
		BetMode:                betMode,
		BetUnit:                betUnit,
		Samples:                owned,
		Picker:                 picker,
		SeedBank:               seedBank,
		EffectiveProbabilities: effective,
	}, nil
}

// validateAliasApproximation bounds both the worst single-outcome drift and
// total variation distance. A per-item check alone is unsafe for a table with
// millions of rows because many individually tiny errors could add up to a
// material distribution change. The caller subsequently replays every bucket,
// Class, moment, and hard row from the accepted effective probabilities.
func validateAliasApproximation(expected, effective []float64, tolerance float64) error {
	if len(expected) != len(effective) {
		return fmt.Errorf("alias outcome count mismatch: got=%d want=%d", len(effective), len(expected))
	}
	if !isFinite(tolerance) || tolerance <= 0 {
		return fmt.Errorf("alias approximation tolerance must be finite and > 0")
	}
	maxDifference := 0.0
	maxIndex := -1
	var absoluteDifferenceSum compensatedSum
	for i := range expected {
		if !isFinite(expected[i]) || !isFinite(effective[i]) || expected[i] < 0 || effective[i] < 0 {
			return fmt.Errorf("alias outcome probability at sample[%d] must be finite and nonnegative", i)
		}
		difference := math.Abs(effective[i] - expected[i])
		absoluteDifferenceSum.Add(difference)
		if difference > maxDifference {
			maxDifference = difference
			maxIndex = i
		}
	}
	totalVariation := 0.5 * absoluteDifferenceSum.Value()
	allowance := scaledTolerance(tolerance, 1)
	if maxDifference > allowance || totalVariation > allowance {
		return fmt.Errorf(
			"alias approximation exceeds %.1e: max_difference=%.17g at sample[%d], total_variation=%.17g",
			tolerance,
			maxDifference,
			maxIndex,
			totalVariation,
		)
	}
	return nil
}

// EffectiveAliasProbabilities reconstructs each outcome's marginal probability
// from Vose alias-table branch thresholds. A uniformly selected column i emits
// i with Prob[i]/N and emits Aliases[i] with (1-Prob[i])/N. Summing both paths is
// the only correct way to compare a runtime table with optimizer probabilities.
func EffectiveAliasProbabilities(table *sampler.AliasTableF64) ([]float64, error) {
	if table == nil {
		return nil, fmt.Errorf("alias table is nil")
	}
	if table.Size <= 0 {
		return nil, fmt.Errorf("alias table size must be > 0")
	}
	if len(table.Prob) != table.Size {
		return nil, fmt.Errorf("alias probability length mismatch: got=%d want=%d", len(table.Prob), table.Size)
	}
	if len(table.Aliases) != table.Size {
		return nil, fmt.Errorf("alias index length mismatch: got=%d want=%d", len(table.Aliases), table.Size)
	}

	effective := make([]float64, table.Size)
	columnMass := 1 / float64(table.Size)
	for i := 0; i < table.Size; i++ {
		prob := table.Prob[i]
		if !isFinite(prob) || prob < 0 || prob > 1 {
			return nil, fmt.Errorf("alias probability at index %d must be finite and in [0,1]: %.17g", i, prob)
		}
		alias := table.Aliases[i]
		if alias < 0 || alias >= table.Size {
			return nil, fmt.Errorf("alias index out of range at index %d: %d", i, alias)
		}
		effective[i] += columnMass * prob
		effective[alias] += columnMass * (1 - prob)
	}

	var probabilitySum compensatedSum
	for _, probability := range effective {
		if !isFinite(probability) || probability < 0 {
			return nil, fmt.Errorf("reconstructed alias probability is invalid: %.17g", probability)
		}
		probabilitySum.Add(probability)
	}
	sum := probabilitySum.Value()
	if math.Abs(sum-1) > scaledTolerance(artifactProbabilityTolerance, sum, 1) {
		return nil, fmt.Errorf("reconstructed alias probability sum mismatch: got=%.17g", sum)
	}
	return effective, nil
}

// PublishedArtifact describes the combined durable result of staging one
// verified mode in every requested format. Complete is false while any format
// still lacks a sibling mode; ManifestPath and ArtifactID are then empty. Once
// every format is complete, Paths name all published payloads, ArtifactID is the
// canonical content identity, and ManifestPath is present when Artifact v1 was
// among the requested formats.
type PublishedArtifact struct {
	Complete         bool
	Formats          []OutputFormat
	ManifestPath     string
	Paths            []string
	ArtifactID       string
	StagingDirectory string
	StagedModes      []int
	MissingModes     []int
}

// FileArtifactWriter persists independently optimized modes beneath Directory.
// PublishMode is the production path: it atomically replaces one mode in the
// persistent game_<gid>.pending area and publishes game_<gid> only after every
// runtime mode is present and the complete bundle passes on-disk verification.
// renamePath is an unexported failure-injection seam used by transaction tests;
// real callers leave it nil and therefore use os.Rename.
type FileArtifactWriter struct {
	Directory  string
	renamePath func(oldPath, newPath string) error
}

// validateMaterializedMode defends the publication boundary even when a caller
// constructs MaterializedMode directly instead of using MaterializeMode.
func validateMaterializedMode(mode MaterializedMode) error {
	if mode.BetMode < 0 {
		return fmt.Errorf("bet mode must be >= 0")
	}
	if mode.BetUnit <= 0 {
		return fmt.Errorf("bet unit must be > 0")
	}
	if mode.Picker == nil || mode.Picker.Size <= 0 {
		return fmt.Errorf("picker must not be empty")
	}
	if len(mode.Samples) != mode.Picker.Size {
		return fmt.Errorf("sample count mismatch: got=%d want=%d", len(mode.Samples), mode.Picker.Size)
	}
	if len(mode.EffectiveProbabilities) != mode.Picker.Size {
		return fmt.Errorf("effective probability count mismatch: got=%d want=%d", len(mode.EffectiveProbabilities), mode.Picker.Size)
	}
	seedLen := len(mode.Samples[0].Snapshot)
	if seedLen == 0 {
		return fmt.Errorf("snapshot length must be > 0")
	}
	if mode.Picker.Size > int(^uint(0)>>1)/seedLen || len(mode.SeedBank) != mode.Picker.Size*seedLen {
		return fmt.Errorf("seed bank length mismatch")
	}
	var probabilitySum compensatedSum
	for i, sample := range mode.Samples {
		if strings.TrimSpace(sample.ClassID) == "" {
			return fmt.Errorf("sample[%d] class id is required", i)
		}
		if !isFinite(sample.Win) || sample.Win < 0 {
			return fmt.Errorf("sample[%d] win must be finite and >= 0", i)
		}
		if len(sample.Snapshot) != seedLen {
			return fmt.Errorf("sample[%d] snapshot length mismatch", i)
		}
		start := i * seedLen
		if !equalBytes(mode.SeedBank[start:start+seedLen], sample.Snapshot) {
			return fmt.Errorf("sample[%d] snapshot does not match seed-bank position", i)
		}
		if !isFinite(sample.Probability) || sample.Probability < 0 {
			return fmt.Errorf("sample[%d] probability must be finite and >= 0", i)
		}
		probabilitySum.Add(sample.Probability)
	}
	sum := probabilitySum.Value()
	if !isFinite(sum) || math.Abs(sum-1) > scaledTolerance(artifactProbabilityTolerance, sum, 1) {
		return fmt.Errorf("sample probability sum must equal 1: got=%.17g", sum)
	}

	effective, err := EffectiveAliasProbabilities(mode.Picker)
	if err != nil {
		return err
	}
	for i := range effective {
		if !isFinite(mode.EffectiveProbabilities[i]) || math.Abs(effective[i]-mode.EffectiveProbabilities[i]) > scaledTolerance(artifactProbabilityTolerance, effective[i], mode.EffectiveProbabilities[i]) {
			return fmt.Errorf("effective probability mismatch at sample[%d]", i)
		}
		if math.Abs(effective[i]-mode.Samples[i].Probability) > scaledTolerance(artifactProbabilityTolerance, effective[i], mode.Samples[i].Probability) {
			return fmt.Errorf("sample probability does not match alias outcome at sample[%d]", i)
		}
	}
	return nil
}

// encodeAliasTable produces the exact little-endian layout required by the
// mmap-backed runtime reader while rejecting values that uint32 cannot encode.
func encodeAliasTable(table *sampler.AliasTableF64) ([]byte, []byte, error) {
	if _, err := EffectiveAliasProbabilities(table); err != nil {
		return nil, nil, err
	}
	prob := make([]byte, table.Size*8)
	aliases := make([]byte, table.Size*4)
	for i := 0; i < table.Size; i++ {
		alias := table.Aliases[i]
		if uint64(alias) > uint64(math.MaxUint32) {
			return nil, nil, fmt.Errorf("alias index at %d exceeds uint32: %d", i, alias)
		}
		binary.LittleEndian.PutUint64(prob[i*8:], math.Float64bits(table.Prob[i]))
		binary.LittleEndian.PutUint32(aliases[i*4:], uint32(alias))
	}
	return prob, aliases, nil
}

// writeArtifactFile writes one staging payload with deterministic permissions.
// Atomicity is supplied at the directory-publication boundary, so no file is
// ever visible to runtime consumers until every sibling and the manifest exist.
func writeArtifactFile(name string, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("artifact payload must not be empty")
	}
	return os.WriteFile(name, data, 0o644)
}

// artifactFileReference binds manifest metadata to the exact staged bytes.
func artifactFileReference(name string, data []byte) optimalrt.FileRef {
	digest := sha256.Sum256(data)
	return optimalrt.FileRef{
		Path:   name,
		Size:   int64(len(data)),
		SHA256: hex.EncodeToString(digest[:]),
	}
}

// contentArtifactID gives identical verified content the same identifier while
// excluding filesystem location and temporary run names from reproducibility.
func contentArtifactID(snapshotFormat string, modes []optimalrt.ManifestMode) string {
	payload, _ := json.Marshal(struct {
		SnapshotFormat string                   `json:"snapshot_format"`
		Modes          []optimalrt.ManifestMode `json:"modes"`
	}{
		SnapshotFormat: snapshotFormat,
		Modes:          modes,
	})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// acquireArtifactPublicationLock serializes writers for one game while leaving
// different game bundles independent. O_EXCL makes lock acquisition atomic;
// refusing an existing lock is safer than allowing two backup/replace sequences
// to interleave and lose the last known-good artifact.
func acquireArtifactPublicationLock(directory string, gid spec.GID) (func(), error) {
	lockPath := filepath.Join(directory, fmt.Sprintf(".game_%d.publish.lock", gid))
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("another publication is active for game_%d", gid)
		}
		return nil, fmt.Errorf("create publication lock: %w", err)
	}
	if err := lock.Close(); err != nil {
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("close publication lock: %w", err)
	}
	return func() { _ = os.Remove(lockPath) }, nil
}

// replaceArtifactDirectory commits a verified staging bundle without ever
// merging files with the prior bundle. Both moves occur within one parent
// directory so each rename is atomic. If installing staging fails after the
// old target was backed up, rollback restores the old target before returning.
func replaceArtifactDirectory(staging, target string, renamePath func(string, string) error) error {
	if renamePath == nil {
		return fmt.Errorf("rename function is nil")
	}
	_, statErr := os.Lstat(target)
	if os.IsNotExist(statErr) {
		if err := renamePath(staging, target); err != nil {
			return fmt.Errorf("install new bundle: %w", err)
		}
		return nil
	}
	if statErr != nil {
		return fmt.Errorf("inspect current bundle: %w", statErr)
	}

	backup := staging + ".previous"
	if err := renamePath(target, backup); err != nil {
		return fmt.Errorf("back up current bundle: %w", err)
	}
	if err := renamePath(staging, target); err != nil {
		if rollbackErr := renamePath(backup, target); rollbackErr != nil {
			return fmt.Errorf("install new bundle: %v; rollback failed: %w", err, rollbackErr)
		}
		return fmt.Errorf("install new bundle: %w (current bundle restored)", err)
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("new bundle committed but remove previous bundle: %w", err)
	}
	return nil
}

// verifyStagedArtifact reopens manifest.json, every mode descriptor, and every
// referenced payload before publication. It checks the descriptor schema used
// by legacy SaveMode, content hashes, the content-derived artifact ID, and the
// exact file set, so incomplete or stale staging content cannot become visible.
func verifyStagedArtifact(staging string) (optimalrt.Manifest, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(staging, "manifest.json"))
	if err != nil {
		return optimalrt.Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest optimalrt.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return optimalrt.Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return optimalrt.Manifest{}, err
	}
	if want := contentArtifactID(manifest.SnapshotFormat, manifest.Modes); manifest.ArtifactID != want {
		return optimalrt.Manifest{}, fmt.Errorf("artifact id mismatch: got=%s want=%s", manifest.ArtifactID, want)
	}

	expectedFiles := map[string]struct{}{"manifest.json": {}}
	for modeIndex, mode := range manifest.Modes {
		descriptorName := fmt.Sprintf("mode_%d.json", modeIndex)
		descriptorBytes, err := os.ReadFile(filepath.Join(staging, descriptorName))
		if err != nil {
			return optimalrt.Manifest{}, fmt.Errorf("mode %d descriptor: read: %w", modeIndex, err)
		}
		var descriptor optimalrt.ManifestMode
		if err := json.Unmarshal(descriptorBytes, &descriptor); err != nil {
			return optimalrt.Manifest{}, fmt.Errorf("mode %d descriptor: parse: %w", modeIndex, err)
		}
		if descriptor != mode {
			return optimalrt.Manifest{}, fmt.Errorf("mode %d descriptor does not match manifest", modeIndex)
		}
		expectedFiles[descriptorName] = struct{}{}

		for kind, ref := range map[string]optimalrt.FileRef{
			"prob": mode.Prob, "aliases": mode.Aliases, "seed_bank": mode.SeedBank,
		} {
			if _, duplicate := expectedFiles[ref.Path]; duplicate {
				return optimalrt.Manifest{}, fmt.Errorf("mode %d %s: duplicate artifact path %q", modeIndex, kind, ref.Path)
			}
			expectedFiles[ref.Path] = struct{}{}
			data, err := os.ReadFile(filepath.Join(staging, ref.Path))
			if err != nil {
				return optimalrt.Manifest{}, fmt.Errorf("mode %d %s: read: %w", modeIndex, kind, err)
			}
			actual := artifactFileReference(ref.Path, data)
			if actual.Size != ref.Size || actual.SHA256 != ref.SHA256 {
				return optimalrt.Manifest{}, fmt.Errorf("mode %d %s: size or sha256 mismatch", modeIndex, kind)
			}
		}
	}
	if err := verifyArtifactFileSet(staging, expectedFiles); err != nil {
		return optimalrt.Manifest{}, err
	}
	if err := verifyArtifactWithProductionLoader(staging, manifest); err != nil {
		return optimalrt.Manifest{}, err
	}
	return manifest, nil
}

// verifyArtifactWithProductionLoader proves the staged files are consumable by
// the same manifest, mmap/in-memory binary, alias, and seed-bank path used by a
// real Machine. The synthetic setting supplies only loader-owned fields. Store
// closure is mandatory here so mmap handles do not outlive staging or prevent
// its subsequent directory rename on platforms with stricter file semantics.
func verifyArtifactWithProductionLoader(staging string, manifest optimalrt.Manifest) (resultErr error) {
	betUnits := make([]int, len(manifest.Modes))
	for i := range manifest.Modes {
		betUnits[i] = manifest.Modes[i].BetUnit
	}
	store, err := optimalrt.NewDirStore(staging, manifest.SnapshotFormat, manifest.Modes[0].SeedLen)
	if err != nil {
		return fmt.Errorf("open production artifact store: %w", err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil && resultErr == nil {
			resultErr = fmt.Errorf("close production artifact store: %w", closeErr)
		}
	}()

	setting := &spec.GameSetting{
		BetUnits: betUnits,
		OptimalSetting: spec.OptimalSetting{
			UseOptimal: true,
			Artifact:   "manifest.json",
		},
	}
	artifact, err := store.Resolve(setting)
	if err != nil {
		return fmt.Errorf("production loader resolve: %w", err)
	}
	if artifact == nil || artifact.ModeCount() != len(manifest.Modes) {
		return fmt.Errorf("production loader mode count mismatch: got=%d want=%d", artifact.ModeCount(), len(manifest.Modes))
	}
	return nil
}

// verifyArtifactFileSet rejects unreferenced files, symlinks, and other special
// entries in staging. A fresh staging directory should contain exactly the
// files described by Artifact v1; enforcing that invariant prevents future
// writer changes from accidentally publishing stale sidecars.
func verifyArtifactFileSet(staging string, expected map[string]struct{}) error {
	remaining := make(map[string]struct{}, len(expected))
	for name := range expected {
		remaining[name] = struct{}{}
	}
	err := filepath.WalkDir(staging, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == staging {
			return nil
		}
		if entry.IsDir() {
			return fmt.Errorf("unexpected artifact directory %q", name)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("artifact entry %q is not a regular file", name)
		}
		relative, err := filepath.Rel(staging, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, ok := remaining[relative]; !ok {
			return fmt.Errorf("unexpected artifact file %q", relative)
		}
		delete(remaining, relative)
		return nil
	})
	if err != nil {
		return err
	}
	for name := range remaining {
		return fmt.Errorf("missing artifact file %q", name)
	}
	return nil
}

// equalBytes compares seed slices without importing a serialization-dependent
// representation into the optimizer's mathematical layers.
func equalBytes(left, right []byte) bool {
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
