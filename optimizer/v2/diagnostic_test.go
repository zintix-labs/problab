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
	"math"
	"strings"
	"testing"
)

// TestHardInfeasibilityReportsAchievableMainBound verifies that diagnostic
// solves remove only the Main family and report the resulting exact achievable
// interval instead of stopping at a generic backend infeasible code.
func TestHardInfeasibilityReportsAchievableMainBound(t *testing.T) {
	prepared := diagnosticPreparedProblem(NumericRange{Min: 0.5, Max: 0.5}, NumericRange{Min: 0, Max: 10}, 1)
	solution := solveDiagnosticFixture(t, prepared)
	if solution.Status != StatusInfeasibleModel || len(solution.Diagnostics) != 1 {
		t.Fatalf("solution=%+v", solution)
	}
	diagnostic := solution.Diagnostics[0]
	if diagnostic.Code != DiagnosticMainProbabilityInfeasible {
		t.Fatalf("diagnostic code=%s want=%s; diagnostic=%+v", diagnostic.Code, DiagnosticMainProbabilityInfeasible, diagnostic)
	}
	if diagnostic.Achievable == nil || math.Abs(diagnostic.Achievable.Min-1) > 1e-9 || math.Abs(diagnostic.Achievable.Max-1) > 1e-9 {
		t.Fatalf("achievable Main bound=%+v, want [1,1]", diagnostic.Achievable)
	}
	if math.Abs(diagnostic.Deficit-0.5) > 1e-9 || len(diagnostic.SourcePaths) == 0 || len(diagnostic.ConstraintIDs) == 0 {
		t.Fatalf("incomplete requested/achievable provenance: %+v", diagnostic)
	}
}

// TestHardInfeasibilityReportsAchievableGlobalCV verifies conversion of an
// auxiliary second-moment range back to the Designer's dimensionless CV unit.
func TestHardInfeasibilityReportsAchievableGlobalCV(t *testing.T) {
	prepared := diagnosticPreparedProblem(NumericRange{Min: 1, Max: 1}, NumericRange{Min: 0, Max: 0}, 0.5)
	solution := solveDiagnosticFixture(t, prepared)
	if solution.Status != StatusInfeasibleModel || len(solution.Diagnostics) != 1 {
		t.Fatalf("solution=%+v", solution)
	}
	diagnostic := solution.Diagnostics[0]
	if diagnostic.Code != DiagnosticGlobalCVInfeasible {
		t.Fatalf("diagnostic code=%s want=%s; diagnostic=%+v", diagnostic.Code, DiagnosticGlobalCVInfeasible, diagnostic)
	}
	wantCV := math.Sqrt(2)
	if diagnostic.Achievable == nil || math.Abs(diagnostic.Achievable.Min-wantCV) > 1e-8 || math.Abs(diagnostic.Achievable.Max-wantCV) > 1e-8 {
		t.Fatalf("achievable CV=%+v, want [%g,%g]", diagnostic.Achievable, wantCV, wantCV)
	}
}

// TestHardInfeasibilityReportsEveryLocallyInfeasibleClass protects the
// multi-conflict case that motivated Class-local diagnosis. Relaxing either
// Class in the full model still leaves the other infeasible, so a full-model
// single-family probe would incorrectly return only a generic failure.
func TestHardInfeasibilityReportsEveryLocallyInfeasibleClass(t *testing.T) {
	prepared := diagnosticPreparedProblem(NumericRange{Min: 0.5, Max: 0.5}, NumericRange{Min: 0, Max: 10}, 1)
	first := prepared.Classes[0]
	first.ID, first.Index, first.Weight, first.Probability = "first", 0, ClassWeightBase/2, 0.5
	second := first
	second.ID, second.Index = "second", 1
	prepared.Classes = []PreparedClass{first, second}

	solution := solveDiagnosticFixture(t, prepared)
	if solution.Status != StatusInfeasibleModel || len(solution.Diagnostics) != 2 {
		t.Fatalf("solution status=%s diagnostics=%+v, want two localized conflicts", solution.Status, solution.Diagnostics)
	}
	for index, classID := range []string{"first", "second"} {
		diagnostic := solution.Diagnostics[index]
		if diagnostic.Code != DiagnosticMainProbabilityInfeasible ||
			!strings.Contains(diagnostic.Message, `class "`+classID+`"`) ||
			!strings.Contains(diagnostic.Message, "raise probability.max to at least 1") {
			t.Fatalf("diagnostic[%d]=%+v, want actionable %s Main bound", index, diagnostic, classID)
		}
		if diagnostic.Requested == nil || diagnostic.Achievable == nil || diagnostic.Deficit <= 0 {
			t.Fatalf("diagnostic[%d] lacks requested/achievable/deficit: %+v", index, diagnostic)
		}
	}
}

// TestHardInfeasibilityMergesAlternativeEditsForOneClass ensures a mean/Main
// conflict is presented as two separately sufficient adjustment paths, not as
// two independent errors that imply both fields must be changed.
func TestHardInfeasibilityMergesAlternativeEditsForOneClass(t *testing.T) {
	prepared := diagnosticPreparedProblem(NumericRange{Min: 0, Max: 0.5}, NumericRange{Min: 0, Max: 10}, 1)
	class := &prepared.Classes[0]
	class.Design.Exp = 1.5
	class.Design.Median = ClosedInterval{0, 3}
	class.Buckets[0].Mean, class.Buckets[0].SecondMoment, class.Buckets[0].MainGroup = 1, 1, 0
	class.Buckets[1].Mean, class.Buckets[1].SecondMoment, class.Buckets[1].MainGroup = 3, 9, -1
	class.Groups[0].BucketIndexes = []int{0}
	class.Others = []int{1}
	solution := solveDiagnosticFixture(t, prepared)
	if solution.Status != StatusInfeasibleModel || len(solution.Diagnostics) != 1 {
		t.Fatalf("solution=%+v", solution)
	}
	diagnostic := solution.Diagnostics[0]
	if diagnostic.Code != DiagnosticHardModelInfeasible ||
		!strings.Contains(diagnostic.Message, "choose one, not all") ||
		!strings.Contains(diagnostic.Message, "raise design.exp") ||
		!strings.Contains(diagnostic.Message, "raise probability.max") {
		t.Fatalf("merged diagnostic does not explain alternative edits: %+v", diagnostic)
	}
	if len(diagnostic.Causes) != 2 {
		t.Fatalf("merged diagnostic causes=%+v, want two structured alternatives", diagnostic.Causes)
	}
}

// TestHardInfeasibilityReportsGuardrailCapacity proves that derived Main-group
// guardrails cannot hide behind a generic Class failure when risk/support upper
// bounds leave less than one unit of normalization-compatible capacity.
func TestHardInfeasibilityReportsGuardrailCapacity(t *testing.T) {
	enabled := true
	prepared := PreparedProblem{
		Plan: ResolvedPlan{
			Plan:          RunPlan{Intent: "guardrail"},
			Intent:        MathIntent{Overall: OverallIntent{CV: NumericRange{Min: 0, Max: 10}}},
			EngineOptions: DefaultEngineOptions(),
		},
		Classes: []PreparedClass{{
			ID: "guardrail-class", Weight: ClassWeightBase, Probability: 1, Intent: true,
			Design: ClassDesign{
				Exp: 1, Median: ClosedInterval{0, 1},
				Subjective: SubjectiveIntent{Intent: &enabled, MainExperience: &MainExperience{
					Groups: []ClosedInterval{{0, 1}, {1, 2}}, Probability: NumericRange{Min: 0, Max: 1}, Prefer: []float64{0.5, 0.5},
				}},
			},
			Buckets: []PreparedBucket{
				{Index: 0, Mean: 1, SecondMoment: 1, CDFAtUpper: 1, RiskCap: 0.3, MainGroup: 0, Samples: []CollectedSample{{Snapshot: []byte{0}}}},
				{Index: 1, Mean: 1, SecondMoment: 1, CDFAtUpper: 1, RiskCap: 0.3, MainGroup: 1, Samples: []CollectedSample{{Snapshot: []byte{1}}}},
				{Index: 2, Mean: 1, SecondMoment: 1, CDFAtUpper: 1, RiskCap: 0.4, MainGroup: -1, Samples: []CollectedSample{{Snapshot: []byte{2}}}},
			},
			Groups: []PreparedGroup{
				{Index: 0, Range: ClosedInterval{0, 1}, BucketIndexes: []int{0}, PreferShare: 0.5},
				{Index: 1, Range: ClosedInterval{1, 2}, BucketIndexes: []int{1}, PreferShare: 0.5},
			},
			Others: []int{2},
		}},
	}

	solution := solveDiagnosticFixture(t, prepared)
	if solution.Status != StatusInfeasibleModel || len(solution.Diagnostics) != 1 {
		t.Fatalf("solution=%+v", solution)
	}
	diagnostic := solution.Diagnostics[0]
	if diagnostic.Code != DiagnosticMainGroupGuardrailInfeasible ||
		diagnostic.Achievable == nil || math.Abs(diagnostic.Achievable.Max-0.9) > 1e-9 ||
		math.Abs(diagnostic.Deficit-0.1) > 1e-9 ||
		!strings.Contains(diagnostic.Message, "increase compatible capacity by at least 0.1") {
		t.Fatalf("guardrail diagnostic is not actionable: %+v", diagnostic)
	}
}

// TestMedianDiagnosticHandlesZeroLowerExpression covers a support-minimum L:
// P(X<L) is identically zero, so the lower diagnostic objective has no terms.
// That exact [0,0] range must not prevent reporting an impossible upper side.
func TestMedianDiagnosticHandlesZeroLowerExpression(t *testing.T) {
	enabled := true
	prepared := PreparedProblem{
		Plan: ResolvedPlan{
			Plan:          RunPlan{Intent: "median-zero"},
			Intent:        MathIntent{Overall: OverallIntent{CV: NumericRange{Min: 0, Max: 10}}},
			EngineOptions: DefaultEngineOptions(),
		},
		Classes: []PreparedClass{{
			ID: "median-zero-class", Weight: ClassWeightBase, Probability: 1, Intent: true,
			Design: ClassDesign{
				Exp: 1, Median: ClosedInterval{0, 1},
				Subjective: SubjectiveIntent{Intent: &enabled, MainExperience: &MainExperience{
					Groups: []ClosedInterval{{0, 3}}, Probability: NumericRange{Min: 1, Max: 1}, Prefer: []float64{1},
				}},
			},
			Buckets: []PreparedBucket{
				{Index: 0, Mean: 1, SecondMoment: 1, CDFBeforeLower: 0, CDFAtUpper: 1, RiskCap: 0.3, MainGroup: 0, Samples: []CollectedSample{{Snapshot: []byte{0}}}},
				{Index: 1, Mean: 1, SecondMoment: 1, CDFBeforeLower: 0, CDFAtUpper: 0, RiskCap: 0.4, MainGroup: 0, Samples: []CollectedSample{{Snapshot: []byte{1}}}},
				{Index: 2, Mean: 1, SecondMoment: 1, CDFBeforeLower: 0, CDFAtUpper: 0, RiskCap: 0.4, MainGroup: 0, Samples: []CollectedSample{{Snapshot: []byte{2}}}},
			},
			Groups: []PreparedGroup{{Index: 0, Range: ClosedInterval{0, 3}, BucketIndexes: []int{0, 1, 2}, PreferShare: 1}},
		}},
	}

	solution := solveDiagnosticFixture(t, prepared)
	if solution.Status != StatusInfeasibleModel || len(solution.Diagnostics) != 1 {
		t.Fatalf("solution=%+v", solution)
	}
	diagnostic := solution.Diagnostics[0]
	if diagnostic.Code != DiagnosticMedianInfeasible ||
		!strings.Contains(diagnostic.Message, "maximum achievable is 0.3") ||
		!strings.Contains(diagnostic.Message, "increase by at least 0.2") {
		t.Fatalf("zero-expression median diagnostic is not actionable: %+v", diagnostic)
	}
}

// solveDiagnosticFixture compiles and executes one hand-built support set while
// preserving the same production Gonum adapter and stage-aware Engine mapping.
func solveDiagnosticFixture(t *testing.T, prepared PreparedProblem) EngineSolution {
	t.Helper()
	compiled, diagnostics, err := CompileHardModel(prepared)
	if err != nil {
		t.Fatalf("CompileHardModel: %v", err)
	}
	if diagnostics.StopsRun() {
		t.Fatalf("unexpected compile diagnostic: %+v", diagnostics)
	}
	solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), compiled)
	if err != nil {
		t.Fatalf("IntentEngine.Solve: %v", err)
	}
	return solution
}

// diagnosticPreparedProblem creates two supported buckets in one all-Main
// Group. mainProbability controls the requested total, cv the requested global
// interval, and riskCap optionally fixes both bucket masses at one half.
func diagnosticPreparedProblem(mainProbability, cv NumericRange, riskCap float64) PreparedProblem {
	enabled := true
	if riskCap <= 0 {
		riskCap = 1
	}
	buckets := []PreparedBucket{
		{Index: 0, Mean: 1, SecondMoment: 1, CDFAtUpper: 1, RiskCap: riskCap, MainGroup: 0, Samples: []CollectedSample{{Snapshot: []byte{0}}}},
		{Index: 1, Mean: 1, SecondMoment: 5, CDFAtUpper: 1, RiskCap: riskCap, MainGroup: 0, Samples: []CollectedSample{{Snapshot: []byte{1}}}},
	}
	main := &MainExperience{Groups: []ClosedInterval{{0, 2}}, Probability: mainProbability, Prefer: []float64{1}}
	class := PreparedClass{
		ID: "diagnostic", Weight: ClassWeightBase, Probability: 1, Intent: true,
		Design: ClassDesign{
			Exp: 1, Median: ClosedInterval{0, 1},
			Subjective: SubjectiveIntent{Intent: &enabled, MainExperience: main},
		},
		Buckets: buckets,
		Groups:  []PreparedGroup{{Index: 0, Range: ClosedInterval{0, 2}, BucketIndexes: []int{0, 1}, PreferShare: 1}},
	}
	return PreparedProblem{
		Plan: ResolvedPlan{
			Plan:          RunPlan{Intent: "diagnostic"},
			Intent:        MathIntent{Overall: OverallIntent{CV: cv}},
			EngineOptions: DefaultEngineOptions(),
		},
		Classes: []PreparedClass{class},
	}
}
