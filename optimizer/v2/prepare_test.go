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
	"math"
	"reflect"
	"testing"
)

// TestPrepareProblemAtomicBucketsUseHalfOpenIntervalsAndCloseOnlyLastEndpoint locks
// boundary ownership so adjacent buckets never double-count or drop a payout.
func TestPrepareProblemAtomicBucketsUseHalfOpenIntervalsAndCloseOnlyLastEndpoint(t *testing.T) {
	class := prepareTestIntentClass(
		"boundary",
		[]float64{0, 1, 2},
		[]ClosedInterval{{0, 2}},
		1,
		ClosedInterval{0, 2},
	)
	samples := []CollectedSample{
		{ClassID: class.Name, Win: 0, Snapshot: []byte{0}, Sequence: 0},
		{ClassID: class.Name, Win: 0.999, Snapshot: []byte{1}, Sequence: 1},
		{ClassID: class.Name, Win: 1, Snapshot: []byte{2}, Sequence: 2},
		{ClassID: class.Name, Win: 2, Snapshot: []byte{3}, Sequence: 3},
	}
	class.Collect.Samples = uint64(len(samples))

	prepared := prepareTestProblem(t, prepareTestPlan(class), []CollectedClass{{Intent: class, Samples: samples}})
	if got, want := len(prepared.Classes[0].Buckets), 2; got != want {
		t.Fatalf("bucket count = %d, want %d", got, want)
	}
	if got, want := prepareTestSequences(prepared.Classes[0].Buckets[0].Samples), []uint64{0, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first bucket sequences = %v, want %v", got, want)
	}
	if got, want := prepareTestSequences(prepared.Classes[0].Buckets[1].Samples), []uint64{2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second bucket sequences = %v, want %v; the shared boundary must belong to the right bucket and the final endpoint to the last bucket", got, want)
	}
}

// TestPrepareProblemAtomicStatisticsUseObservedPayoutsForMeanMomentAndCDF proves LP
// coefficients come from empirical samples rather than configured bucket midpoints.
func TestPrepareProblemAtomicStatisticsUseObservedPayoutsForMeanMomentAndCDF(t *testing.T) {
	class := prepareTestIntentClass(
		"statistics",
		[]float64{0, 10, 20},
		[]ClosedInterval{{0, 20}},
		5,
		ClosedInterval{2, 14},
	)
	samples := []CollectedSample{
		{ClassID: class.Name, Win: 1, Snapshot: []byte{0}, Sequence: 0},
		{ClassID: class.Name, Win: 3, Snapshot: []byte{1}, Sequence: 1},
		{ClassID: class.Name, Win: 10, Snapshot: []byte{2}, Sequence: 2},
		{ClassID: class.Name, Win: 14, Snapshot: []byte{3}, Sequence: 3},
		{ClassID: class.Name, Win: 20, Snapshot: []byte{4}, Sequence: 4},
	}
	class.Collect.Samples = uint64(len(samples))

	prepared := prepareTestProblem(t, prepareTestPlan(class), []CollectedClass{{Intent: class, Samples: samples}})
	first, second := prepared.Classes[0].Buckets[0], prepared.Classes[0].Buckets[1]
	prepareTestClose(t, "first mean", first.Mean, 2)
	prepareTestClose(t, "first second moment", first.SecondMoment, 5)
	prepareTestClose(t, "first CDF before median lower", first.CDFBeforeLower, 0.5)
	prepareTestClose(t, "first CDF at median upper", first.CDFAtUpper, 1)
	prepareTestClose(t, "second mean", second.Mean, 44.0/3.0)
	prepareTestClose(t, "second second moment", second.SecondMoment, 696.0/3.0)
	prepareTestClose(t, "second CDF before median lower", second.CDFBeforeLower, 0)
	prepareTestClose(t, "second CDF at median upper", second.CDFAtUpper, 2.0/3.0)

	if first.Mean == 5 || second.Mean == 15 {
		t.Fatal("atomic statistics unexpectedly use configured interval midpoints instead of observed payouts")
	}
}

// TestPrepareProblemRejectsDuplicateReplayIdentityWithinAtomicSupport prevents one
// RNG snapshot from representing contradictory outcomes in the artifact support.
func TestPrepareProblemRejectsDuplicateReplayIdentityWithinAtomicSupport(t *testing.T) {
	class := prepareTestIntentClass(
		"duplicates",
		[]float64{0, 2},
		[]ClosedInterval{{0, 2}},
		1,
		ClosedInterval{0, 2},
	)
	class.Collect.Samples = 2
	sharedSnapshot := []byte{7, 8, 9}
	collected := CollectedProblem{
		BetUnit: 1,
		Classes: []CollectedClass{{
			Intent: class,
			Samples: []CollectedSample{
				{ClassID: class.Name, Win: 0.5, Snapshot: append([]byte(nil), sharedSnapshot...), Sequence: 11},
				{ClassID: class.Name, Win: 1.5, Snapshot: append([]byte(nil), sharedSnapshot...), Sequence: 29},
			},
		}},
	}

	prepared, diagnostics, err := PrepareProblem(prepareTestPlan(class), collected)
	if err != nil {
		t.Fatalf("PrepareProblem returned operational error: %v", err)
	}
	if len(prepared.Classes) != 0 {
		t.Fatalf("PrepareProblem returned %d prepared classes after duplicate replay identity", len(prepared.Classes))
	}
	if got, want := len(diagnostics), 1; got != want {
		t.Fatalf("diagnostic count = %d, want %d", got, want)
	}
	if diagnostics[0].Code != DiagnosticDuplicateReplayIdentity || diagnostics[0].Status != StatusInfeasibleSupport {
		t.Fatalf("diagnostic = (%s, %s), want (%s, %s)", diagnostics[0].Code, diagnostics[0].Status, DiagnosticDuplicateReplayIdentity, StatusInfeasibleSupport)
	}
}

// TestPrepareProblemIntentFalseUsesEmpiricalUniformMathWithoutHiddenRiskCap verifies
// an uncontrolled Class stays empirical-uniform and gains no implicit constraint.
func TestPrepareProblemIntentFalseUsesEmpiricalUniformMathWithoutHiddenRiskCap(t *testing.T) {
	disabled := false
	class := ClassIntent{
		Name:   "empirical",
		Weight: ClassWeightBase,
		Collect: CollectIntent{
			Samples:  2,
			WinRange: ClosedInterval{0, 4},
		},
		Design: ClassDesign{
			Exp:        2,
			Median:     ClosedInterval{1, 1},
			Subjective: SubjectiveIntent{Intent: &disabled},
			Risk:       nil,
		},
	}
	samples := []CollectedSample{
		{ClassID: class.Name, Win: 1, Snapshot: []byte{1}, Sequence: 0},
		{ClassID: class.Name, Win: 3, Snapshot: []byte{2}, Sequence: 1},
	}

	prepared := prepareTestProblem(t, prepareTestPlan(class), []CollectedClass{{Intent: class, Samples: samples}})
	gotClass := prepared.Classes[0]
	if gotClass.Intent {
		t.Fatal("intent:false class was prepared as an LP-controlled class")
	}
	if got, want := len(gotClass.Buckets), 1; got != want {
		t.Fatalf("empirical bucket count = %d, want %d", got, want)
	}
	bucket := gotClass.Buckets[0]
	prepareTestClose(t, "empirical mean", bucket.Mean, 2)
	prepareTestClose(t, "empirical second moment", bucket.SecondMoment, 5)
	if !math.IsInf(bucket.RiskCap, 1) {
		t.Fatalf("risk-omitted intent:false RiskCap = %g, want +Inf (no hidden cap)", bucket.RiskCap)
	}
}

// TestPrepareProblemIntentFalseEnforcesOnlyExplicitCollisionRisk confirms an omitted
// risk policy is inert while an explicit impossible collision cap stops preparation.
func TestPrepareProblemIntentFalseEnforcesOnlyExplicitCollisionRisk(t *testing.T) {
	disabled := false
	class := ClassIntent{
		Name:   "empirical-risk",
		Weight: ClassWeightBase,
		Collect: CollectIntent{
			Samples:  2,
			WinRange: ClosedInterval{0, 4},
		},
		Design: ClassDesign{
			Exp:        2,
			Median:     ClosedInterval{1, 1},
			Subjective: SubjectiveIntent{Intent: &disabled},
			Risk: &RiskIntent{
				Rounds:    100,
				Collision: CollisionIntent{Max: 0.01},
			},
		},
	}
	collected := CollectedProblem{
		BetUnit: 1,
		Classes: []CollectedClass{{
			Intent: class,
			Samples: []CollectedSample{
				{ClassID: class.Name, Win: 1, Snapshot: []byte{1}, Sequence: 0},
				{ClassID: class.Name, Win: 3, Snapshot: []byte{2}, Sequence: 1},
			},
		}},
	}

	_, diagnostics, err := PrepareProblem(prepareTestPlan(class), collected)
	if err != nil {
		t.Fatalf("PrepareProblem returned operational error: %v", err)
	}
	if got, want := len(diagnostics), 1; got != want {
		t.Fatalf("diagnostic count = %d, want %d", got, want)
	}
	if diagnostics[0].Code != DiagnosticUniformClassRiskInfeasible {
		t.Fatalf("diagnostic code = %s, want %s", diagnostics[0].Code, DiagnosticUniformClassRiskInfeasible)
	}
}

// TestRiskCapacityDiagnosticReportsRequestedAchievableAndInputs ensures a
// post-collection risk contradiction explains both sides of the inequality and
// the effective policy values instead of returning only a generic support code.
func TestRiskCapacityDiagnosticReportsRequestedAchievableAndInputs(t *testing.T) {
	risk := &RiskIntent{Rounds: 100, Collision: CollisionIntent{Max: 0.01}}
	class := PreparedClass{
		ID: "risk-detail", Index: 3, Probability: 0.25,
		Design: ClassDesign{Exp: 1, Risk: risk},
		Buckets: []PreparedBucket{
			{Mean: 1, RiskCap: 0.2, Samples: []CollectedSample{{Snapshot: []byte{1}}}},
			{Mean: 1, RiskCap: 0.3, Samples: []CollectedSample{{Snapshot: []byte{2}}, {Snapshot: []byte{3}}}},
		},
	}
	plan := prepareTestPlan()
	plan.Plan.Intent = "risk-intent"

	diagnostic := validateIntentSupport(class, plan)
	if diagnostic.Code != DiagnosticRiskCapacityInfeasible || diagnostic.Status != StatusInfeasibleSupport {
		t.Fatalf("diagnostic = %+v, want RiskCapacityInfeasible", diagnostic)
	}
	if diagnostic.Requested == nil || diagnostic.Requested.Min != 1 || diagnostic.Requested.Max != 1 {
		t.Fatalf("requested mass = %+v, want [1,1]", diagnostic.Requested)
	}
	if diagnostic.Achievable == nil || math.Abs(diagnostic.Achievable.Max-0.5) > 1e-12 || math.Abs(diagnostic.Deficit-0.5) > 1e-12 {
		t.Fatalf("achievable/deficit = %+v/%g, want max 0.5 and deficit 0.5", diagnostic.Achievable, diagnostic.Deficit)
	}
	if len(diagnostic.Causes) != 1 {
		t.Fatalf("causes = %+v, want one structured explanation", diagnostic.Causes)
	}
	wantMetrics := map[string]float64{
		"class_probability": 0.25,
		"supported_buckets": 2,
		"supported_samples": 3,
		"risk_rounds":       100,
		"collision_max":     0.01,
	}
	for _, metric := range diagnostic.Causes[0].Metrics {
		if want, exists := wantMetrics[metric.Name]; exists {
			if math.Abs(metric.Value-want) > 1e-12 {
				t.Fatalf("metric %s = %g, want %g", metric.Name, metric.Value, want)
			}
			delete(wantMetrics, metric.Name)
		}
	}
	if len(wantMetrics) != 0 {
		t.Fatalf("diagnostic omitted metrics: %v", wantMetrics)
	}
}

// prepareTestIntentClass creates a controlled Class fixture whose Main preferences
// are deliberately uniform, leaving each test to vary only its targeted invariant.
func prepareTestIntentClass(name string, boundaries []float64, groups []ClosedInterval, exp float64, median ClosedInterval) ClassIntent {
	enabled := true
	prefer := make([]float64, len(groups))
	for i := range prefer {
		prefer[i] = 1
	}
	return ClassIntent{
		Name:   name,
		Weight: ClassWeightBase,
		Collect: CollectIntent{
			WinRange: ClosedInterval{boundaries[0], boundaries[len(boundaries)-1]},
		},
		Design: ClassDesign{
			Exp:    exp,
			Median: median,
			Subjective: SubjectiveIntent{
				Intent:  &enabled,
				Buckets: append([]float64(nil), boundaries...),
				MainExperience: &MainExperience{
					Groups:      append([]ClosedInterval(nil), groups...),
					Probability: NumericRange{Min: 0, Max: 1},
					Prefer:      prefer,
				},
			},
		},
	}
}

// prepareTestPlan wraps Class fixtures in the smallest valid resolved v2 plan so
// preparation tests exercise production defaults without YAML parsing noise.
func prepareTestPlan(classes ...ClassIntent) ResolvedPlan {
	return ResolvedPlan{
		Version: ConfigVersion,
		Plan: RunPlan{
			ID:         "prepare-test",
			Engine:     EngineIntentLPV2,
			Intent:     "test-intent",
			Target:     Target{BetModes: []int{0}},
			Collection: CollectionOptions{Workers: 1, BatchSize: 1, MaxSpins: 1},
			CandidateSelection: CandidateSelectionOptions{
				Evaluator:     "none",
				MaxCandidates: 1,
			},
			Output: OutputOptions{Format: []OutputFormat{OutputOptimalArtifactV1}, Directory: "prepare-test-output"},
		},
		Intent: MathIntent{
			Overall: OverallIntent{CV: NumericRange{Min: 0, Max: 10}},
			Classes: append([]ClassIntent(nil), classes...),
		},
		EngineOptions: DefaultEngineOptions(),
	}
}

// prepareTestProblem invokes the production preparation gate and requires a usable
// result, keeping success-path tests focused on their prepared mathematical data.
func prepareTestProblem(t *testing.T, plan ResolvedPlan, classes []CollectedClass) PreparedProblem {
	t.Helper()
	prepared, diagnostics, err := PrepareProblem(plan, CollectedProblem{
		BetUnit: 1,
		Classes: classes,
	})
	if err != nil {
		t.Fatalf("PrepareProblem returned operational error: %v", err)
	}
	if diagnostics.StopsRun() {
		t.Fatalf("PrepareProblem returned stopping diagnostics: %+v", diagnostics)
	}
	return prepared
}

// prepareTestSequences extracts replay sequence identifiers to make deterministic
// bucket membership and ordering assertions concise and unambiguous.
func prepareTestSequences(samples []CollectedSample) []uint64 {
	sequences := make([]uint64, len(samples))
	for i, sample := range samples {
		sequences[i] = sample.Sequence
	}
	return sequences
}

// prepareTestClose compares derived statistics at a precision tight enough to catch
// accidental midpoint, normalization, or endpoint changes in preparation math.
func prepareTestClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("%s = %.15g, want %.15g", name, got, want)
	}
}
