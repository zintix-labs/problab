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
	"fmt"
	"math"
	"sort"

	"github.com/zintix-labs/problab"
)

// expandedSampleRef keeps the atomic-bucket identity beside a collected replay
// atom while a Class is restored to global collection sequence. Preparation
// stores samples by bucket for LP statistics; artifact order must deliberately
// undo that grouping without losing the bucket that owns each probability.
type expandedSampleRef struct {
	bucketIndex int
	sample      CollectedSample
}

// ExpandSolution converts conditional LP bucket masses into unconditional
// per-outcome artifact probabilities. Class probability is applied exactly
// once: an intent:true outcome receives c_k*p[k,i]/n[k,i], while every outcome
// in an empirical-uniform intent:false Class receives c_k/N_k. Empty atomic
// buckets contribute no rows.
//
// Output order is part of artifact identity. Classes remain in Designer
// declaration order and samples within each Class are stable-sorted by their
// original collection Sequence, even when preparation placed them in different
// atomic buckets. Snapshots are copied so the returned artifact input does not
// retain mutable prepared storage.
//
// A mismatched bet identity, malformed primary vector, non-finite or negative
// mass, or broken prepared support is an internal materialization error. It is
// returned explicitly so the Tuner cannot pass a nil outcome table to the alias
// builder and obscure the actual model-to-artifact boundary failure.
func ExpandSolution(compiled CompiledModel, solution EngineSolution, betMode, betUnit int) ([]MaterializedSample, error) {
	if betMode != compiled.Prepared.BetMode || betUnit != compiled.Prepared.BetUnit || betUnit <= 0 {
		return nil, fmt.Errorf(
			"prepared bet identity mismatch: got mode=%d unit=%d want mode=%d unit=%d",
			betMode, betUnit, compiled.Prepared.BetMode, compiled.Prepared.BetUnit,
		)
	}
	primary, err := solutionPrimaryMasses(compiled, solution)
	if err != nil {
		return nil, err
	}

	expanded := make([]MaterializedSample, 0)
	for classIndex, class := range compiled.Prepared.Classes {
		if !isFinite(class.Probability) || class.Probability < 0 {
			return nil, fmt.Errorf("class %q probability must be finite and nonnegative", class.ID)
		}
		references := classSequenceReferences(class)
		if len(references) == 0 {
			return nil, fmt.Errorf("class %q has no replayable samples", class.ID)
		}
		for _, reference := range references {
			bucket := class.Buckets[reference.bucketIndex]
			probability := 0.0
			if class.Intent {
				if len(bucket.Samples) == 0 {
					continue
				}
				probability = class.Probability * primary[classIndex][reference.bucketIndex] / float64(len(bucket.Samples))
			} else {
				probability = class.Probability / float64(len(references))
			}
			if !isFinite(probability) || probability < 0 {
				return nil, fmt.Errorf("class %q bucket %d expanded probability must be finite and nonnegative", class.ID, reference.bucketIndex)
			}
			expanded = append(expanded, MaterializedSample{
				ClassID:     class.ID,
				BucketIndex: reference.bucketIndex,
				Win:         reference.sample.Win,
				Snapshot:    append([]byte(nil), reference.sample.Snapshot...),
				Probability: probability,
			})
		}
	}
	return expanded, nil
}

// classSequenceReferences flattens supported buckets and then stable-sorts the
// resulting replay atoms by collection Sequence. Stable sorting is intentional:
// a malformed adapter that repeats a Sequence still gets deterministic order,
// while duplicate replay identity remains the preparation stage's diagnostic.
func classSequenceReferences(class PreparedClass) []expandedSampleRef {
	references := make([]expandedSampleRef, 0)
	for bucketIndex, bucket := range class.Buckets {
		for _, sample := range bucket.Samples {
			references = append(references, expandedSampleRef{bucketIndex: bucketIndex, sample: sample})
		}
	}
	sort.SliceStable(references, func(i, j int) bool {
		return references[i].sample.Sequence < references[j].sample.Sequence
	})
	return references
}

// solutionPrimaryMasses validates and expands EngineSolution.Primary into a
// Class/bucket matrix. CompiledModel.Primary, rather than variable-name parsing
// or positional guesses, is the sole semantic mapping. Every intent:true
// bucket must occur exactly once, including unsupported buckets fixed to zero.
func solutionPrimaryMasses(compiled CompiledModel, solution EngineSolution) ([][]float64, error) {
	if len(solution.Primary) != len(compiled.Primary) {
		return nil, fmt.Errorf("primary length mismatch: got=%d want=%d", len(solution.Primary), len(compiled.Primary))
	}
	masses := make([][]float64, len(compiled.Prepared.Classes))
	seen := make([][]bool, len(compiled.Prepared.Classes))
	for classIndex, class := range compiled.Prepared.Classes {
		masses[classIndex] = make([]float64, len(class.Buckets))
		seen[classIndex] = make([]bool, len(class.Buckets))
	}
	for primaryIndex, descriptor := range compiled.Primary {
		if descriptor.ClassIndex < 0 || descriptor.ClassIndex >= len(compiled.Prepared.Classes) {
			return nil, fmt.Errorf("primary[%d] class index out of range: %d", primaryIndex, descriptor.ClassIndex)
		}
		class := compiled.Prepared.Classes[descriptor.ClassIndex]
		if !class.Intent {
			return nil, fmt.Errorf("primary[%d] refers to empirical-uniform class %q", primaryIndex, class.ID)
		}
		if descriptor.BucketIndex < 0 || descriptor.BucketIndex >= len(class.Buckets) {
			return nil, fmt.Errorf("primary[%d] bucket index out of range: %d", primaryIndex, descriptor.BucketIndex)
		}
		if seen[descriptor.ClassIndex][descriptor.BucketIndex] {
			return nil, fmt.Errorf("duplicate primary mapping for class %q bucket %d", class.ID, descriptor.BucketIndex)
		}
		value := solution.Primary[primaryIndex]
		if !isFinite(value) || value < 0 {
			return nil, fmt.Errorf("primary[%d] must be finite and nonnegative: %.17g", primaryIndex, value)
		}
		masses[descriptor.ClassIndex][descriptor.BucketIndex] = value
		seen[descriptor.ClassIndex][descriptor.BucketIndex] = true
	}
	for classIndex, class := range compiled.Prepared.Classes {
		if !class.Intent {
			continue
		}
		for bucketIndex := range class.Buckets {
			if !seen[classIndex][bucketIndex] {
				return nil, fmt.Errorf("missing primary mapping for class %q bucket %d", class.ID, bucketIndex)
			}
		}
	}
	return masses, nil
}

// artifactReplay is the independently reconstructed semantic distribution of a
// MaterializedMode. It uses alias-table marginals as the source of truth; the
// redundant EffectiveProbabilities field and sample Probability annotations
// are checked by validateMaterializedMode but never trusted for hard replay.
type artifactReplay struct {
	effective         []float64
	classTotals       []float64
	bucketConditional [][]float64
	mean              float64
	secondMoment      float64
}

// replayArtifactDistribution converts runtime alias outcomes back into Class
// totals and conditional atomic-bucket masses. Unknown Class IDs, invalid
// bucket indexes, and non-positive Class probabilities are representation
// failures returned as text for a VerificationCheck, never Go errors.
func replayArtifactDistribution(compiled CompiledModel, mode MaterializedMode) (artifactReplay, string) {
	effective, err := EffectiveAliasProbabilities(mode.Picker)
	if err != nil {
		return artifactReplay{}, err.Error()
	}
	if len(effective) != len(mode.Samples) {
		return artifactReplay{}, fmt.Sprintf("alias outcome count=%d sample count=%d", len(effective), len(mode.Samples))
	}

	replay := artifactReplay{
		effective:         effective,
		classTotals:       make([]float64, len(compiled.Prepared.Classes)),
		bucketConditional: make([][]float64, len(compiled.Prepared.Classes)),
	}
	classByID := make(map[string]int, len(compiled.Prepared.Classes))
	for classIndex, class := range compiled.Prepared.Classes {
		if _, duplicate := classByID[class.ID]; duplicate {
			return artifactReplay{}, fmt.Sprintf("duplicate prepared class id %q", class.ID)
		}
		classByID[class.ID] = classIndex
		replay.bucketConditional[classIndex] = make([]float64, len(class.Buckets))
	}
	for sampleIndex, sample := range mode.Samples {
		classIndex, exists := classByID[sample.ClassID]
		if !exists {
			return artifactReplay{}, fmt.Sprintf("sample[%d] has unknown class id %q", sampleIndex, sample.ClassID)
		}
		if sample.BucketIndex < 0 || sample.BucketIndex >= len(compiled.Prepared.Classes[classIndex].Buckets) {
			return artifactReplay{}, fmt.Sprintf("sample[%d] bucket index out of range: %d", sampleIndex, sample.BucketIndex)
		}
		probability := effective[sampleIndex]
		replay.classTotals[classIndex] += probability
		replay.bucketConditional[classIndex][sample.BucketIndex] += probability
		replay.mean += probability * sample.Win
		replay.secondMoment += probability * sample.Win * sample.Win
	}
	for classIndex, class := range compiled.Prepared.Classes {
		if !isFinite(class.Probability) || class.Probability <= 0 {
			return artifactReplay{}, fmt.Sprintf("class %q probability must be finite and positive", class.ID)
		}
		for bucketIndex := range replay.bucketConditional[classIndex] {
			replay.bucketConditional[classIndex][bucketIndex] /= class.Probability
		}
	}
	return replay, ""
}

// VerifyRuntimeMaterialized restores every seed-bank entry into a raw Machine,
// executes the real game logic for the selected bet mode, and then runs the
// ordinary alias/hard verification using those replayed payouts. This is the
// final protection against a stale or misaligned snapshot: collected Win
// annotations are expectations only and cannot prove what production runtime
// will actually emit after restoring the persisted Core state.
//
// Context cancellation and inability to construct the runtime dependency are
// operational errors. A malformed snapshot, a non-reproducible payout, or a
// replay that no longer satisfies its assigned Class/bucket is instead a failed
// VerificationCheck, allowing Tuner to return ARTIFACT_INVALID without
// publishing the bundle.
func VerifyRuntimeMaterialized(
	ctx context.Context,
	lab *problab.Problab,
	compiled CompiledModel,
	solution EngineSolution,
	mode MaterializedMode,
) (VerificationReport, error) {
	replayed, runtimeCheck, err := replayMaterializedSnapshots(ctx, lab, compiled, mode)
	if err != nil {
		return VerificationReport{}, err
	}
	checks := []VerificationCheck{runtimeCheck}
	if !runtimeCheck.Pass {
		return finalizeVerification(checks), nil
	}
	semantic := VerifyMaterialized(compiled, solution, replayed)
	checks = append(checks, semantic.Checks...)
	return finalizeVerification(checks), nil
}

// replayMaterializedSnapshots creates an owned semantic copy of mode whose Win
// and BucketIndex values come from actual raw-game replay of SeedBank, never
// from MaterializedSample annotations. ClassID remains the modeled ownership;
// replay must still satisfy that Class's predicate. A changed bucket is written
// into the copy so subsequent hard replay measures the runtime distribution,
// while the model-alignment check also records that the replay identity drifted.
func replayMaterializedSnapshots(
	ctx context.Context,
	lab *problab.Problab,
	compiled CompiledModel,
	mode MaterializedMode,
) (MaterializedMode, VerificationCheck, error) {
	const checkName = "artifact.snapshot_runtime_replay"
	if ctx == nil {
		return MaterializedMode{}, VerificationCheck{}, fmt.Errorf("runtime artifact replay requires a context")
	}
	if lab == nil {
		return MaterializedMode{}, VerificationCheck{}, fmt.Errorf("runtime artifact replay requires Problab")
	}
	if err := ctx.Err(); err != nil {
		return MaterializedMode{}, VerificationCheck{}, err
	}
	if err := validateMaterializedMode(mode); err != nil {
		return MaterializedMode{}, textVerificationCheck(checkName, false, err.Error(), "every seed-bank entry restores and reproduces its modeled payout, Class predicate, and atomic bucket"), nil
	}
	machine, err := lab.NewUnoptimizedMachineWithSeed(compiled.Prepared.Game, compiled.Prepared.Plan.Plan.Seed, true)
	if err != nil {
		return MaterializedMode{}, VerificationCheck{}, fmt.Errorf("construct raw runtime-replay machine: %w", err)
	}
	tagger, predicates, err := compileTagPredicates(compiled.Prepared.Plan.Intent.Classes)
	if err != nil {
		return MaterializedMode{}, VerificationCheck{}, fmt.Errorf("compile runtime-replay Class predicates: %w", err)
	}

	classByID := make(map[string]int, len(compiled.Prepared.Classes))
	for classIndex, class := range compiled.Prepared.Classes {
		classByID[class.ID] = classIndex
	}
	replayed := cloneMaterializedMode(mode)
	seedLen := len(mode.Samples[0].Snapshot)
	for sampleIndex, sample := range mode.Samples {
		if err := ctx.Err(); err != nil {
			return MaterializedMode{}, VerificationCheck{}, err
		}
		start := sampleIndex * seedLen
		seed := mode.SeedBank[start : start+seedLen]
		if err := machine.RestoreCore(seed); err != nil {
			return MaterializedMode{}, textVerificationCheck(
				checkName, false,
				fmt.Sprintf("sample[%d] seed-bank snapshot cannot be restored: %v", sampleIndex, err),
				"every seed-bank entry restores and reproduces its modeled payout, Class predicate, and atomic bucket",
			), nil
		}
		result := machine.SpinInternal(mode.BetMode)
		actualBet := 0
		if result != nil {
			actualBet = result.Bet
		}
		if result == nil || actualBet <= 0 || actualBet != mode.BetUnit {
			return MaterializedMode{}, textVerificationCheck(
				checkName, false,
				fmt.Sprintf("sample[%d] replay returned invalid bet: result_nil=%t bet=%d want=%d", sampleIndex, result == nil, actualBet, mode.BetUnit),
				"every seed-bank entry restores and reproduces its modeled payout, Class predicate, and atomic bucket",
			), nil
		}
		win := float64(result.TotalWin) / float64(actualBet)
		if !isFinite(win) || win < 0 {
			return MaterializedMode{}, textVerificationCheck(
				checkName, false, fmt.Sprintf("sample[%d] replay returned invalid normalized payout %.17g", sampleIndex, win),
				"every seed-bank entry restores and reproduces its modeled payout, Class predicate, and atomic bucket",
			), nil
		}
		classIndex, exists := classByID[sample.ClassID]
		if !exists || classIndex >= len(predicates) {
			return MaterializedMode{}, textVerificationCheck(
				checkName, false, fmt.Sprintf("sample[%d] refers to unknown Class %q", sampleIndex, sample.ClassID),
				"every seed-bank entry restores and reproduces its modeled payout, Class predicate, and atomic bucket",
			), nil
		}
		tags := uint64(0)
		if tagger != nil {
			tags = tagger.Tagging(result)
		}
		intent := compiled.Prepared.Plan.Intent.Classes[classIndex]
		if !classAccepts(intent, predicates[classIndex], tags, win) {
			return MaterializedMode{}, textVerificationCheck(
				checkName, false,
				fmt.Sprintf("sample[%d] replay payout/tags no longer satisfy assigned Class %q", sampleIndex, sample.ClassID),
				"every seed-bank entry restores and reproduces its modeled payout, Class predicate, and atomic bucket",
			), nil
		}
		bucketIndex := 0
		if compiled.Prepared.Classes[classIndex].Intent {
			bucketIndex = atomicBucketIndex(intent.Design.Subjective.Buckets, win)
		}
		if bucketIndex < 0 || bucketIndex >= len(compiled.Prepared.Classes[classIndex].Buckets) {
			return MaterializedMode{}, textVerificationCheck(
				checkName, false,
				fmt.Sprintf("sample[%d] replay payout %.17g has no atomic bucket in Class %q", sampleIndex, win, sample.ClassID),
				"every seed-bank entry restores and reproduces its modeled payout, Class predicate, and atomic bucket",
			), nil
		}
		if win != sample.Win {
			return MaterializedMode{}, textVerificationCheck(
				checkName, false,
				fmt.Sprintf("sample[%d] replay payout=%.17g differs from modeled payout=%.17g", sampleIndex, win, sample.Win),
				"every seed-bank entry restores and reproduces its modeled payout, Class predicate, and atomic bucket",
			), nil
		}
		if bucketIndex != sample.BucketIndex {
			return MaterializedMode{}, textVerificationCheck(
				checkName, false,
				fmt.Sprintf("sample[%d] replay bucket=%d differs from modeled bucket=%d", sampleIndex, bucketIndex, sample.BucketIndex),
				"every seed-bank entry restores and reproduces its modeled payout, Class predicate, and atomic bucket",
			), nil
		}
		replayed.Samples[sampleIndex].Win = win
		replayed.Samples[sampleIndex].BucketIndex = bucketIndex
	}
	return replayed, textVerificationCheck(
		checkName, true, fmt.Sprintf("replayed_outcomes=%d", len(replayed.Samples)),
		"every seed-bank entry restores and reproduces its modeled payout, Class predicate, and atomic bucket",
	), nil
}

// cloneMaterializedMode deep-copies every mutable byte/float slice used during
// runtime replay. Picker is immutable after construction and can be shared;
// replacing replayed semantic metadata must never mutate the publish input.
func cloneMaterializedMode(mode MaterializedMode) MaterializedMode {
	cloned := mode
	cloned.SeedBank = append([]byte(nil), mode.SeedBank...)
	cloned.EffectiveProbabilities = append([]float64(nil), mode.EffectiveProbabilities...)
	cloned.Samples = make([]MaterializedSample, len(mode.Samples))
	for i, sample := range mode.Samples {
		cloned.Samples[i] = sample
		cloned.Samples[i].Snapshot = append([]byte(nil), sample.Snapshot...)
	}
	return cloned
}

// VerifyMaterialized independently replays a runtime-ready alias artifact
// against the compiled hard model and selected primary solution. It never
// returns an error: malformed dimensions and representation damage become
// deterministic failed checks so callers can publish the complete verification
// record while refusing the artifact.
//
// Verification deliberately crosses every semantic boundary: the artifact
// helper checks seed/sample positional alignment, alias thresholds are expanded
// into true marginals, marginals are aggregated into conditional p[k,i], the
// original named hard rows are replayed, and unconditional Class totals, mean,
// second moment, and CV bounds are checked again from per-sample payouts.
func VerifyMaterialized(compiled CompiledModel, solution EngineSolution, mode MaterializedMode) VerificationReport {
	tolerance := verificationTolerance(compiled)
	checks := make([]VerificationCheck, 0)

	structureErr := validateMaterializedMode(mode)
	checks = append(checks, textVerificationCheck(
		"artifact.structure_and_seed_alignment",
		structureErr == nil,
		verificationErrorActual(structureErr),
		"valid materialized mode with sample/seed-bank positional alignment",
	))

	replay, replayFailure := replayArtifactDistribution(compiled, mode)
	checks = append(checks, textVerificationCheck(
		"artifact.alias_effective_probability_replay",
		replayFailure == "",
		verificationFailureActual(replayFailure, fmt.Sprintf("reconstructed_outcomes=%d", len(replay.effective))),
		"valid marginal probabilities reconstructed from alias thresholds and aliases",
	))

	expected, expansionErr := ExpandSolution(compiled, solution, mode.BetMode, mode.BetUnit)
	alignmentPass, alignmentActual := verifyExpandedAlignment(expected, expansionErr, mode, replay.effective, tolerance)
	checks = append(checks, textVerificationCheck(
		"artifact.model_sample_alignment",
		alignmentPass,
		alignmentActual,
		"Class/Sequence-expanded model samples, snapshots, payouts, and marginals",
	))

	primary, primaryErr := solutionPrimaryMasses(compiled, solution)
	primaryReady := primaryErr == nil && replayFailure == ""
	if primaryErr != nil {
		checks = append(checks, textVerificationCheck("solution.primary_dimensions", false, primaryErr.Error(), "one finite nonnegative primary value per intent Class bucket"))
	} else {
		checks = append(checks, textVerificationCheck("solution.primary_dimensions", true, fmt.Sprintf("primary_values=%d", len(solution.Primary)), "one finite nonnegative primary value per intent Class bucket"))
	}

	for classIndex, class := range compiled.Prepared.Classes {
		for bucketIndex := range class.Buckets {
			actual, required, pass := math.NaN(), math.NaN(), false
			if replayFailure == "" {
				actual = replay.bucketConditional[classIndex][bucketIndex]
			}
			if class.Intent && primaryErr == nil {
				required = primary[classIndex][bucketIndex]
				pass = replayFailure == "" && nearWithTolerance(actual, required, tolerance)
			} else if !class.Intent {
				required = 1
				pass = replayFailure == "" && nearWithTolerance(actual, required, tolerance)
			}
			checks = append(checks, numericVerificationCheck(
				fmt.Sprintf("class.%s.bucket.%04d.conditional_mass", class.ID, bucketIndex),
				pass, actual, required, tolerance,
			))
		}
	}

	artifactPrimary, artifactPrimaryFailure := artifactHardValues(compiled, replay, primaryReady)
	rowViolation, boundViolation, hardPass := math.Inf(1), math.Inf(1), false
	if artifactPrimaryFailure == "" {
		rowViolation, boundViolation, hardPass = replaySemanticSolution(compiled.Hard, artifactPrimary, tolerance)
	}
	checks = append(checks, textVerificationCheckWithTolerance(
		"hard.semantic_row_and_bound_replay",
		hardPass,
		verificationFailureActual(artifactPrimaryFailure, fmt.Sprintf("max_row_violation=%.17g max_bound_violation=%.17g", rowViolation, boundViolation)),
		"all compiled hard rows and primary bounds satisfied",
		tolerance,
	))

	for classIndex, class := range compiled.Prepared.Classes {
		actual := math.NaN()
		if replayFailure == "" {
			actual = replay.classTotals[classIndex]
		}
		checks = append(checks, numericVerificationCheck(
			fmt.Sprintf("class.%s.unconditional_probability", class.ID),
			replayFailure == "" && nearWithTolerance(actual, class.Probability, tolerance),
			actual, class.Probability, tolerance,
		))
	}

	targetMean := compiled.Prepared.ExpectedRTP()
	checks = append(checks, numericVerificationCheck(
		"overall.expected_rtp",
		replayFailure == "" && nearWithTolerance(replay.mean, targetMean, tolerance),
		verificationValueOrNaN(replayFailure, replay.mean), targetMean, tolerance,
	))

	cvRange := compiled.Prepared.Plan.Intent.Overall.CV
	lowerSecond := targetMean * targetMean * (1 + cvRange.Min*cvRange.Min)
	upperSecond := targetMean * targetMean * (1 + cvRange.Max*cvRange.Max)
	actualCV := math.NaN()
	if replayFailure == "" && replay.mean > 0 {
		actualCV = math.Sqrt(math.Max(0, replay.secondMoment-replay.mean*replay.mean)) / replay.mean
	}
	secondPass := replayFailure == "" && valueWithinRange(replay.secondMoment, lowerSecond, upperSecond, tolerance)
	checks = append(checks, textVerificationCheckWithTolerance(
		"overall.second_moment_and_cv",
		secondPass,
		fmt.Sprintf("second_moment=%.17g cv=%.17g", verificationValueOrNaN(replayFailure, replay.secondMoment), actualCV),
		fmt.Sprintf("second_moment in [%.17g, %.17g], configured_cv in [%.17g, %.17g]", lowerSecond, upperSecond, cvRange.Min, cvRange.Max),
		tolerance,
	))

	return finalizeVerification(checks)
}

// verifyExpandedAlignment ensures the alias outcome positions still correspond
// to the Class/Sequence ordering and replay atoms used to derive probabilities.
// It compares reconstructed marginals, not alias-column threshold values.
func verifyExpandedAlignment(expected []MaterializedSample, expansionErr error, mode MaterializedMode, effective []float64, tolerance float64) (bool, string) {
	if expansionErr != nil {
		return false, expansionErr.Error()
	}
	if len(expected) != len(mode.Samples) || len(effective) != len(mode.Samples) {
		return false, fmt.Sprintf("expected=%d samples=%d marginals=%d", len(expected), len(mode.Samples), len(effective))
	}
	maxDifference := 0.0
	maxIndex := -1
	var absoluteDifferenceSum compensatedSum
	for i := range expected {
		actual := mode.Samples[i]
		if actual.ClassID != expected[i].ClassID || actual.BucketIndex != expected[i].BucketIndex || actual.Win != expected[i].Win || !equalBytes(actual.Snapshot, expected[i].Snapshot) {
			return false, fmt.Sprintf("sample[%d] replay identity or semantic metadata mismatch", i)
		}
		if !nearWithTolerance(effective[i], expected[i].Probability, tolerance) {
			return false, fmt.Sprintf("sample[%d] marginal=%.17g expected=%.17g", i, effective[i], expected[i].Probability)
		}
		difference := math.Abs(effective[i] - expected[i].Probability)
		absoluteDifferenceSum.Add(difference)
		if difference > maxDifference {
			maxDifference = difference
			maxIndex = i
		}
	}
	totalVariation := 0.5 * absoluteDifferenceSum.Value()
	if totalVariation > scaledTolerance(tolerance, 1) {
		return false, fmt.Sprintf("alias total variation %.17g exceeds tolerance %.1e; max_difference=%.17g at sample[%d]", totalVariation, tolerance, maxDifference, maxIndex)
	}
	return true, fmt.Sprintf("aligned_samples=%d max_marginal_difference=%.17g total_variation=%.17g", len(expected), maxDifference, totalVariation)
}

// artifactHardValues orders reconstructed conditional bucket masses exactly as
// CompiledModel.Hard.Variables expects. It rejects any non-primary hard column
// instead of silently supplying zero, because hard replay must remain tied to
// the compiler's declared semantic model.
func artifactHardValues(compiled CompiledModel, replay artifactReplay, ready bool) ([]float64, string) {
	if !ready {
		return nil, "primary or artifact reconstruction unavailable"
	}
	byID := make(map[VariableID]float64, len(compiled.Primary))
	for _, descriptor := range compiled.Primary {
		if descriptor.ClassIndex < 0 || descriptor.ClassIndex >= len(replay.bucketConditional) ||
			descriptor.BucketIndex < 0 || descriptor.BucketIndex >= len(replay.bucketConditional[descriptor.ClassIndex]) {
			return nil, fmt.Sprintf("invalid compiled primary mapping %q", descriptor.ID)
		}
		byID[descriptor.ID] = replay.bucketConditional[descriptor.ClassIndex][descriptor.BucketIndex]
	}
	values := make([]float64, len(compiled.Hard.Variables))
	for column, variable := range compiled.Hard.Variables {
		value, exists := byID[variable.ID]
		if !exists {
			return nil, fmt.Sprintf("hard variable %q is not a primary bucket mass", variable.ID)
		}
		values[column] = value
	}
	return values, ""
}

// verificationTolerance returns the configured semantic replay tolerance and
// supplies the package default for hand-built test adapters with a zero value.
func verificationTolerance(compiled CompiledModel) float64 {
	tolerance := compiled.Prepared.Plan.EngineOptions.FeasibilityTolerance
	if !isFinite(tolerance) || tolerance <= 0 {
		return DefaultFeasibilityTolerance
	}
	return tolerance
}

// nearWithTolerance applies the same scale-aware comparison convention used by
// the solver replay so report checks do not disagree merely due to units.
func nearWithTolerance(actual, required, tolerance float64) bool {
	return isFinite(actual) && isFinite(required) && math.Abs(actual-required) <= scaledTolerance(tolerance, actual, required)
}

// valueWithinRange checks an inclusive numerical interval with scale-aware
// endpoint allowance. It is used for the second-moment form of configured CV.
func valueWithinRange(actual, lower, upper, tolerance float64) bool {
	if !isFinite(actual) || !isFinite(lower) || !isFinite(upper) {
		return false
	}
	allowance := scaledTolerance(tolerance, actual, lower, upper)
	return actual >= lower-allowance && actual <= upper+allowance
}

// numericVerificationCheck renders a stable high-precision scalar comparison.
// Keeping formatting here prevents individual checks from choosing incompatible
// precision or locale conventions.
func numericVerificationCheck(name string, pass bool, actual, required, tolerance float64) VerificationCheck {
	return VerificationCheck{
		Name: name, Pass: pass,
		Actual: fmt.Sprintf("%.17g", actual), Required: fmt.Sprintf("%.17g", required),
		Tolerance: tolerance,
	}
}

// textVerificationCheck constructs a non-numerical stable verification item.
func textVerificationCheck(name string, pass bool, actual, required string) VerificationCheck {
	return VerificationCheck{Name: name, Pass: pass, Actual: actual, Required: required}
}

// textVerificationCheckWithTolerance constructs a compound numerical check
// whose Actual and Required fields must carry more than one scalar.
func textVerificationCheckWithTolerance(name string, pass bool, actual, required string, tolerance float64) VerificationCheck {
	return VerificationCheck{Name: name, Pass: pass, Actual: actual, Required: required, Tolerance: tolerance}
}

// verificationErrorActual turns the artifact helper's nil/error convention into
// the textual convention used by VerificationCheck.
func verificationErrorActual(err error) string {
	if err != nil {
		return err.Error()
	}
	return "valid"
}

// verificationFailureActual selects a failure explanation when present and a
// deterministic success measurement otherwise.
func verificationFailureActual(failure, success string) string {
	if failure != "" {
		return failure
	}
	return success
}

// verificationValueOrNaN avoids accidentally reporting a zero measurement
// when alias reconstruction failed before a value could be observed.
func verificationValueOrNaN(failure string, value float64) float64 {
	if failure != "" {
		return math.NaN()
	}
	return value
}

// finalizeVerification computes the report-level Pass bit as the conjunction
// of checks in their already stable construction order.
func finalizeVerification(checks []VerificationCheck) VerificationReport {
	pass := true
	for _, check := range checks {
		if !check.Pass {
			pass = false
		}
	}
	return VerificationReport{Pass: pass, Checks: checks}
}
