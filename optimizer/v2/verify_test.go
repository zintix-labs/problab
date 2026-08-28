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
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

// TestExpandSolutionAppliesClassProbabilityOnceAndRestoresSequenceOrder verifies
// bucket mass becomes per-sample artifact mass exactly once in deterministic order.
func TestExpandSolutionAppliesClassProbabilityOnceAndRestoresSequenceOrder(t *testing.T) {
	compiled, solution := verificationTestFixture(t)
	samples, err := ExpandSolution(compiled, solution, 0, 10)
	if err != nil {
		t.Fatalf("ExpandSolution: %v", err)
	}
	if got, want := len(samples), 5; got != want {
		t.Fatalf("expanded sample count=%d want=%d", got, want)
	}

	gotOrder := make([]byte, len(samples))
	gotClasses := make([]string, len(samples))
	for i, sample := range samples {
		gotOrder[i] = sample.Snapshot[1]
		gotClasses[i] = sample.ClassID
	}
	if want := []byte{1, 3, 5, 2, 4}; !reflect.DeepEqual(gotOrder, want) {
		t.Fatalf("snapshot sequence order=%v want=%v", gotOrder, want)
	}
	if want := []string{"controlled", "controlled", "controlled", "empirical", "empirical"}; !reflect.DeepEqual(gotClasses, want) {
		t.Fatalf("Class declaration order=%v want=%v", gotClasses, want)
	}

	wantProbability := []float64{0.1, 0.2, 0.1, 0.3, 0.3}
	for i := range samples {
		verifyTestClose(t, "expanded probability", samples[i].Probability, wantProbability[i])
	}
	// controlled bucket 0 has c=0.4, p=0.5, n=2, hence 0.1.
	// Applying c a second time would incorrectly produce 0.04.
	if samples[0].Probability == 0.04 {
		t.Fatal("intent:true expansion multiplied Class probability twice")
	}

	solution.Primary[0] = math.NaN()
	if _, err := ExpandSolution(compiled, solution, 0, 10); err == nil {
		t.Fatal("ExpandSolution accepted a non-finite primary mass")
	}
}

// TestVerifyMaterializedReplaysAliasMarginalsThroughHardModel proves verification
// derives true alias outcomes and rechecks them against every hard semantic row.
func TestVerifyMaterializedReplaysAliasMarginalsThroughHardModel(t *testing.T) {
	compiled, solution := verificationTestFixture(t)
	samples, err := ExpandSolution(compiled, solution, 0, 10)
	if err != nil {
		t.Fatalf("ExpandSolution: %v", err)
	}
	mode, err := MaterializeMode(0, 10, samples)
	if err != nil {
		t.Fatalf("MaterializeMode: %v", err)
	}
	if mode.Picker.Prob[0] == mode.EffectiveProbabilities[0] {
		t.Fatal("fixture did not distinguish alias threshold from outcome marginal")
	}

	report := VerifyMaterialized(compiled, solution, mode)
	if !report.Pass {
		for _, check := range report.Checks {
			if !check.Pass {
				t.Logf("failed check: %+v", check)
			}
		}
		t.Fatal("alias-effective semantic replay did not pass")
	}
	for _, name := range []string{
		"artifact.structure_and_seed_alignment",
		"artifact.alias_effective_probability_replay",
		"class.controlled.bucket.0000.conditional_mass",
		"hard.semantic_row_and_bound_replay",
		"class.controlled.unconditional_probability",
		"class.empirical.unconditional_probability",
		"overall.expected_rtp",
		"overall.second_moment_and_cv",
	} {
		check, found := verificationTestCheck(report, name)
		if !found || !check.Pass {
			t.Fatalf("verification check %q = %+v found=%v", name, check, found)
		}
	}

	// EffectiveProbabilities is redundant metadata. Corrupting it must fail the
	// artifact helper check, while the hard replay still succeeds from Picker.
	mode.EffectiveProbabilities[0] += 0.01
	corrupt := VerifyMaterialized(compiled, solution, mode)
	if corrupt.Pass {
		t.Fatal("verification accepted corrupted effective-probability metadata")
	}
	structure, _ := verificationTestCheck(corrupt, "artifact.structure_and_seed_alignment")
	hard, _ := verificationTestCheck(corrupt, "hard.semantic_row_and_bound_replay")
	if structure.Pass || !hard.Pass {
		t.Fatalf("corrupt metadata checks: structure=%+v hard=%+v", structure, hard)
	}
}

// TestBuildIntentQualityReportSeparatesMainAndOtherMetrics locks reporting semantics
// so Main profile quality and Other visibility are not collapsed into one claim.
func TestBuildIntentQualityReportSeparatesMainAndOtherMetrics(t *testing.T) {
	compiled, solution := intentReportTestFixture()
	report := BuildIntentQualityReport(compiled, solution)
	if report.PhaseA.FixedValue != 0.125 || report.PhaseB.FixedValue != 0.75 {
		t.Fatalf("bisection reports were not preserved: PhaseA=%+v PhaseB=%+v", report.PhaseA, report.PhaseB)
	}
	if !reflect.DeepEqual(report.PhaseA, report.MainProfileOptimization) || !reflect.DeepEqual(report.PhaseB, report.OtherBucketVisibilityOptimization) {
		t.Fatal("deprecated intent report aliases differ from canonical fields")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"main_profile_optimization"`, `"other_bucket_visibility_optimization"`,
		`"main_group_internal_visibility_optimization"`, `"canonical_bucket_probability_selection"`,
		`"phase_a_profile"`, `"phase_b_other_visibility"`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("intent report JSON %s missing key %s", raw, key)
		}
	}
	if got, want := len(report.Classes), 2; got != want {
		t.Fatalf("Class report count=%d want=%d", got, want)
	}

	controlled := report.Classes[0]
	verifyTestClose(t, "Main total", controlled.MainTotal, 0.5)
	if want := []float64{0.6, 0.4}; !reflect.DeepEqual(controlled.WantedMainProfile, want) {
		t.Fatalf("wanted Main profile=%v want=%v", controlled.WantedMainProfile, want)
	}
	for i, want := range []float64{0.6, 0.4} {
		verifyTestClose(t, "actual Main profile", controlled.ActualMainProfile[i], want)
	}
	verifyTestClose(t, "Main relative deviation", controlled.MainRelativeDeviation, 0)
	if got, want := controlled.UnconstrainedDimensions, []string{"main_group[0].internal_atomic_bucket_shape"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unconstrained dimensions=%v want=%v", got, want)
	}
	if got, want := len(controlled.MainGroupVisibility), 2; got != want {
		t.Fatalf("Main Group visibility reports=%d want=%d", got, want)
	}
	firstMain := controlled.MainGroupVisibility[0]
	if !firstMain.Applicable || firstMain.SupportedCount != 2 {
		t.Fatalf("first Main Group visibility=%+v", firstMain)
	}
	verifyTestClose(t, "first Main Group total", firstMain.GroupTotal, 0.3)
	verifyTestClose(t, "first Main Group minimum share", firstMain.MinimumShare, 1.0/3)
	verifyTestClose(t, "first Main Group retention", firstMain.Retention, 2.0/3)
	if controlled.MainGroupVisibility[1].Applicable || controlled.MainGroupVisibility[1].InapplicableReason != "fewer-than-two-supported-buckets" {
		t.Fatalf("singleton Main Group visibility=%+v", controlled.MainGroupVisibility[1])
	}
	if got := controlled.RemainingDegreesOfFreedom; len(got) != 1 || got[0].State != "unconstrained" {
		t.Fatalf("remaining Main Group freedom=%+v", got)
	}

	other := controlled.OtherVisibility
	if !other.Applicable {
		t.Fatal("supported nonzero Others were reported not applicable")
	}
	verifyTestClose(t, "Other total", other.OtherTotal, 0.5)
	verifyTestClose(t, "perfect uniform Other share", other.PerfectUniformShare, 0.5)
	verifyTestClose(t, "Other bucket Class retention", other.ClassRetention, 0.8)
	verifyTestClose(t, "report-only Other uniformity", other.UniformityRetentionReport, 0.9)
	if got, want := len(other.Buckets), 2; got != want {
		t.Fatalf("Other bucket report count=%d want=%d", got, want)
	}
	verifyTestClose(t, "first Other mass", other.Buckets[0].Mass, 0.3)
	verifyTestClose(t, "first Other relative share", other.Buckets[0].RelativeShare, 0.6)
	verifyTestClose(t, "first Other risk cap", other.Buckets[0].RiskCap, 0.35)
	if report.Classes[1].OtherVisibility.Applicable {
		t.Fatal("Class without Others fabricated an Other ratio")
	}
	compiled.Prepared.Classes[0].Buckets[1].Samples = nil
	emptyReport := BuildIntentQualityReport(compiled, solution).Classes[0].MainGroupVisibility[0]
	if emptyReport.SupportedCount != 1 || emptyReport.Buckets[1].Supported || emptyReport.Buckets[1].Mass != 0 || emptyReport.InapplicableReason != "fewer-than-two-supported-buckets" {
		t.Fatalf("unsupported Main bucket report=%+v", emptyReport)
	}
}

func TestMainGroupVisibilityReportDistinguishesVisibilityFloorEqualizationAndZeroTotal(t *testing.T) {
	compiled, solution := intentReportTestFixture()
	solution.Primary[0], solution.Primary[1] = 0.15, 0.15
	solution.MainGroupInternalVisibilityOptimization = BisectionReport{Applicable: true, FixedValue: 1}
	report := BuildIntentQualityReport(compiled, solution)
	freedom := report.Classes[0].RemainingDegreesOfFreedom
	if len(freedom) != 1 || freedom[0].State != "fully-equalized" {
		t.Fatalf("rho=1 freedom=%+v", freedom)
	}
	if report.Classes[0].MainGroupVisibility[0].Retention != 1 {
		t.Fatalf("equal Main Group retention=%g", report.Classes[0].MainGroupVisibility[0].Retention)
	}
	if len(report.Classes[0].UnconstrainedDimensions) != 0 {
		t.Fatalf("rho=1 legacy unconstrained view=%v", report.Classes[0].UnconstrainedDimensions)
	}

	solution.MainGroupInternalVisibilityOptimization.FixedValue = 0.5
	report = BuildIntentQualityReport(compiled, solution)
	if got := report.Classes[0].RemainingDegreesOfFreedom[0]; got.State != "visibility-floor-only" || got.Constraint == "" {
		t.Fatalf("rho=0.5 freedom=%+v", got)
	}

	solution.Primary[0], solution.Primary[1] = 0, 0
	report = BuildIntentQualityReport(compiled, solution)
	group := report.Classes[0].MainGroupVisibility[0]
	if group.Applicable || group.InapplicableReason != "main-group-total-not-positive" {
		t.Fatalf("zero-total Main Group=%+v", group)
	}
	// The model-wide rho lock is still 0.5 here, but this group's supported mass
	// is zero, so its visibility row p_i >= share*0 constrains nothing. The
	// freedom report must say so instead of echoing the shared floor.
	zeroFreedom := report.Classes[0].RemainingDegreesOfFreedom
	if len(zeroFreedom) != 1 || zeroFreedom[0].State != "unconstrained" || zeroFreedom[0].Constraint != "" {
		t.Fatalf("zero-total Main Group must report an unconstrained internal shape, got %+v", zeroFreedom)
	}
	if got := report.Classes[0].UnconstrainedDimensions; len(got) != 1 || got[0] != zeroFreedom[0].Path {
		t.Fatalf("zero-total Main Group path must appear in the legacy unconstrained view, got %v", got)
	}
}

func TestOtherVisibilityReportUsesConfiguredFeasibilityTolerance(t *testing.T) {
	class := PreparedClass{
		Others: []int{1},
		Buckets: []PreparedBucket{
			{Samples: []CollectedSample{{Snapshot: []byte{0}}}},
			{Samples: []CollectedSample{{Snapshot: []byte{1}}}},
		},
	}
	masses := []float64{0.9999, 0.0001}
	if report := buildOtherVisibilityReport(class, masses, 0.9999, 1e-3); report.Applicable {
		t.Fatalf("Other visibility should be inapplicable within configured tolerance: %+v", report)
	}
	if report := buildOtherVisibilityReport(class, masses, 0.9999, 1e-6); !report.Applicable {
		t.Fatalf("Other visibility should be measurable above configured tolerance: %+v", report)
	}
}

// verificationTestFixture creates a two-Class distribution with one
// LP-controlled Class and one empirical-uniform Class. Its nonuniform alias
// weights make threshold-versus-marginal confusion observable in verification.
func verificationTestFixture(t *testing.T) (CompiledModel, EngineSolution) {
	t.Helper()
	enabled, disabled := true, false
	controlled := PreparedClass{
		ID: "controlled", Index: 0, Weight: 400_000, Probability: 0.4, Intent: true,
		Design: ClassDesign{
			Exp: 2, Median: ClosedInterval{0, 4},
			Subjective: SubjectiveIntent{Intent: &enabled, MainExperience: &MainExperience{
				Probability: NumericRange{Min: 0.5, Max: 0.5}, Prefer: []float64{1},
			}},
		},
		Buckets: []PreparedBucket{
			{
				Index: 0, Samples: []CollectedSample{
					{ClassID: "controlled", Win: 1, Snapshot: []byte{1, 5}, Sequence: 5},
					{ClassID: "controlled", Win: 1, Snapshot: []byte{1, 1}, Sequence: 1},
				},
				Mean: 1, SecondMoment: 1, CDFAtUpper: 1, RiskCap: math.Inf(1), MainGroup: 0,
			},
			{
				Index: 1, Samples: []CollectedSample{
					{ClassID: "controlled", Win: 3, Snapshot: []byte{1, 3}, Sequence: 3},
				},
				Mean: 3, SecondMoment: 9, CDFAtUpper: 1, RiskCap: math.Inf(1), MainGroup: -1,
			},
		},
		Groups: []PreparedGroup{{Index: 0, BucketIndexes: []int{0}, PreferShare: 1}},
		Others: []int{1},
	}
	empirical := PreparedClass{
		ID: "empirical", Index: 1, Weight: 600_000, Probability: 0.6, Intent: false,
		Design: ClassDesign{Exp: 1, Median: ClosedInterval{0, 2}, Subjective: SubjectiveIntent{Intent: &disabled}},
		Buckets: []PreparedBucket{{
			Index: 0,
			Samples: []CollectedSample{
				{ClassID: "empirical", Win: 0, Snapshot: []byte{2, 4}, Sequence: 4},
				{ClassID: "empirical", Win: 2, Snapshot: []byte{2, 2}, Sequence: 2},
			},
			Mean: 1, SecondMoment: 2, RiskCap: math.Inf(1), MainGroup: -1,
		}},
	}
	mean, secondMoment := 1.4, 3.2
	cv := math.Sqrt(secondMoment-mean*mean) / mean
	prepared := PreparedProblem{
		BetMode: 0, BetUnit: 10,
		Plan: ResolvedPlan{
			Version:       ConfigVersion,
			Plan:          RunPlan{ID: "verify-test", Intent: "verify-test"},
			Intent:        MathIntent{Overall: OverallIntent{CV: NumericRange{Min: cv, Max: cv}}},
			EngineOptions: DefaultEngineOptions(),
		},
		Classes: []PreparedClass{controlled, empirical},
	}
	compiled, diagnostics, err := CompileHardModel(prepared)
	if err != nil {
		t.Fatalf("CompileHardModel: %v", err)
	}
	if diagnostics.StopsRun() {
		t.Fatalf("CompileHardModel diagnostics: %+v", diagnostics)
	}
	return compiled, EngineSolution{
		Status:  StatusOptimal,
		Problem: compiled.Hard,
		Values:  []float64{0.5, 0.5},
		Primary: []float64{0.5, 0.5},
	}
}

// intentReportTestFixture isolates reporting semantics from LP feasibility so
// the test can exercise a multi-bucket Main Group and unequal supported Others.
func intentReportTestFixture() (CompiledModel, EngineSolution) {
	enabled := true
	controlled := PreparedClass{
		ID: "quality", Index: 0, Probability: 1, Intent: true,
		Design: ClassDesign{Subjective: SubjectiveIntent{Intent: &enabled}},
		Buckets: []PreparedBucket{
			{Index: 0, Samples: []CollectedSample{{Snapshot: []byte{0}}}, RiskCap: math.Inf(1), MainGroup: 0},
			{Index: 1, Samples: []CollectedSample{{Snapshot: []byte{1}}}, RiskCap: math.Inf(1), MainGroup: 0},
			{Index: 2, Samples: []CollectedSample{{Snapshot: []byte{2}}}, RiskCap: math.Inf(1), MainGroup: 1},
			{Index: 3, Samples: []CollectedSample{{Snapshot: []byte{3}}}, RiskCap: 0.35, MainGroup: -1},
			{Index: 4, Samples: []CollectedSample{{Snapshot: []byte{4}}}, RiskCap: math.Inf(1), MainGroup: -1},
		},
		Groups: []PreparedGroup{
			{Index: 0, BucketIndexes: []int{0, 1}, PreferShare: 0.6},
			{Index: 1, BucketIndexes: []int{2}, PreferShare: 0.4},
		},
		Others: []int{3, 4},
	}
	noOthers := PreparedClass{
		ID: "no-others", Index: 1, Probability: 1, Intent: true,
		Design:  ClassDesign{Subjective: SubjectiveIntent{Intent: &enabled}},
		Buckets: []PreparedBucket{{Index: 0, Samples: []CollectedSample{{Snapshot: []byte{5}}}, RiskCap: math.Inf(1), MainGroup: 0}},
		Groups:  []PreparedGroup{{Index: 0, BucketIndexes: []int{0}, PreferShare: 1}},
	}
	classes := []PreparedClass{controlled, noOthers}
	primary := make([]PrimaryVariable, 0, 6)
	for classIndex, class := range classes {
		for bucketIndex := range class.Buckets {
			primary = append(primary, PrimaryVariable{
				ID:         VariableID("report-primary-" + string(rune('a'+len(primary)))),
				ClassIndex: classIndex, BucketIndex: bucketIndex,
			})
		}
	}
	compiled := CompiledModel{Prepared: PreparedProblem{Classes: classes}, Primary: primary}
	solution := EngineSolution{
		Status:                            StatusOptimal,
		Primary:                           []float64{0.2, 0.1, 0.2, 0.3, 0.2, 1},
		MainProfileOptimization:           BisectionReport{Objective: "delta", Direction: "minimize", FixedValue: 0.125},
		OtherBucketVisibilityOptimization: BisectionReport{Objective: "rho", Direction: "maximize", FixedValue: 0.75},
	}
	solution.PhaseA = solution.MainProfileOptimization
	solution.PhaseB = solution.OtherBucketVisibilityOptimization
	return compiled, solution
}

// verificationTestCheck locates a named check without relying on an incidental
// numeric index while still letting production preserve stable check order.
func verificationTestCheck(report VerificationReport, name string) (VerificationCheck, bool) {
	for _, check := range report.Checks {
		if check.Name == name {
			return check, true
		}
	}
	return VerificationCheck{}, false
}

// verifyTestClose compares deterministic report values at tighter precision
// than the production feasibility allowance.
func verifyTestClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("%s=%.17g want=%.17g", name, got, want)
	}
}
