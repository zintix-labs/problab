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
	"testing"
)

// TestMultiIntentJointFairnessWithGlobalCVCoupling is the production v2 golden
// missing from the original single-intent nine-case POC. Class A has two Others
// and Class B has six. Both share one Main profile delta and one normalized
// Other visibility rho, while an exact global CV row couples their high-second-moment buckets.
// Solving the Classes independently cannot enforce that shared second moment.
func TestMultiIntentJointFairnessWithGlobalCVCoupling(t *testing.T) {
	prepared := jointFairnessPreparedProblem()
	compiled, diagnostics, err := CompileHardModel(prepared)
	if err != nil {
		t.Fatalf("CompileHardModel: %v", err)
	}
	if diagnostics.StopsRun() {
		t.Fatalf("CompileHardModel diagnostics: %+v", diagnostics)
	}
	solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), compiled)
	if err != nil {
		t.Fatalf("IntentEngine.Solve: %v", err)
	}
	if solution.Status != StatusOptimal {
		t.Fatalf("status=%s diagnostics=%+v evidence=%+v", solution.Status, solution.Diagnostics, solution.Evidence)
	}
	if solution.MainProfileOptimization.Upper != 0 {
		t.Fatalf("common Main profile upper=%g, want exact profile delta 0", solution.MainProfileOptimization.Upper)
	}
	if math.Abs(solution.OtherBucketVisibilityOptimization.Lower-0.75) > 2e-7 {
		t.Fatalf("common normalized Other visibility rho lower=%.12g, want 0.75", solution.OtherBucketVisibilityOptimization.Lower)
	}

	report := BuildIntentQualityReport(compiled, solution)
	if len(report.Classes) != 2 || len(report.Classes[0].OtherVisibility.Buckets) != 2 || len(report.Classes[1].OtherVisibility.Buckets) != 6 {
		t.Fatalf("unexpected joint report shape: %+v", report.Classes)
	}
	for _, class := range report.Classes {
		if class.MainRelativeDeviation > prepared.Plan.EngineOptions.ProfileTolerance+1e-8 {
			t.Fatalf("class %q profile deviation %.12g exceeds common lock", class.Class, class.MainRelativeDeviation)
		}
		if class.OtherVisibility.ClassRetention+2e-7 < solution.OtherBucketVisibilityOptimization.FixedValue {
			t.Fatalf("class %q retention %.12g below common rho lock %.12g", class.Class, class.OtherVisibility.ClassRetention, solution.OtherBucketVisibilityOptimization.FixedValue)
		}
	}

	primary, err := solutionPrimaryMasses(compiled, solution)
	if err != nil {
		t.Fatalf("solutionPrimaryMasses: %v", err)
	}
	secondMoment := 0.0
	for classIndex, class := range prepared.Classes {
		for bucketIndex, bucket := range class.Buckets {
			secondMoment += class.Probability * primary[classIndex][bucketIndex] * bucket.SecondMoment
		}
	}
	if math.Abs(secondMoment-1.25) > 1e-8 {
		t.Fatalf("joint second moment=%.12g, want CV-coupled 1.25", secondMoment)
	}
}

// TestRealSolverMainGroupVisibilityPreventsCanonicalZeroing exercises the new
// neutral refinement with a real backend. Exact mean makes perfect internal
// uniformity infeasible, but the max-min lock still keeps all three supported
// siblings visible before canonical tie-breaking.
func TestRealSolverMainGroupVisibilityPreventsCanonicalZeroing(t *testing.T) {
	class := modelTestIntentClass(
		0, "main-internal", 1,
		[]float64{0, 1, 3}, []float64{0, 1, 9},
		[][]int{{0, 1, 2}}, nil,
	)
	class.Design.Exp = 1
	class.Design.Subjective.MainExperience.Probability = NumericRange{Min: 1, Max: 1}
	prepared := modelTestPrepared(OverallIntent{CV: NumericRange{Min: 0, Max: 10}}, class)
	prepared.Plan.EngineOptions.MainGroupInternalVisibilityBisectionIterations = 40
	compiled := modelTestCompile(t, prepared)
	solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), compiled)
	if err != nil {
		t.Fatal(err)
	}
	if solution.Status != StatusOptimal {
		t.Fatalf("status=%q diagnostics=%+v", solution.Status, solution.Diagnostics)
	}
	visibility := solution.MainGroupInternalVisibilityOptimization
	if !visibility.Applicable || math.Abs(visibility.Lower-0.75) > 2e-7 {
		t.Fatalf("Main Group internal visibility=%+v want rho≈0.75", visibility)
	}
	groupTotal := solution.Primary[0] + solution.Primary[1] + solution.Primary[2]
	floor := visibility.FixedValue * groupTotal / 3
	for bucketIndex, mass := range solution.Primary {
		if mass+2e-8 < floor {
			t.Fatalf("bucket %d mass %.12g below locked floor %.12g", bucketIndex, mass, floor)
		}
	}
	if solution.CanonicalBucketProbabilitySelection.Solves != 3 {
		t.Fatalf("canonical report=%+v", solution.CanonicalBucketProbabilitySelection)
	}
	again, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), compiled)
	if err != nil {
		t.Fatal(err)
	}
	firstHash, err := hashCanonicalJSON(struct {
		Primary []float64
		Intent  IntentQualityReport
	}{solution.Primary, BuildIntentQualityReport(compiled, solution)})
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := hashCanonicalJSON(struct {
		Primary []float64
		Intent  IntentQualityReport
	}{again.Primary, BuildIntentQualityReport(compiled, again)})
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("same input/backend produced different solution report hashes: %s != %s", firstHash, secondHash)
	}
}

func TestRealSolverMainGroupVisibilityUsesWorstEligibleGroupAcrossClasses(t *testing.T) {
	compiled := fixedMultiGroupVisibilityModel()
	solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), compiled)
	if err != nil {
		t.Fatal(err)
	}
	visibility := solution.MainGroupInternalVisibilityOptimization
	if solution.Status != StatusOptimal || !visibility.Applicable || math.Abs(visibility.Lower-0.3) > 2e-7 {
		t.Fatalf("solution status=%q visibility=%+v diagnostics=%+v", solution.Status, visibility, solution.Diagnostics)
	}
	report := BuildIntentQualityReport(compiled, solution)
	wantRetentions := [][]float64{{0.8}, {0.3, 1}}
	for classIndex, classReport := range report.Classes {
		for groupIndex, groupReport := range classReport.MainGroupVisibility {
			if math.Abs(groupReport.Retention-wantRetentions[classIndex][groupIndex]) > 2e-7 {
				t.Fatalf("class %d group %d retention=%.12g want=%.12g", classIndex, groupIndex, groupReport.Retention, wantRetentions[classIndex][groupIndex])
			}
			if groupReport.Retention+2e-7 < visibility.FixedValue {
				t.Fatalf("class %d group %d retention %.12g below common lock %.12g", classIndex, groupIndex, groupReport.Retention, visibility.FixedValue)
			}
		}
	}
}

func fixedMultiGroupVisibilityModel() CompiledModel {
	options := DefaultEngineOptions()
	options.MainGroupInternalVisibilityBisectionIterations = 40
	classes := []PreparedClass{
		{
			ID: "class-a", Index: 0, Intent: true,
			Buckets: []PreparedBucket{
				{Index: 0, Samples: []CollectedSample{{Snapshot: []byte("a0")}}, MainGroup: 0},
				{Index: 1, Samples: []CollectedSample{{Snapshot: []byte("a1")}}, MainGroup: 0},
			},
			Groups: []PreparedGroup{{Index: 0, BucketIndexes: []int{0, 1}, PreferShare: 1}},
		},
		{
			ID: "class-b", Index: 1, Intent: true,
			Buckets: []PreparedBucket{
				{Index: 0, Samples: []CollectedSample{{Snapshot: []byte("b0")}}, MainGroup: 0},
				{Index: 1, Samples: []CollectedSample{{Snapshot: []byte("b1")}}, MainGroup: 0},
				{Index: 2, Samples: []CollectedSample{{Snapshot: []byte("b2")}}, MainGroup: 0},
				{Index: 3, Samples: []CollectedSample{{Snapshot: []byte("b3")}}, MainGroup: 1},
				{Index: 4, Samples: []CollectedSample{{Snapshot: []byte("b4")}}, MainGroup: 1},
			},
			Groups: []PreparedGroup{
				{Index: 0, BucketIndexes: []int{0, 1, 2}, PreferShare: 0.5},
				{Index: 1, BucketIndexes: []int{3, 4}, PreferShare: 0.5},
			},
		},
	}
	fixed := [][]float64{{0.4, 0.6}, {0.05, 0.1, 0.35, 0.25, 0.25}}
	compiled := CompiledModel{
		Prepared: PreparedProblem{
			Plan:    ResolvedPlan{Plan: RunPlan{Intent: "multi-group-visibility"}, EngineOptions: options},
			Classes: classes,
		},
		ClassVariables: make([][]VariableID, len(classes)),
		VariableIndex:  make(map[VariableID]int),
	}
	for classIndex, class := range classes {
		for bucketIndex := range class.Buckets {
			id := VariableID(fmt.Sprintf("p:%04d:%04d", classIndex, bucketIndex))
			compiled.VariableIndex[id] = len(compiled.Hard.Variables)
			compiled.Hard.Variables = append(compiled.Hard.Variables, LinearVariable{ID: id, Lower: 0, Upper: 1})
			compiled.Hard.Rows = append(compiled.Hard.Rows, LinearRow{
				ID: RowID(fmt.Sprintf("fixed:%04d:%04d", classIndex, bucketIndex)), Family: "test_fixed_mass",
				Origin: OriginSystemInvariant, Sense: SenseEQ, RHS: fixed[classIndex][bucketIndex],
				Terms: []LinearTerm{{Variable: id, Coeff: 1}},
			})
			compiled.ClassVariables[classIndex] = append(compiled.ClassVariables[classIndex], id)
			compiled.Primary = append(compiled.Primary, PrimaryVariable{ID: id, ClassIndex: classIndex, BucketIndex: bucketIndex})
		}
	}
	return compiled
}

// jointFairnessPreparedProblem constructs two already-collected Classes whose
// bucket means are all one. Only their second moments differ: one high-moment
// Other in each Class must share the global mass budget p_A+p_B=0.25. Perfect
// uniform Others would require 1/4+1/12=1/3, so the common normalized optimum is
// rho=(1/4)/(1/3)=0.75 regardless of the Classes' different Other counts.
func jointFairnessPreparedProblem() PreparedProblem {
	options := DefaultEngineOptions()
	options.ProfileBisectionIterations = 40
	options.OtherVisibilityBisectionIterations = 40
	enabled := true
	classes := []PreparedClass{
		jointFairnessClass("class-a", 0, 500_000, 2, &enabled),
		jointFairnessClass("class-b", 1, 500_000, 6, &enabled),
	}
	return PreparedProblem{
		Plan: ResolvedPlan{
			Version: ConfigVersion,
			Plan:    RunPlan{ID: "joint-fairness", Intent: "joint-fairness"},
			Intent: MathIntent{Overall: OverallIntent{
				CV: NumericRange{Min: 0.5, Max: 0.5},
			}},
			EngineOptions: options,
		},
		BetMode: 0, BetUnit: 1, Classes: classes,
	}
}

// jointFairnessClass gives a Class fixed Main mass 0.5, one Main bucket, and
// otherCount supported Others. The final Other has second moment three; every
// other bucket has second moment one.
func jointFairnessClass(id string, classIndex, weight, otherCount int, enabled *bool) PreparedClass {
	buckets := make([]PreparedBucket, otherCount+1)
	for bucketIndex := range buckets {
		secondMoment := 1.0
		if bucketIndex == len(buckets)-1 {
			secondMoment = 3
		}
		buckets[bucketIndex] = PreparedBucket{
			Index: bucketIndex, Mean: 1, SecondMoment: secondMoment,
			CDFBeforeLower: 0, CDFAtUpper: 1, RiskCap: math.Inf(1), MainGroup: -1,
			Samples: []CollectedSample{{
				ClassID: id, Win: 1, Snapshot: []byte(fmt.Sprintf("%s-%d", id, bucketIndex)), Sequence: uint64(bucketIndex),
			}},
		}
	}
	buckets[0].MainGroup = 0
	others := make([]int, otherCount)
	for i := range others {
		others[i] = i + 1
	}
	main := &MainExperience{
		Groups: []ClosedInterval{{0, 1}}, Probability: NumericRange{Min: 0.5, Max: 0.5}, Prefer: []float64{1},
	}
	return PreparedClass{
		ID: id, Index: classIndex, Weight: weight,
		Probability: float64(weight) / ClassWeightBase, Intent: true,
		Design: ClassDesign{
			Exp: 1, Median: ClosedInterval{0, 1},
			Subjective: SubjectiveIntent{Intent: enabled, MainExperience: main},
		},
		Buckets: buckets,
		Groups:  []PreparedGroup{{Index: 0, Range: ClosedInterval{0, 1}, BucketIndexes: []int{0}, PreferShare: 1}},
		Others:  others,
	}
}
