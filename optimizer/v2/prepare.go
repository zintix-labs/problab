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
	"sort"

	"github.com/zintix-labs/problab/spec"
)

// PreparedProblem is the immutable-by-convention mathematical view compiled
// from collected replay atoms. All payout values are already multipliers; no
// later stage may divide by BetUnit again.
type PreparedProblem struct {
	Plan    ResolvedPlan
	Game    spec.GID
	BetMode int
	BetUnit int
	Spins   uint64
	Classes []PreparedClass
}

// PreparedClass combines the fixed unconditional Class probability with either
// one empirical-uniform bucket or an ordered set of LP-controlled atomic
// buckets. Group and Other indexes refer to Buckets and are always sorted.
type PreparedClass struct {
	ID           string
	Index        int
	Weight       int
	Probability  float64
	CollectRange ClosedInterval
	Design       ClassDesign
	Intent       bool
	Buckets      []PreparedBucket
	Groups       []PreparedGroup
	Others       []int
}

// PreparedBucket contains exact statistics of the replay atoms assigned by
// the configured half-open interval rule. Mean and SecondMoment are empirical
// values, never boundary midpoints. RiskCap is +Inf when risk was omitted.
type PreparedBucket struct {
	Index          int
	Lower          float64
	Upper          float64
	Samples        []CollectedSample
	Mean           float64
	SecondMoment   float64
	Minimum        float64
	Maximum        float64
	CDFBeforeLower float64
	CDFAtUpper     float64
	RiskCap        float64
	MainGroup      int
}

// Supported reports whether the bucket contains at least one unique replay
// atom. Empty support is represented explicitly so the model can force p=0.
func (b PreparedBucket) Supported() bool { return len(b.Samples) > 0 }

// PreparedGroup maps one semantic Main range to complete atomic buckets. The
// solver constrains only the sum over BucketIndexes; it must not infer an
// internal uniform or smooth shape for a multi-bucket group.
type PreparedGroup struct {
	Index         int
	Range         ClosedInterval
	BucketIndexes []int
	PreferShare   float64
}

// PrepareProblem validates collection support and computes the only statistics
// the LP may consume. Expected support failures return typed diagnostics and a
// nil Go error; malformed internal collection values return an operational
// error because they indicate a broken Collector or test adapter.
func PrepareProblem(plan ResolvedPlan, collected CollectedProblem) (PreparedProblem, Diagnostics, error) {
	prepared := PreparedProblem{
		Plan: plan, Game: collected.Game, BetMode: collected.BetMode,
		BetUnit: collected.BetUnit, Spins: collected.Spins,
		Classes: make([]PreparedClass, 0, len(plan.Intent.Classes)),
	}
	if len(collected.Classes) != len(plan.Intent.Classes) {
		return PreparedProblem{}, nil, fmt.Errorf("collector returned %d classes for %d intents", len(collected.Classes), len(plan.Intent.Classes))
	}
	for classIndex, intent := range plan.Intent.Classes {
		collectedClass := collected.Classes[classIndex]
		if collectedClass.Intent.Name != "" && collectedClass.Intent.Name != intent.Name {
			return PreparedProblem{}, nil, fmt.Errorf("collector class[%d] identity mismatch: got %q want %q", classIndex, collectedClass.Intent.Name, intent.Name)
		}
		if uint64(len(collectedClass.Samples)) != intent.Collect.Samples {
			return PreparedProblem{}, Diagnostics{supportDiagnostic(
				DiagnosticCollectionInsufficient,
				fmt.Sprintf("class %q collected %d of %d requested samples", intent.Name, len(collectedClass.Samples), intent.Collect.Samples),
				fmt.Sprintf("intents.%s.classes[%d].collect.samples", plan.Plan.Intent, classIndex),
			)}, nil
		}
		class, diagnostic, err := prepareClass(plan, classIndex, intent, collectedClass.Samples)
		if err != nil {
			return PreparedProblem{}, nil, err
		}
		if diagnostic.StopsRun() {
			return PreparedProblem{}, Diagnostics{diagnostic}, nil
		}
		prepared.Classes = append(prepared.Classes, class)
	}
	return prepared, nil, nil
}

// prepareClass chooses the empirical-uniform or atomic-bucket representation,
// then performs prechecks whose answer does not require an LP solve.
func prepareClass(plan ResolvedPlan, classIndex int, intent ClassIntent, samples []CollectedSample) (PreparedClass, Diagnostic, error) {
	class := PreparedClass{
		ID: intent.Name, Index: classIndex, Weight: intent.Weight,
		Probability:  float64(intent.Weight) / ClassWeightBase,
		CollectRange: intent.Collect.WinRange,
		Design:       intent.Design, Intent: intent.Design.Subjective.Enabled(),
	}
	if !class.Intent {
		bucket, diagnostic, err := prepareUniformBucket(plan, class, samples)
		if err != nil || diagnostic.StopsRun() {
			return PreparedClass{}, diagnostic, err
		}
		class.Buckets = []PreparedBucket{bucket}
		return class, Diagnostic{}, nil
	}

	buckets, err := prepareAtomicBuckets(class, samples)
	if err != nil {
		return PreparedClass{}, Diagnostic{}, err
	}
	class.Buckets = buckets
	class.Groups, class.Others = prepareMembership(intent.Design.Subjective, buckets)
	if diagnostic := validateReplayIdentity(class, plan.Plan.Intent); diagnostic.StopsRun() {
		return PreparedClass{}, diagnostic, nil
	}
	if diagnostic := validateIntentSupport(class, plan); diagnostic.StopsRun() {
		return PreparedClass{}, diagnostic, nil
	}
	return class, Diagnostic{}, nil
}

// prepareUniformBucket keeps every outcome at probability 1/N and verifies
// exact mean, lower-median range, and optional fixed-p=1 collision capacity.
// No LP variable is created later for this Class.
func prepareUniformBucket(plan ResolvedPlan, class PreparedClass, samples []CollectedSample) (PreparedBucket, Diagnostic, error) {
	bucket, err := summarizeBucket(0, class.Design, class.Probability, samples, class.Design.Risk)
	if err != nil {
		return PreparedBucket{}, Diagnostic{}, err
	}
	bucket.Lower = samples[0].Win
	bucket.Upper = samples[0].Win
	for _, sample := range samples[1:] {
		bucket.Lower = math.Min(bucket.Lower, sample.Win)
		bucket.Upper = math.Max(bucket.Upper, sample.Win)
	}
	if diagnostic := validateReplayIdentity(PreparedClass{ID: class.ID, Buckets: []PreparedBucket{bucket}}, plan.Plan.Intent); diagnostic.StopsRun() {
		return PreparedBucket{}, diagnostic, nil
	}
	tolerance := plan.EngineOptions.FeasibilityTolerance
	median := empiricalLowerMedian(samples)
	meanOK := math.Abs(bucket.Mean-class.Design.Exp) <= scaledTolerance(tolerance, bucket.Mean, class.Design.Exp)
	medianOK := median+scaledTolerance(tolerance, median, class.Design.Median.Lower()) >= class.Design.Median.Lower() &&
		median-scaledTolerance(tolerance, median, class.Design.Median.Upper()) <= class.Design.Median.Upper()
	if !meanOK || !medianOK {
		return PreparedBucket{}, supportDiagnostic(
			DiagnosticUniformClassMathInfeasible,
			fmt.Sprintf("empirical-uniform class %q has mean %.12g and lower median %.12g outside its exact design math", class.ID, bucket.Mean, median),
			fmt.Sprintf("intents.%s.classes[%d].design", plan.Plan.Intent, class.Index),
		), nil
	}
	if class.Design.Risk != nil && bucket.RiskCap+scaledTolerance(tolerance, bucket.RiskCap, 1) < 1 {
		return PreparedBucket{}, supportDiagnostic(
			DiagnosticUniformClassRiskInfeasible,
			fmt.Sprintf("empirical-uniform class %q requires mass 1 but collision risk cap is %.12g", class.ID, bucket.RiskCap),
			fmt.Sprintf("intents.%s.classes[%d].design.risk", plan.Plan.Intent, class.Index),
		), nil
	}
	return bucket, Diagnostic{}, nil
}

// prepareAtomicBuckets applies [b_i,b_i+1) intervals and closes only the last
// interval at the right endpoint. It preserves sample sequence inside each
// bucket and computes CDF coefficients at the configured median thresholds.
func prepareAtomicBuckets(class PreparedClass, samples []CollectedSample) ([]PreparedBucket, error) {
	boundaries := class.Design.Subjective.Buckets
	bucketSamples := make([][]CollectedSample, len(boundaries)-1)
	for _, sample := range samples {
		index := atomicBucketIndex(boundaries, sample.Win)
		if index < 0 {
			return nil, fmt.Errorf("class %q sample sequence %d with win %.12g falls outside atomic boundaries", class.ID, sample.Sequence, sample.Win)
		}
		bucketSamples[index] = append(bucketSamples[index], sample)
	}
	buckets := make([]PreparedBucket, len(bucketSamples))
	for i := range bucketSamples {
		bucket, err := summarizeBucket(i, class.Design, class.Probability, bucketSamples[i], class.Design.Risk)
		if err != nil {
			return nil, err
		}
		bucket.Lower = boundaries[i]
		bucket.Upper = boundaries[i+1]
		bucket.MainGroup = -1
		buckets[i] = bucket
	}
	return buckets, nil
}

// atomicBucketIndex returns the unique interval containing value according to
// canonical v2 endpoint semantics, or -1 when value is outside the boundaries.
func atomicBucketIndex(boundaries []float64, value float64) int {
	if len(boundaries) < 2 || value < boundaries[0] || value > boundaries[len(boundaries)-1] {
		return -1
	}
	index := sort.Search(len(boundaries)-1, func(i int) bool { return value < boundaries[i+1] })
	if index < len(boundaries)-1 {
		return index
	}
	if value == boundaries[len(boundaries)-1] {
		return len(boundaries) - 2
	}
	return -1
}

// summarizeBucket calculates mean, second moment, median CDF coefficients, and
// the optional birthday/Poisson risk cap from actual unique replay atoms.
func summarizeBucket(index int, design ClassDesign, classProbability float64, samples []CollectedSample, risk *RiskIntent) (PreparedBucket, error) {
	bucket := PreparedBucket{Index: index, Samples: append([]CollectedSample(nil), samples...), RiskCap: math.Inf(1), MainGroup: -1}
	if len(samples) == 0 {
		return bucket, nil
	}
	bucket.Minimum, bucket.Maximum = samples[0].Win, samples[0].Win
	beforeLower, atUpper := 0, 0
	for _, sample := range samples {
		if !isFinite(sample.Win) || len(sample.Snapshot) == 0 {
			return PreparedBucket{}, fmt.Errorf("bucket %d contains a non-finite payout or empty replay snapshot", index)
		}
		bucket.Mean += sample.Win
		bucket.SecondMoment += sample.Win * sample.Win
		bucket.Minimum = math.Min(bucket.Minimum, sample.Win)
		bucket.Maximum = math.Max(bucket.Maximum, sample.Win)
		if sample.Win < design.Median.Lower() {
			beforeLower++
		}
		if sample.Win <= design.Median.Upper() {
			atUpper++
		}
	}
	n := float64(len(samples))
	bucket.Mean /= n
	bucket.SecondMoment /= n
	bucket.CDFBeforeLower = float64(beforeLower) / n
	bucket.CDFAtUpper = float64(atUpper) / n
	if risk != nil {
		pairs := float64(risk.Rounds) * float64(risk.Rounds-1) / 2
		bucket.RiskCap = math.Sqrt(n*(-math.Log1p(-risk.Collision.Max))/pairs) / classProbability
	}
	return bucket, nil
}

// prepareMembership maps each complete atomic bucket to at most one Main Group
// and normalizes Designer prefer weights at group level. Supported non-Main
// buckets become the only supported Other-visibility set.
func prepareMembership(subjective SubjectiveIntent, buckets []PreparedBucket) ([]PreparedGroup, []int) {
	main := subjective.MainExperience
	groups := make([]PreparedGroup, len(main.Groups))
	preferTotal := 0.0
	for _, value := range main.Prefer {
		preferTotal += value
	}
	for groupIndex, groupRange := range main.Groups {
		groups[groupIndex] = PreparedGroup{Index: groupIndex, Range: groupRange, PreferShare: main.Prefer[groupIndex] / preferTotal}
		for bucketIndex := range buckets {
			if buckets[bucketIndex].Lower >= groupRange.Lower() && buckets[bucketIndex].Upper <= groupRange.Upper() {
				buckets[bucketIndex].MainGroup = groupIndex
				groups[groupIndex].BucketIndexes = append(groups[groupIndex].BucketIndexes, bucketIndex)
			}
		}
	}
	others := make([]int, 0)
	for bucketIndex, bucket := range buckets {
		if bucket.MainGroup < 0 && bucket.Supported() {
			others = append(others, bucketIndex)
		}
	}
	return groups, others
}

// validateReplayIdentity rejects duplicate Core snapshots inside an atomic risk
// support set. Payout equality is allowed; only byte-identical replay identity
// would falsely inflate n and understate collision probability.
func validateReplayIdentity(class PreparedClass, intentID string) Diagnostic {
	for bucketIndex, bucket := range class.Buckets {
		seen := make(map[string]uint64, len(bucket.Samples))
		for _, sample := range bucket.Samples {
			identity := string(sample.Snapshot)
			if first, duplicate := seen[identity]; duplicate {
				return supportDiagnostic(
					DiagnosticDuplicateReplayIdentity,
					fmt.Sprintf("class %q bucket %d repeats replay identity at sequences %d and %d", class.ID, bucketIndex, first, sample.Sequence),
					fmt.Sprintf("intents.%s.classes[%d]", intentID, class.Index),
				)
			}
			seen[identity] = sample.Sequence
		}
	}
	return Diagnostic{}
}

// validateIntentSupport proves simple necessary conditions before invoking a
// backend: target mean in the supported bucket-mean hull, enough aggregate
// risk capacity to normalize, and nonempty support for every semantic Main
// Group. LP-coupled median/Main/CV conflicts remain model diagnostics.
func validateIntentSupport(class PreparedClass, plan ResolvedPlan) Diagnostic {
	minimumMean, maximumMean := math.Inf(1), math.Inf(-1)
	capacity := 0.0
	supportedBuckets := 0
	supportedSamples := 0
	for _, bucket := range class.Buckets {
		if !bucket.Supported() {
			continue
		}
		supportedBuckets++
		supportedSamples += len(bucket.Samples)
		minimumMean = math.Min(minimumMean, bucket.Mean)
		maximumMean = math.Max(maximumMean, bucket.Mean)
		capacity += math.Min(1, bucket.RiskCap)
	}
	tolerance := plan.EngineOptions.FeasibilityTolerance
	if math.IsInf(minimumMean, 1) || class.Design.Exp < minimumMean-scaledTolerance(tolerance, class.Design.Exp, minimumMean) ||
		class.Design.Exp > maximumMean+scaledTolerance(tolerance, class.Design.Exp, maximumMean) {
		return supportDiagnostic(
			DiagnosticMeanSupportInfeasible,
			fmt.Sprintf("class %q target mean %.12g is outside supported bucket-mean hull [%.12g, %.12g]", class.ID, class.Design.Exp, minimumMean, maximumMean),
			fmt.Sprintf("intents.%s.classes[%d].design.exp", plan.Plan.Intent, class.Index),
		)
	}
	if capacity+scaledTolerance(tolerance, capacity, 1) < 1 {
		diagnostic := supportDiagnostic(
			DiagnosticRiskCapacityInfeasible,
			fmt.Sprintf("class %q aggregate supported risk capacity %.12g is below required normalized mass 1; the configured collision policy is too restrictive for the collected replay support", class.ID, capacity),
			fmt.Sprintf("intents.%s.classes[%d].design.risk", plan.Plan.Intent, class.Index),
		)
		diagnostic.Requested = &Bound{Min: 1, Max: 1}
		diagnostic.Achievable = &Bound{Min: 0, Max: capacity}
		diagnostic.Deficit = 1 - capacity
		metrics := []NamedValue{
			{Name: "required_normalized_mass", Value: 1},
			{Name: "aggregate_supported_risk_capacity", Value: capacity},
			{Name: "capacity_deficit", Value: 1 - capacity},
			{Name: "class_probability", Value: class.Probability},
			{Name: "supported_buckets", Value: float64(supportedBuckets), Unit: "buckets"},
			{Name: "supported_samples", Value: float64(supportedSamples), Unit: "samples"},
		}
		if class.Design.Risk != nil {
			metrics = append(metrics,
				NamedValue{Name: "risk_rounds", Value: float64(class.Design.Risk.Rounds), Unit: "rounds"},
				NamedValue{Name: "collision_max", Value: class.Design.Risk.Collision.Max},
			)
		}
		diagnostic.Causes = []Cause{{
			Summary:     "the sum of all supported bucket caps cannot hold one complete conditional Class distribution",
			SourcePaths: append([]string(nil), diagnostic.SourcePaths...),
			Metrics:     metrics,
		}}
		return diagnostic
	}
	for _, group := range class.Groups {
		supported := false
		for _, bucketIndex := range group.BucketIndexes {
			supported = supported || class.Buckets[bucketIndex].Supported()
		}
		if !supported {
			return supportDiagnostic(
				DiagnosticMainExperienceSupportInfeasible,
				fmt.Sprintf("class %q Main Group %d has no replayable support", class.ID, group.Index),
				fmt.Sprintf("intents.%s.classes[%d].design.subjective.main_experience.groups[%d]", plan.Plan.Intent, class.Index, group.Index),
			)
		}
	}
	return Diagnostic{}
}

// empiricalLowerMedian returns the smallest observed payout whose cumulative
// empirical mass is at least one half. Input ordering is not mutated.
func empiricalLowerMedian(samples []CollectedSample) float64 {
	values := make([]float64, len(samples))
	for i, sample := range samples {
		values[i] = sample.Win
	}
	sort.Float64s(values)
	return values[(len(values)-1)/2]
}

// supportDiagnostic centralizes the public status/representation assigned to
// deterministic post-collection feasibility failures.
func supportDiagnostic(code DiagnosticCode, message, sourcePath string) Diagnostic {
	return Diagnostic{
		Code: code, Status: StatusInfeasibleSupport, Message: message,
		SourcePaths: []string{sourcePath}, Representation: RepresentationAtomicBuckets,
	}
}
