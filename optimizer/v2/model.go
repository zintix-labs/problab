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
)

// PrimaryVariable ties a stable LP column to its Class and atomic bucket. Only
// these p[k,i] variables participate in canonical candidate identity; d-profile,
// slack, and surplus auxiliaries are deliberately excluded.
type PrimaryVariable struct {
	ID          VariableID
	ClassIndex  int
	BucketIndex int
}

// CompiledModel owns the immutable hard model and stable semantic mappings
// needed to add approved semantic preference rows without parsing variable IDs.
type CompiledModel struct {
	Prepared       PreparedProblem
	Hard           LinearProblem
	Primary        []PrimaryVariable
	ClassVariables [][]VariableID
	VariableIndex  map[VariableID]int
}

// CompileHardModel translates Designer hard intent, system invariants, derived
// safety caps, and semantic guardrails into named linear rows. Fixed empirical
// Classes move into row RHS constants exactly once. A constant-only conflict is
// returned as a typed diagnostic instead of constructing a meaningless row.
func CompileHardModel(prepared PreparedProblem) (CompiledModel, Diagnostics, error) {
	compiled := CompiledModel{
		Prepared:       prepared,
		ClassVariables: make([][]VariableID, len(prepared.Classes)),
		VariableIndex:  make(map[VariableID]int),
	}
	for classIndex, class := range prepared.Classes {
		if !class.Intent {
			continue
		}
		source := fmt.Sprintf("intents.%s.classes[%d]", prepared.Plan.Plan.Intent, classIndex)
		compiled.ClassVariables[classIndex] = make([]VariableID, len(class.Buckets))
		for bucketIndex, bucket := range class.Buckets {
			id := VariableID(fmt.Sprintf("p:%04d:%04d", classIndex, bucketIndex))
			upper := 1.0
			upperProvenance := []BoundProvenance{{
				Origin: OriginSystemInvariant, YAMLPath: source + ".design.subjective.buckets",
				Description: "conditional atomic-bucket mass cannot exceed one", Bound: 1,
			}}
			if !bucket.Supported() {
				upper = 0
				upperProvenance = append(upperProvenance, BoundProvenance{
					Origin: OriginSystemInvariant, YAMLPath: fmt.Sprintf("%s.design.subjective.buckets[%d]", source, bucketIndex),
					Description: "replay-unsupported atomic bucket is forced to zero", Bound: 0,
				})
			}
			if !math.IsInf(bucket.RiskCap, 1) {
				upper = math.Min(upper, bucket.RiskCap)
				upperProvenance = append(upperProvenance, BoundProvenance{
					Origin: OriginDerivedSafety, YAMLPath: source + ".design.risk",
					Description: "collision policy limits conditional bucket mass using collected unique replay support", Bound: bucket.RiskCap,
				})
			}
			if err := addVariable(&compiled.Hard, LinearVariable{
				ID: id, Lower: 0, Upper: upper,
				LowerProvenance: []BoundProvenance{{
					Origin: OriginSystemInvariant, YAMLPath: source + ".design.subjective.buckets",
					Description: "conditional atomic-bucket mass is nonnegative", Bound: 0,
				}},
				UpperProvenance: upperProvenance,
			}); err != nil {
				return CompiledModel{}, nil, err
			}
			compiled.VariableIndex[id] = len(compiled.Primary)
			compiled.Primary = append(compiled.Primary, PrimaryVariable{ID: id, ClassIndex: classIndex, BucketIndex: bucketIndex})
			compiled.ClassVariables[classIndex][bucketIndex] = id
		}
		if err := compileClassHardRows(&compiled, classIndex); err != nil {
			return CompiledModel{}, nil, err
		}
	}
	if diagnostic, err := compileGlobalRows(&compiled); err != nil {
		return CompiledModel{}, nil, err
	} else if diagnostic.StopsRun() {
		return CompiledModel{}, Diagnostics{diagnostic}, nil
	}
	return compiled, nil, nil
}

// compileClassHardRows emits normalization, exact conditional mean, weighted
// lower-median bounds, Main total bounds, and group-level semantic guardrails
// for one intent:true Class in deterministic family order.
func compileClassHardRows(compiled *CompiledModel, classIndex int) error {
	class := compiled.Prepared.Classes[classIndex]
	prefix := fmt.Sprintf("class:%04d", classIndex)
	source := fmt.Sprintf("intents.%s.classes[%d]", compiled.Prepared.Plan.Plan.Intent, classIndex)
	all := classBucketCoefficients(compiled, classIndex, nil, func(PreparedBucket) float64 { return 1 })
	if err := addRow(&compiled.Hard, LinearRow{
		ID: RowID(prefix + ":normalization"), Family: "normalization", Origin: OriginSystemInvariant,
		ClassID: class.ID, YAMLPath: source, Description: "conditional atomic-bucket mass sums to one",
		Sense: SenseEQ, RHS: 1, Terms: sortedTerms(all),
	}); err != nil {
		return err
	}
	mean := classBucketCoefficients(compiled, classIndex, nil, func(bucket PreparedBucket) float64 { return bucket.Mean })
	if err := addRow(&compiled.Hard, LinearRow{
		ID: RowID(prefix + ":mean"), Family: "class_mean", Origin: OriginDesignerHard,
		ClassID: class.ID, YAMLPath: source + ".design.exp", Description: "exact Class conditional payout multiplier mean",
		Sense: SenseEQ, RHS: class.Design.Exp, Terms: sortedTerms(mean),
	}); err != nil {
		return err
	}
	medianLower := classBucketCoefficients(compiled, classIndex, nil, func(bucket PreparedBucket) float64 { return bucket.CDFBeforeLower })
	if err := addRow(&compiled.Hard, LinearRow{
		ID: RowID(prefix + ":median:lower"), Family: "class_median", Origin: OriginDesignerHard,
		ClassID: class.ID, YAMLPath: source + ".design.median[0]", Description: "weighted lower median is not below the configured lower endpoint",
		Sense: SenseLE, RHS: 0.5 - compiled.Prepared.Plan.EngineOptions.QuantileEpsilon, Terms: sortedTerms(medianLower),
	}); err != nil {
		return err
	}
	medianUpper := classBucketCoefficients(compiled, classIndex, nil, func(bucket PreparedBucket) float64 { return bucket.CDFAtUpper })
	if err := addRow(&compiled.Hard, LinearRow{
		ID: RowID(prefix + ":median:upper"), Family: "class_median", Origin: OriginDesignerHard,
		ClassID: class.ID, YAMLPath: source + ".design.median[1]", Description: "weighted lower median is not above the configured upper endpoint",
		Sense: SenseGE, RHS: 0.5, Terms: sortedTerms(medianUpper),
	}); err != nil {
		return err
	}
	main := mainCoefficients(*compiled, classIndex, 1)
	mainIntent := class.Design.Subjective.MainExperience
	if err := addRow(&compiled.Hard, LinearRow{
		ID: RowID(prefix + ":main:min"), Family: "main_total", Origin: OriginDesignerHard,
		ClassID: class.ID, YAMLPath: source + ".design.subjective.main_experience.probability.min", Description: "Main semantic groups carry at least the configured conditional mass",
		Sense: SenseGE, RHS: mainIntent.Probability.Min, Terms: sortedTerms(main),
	}); err != nil {
		return err
	}
	if err := addRow(&compiled.Hard, LinearRow{
		ID: RowID(prefix + ":main:max"), Family: "main_total", Origin: OriginDesignerHard,
		ClassID: class.ID, YAMLPath: source + ".design.subjective.main_experience.probability.max", Description: "Main semantic groups carry at most the configured conditional mass",
		Sense: SenseLE, RHS: mainIntent.Probability.Max, Terms: sortedTerms(main),
	}); err != nil {
		return err
	}
	if len(class.Others) == 0 {
		return nil
	}
	for groupIndex, group := range class.Groups {
		coefficients := classBucketCoefficients(compiled, classIndex, group.BucketIndexes, func(PreparedBucket) float64 { return 1 })
		for _, bucketIndex := range class.Others {
			addCoefficient(coefficients, compiled.ClassVariables[classIndex][bucketIndex], -1/float64(len(class.Others)))
		}
		if err := addRow(&compiled.Hard, LinearRow{
			ID: RowID(fmt.Sprintf("%s:main:guardrail:%04d", prefix, groupIndex)), Family: "main_group_guardrail", Origin: OriginDerivedSemanticGuardrail,
			ClassID: class.ID, YAMLPath: fmt.Sprintf("%s.design.subjective.main_experience.groups[%d]", source, groupIndex), Description: MainSemanticAxiomVersion + ": Main Group mass is not lower than supported Other atomic-bucket average mass",
			Sense: SenseGE, RHS: 0, Terms: sortedTerms(coefficients),
		}); err != nil {
			return err
		}
	}
	return nil
}

// compileGlobalRows adds linearized second-moment CV bounds. No global-mean row
// is needed: fixed Class probabilities plus every Class's exact conditional Exp
// already imply one unconditional RTP. Empirical-uniform Class contributions
// are subtracted from each RHS; intent:true contributions retain their c_k
// multiplier exactly once.
func compileGlobalRows(compiled *CompiledModel) (Diagnostic, error) {
	secondCoefficients := make(map[VariableID]float64)
	fixedSecond := 0.0
	for classIndex, class := range compiled.Prepared.Classes {
		if !class.Intent {
			fixedSecond += class.Probability * class.Buckets[0].SecondMoment
			continue
		}
		for bucketIndex, bucket := range class.Buckets {
			id := compiled.ClassVariables[classIndex][bucketIndex]
			addCoefficient(secondCoefficients, id, class.Probability*bucket.SecondMoment)
		}
	}
	overall := compiled.Prepared.Plan.Intent.Overall
	expectedRTP := compiled.Prepared.ExpectedRTP()
	meanSquare := expectedRTP * expectedRTP
	lowerSecond := meanSquare * (1 + overall.CV.Min*overall.CV.Min)
	upperSecond := meanSquare * (1 + overall.CV.Max*overall.CV.Max)
	if len(secondCoefficients) == 0 {
		tolerance := scaledTolerance(compiled.Prepared.Plan.EngineOptions.FeasibilityTolerance, fixedSecond, lowerSecond, upperSecond)
		if fixedSecond < lowerSecond-tolerance || fixedSecond > upperSecond+tolerance {
			return supportDiagnostic(DiagnosticGlobalCVInfeasible, fmt.Sprintf("fixed empirical second moment %.12g is outside CV-derived range [%.12g, %.12g]", fixedSecond, lowerSecond, upperSecond), "overall.cv"), nil
		}
		return Diagnostic{}, nil
	}
	if err := addRow(&compiled.Hard, LinearRow{
		ID: "global:cv:min", Family: "overall_cv", Origin: OriginDesignerHard,
		YAMLPath: "overall.cv.min", Description: "unconditional second moment meets the configured minimum CV",
		Sense: SenseGE, RHS: lowerSecond - fixedSecond, Terms: sortedTerms(secondCoefficients),
	}); err != nil {
		return Diagnostic{}, err
	}
	if err := addRow(&compiled.Hard, LinearRow{
		ID: "global:cv:max", Family: "overall_cv", Origin: OriginDesignerHard,
		YAMLPath: "overall.cv.max", Description: "unconditional second moment meets the configured maximum CV",
		Sense: SenseLE, RHS: upperSecond - fixedSecond, Terms: sortedTerms(secondCoefficients),
	}); err != nil {
		return Diagnostic{}, err
	}
	return Diagnostic{}, nil
}

// ExpectedRTP derives the unconditional payout mean from the prepared Class
// contracts used by the LP. Keeping this separate from MathIntent.ExpectedRTP
// lets compiler and verifier fixtures remain self-contained while production
// preparation still yields the exact same declaration-ordered calculation.
func (prepared PreparedProblem) ExpectedRTP() float64 {
	var total compensatedSum
	for _, class := range prepared.Classes {
		total.Add(class.Probability * class.Design.Exp)
	}
	return total.Value()
}

// BuildMainProfileDeviationProblem augments the hard model with absolute Main-profile
// auxiliaries and one fixed-delta row per intent Class. Since delta is a probe
// constant, the resulting feasibility problem remains linear even though Main
// total is variable.
func BuildMainProfileDeviationProblem(compiled CompiledModel, delta float64) (LinearProblem, error) {
	if !isFinite(delta) || delta < 0 || delta > 2 {
		return LinearProblem{}, fmt.Errorf("Main profile deviation delta must be in [0,2]")
	}
	problem := cloneLinearProblem(compiled.Hard)
	for classIndex, class := range compiled.Prepared.Classes {
		if !class.Intent {
			continue
		}
		deviations := make(map[VariableID]float64, len(class.Groups))
		for groupIndex, group := range class.Groups {
			deviation := VariableID(fmt.Sprintf("d:main:%04d:%04d", classIndex, groupIndex))
			if err := addVariable(&problem, LinearVariable{ID: deviation, Lower: 0, Upper: math.Inf(1)}); err != nil {
				return LinearProblem{}, err
			}
			deviations[deviation] = 1
			difference := mainCoefficients(compiled, classIndex, group.PreferShare)
			for _, bucketIndex := range group.BucketIndexes {
				addCoefficient(difference, compiled.ClassVariables[classIndex][bucketIndex], -1)
			}
			addCoefficient(difference, deviation, 1)
			if err := addRow(&problem, LinearRow{
				ID: RowID(fmt.Sprintf("main-profile-deviation:class-%04d:group-%04d:positive", classIndex, groupIndex)), Family: "main_profile_deviation", Origin: OriginDesignerPreference,
				ClassID: class.ID, Description: "absolute Main Group profile deviation positive side", Sense: SenseGE, RHS: 0, Terms: sortedTerms(difference),
			}); err != nil {
				return LinearProblem{}, err
			}
			negative := mainCoefficients(compiled, classIndex, -group.PreferShare)
			for _, bucketIndex := range group.BucketIndexes {
				addCoefficient(negative, compiled.ClassVariables[classIndex][bucketIndex], 1)
			}
			addCoefficient(negative, deviation, 1)
			if err := addRow(&problem, LinearRow{
				ID: RowID(fmt.Sprintf("main-profile-deviation:class-%04d:group-%04d:negative", classIndex, groupIndex)), Family: "main_profile_deviation", Origin: OriginDesignerPreference,
				ClassID: class.ID, Description: "absolute Main Group profile deviation negative side", Sense: SenseGE, RHS: 0, Terms: sortedTerms(negative),
			}); err != nil {
				return LinearProblem{}, err
			}
		}
		for id, coefficient := range mainCoefficients(compiled, classIndex, -delta) {
			addCoefficient(deviations, id, coefficient)
		}
		if err := addRow(&problem, LinearRow{
			ID: RowID(fmt.Sprintf("main-profile-deviation:class-%04d:fixed-delta", classIndex)), Family: "main_profile_deviation_lock", Origin: OriginDesignerPreference,
			ClassID: class.ID, Description: "sum of Main Group deviations is at most fixed delta times Main total", Sense: SenseLE, RHS: 0, Terms: sortedTerms(deviations),
		}); err != nil {
			return LinearProblem{}, err
		}
	}
	return problem, nil
}

// BuildPhaseAProblem is the compatibility entry point for older Go consumers.
// Deprecated: use BuildMainProfileDeviationProblem.
func BuildPhaseAProblem(compiled CompiledModel, delta float64) (LinearProblem, error) {
	return BuildMainProfileDeviationProblem(compiled, delta)
}

// AddOtherBucketVisibilityRows clones a Main-profile-locked problem and adds fixed-rho visibility
// rows for supported Other buckets only. rho is normalized by Other count, so
// Classes with different numbers of Others share one dimensionless fairness
// target without a scale bias.
func AddOtherBucketVisibilityRows(base LinearProblem, compiled CompiledModel, rho float64) (LinearProblem, error) {
	if !isFinite(rho) || rho < 0 || rho > 1 {
		return LinearProblem{}, fmt.Errorf("Other bucket visibility rho must be in [0,1]")
	}
	problem := cloneLinearProblem(base)
	for classIndex, class := range compiled.Prepared.Classes {
		if !class.Intent || len(class.Others) == 0 {
			continue
		}
		share := rho / float64(len(class.Others))
		for _, bucketIndex := range class.Others {
			coefficients := mainCoefficients(compiled, classIndex, share)
			addCoefficient(coefficients, compiled.ClassVariables[classIndex][bucketIndex], 1)
			if err := addRow(&problem, LinearRow{
				ID: RowID(fmt.Sprintf("other-bucket-visibility:class-%04d:bucket-%04d:fixed-rho", classIndex, bucketIndex)), Family: "other_bucket_visibility", Origin: OriginSystemNeutralPreference,
				ClassID: class.ID, Description: "supported Other mass retains the fixed normalized share of total Other mass", Sense: SenseGE, RHS: share, Terms: sortedTerms(coefficients),
			}); err != nil {
				return LinearProblem{}, err
			}
		}
	}
	return problem, nil
}

// AddPhaseBRows is the compatibility entry point for older Go consumers.
// Deprecated: use AddOtherBucketVisibilityRows.
func AddPhaseBRows(base LinearProblem, compiled CompiledModel, rho float64) (LinearProblem, error) {
	return AddOtherBucketVisibilityRows(base, compiled, rho)
}

// supportedMainBucketIndexes returns the replay-supported atomic buckets in one
// Main Group while preserving configured order. Malformed prepared membership is
// an internal model error rather than a reason to silently change a denominator.
func supportedMainBucketIndexes(class PreparedClass, group PreparedGroup) ([]int, error) {
	supported := make([]int, 0, len(group.BucketIndexes))
	seen := make(map[int]struct{}, len(group.BucketIndexes))
	for _, bucketIndex := range group.BucketIndexes {
		if bucketIndex < 0 || bucketIndex >= len(class.Buckets) {
			return nil, fmt.Errorf("class %q Main Group %d bucket index out of range: %d", class.ID, group.Index, bucketIndex)
		}
		if _, duplicate := seen[bucketIndex]; duplicate {
			return nil, fmt.Errorf("class %q Main Group %d repeats bucket index %d", class.ID, group.Index, bucketIndex)
		}
		seen[bucketIndex] = struct{}{}
		if class.Buckets[bucketIndex].Supported() {
			supported = append(supported, bucketIndex)
		}
	}
	return supported, nil
}

// AddMainGroupInternalVisibilityRows adds a fixed common normalized visibility
// floor for supported siblings inside every eligible Main Group. It is a neutral
// refinement of the supplied problem and never participates in the hard model.
func AddMainGroupInternalVisibilityRows(base LinearProblem, compiled CompiledModel, rho float64) (LinearProblem, error) {
	if !isFinite(rho) || rho < 0 || rho > 1 {
		return LinearProblem{}, fmt.Errorf("Main Group internal visibility rho must be in [0,1]")
	}
	problem := cloneLinearProblem(base)
	for classIndex, class := range compiled.Prepared.Classes {
		if !class.Intent {
			continue
		}
		for groupIndex, group := range class.Groups {
			supported, err := supportedMainBucketIndexes(class, group)
			if err != nil {
				return LinearProblem{}, err
			}
			if len(supported) < 2 {
				continue
			}
			share := rho / float64(len(supported))
			for _, bucketIndex := range supported {
				coefficients := map[VariableID]float64{
					compiled.ClassVariables[classIndex][bucketIndex]: 1,
				}
				for _, siblingIndex := range supported {
					addCoefficient(coefficients, compiled.ClassVariables[classIndex][siblingIndex], -share)
				}
				if err := addRow(&problem, LinearRow{
					ID:          RowID(fmt.Sprintf("main-group-internal-visibility:class-%04d:group-%04d:bucket-%04d:fixed-rho", classIndex, groupIndex, bucketIndex)),
					Family:      "main_group_internal_visibility",
					Origin:      OriginSystemNeutralPreference,
					ClassID:     class.ID,
					YAMLPath:    fmt.Sprintf("intents.%s.classes[%d].design.subjective.main_experience.groups[%d]", compiled.Prepared.Plan.Plan.Intent, classIndex, groupIndex),
					Description: "supported Main bucket retains the fixed normalized share of its Main Group mass",
					Sense:       SenseGE,
					RHS:         0,
					Terms:       sortedTerms(coefficients),
				}); err != nil {
					return LinearProblem{}, err
				}
			}
		}
	}
	return problem, nil
}

// classBucketCoefficients builds a deterministic coefficient map for all
// buckets or the supplied subset. The callback receives the PreparedBucket so
// callers can compile means, moments, CDFs, and mass expressions uniformly.
func classBucketCoefficients(compiled *CompiledModel, classIndex int, subset []int, coefficient func(PreparedBucket) float64) map[VariableID]float64 {
	result := make(map[VariableID]float64)
	class := compiled.Prepared.Classes[classIndex]
	if subset == nil {
		subset = make([]int, len(class.Buckets))
		for i := range subset {
			subset[i] = i
		}
	}
	for _, bucketIndex := range subset {
		addCoefficient(result, compiled.ClassVariables[classIndex][bucketIndex], coefficient(class.Buckets[bucketIndex]))
	}
	return result
}

// mainCoefficients returns coefficient times the total mass of every atomic
// bucket contained in a semantic Main Group.
func mainCoefficients(compiled CompiledModel, classIndex int, coefficient float64) map[VariableID]float64 {
	result := make(map[VariableID]float64)
	for _, group := range compiled.Prepared.Classes[classIndex].Groups {
		for _, bucketIndex := range group.BucketIndexes {
			addCoefficient(result, compiled.ClassVariables[classIndex][bucketIndex], coefficient)
		}
	}
	return result
}

// addCoefficient accumulates sparse expressions while removing exact zeros.
// This keeps sortedTerms canonical when a bucket participates in two algebraic
// expressions whose coefficients cancel.
func addCoefficient(coefficients map[VariableID]float64, variable VariableID, amount float64) {
	if amount == 0 {
		return
	}
	coefficients[variable] += amount
	if coefficients[variable] == 0 {
		delete(coefficients, variable)
	}
}

// PrimaryValues maps an optimal solver vector back to ordered p[k,i] values.
// It rejects phase-specific vectors whose declared semantic variables do not
// agree with the CompiledModel rather than guessing by column position.
func (compiled CompiledModel) PrimaryValues(problem LinearProblem, values []float64) ([]float64, error) {
	if len(values) != len(problem.Variables) {
		return nil, fmt.Errorf("solution has %d values for %d semantic variables", len(values), len(problem.Variables))
	}
	indices := make(map[VariableID]int, len(problem.Variables))
	for i, variable := range problem.Variables {
		indices[variable.ID] = i
	}
	primary := make([]float64, len(compiled.Primary))
	for i, variable := range compiled.Primary {
		column, exists := indices[variable.ID]
		if !exists {
			return nil, fmt.Errorf("solution is missing primary variable %q", variable.ID)
		}
		primary[i] = values[column]
	}
	return primary, nil
}
