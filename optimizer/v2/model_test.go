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
	"reflect"
	"strings"
	"testing"
)

// TestCompileHardModelMulticlassGlobalMomentAppliesEachClassProbabilityExactlyOnce
// guards CV-moment weighting and proves the now-derived RTP does not create a
// mathematically redundant global mean row.
func TestCompileHardModelMulticlassGlobalMomentAppliesEachClassProbabilityExactlyOnce(t *testing.T) {
	classA := modelTestIntentClass(0, "A", 0.2, []float64{2, 4}, []float64{5, 17}, [][]int{{0}}, []int{1})
	classB := modelTestIntentClass(1, "B", 0.3, []float64{1, 5}, []float64{2, 26}, [][]int{{0}}, []int{1})
	classC := modelTestFixedClass(2, "C", 0.5, 3, 10)
	compiled := modelTestCompile(t, modelTestPrepared(OverallIntent{
		CV: NumericRange{Min: 0, Max: 10},
	}, classA, classB, classC))

	if got := modelTestFamilyCount(compiled.Hard, "overall_mean"); got != 0 {
		t.Fatalf("derived RTP compiled %d redundant overall-mean rows", got)
	}
	modelTestClose(t, "derived expected RTP", compiled.Prepared.ExpectedRTP(), 2.2)

	second := modelTestRow(t, compiled.Hard, "global:cv:min")
	modelTestRequireTerms(t, second, map[VariableID]float64{
		"p:0000:0000": 1.0,
		"p:0000:0001": 3.4,
		"p:0001:0000": 0.6,
		"p:0001:0001": 7.8,
	})
	modelTestClose(t, "global CV-min RHS", second.RHS, -0.16) // 2.2^2 - 0.5*10.

	// Class normalization remains conditional. Multiplying these rows by c_k
	// would apply the fixed class probability a second time during expansion.
	modelTestRequireTerms(t, modelTestRow(t, compiled.Hard, "class:0000:normalization"), map[VariableID]float64{
		"p:0000:0000": 1,
		"p:0000:0001": 1,
	})
	modelTestRequireTerms(t, modelTestRow(t, compiled.Hard, "class:0001:normalization"), map[VariableID]float64{
		"p:0001:0000": 1,
		"p:0001:0001": 1,
	})
}

// TestCompileHardModelMainGuardrailComparesWholeGroupMassWithOtherAverage proves the
// guardrail constrains a complete Main Group, not each atomic bucket independently.
func TestCompileHardModelMainGuardrailComparesWholeGroupMassWithOtherAverage(t *testing.T) {
	class := modelTestIntentClass(
		0,
		"main-shape",
		1,
		[]float64{1, 2, 3, 4, 5},
		[]float64{1, 4, 9, 16, 25},
		[][]int{{0, 1}, {2}},
		[]int{3, 4},
	)
	compiled := modelTestCompile(t, modelTestPrepared(OverallIntent{
		CV: NumericRange{Min: 0, Max: 10},
	}, class))

	firstGroup := modelTestRow(t, compiled.Hard, "class:0000:main:guardrail:0000")
	if firstGroup.Family != "main_group_guardrail" || firstGroup.Origin != OriginDerivedSemanticGuardrail || firstGroup.Sense != SenseGE || firstGroup.RHS != 0 {
		t.Fatalf("first guardrail metadata = family %q origin %q sense %s RHS %g", firstGroup.Family, firstGroup.Origin, firstGroup.Sense, firstGroup.RHS)
	}
	if !strings.Contains(firstGroup.Description, MainSemanticAxiomVersion) {
		t.Fatalf("guardrail description %q does not identify semantic axiom %q", firstGroup.Description, MainSemanticAxiomVersion)
	}
	modelTestRequireTerms(t, firstGroup, map[VariableID]float64{
		"p:0000:0000": 1,
		"p:0000:0001": 1,
		"p:0000:0003": -0.5,
		"p:0000:0004": -0.5,
	})

	secondGroup := modelTestRow(t, compiled.Hard, "class:0000:main:guardrail:0001")
	modelTestRequireTerms(t, secondGroup, map[VariableID]float64{
		"p:0000:0002": 1,
		"p:0000:0003": -0.5,
		"p:0000:0004": -0.5,
	})
}

// TestCompileHardModelPreservesVariableBoundProvenance makes derived collision
// safety and replay-support invariants inspectable even though both compile to
// ordinary LP column bounds rather than named constraint rows.
func TestCompileHardModelPreservesVariableBoundProvenance(t *testing.T) {
	class := modelTestIntentClass(
		0, "bound-provenance", 1,
		[]float64{1, 2, 3}, []float64{1, 4, 9},
		[][]int{{0}}, []int{1, 2},
	)
	class.Buckets[0].RiskCap = 0.4
	class.Buckets[2].Samples = nil
	compiled := modelTestCompile(t, modelTestPrepared(OverallIntent{CV: NumericRange{Min: 0, Max: 10}}, class))

	findVariable := func(id VariableID) LinearVariable {
		t.Helper()
		for _, variable := range compiled.Hard.Variables {
			if variable.ID == id {
				return variable
			}
		}
		t.Fatalf("missing variable %q", id)
		return LinearVariable{}
	}
	active := findVariable("p:0000:0000")
	if active.Upper != 0.4 || len(active.UpperProvenance) != 2 || active.UpperProvenance[1].Origin != OriginDerivedSafety {
		t.Fatalf("risk-capped variable provenance=%+v", active)
	}
	unsupported := findVariable("p:0000:0002")
	if unsupported.Upper != 0 || len(unsupported.UpperProvenance) != 2 || unsupported.UpperProvenance[1].Origin != OriginSystemInvariant {
		t.Fatalf("unsupported variable provenance=%+v", unsupported)
	}
	if err := validateLinearProblem(
		compiled.Hard,
		LinearObjective{Origin: ObjectiveHardFeasibility},
		SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9},
	); err != nil {
		t.Fatalf("compiled bound provenance failed model validation: %v", err)
	}
}

// TestBuildMainProfileDeviationProblemUsesOneCommonDeltaForEveryIntentClass locks the fairness
// contract that every controlled Class shares the same profile-deviation probe.
func TestBuildMainProfileDeviationProblemUsesOneCommonDeltaForEveryIntentClass(t *testing.T) {
	classA := modelTestIntentClass(
		0,
		"A",
		0.4,
		[]float64{1, 2, 3, 4},
		[]float64{1, 4, 9, 16},
		[][]int{{0}, {1}},
		[]int{2, 3},
	)
	classB := modelTestIntentClass(
		1,
		"B",
		0.6,
		[]float64{1, 2, 3, 4, 5, 6},
		[]float64{1, 4, 9, 16, 25, 36},
		[][]int{{0, 1}, {2}},
		[]int{3, 4, 5},
	)
	compiled := modelTestCompile(t, modelTestPrepared(OverallIntent{
		CV: NumericRange{Min: 0, Max: 10},
	}, classA, classB))

	const delta = 0.25
	problem, err := BuildMainProfileDeviationProblem(compiled, delta)
	if err != nil {
		t.Fatalf("BuildMainProfileDeviationProblem: %v", err)
	}
	if got, want := modelTestFamilyCount(problem, "main_profile_deviation_lock"), 2; got != want {
		t.Fatalf("Main profile lock row count = %d, want %d", got, want)
	}

	lockA := modelTestRow(t, problem, "main-profile-deviation:class-0000:fixed-delta")
	if lockA.Sense != SenseLE || lockA.RHS != 0 {
		t.Fatalf("class A Main profile lock = sense %s RHS %g, want LE 0", lockA.Sense, lockA.RHS)
	}
	modelTestRequireTerms(t, lockA, map[VariableID]float64{
		"d:main:0000:0000": 1,
		"d:main:0000:0001": 1,
		"p:0000:0000":      -delta,
		"p:0000:0001":      -delta,
	})

	lockB := modelTestRow(t, problem, "main-profile-deviation:class-0001:fixed-delta")
	if lockB.Sense != SenseLE || lockB.RHS != 0 {
		t.Fatalf("class B Main profile lock = sense %s RHS %g, want LE 0", lockB.Sense, lockB.RHS)
	}
	modelTestRequireTerms(t, lockB, map[VariableID]float64{
		"d:main:0001:0000": 1,
		"d:main:0001:0001": 1,
		"p:0001:0000":      -delta,
		"p:0001:0001":      -delta,
		"p:0001:0002":      -delta,
	})
}

// TestAddOtherBucketVisibilityRowsNormalizesCommonRhoByEachClassOtherCount verifies the shared
// visibility score is translated to a per-Other floor using each Class's support.
func TestAddOtherBucketVisibilityRowsNormalizesCommonRhoByEachClassOtherCount(t *testing.T) {
	classA := modelTestIntentClass(
		0,
		"A",
		0.4,
		[]float64{1, 2, 3, 4},
		[]float64{1, 4, 9, 16},
		[][]int{{0}, {1}},
		[]int{2, 3},
	)
	classB := modelTestIntentClass(
		1,
		"B",
		0.6,
		[]float64{1, 2, 3, 4, 5, 6},
		[]float64{1, 4, 9, 16, 25, 36},
		[][]int{{0, 1}, {2}},
		[]int{3, 4, 5},
	)
	compiled := modelTestCompile(t, modelTestPrepared(OverallIntent{
		CV: NumericRange{Min: 0, Max: 10},
	}, classA, classB))

	const rho = 0.6
	problem, err := AddOtherBucketVisibilityRows(compiled.Hard, compiled, rho)
	if err != nil {
		t.Fatalf("AddOtherBucketVisibilityRows: %v", err)
	}
	if got, want := modelTestFamilyCount(problem, "other_bucket_visibility"), 5; got != want {
		t.Fatalf("Other bucket visibility row count = %d, want %d", got, want)
	}

	// Class A has two Others, so its common normalized share is rho/2 = 0.3.
	rowA := modelTestRow(t, problem, "other-bucket-visibility:class-0000:bucket-0002:fixed-rho")
	if rowA.Sense != SenseGE {
		t.Fatalf("class A Other visibility sense = %s, want GE", rowA.Sense)
	}
	modelTestClose(t, "class A normalized rho RHS", rowA.RHS, 0.3)
	modelTestRequireTerms(t, rowA, map[VariableID]float64{
		"p:0000:0000": 0.3,
		"p:0000:0001": 0.3,
		"p:0000:0002": 1,
	})

	// Class B has three Others, so the exact same common rho becomes rho/3 = 0.2.
	rowB := modelTestRow(t, problem, "other-bucket-visibility:class-0001:bucket-0003:fixed-rho")
	if rowB.Sense != SenseGE {
		t.Fatalf("class B Other visibility sense = %s, want GE", rowB.Sense)
	}
	modelTestClose(t, "class B normalized rho RHS", rowB.RHS, 0.2)
	modelTestRequireTerms(t, rowB, map[VariableID]float64{
		"p:0001:0000": 0.2,
		"p:0001:0001": 0.2,
		"p:0001:0002": 0.2,
		"p:0001:0003": 1,
	})

	for _, bucketIndex := range classA.Others {
		modelTestClose(t, "class A common normalized rho", modelTestRow(t, problem, RowID(fmt.Sprintf("other-bucket-visibility:class-0000:bucket-%04d:fixed-rho", bucketIndex))).RHS, 0.3)
	}
	for _, bucketIndex := range classB.Others {
		modelTestClose(t, "class B common normalized rho", modelTestRow(t, problem, RowID(fmt.Sprintf("other-bucket-visibility:class-0001:bucket-%04d:fixed-rho", bucketIndex))).RHS, 0.2)
	}
}

func TestMainGroupInternalVisibilityRowsUseSupportedSiblingDenominator(t *testing.T) {
	class := modelTestIntentClass(
		0, "main-siblings", 1,
		[]float64{1, 2, 3}, []float64{1, 4, 9},
		[][]int{{0, 1, 2}}, nil,
	)
	// Keep the configured middle bucket but make it replay-unsupported.
	class.Buckets[1].Samples = nil
	compiled := modelTestCompile(t, modelTestPrepared(OverallIntent{CV: NumericRange{Min: 0, Max: 10}}, class))
	base := cloneLinearProblem(compiled.Hard)
	if got := modelTestFamilyCount(compiled.Hard, "main_group_internal_visibility"); got != 0 {
		t.Fatalf("hard model contains %d Main Group internal soft rows", got)
	}

	zero, err := AddMainGroupInternalVisibilityRows(compiled.Hard, compiled, 0)
	if err != nil {
		t.Fatalf("rho=0: %v", err)
	}
	if got, want := modelTestFamilyCount(zero, "main_group_internal_visibility"), 2; got != want {
		t.Fatalf("rho=0 rows=%d want=%d", got, want)
	}
	modelTestRequireTerms(t, modelTestRow(t, zero, "main-group-internal-visibility:class-0000:group-0000:bucket-0000:fixed-rho"), map[VariableID]float64{
		"p:0000:0000": 1,
	})

	one, err := AddMainGroupInternalVisibilityRows(compiled.Hard, compiled, 1)
	if err != nil {
		t.Fatalf("rho=1: %v", err)
	}
	first := modelTestRow(t, one, "main-group-internal-visibility:class-0000:group-0000:bucket-0000:fixed-rho")
	modelTestRequireTerms(t, first, map[VariableID]float64{
		"p:0000:0000": 0.5,
		"p:0000:0002": -0.5,
	})
	if first.Family != "main_group_internal_visibility" || first.Origin != OriginSystemNeutralPreference || first.Sense != SenseGE || first.RHS != 0 {
		t.Fatalf("row metadata=%+v", first)
	}
	if want := "intents.model-test.classes[0].design.subjective.main_experience.groups[0]"; first.YAMLPath != want {
		t.Fatalf("YAMLPath=%q want=%q", first.YAMLPath, want)
	}
	if !reflect.DeepEqual(compiled.Hard, base) {
		t.Fatal("visibility builder mutated its base problem")
	}
}

func TestMainGroupInternalVisibilityRowsUseThreeSupportedBuckets(t *testing.T) {
	class := modelTestIntentClass(
		0, "three-siblings", 1,
		[]float64{1, 2, 3}, []float64{1, 4, 9},
		[][]int{{0, 1, 2}}, nil,
	)
	compiled := modelTestCompile(t, modelTestPrepared(OverallIntent{CV: NumericRange{Min: 0, Max: 10}}, class))
	const rho = 0.6
	problem, err := AddMainGroupInternalVisibilityRows(compiled.Hard, compiled, rho)
	if err != nil {
		t.Fatalf("AddMainGroupInternalVisibilityRows: %v", err)
	}
	modelTestRequireTerms(t, modelTestRow(t, problem, "main-group-internal-visibility:class-0000:group-0000:bucket-0001:fixed-rho"), map[VariableID]float64{
		"p:0000:0000": -0.2,
		"p:0000:0001": 0.8,
		"p:0000:0002": -0.2,
	})
}

func TestMainGroupInternalVisibilityRowsSkipIneligibleGroupsAndRejectBadIndexes(t *testing.T) {
	class := modelTestIntentClass(
		0, "single", 1,
		[]float64{1, 2}, []float64{1, 4},
		[][]int{{0}}, []int{1},
	)
	compiled := modelTestCompile(t, modelTestPrepared(OverallIntent{CV: NumericRange{Min: 0, Max: 10}}, class))
	problem, err := AddMainGroupInternalVisibilityRows(compiled.Hard, compiled, 0.5)
	if err != nil {
		t.Fatalf("singleton group: %v", err)
	}
	if got := modelTestFamilyCount(problem, "main_group_internal_visibility"); got != 0 {
		t.Fatalf("singleton group added %d rows", got)
	}

	compiled.Prepared.Classes[0].Buckets[0].Samples = nil
	problem, err = AddMainGroupInternalVisibilityRows(compiled.Hard, compiled, 0.5)
	if err != nil {
		t.Fatalf("ineligible groups: %v", err)
	}
	if got := modelTestFamilyCount(problem, "main_group_internal_visibility"); got != 0 {
		t.Fatalf("ineligible groups added %d rows", got)
	}

	compiled.Prepared.Classes[0].Groups[0].BucketIndexes = []int{0, 9}
	if _, err := AddMainGroupInternalVisibilityRows(compiled.Hard, compiled, 0.5); err == nil {
		t.Fatal("invalid Main Group bucket index was silently accepted")
	}
	compiled.Prepared.Classes[0].Groups[0].BucketIndexes = []int{0, 0}
	if _, err := AddMainGroupInternalVisibilityRows(compiled.Hard, compiled, 0.5); err == nil {
		t.Fatal("duplicate Main Group bucket index was silently accepted")
	}
}

func TestDeprecatedModelWrappersMatchCanonicalBuilders(t *testing.T) {
	class := modelTestIntentClass(0, "compat", 1, []float64{1, 2}, []float64{1, 4}, [][]int{{0}}, []int{1})
	compiled := modelTestCompile(t, modelTestPrepared(OverallIntent{CV: NumericRange{Min: 0, Max: 10}}, class))
	canonicalMain, err := BuildMainProfileDeviationProblem(compiled, 0.25)
	if err != nil {
		t.Fatal(err)
	}
	legacyMain, err := BuildPhaseAProblem(compiled, 0.25)
	if err != nil || !reflect.DeepEqual(canonicalMain, legacyMain) {
		t.Fatalf("BuildPhaseAProblem differs from canonical builder: err=%v", err)
	}
	canonicalOther, err := AddOtherBucketVisibilityRows(compiled.Hard, compiled, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	legacyOther, err := AddPhaseBRows(compiled.Hard, compiled, 0.5)
	if err != nil || !reflect.DeepEqual(canonicalOther, legacyOther) {
		t.Fatalf("AddPhaseBRows differs from canonical builder: err=%v", err)
	}
}

// modelTestPrepared assembles prepared Class fixtures under production engine
// defaults while normalizing their indexes to deterministic declaration order.
func modelTestPrepared(overall OverallIntent, classes ...PreparedClass) PreparedProblem {
	for i := range classes {
		classes[i].Index = i
	}
	return PreparedProblem{
		Plan: ResolvedPlan{
			Version: ConfigVersion,
			Plan: RunPlan{
				ID:         "model-test",
				Engine:     EngineIntentLPV2,
				Intent:     "model-test",
				Target:     Target{BetModes: []int{0}},
				Collection: CollectionOptions{Workers: 1, BatchSize: 1, MaxSpins: 1},
				CandidateSelection: CandidateSelectionOptions{
					Evaluator:     "none",
					MaxCandidates: 1,
				},
				Output: OutputOptions{Format: []OutputFormat{OutputOptimalArtifactV1}, Directory: "model-test-output"},
			},
			Intent: MathIntent{
				Overall: overall,
			},
			EngineOptions: DefaultEngineOptions(),
		},
		Classes: classes,
	}
}

// modelTestIntentClass builds an LP-controlled Class with explicit atomic moments,
// Main membership, and Others so row-coefficient tests control every input term.
func modelTestIntentClass(index int, id string, probability float64, means, secondMoments []float64, groupBuckets [][]int, others []int) PreparedClass {
	if len(means) != len(secondMoments) {
		panic("modelTestIntentClass requires one second moment per mean")
	}
	buckets := make([]PreparedBucket, len(means))
	for i := range buckets {
		buckets[i] = PreparedBucket{
			Index:        i,
			Lower:        float64(i),
			Upper:        float64(i + 1),
			Samples:      []CollectedSample{{Snapshot: []byte{byte(index + 1), byte(i + 1)}}},
			Mean:         means[i],
			SecondMoment: secondMoments[i],
			Minimum:      means[i],
			Maximum:      means[i],
			CDFAtUpper:   1,
			RiskCap:      math.Inf(1),
			MainGroup:    -1,
		}
	}
	groups := make([]PreparedGroup, len(groupBuckets))
	prefer := make([]float64, len(groupBuckets))
	for i, indexes := range groupBuckets {
		prefer[i] = 1
		groups[i] = PreparedGroup{
			Index:         i,
			BucketIndexes: append([]int(nil), indexes...),
			PreferShare:   1 / float64(len(groupBuckets)),
		}
		for _, bucketIndex := range indexes {
			buckets[bucketIndex].MainGroup = i
		}
	}
	enabled := true
	return PreparedClass{
		ID:          id,
		Index:       index,
		Weight:      int(probability * ClassWeightBase),
		Probability: probability,
		Intent:      true,
		Buckets:     buckets,
		Groups:      groups,
		Others:      append([]int(nil), others...),
		Design: ClassDesign{
			Exp:    means[0],
			Median: ClosedInterval{0, 100},
			Subjective: SubjectiveIntent{
				Intent: &enabled,
				MainExperience: &MainExperience{
					Probability: NumericRange{Min: 0, Max: 1},
					Prefer:      prefer,
				},
			},
		},
	}
}

// modelTestFixedClass builds an empirical-uniform Class whose contribution is fixed,
// allowing global-row tests to distinguish constants from controlled variables.
func modelTestFixedClass(index int, id string, probability, mean, secondMoment float64) PreparedClass {
	disabled := false
	return PreparedClass{
		ID:          id,
		Index:       index,
		Weight:      int(probability * ClassWeightBase),
		Probability: probability,
		Intent:      false,
		Buckets: []PreparedBucket{{
			Index:        0,
			Samples:      []CollectedSample{{Snapshot: []byte{byte(index + 1)}}},
			Mean:         mean,
			SecondMoment: secondMoment,
			RiskCap:      math.Inf(1),
			MainGroup:    -1,
		}},
		Design: ClassDesign{
			Exp:        mean,
			Median:     ClosedInterval{mean, mean},
			Subjective: SubjectiveIntent{Intent: &disabled},
		},
	}
}

// modelTestCompile runs the production compiler and requires a non-stopping model,
// so individual tests can inspect row semantics without repeating stage checks.
func modelTestCompile(t *testing.T, prepared PreparedProblem) CompiledModel {
	t.Helper()
	compiled, diagnostics, err := CompileHardModel(prepared)
	if err != nil {
		t.Fatalf("CompileHardModel: %v", err)
	}
	if diagnostics.StopsRun() {
		t.Fatalf("CompileHardModel returned stopping diagnostics: %+v", diagnostics)
	}
	return compiled
}

// modelTestRow resolves a semantic row by stable identifier and fails immediately
// when compilation omitted or renamed the contract under examination.
func modelTestRow(t *testing.T, problem LinearProblem, id RowID) LinearRow {
	t.Helper()
	for _, row := range problem.Rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("row %q was not compiled", id)
	return LinearRow{}
}

// modelTestRequireTerms compares a row's complete sparse coefficient set, catching
// both wrong values and unexpected variables that could alter feasible geometry.
func modelTestRequireTerms(t *testing.T, row LinearRow, want map[VariableID]float64) {
	t.Helper()
	got := make(map[VariableID]float64, len(row.Terms))
	for _, term := range row.Terms {
		got[term.Variable] = term.Coeff
	}
	if len(got) != len(want) {
		t.Fatalf("row %q coefficients = %v, want %v", row.ID, got, want)
	}
	for variable, wantCoefficient := range want {
		gotCoefficient, exists := got[variable]
		if !exists || math.Abs(gotCoefficient-wantCoefficient) > 1e-12 {
			t.Fatalf("row %q coefficients = %v, want %v", row.ID, got, want)
		}
	}
}

// modelTestFamilyCount counts semantic row families to verify phase construction
// adds exactly one required constraint per applicable Class or bucket.
func modelTestFamilyCount(problem LinearProblem, family string) int {
	count := 0
	for _, row := range problem.Rows {
		if row.Family == family {
			count++
		}
	}
	return count
}

// modelTestClose applies strict deterministic tolerance to coefficients whose small
// drift would change the mathematical intent encoded by the LP.
func modelTestClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("%s = %.15g, want %.15g", name, got, want)
	}
}
