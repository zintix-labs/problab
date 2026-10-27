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
	"fmt"
	"math"
	"strings"

	"github.com/zintix-labs/problab/sdk/sampler"
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

// PublishedArtifact describes the gacha pair published for one verified mode.
// Directory is the immutable publication directory and Paths name its gacha and
// seed bank. ArtifactID is the pair's content identity; Reused reports that the
// same content had already been published and the existing directory was kept.
type PublishedArtifact struct {
	Formats    []OutputFormat
	Directory  string
	Paths      []string
	ArtifactID string
	Reused     bool
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
