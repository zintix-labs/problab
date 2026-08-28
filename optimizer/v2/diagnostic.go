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
	"strings"
)

type diagnosticRangeResult struct {
	minimum float64
	maximum float64
	proved  bool
}

// diagnoseHardModelInfeasibility runs bounded, deterministic auxiliary LPs
// after—and only after—the immutable hard model is explicitly infeasible. It
// first projects the hard model into one independent problem per intent Class.
// This is essential for useful reporting: if fg_02 and fg_07 are both locally
// infeasible, relaxing an fg_02 row in the full model would still be blocked by
// fg_07 and would incorrectly collapse to a generic HardModelInfeasible result.
//
// Every locally infeasible Class produces one ordered diagnostic. Within that
// diagnostic, multiple proved range findings are adjustment alternatives for
// the same conflict (for example, raise exact mean OR raise Main maximum), not
// a claim that the Designer must change every listed field. Diagnostic solves
// never relax the real Run, retry collection, or become candidate witnesses.
func (e *IntentEngine) diagnoseHardModelInfeasibility(
	ctx context.Context,
	compiled CompiledModel,
	options SolveOptions,
) (Diagnostics, error) {
	localDiagnostics := make(Diagnostics, 0)
	for classIndex, class := range compiled.Prepared.Classes {
		if !class.Intent {
			continue
		}
		local := classLocalHardProblem(compiled, classIndex)
		if len(local.Rows) == 0 {
			continue
		}
		result, err := e.solver.Solve(ctx, local, LinearObjective{Name: "diagnostic-bound", Sense: Minimize, Origin: ObjectiveDiagnostic}, options)
		if err != nil {
			return nil, err
		}
		if result.Status != SolveInfeasible {
			continue
		}

		findings, err := e.diagnoseClassHardConflict(ctx, compiled, local, classIndex, options)
		if err != nil {
			return nil, err
		}
		if len(findings) == 0 {
			localDiagnostics = append(localDiagnostics, Diagnostic{
				Code: DiagnosticHardModelInfeasible, Status: StatusInfeasibleModel,
				Message:     fmt.Sprintf("class %q has incompatible hard rules, but no single removable semantic family exposes an achievable adjustment bound", class.ID),
				SourcePaths: classHardSourcePaths(local), ConstraintIDs: classHardConstraintIDs(local),
				Representation: RepresentationAtomicBuckets,
			})
			continue
		}
		localDiagnostics = append(localDiagnostics, findings...)
	}
	if len(localDiagnostics) > 0 {
		return localDiagnostics, nil
	}

	// If every Class is locally feasible, retain the full-model probes for truly
	// coupled constraints such as overall CV. Stable family order is intentional.
	for classIndex, class := range compiled.Prepared.Classes {
		if !class.Intent {
			continue
		}
		if diagnostic, proved, err := e.diagnoseClassMean(ctx, compiled, compiled.Hard, classIndex, options); err != nil || proved {
			return diagnosticsWhenProved(diagnostic, proved), err
		}
		if diagnostic, proved, err := e.diagnoseClassMedian(ctx, compiled, compiled.Hard, classIndex, options); err != nil || proved {
			return diagnosticsWhenProved(diagnostic, proved), err
		}
		if diagnostic, proved, err := e.diagnoseMainProbability(ctx, compiled, compiled.Hard, classIndex, options); err != nil || proved {
			return diagnosticsWhenProved(diagnostic, proved), err
		}
	}
	if diagnostic, proved, err := e.diagnoseGlobalCV(ctx, compiled, options); err != nil || proved {
		return diagnosticsWhenProved(diagnostic, proved), err
	}
	return nil, nil
}

// diagnoseClassHardConflict first computes every single-family relaxation that
// fully explains one Class-local infeasibility. More than one such finding is
// merged as alternative edits to the same conflict. If no family can be
// relaxed in the full local model, the Class contains multiple independent
// conflicts; focused family subsets then prove and report each sufficient
// conflict separately instead of allowing them to mask one another.
func (e *IntentEngine) diagnoseClassHardConflict(
	ctx context.Context,
	compiled CompiledModel,
	source LinearProblem,
	classIndex int,
	options SolveOptions,
) (Diagnostics, error) {
	findings := make(Diagnostics, 0, 3)
	if diagnostic, proved, err := e.diagnoseClassMean(ctx, compiled, source, classIndex, options); err != nil {
		return nil, err
	} else if proved {
		findings = append(findings, diagnostic)
	}
	if diagnostic, proved, err := e.diagnoseClassMedian(ctx, compiled, source, classIndex, options); err != nil {
		return nil, err
	} else if proved {
		findings = append(findings, diagnostic)
	}
	if diagnostic, proved, err := e.diagnoseMainProbability(ctx, compiled, source, classIndex, options); err != nil {
		return nil, err
	} else if proved {
		findings = append(findings, diagnostic)
	}
	if len(findings) > 0 {
		return Diagnostics{mergeClassDiagnosticFindings(compiled.Prepared.Classes[classIndex].ID, findings)}, nil
	}

	focused := make(Diagnostics, 0, 4)
	meanProblem := classFocusedHardProblem(source, "class_mean")
	if diagnostic, proved, err := e.diagnoseClassMean(ctx, compiled, meanProblem, classIndex, options); err != nil {
		return nil, err
	} else if proved {
		focused = append(focused, diagnostic)
	}
	medianProblem := classFocusedHardProblem(source, "class_median")
	if diagnostic, proved, err := e.diagnoseClassMedian(ctx, compiled, medianProblem, classIndex, options); err != nil {
		return nil, err
	} else if proved {
		focused = append(focused, diagnostic)
	}
	mainProblem := classFocusedHardProblem(source, "main_total")
	if diagnostic, proved, err := e.diagnoseMainProbability(ctx, compiled, mainProblem, classIndex, options); err != nil {
		return nil, err
	} else if proved {
		focused = append(focused, diagnostic)
	}
	guardrailProblem := classFocusedHardProblem(source, "main_group_guardrail")
	if diagnostic, proved, err := e.diagnoseMainGroupGuardrailCapacity(ctx, compiled, guardrailProblem, classIndex, options); err != nil {
		return nil, err
	} else if proved {
		focused = append(focused, diagnostic)
	}
	return focused, nil
}

// diagnoseClassMean computes the conditional mean range available after only
// that Class's exact mean row is removed. It reports a typed hard-model cause
// when other hard rules narrow the empirical coefficient hull around Exp.
func (e *IntentEngine) diagnoseClassMean(
	ctx context.Context,
	compiled CompiledModel,
	source LinearProblem,
	classIndex int,
	options SolveOptions,
) (Diagnostic, bool, error) {
	class := compiled.Prepared.Classes[classIndex]
	row := findSemanticRow(source, "class_mean", class.ID)
	if row == nil {
		return Diagnostic{}, false, nil
	}
	rangeResult, err := e.diagnosticRange(ctx, source, func(candidate LinearRow) bool {
		return candidate.Family == "class_mean" && candidate.ClassID == class.ID
	}, row.Terms, options)
	if err != nil || !rangeResult.proved {
		return Diagnostic{}, false, err
	}
	tolerance := scaledTolerance(options.FeasibilityTolerance, class.Design.Exp, rangeResult.minimum, rangeResult.maximum)
	if class.Design.Exp >= rangeResult.minimum-tolerance && class.Design.Exp <= rangeResult.maximum+tolerance {
		return Diagnostic{}, false, nil
	}
	deficit := intervalDeficit(class.Design.Exp, class.Design.Exp, rangeResult.minimum, rangeResult.maximum)
	message := fmt.Sprintf("class %q exact mean %.12g is outside achievable [%.12g, %.12g] under the remaining normalization, median, Main, support/risk, and semantic rules", class.ID, class.Design.Exp, rangeResult.minimum, rangeResult.maximum)
	if class.Design.Exp < rangeResult.minimum {
		message = fmt.Sprintf("class %q exact mean %.12g is below the minimum achievable %.12g; raise design.exp to at least %.12g (minimum change %.12g), or relax a limiting median/Main/support/risk rule", class.ID, class.Design.Exp, rangeResult.minimum, rangeResult.minimum, deficit)
	} else if class.Design.Exp > rangeResult.maximum {
		message = fmt.Sprintf("class %q exact mean %.12g is above the maximum achievable %.12g; lower design.exp to at most %.12g (minimum change %.12g), or relax a limiting median/Main/support/risk rule", class.ID, class.Design.Exp, rangeResult.maximum, rangeResult.maximum, deficit)
	}
	diagnostic := modelBoundDiagnostic(
		DiagnosticClassMeanInfeasible,
		message,
		row, Bound{Min: class.Design.Exp, Max: class.Design.Exp}, Bound{Min: rangeResult.minimum, Max: rangeResult.maximum}, deficit,
	)
	diagnostic.Causes = []Cause{{
		Summary:     "configured exact Class mean does not intersect the range allowed by every other local hard rule",
		SourcePaths: effectiveRiskSourcePaths(compiled, classIndex),
		Metrics: []NamedValue{
			{Name: "configured_exact_mean", Value: class.Design.Exp, Unit: "payout_multiplier"},
			{Name: "minimum_achievable_mean", Value: rangeResult.minimum, Unit: "payout_multiplier"},
			{Name: "maximum_achievable_mean", Value: rangeResult.maximum, Unit: "payout_multiplier"},
			{Name: "minimum_required_change", Value: deficit, Unit: "payout_multiplier"},
		},
	}}
	diagnostic.SourcePaths = appendUniqueStrings(diagnostic.SourcePaths, effectiveRiskSourcePaths(compiled, classIndex)...)
	return diagnostic, true, nil
}

// diagnoseClassMedian removes both weighted-lower-median rows, then minimizes
// P(X<L) and maximizes P(X<=U). Either side can independently prove that the
// configured median interval is unreachable under the remaining hard rules.
func (e *IntentEngine) diagnoseClassMedian(
	ctx context.Context,
	compiled CompiledModel,
	source LinearProblem,
	classIndex int,
	options SolveOptions,
) (Diagnostic, bool, error) {
	class := compiled.Prepared.Classes[classIndex]
	lower := findRowByID(source, RowID(fmt.Sprintf("class:%04d:median:lower", classIndex)))
	upper := findRowByID(source, RowID(fmt.Sprintf("class:%04d:median:upper", classIndex)))
	if lower == nil || upper == nil {
		return Diagnostic{}, false, nil
	}
	removeMedian := func(candidate LinearRow) bool {
		return candidate.Family == "class_median" && candidate.ClassID == class.ID
	}
	lowerRange, err := e.diagnosticRange(ctx, source, removeMedian, lower.Terms, options)
	if err != nil {
		return Diagnostic{}, false, err
	}
	upperRange, err := e.diagnosticRange(ctx, source, removeMedian, upper.Terms, options)
	if err != nil || !lowerRange.proved || !upperRange.proved {
		return Diagnostic{}, false, err
	}
	lowerLimit := 0.5 - compiled.Prepared.Plan.EngineOptions.QuantileEpsilon
	upperLimit := 0.5
	tolerance := options.FeasibilityTolerance
	if lowerRange.minimum <= lowerLimit+scaledTolerance(tolerance, lowerRange.minimum, lowerLimit) &&
		upperRange.maximum >= upperLimit-scaledTolerance(tolerance, upperRange.maximum, upperLimit) {
		return Diagnostic{}, false, nil
	}
	lowerDeficit := math.Max(0, lowerRange.minimum-lowerLimit)
	upperDeficit := math.Max(0, upperLimit-upperRange.maximum)
	deficit := math.Max(lowerDeficit, upperDeficit)
	parts := make([]string, 0, 2)
	causes := make([]Cause, 0, 2)
	if lowerDeficit > 0 {
		parts = append(parts, fmt.Sprintf("median lower endpoint %.12g requires P(X<L) <= %.12g, but the minimum achievable is %.12g; cumulative mass below L must decrease by at least %.12g, or the endpoint/support/risk rules must change", class.Design.Median.Lower(), lowerLimit, lowerRange.minimum, lowerDeficit))
		causes = append(causes, Cause{Summary: "too much unavoidable probability lies below the configured median lower endpoint", Metrics: []NamedValue{
			{Name: "required_maximum_probability_below_L", Value: lowerLimit, Unit: "conditional_probability"},
			{Name: "minimum_achievable_probability_below_L", Value: lowerRange.minimum, Unit: "conditional_probability"},
			{Name: "minimum_required_probability_change", Value: lowerDeficit, Unit: "conditional_probability"},
		}})
	}
	if upperDeficit > 0 {
		parts = append(parts, fmt.Sprintf("median upper endpoint %.12g requires P(X<=U) >= %.12g, but the maximum achievable is %.12g; cumulative mass at or below U must increase by at least %.12g, or the endpoint/support/risk rules must change", class.Design.Median.Upper(), upperLimit, upperRange.maximum, upperDeficit))
		causes = append(causes, Cause{Summary: "too little achievable probability lies at or below the configured median upper endpoint", Metrics: []NamedValue{
			{Name: "required_minimum_probability_at_or_below_U", Value: upperLimit, Unit: "conditional_probability"},
			{Name: "maximum_achievable_probability_at_or_below_U", Value: upperRange.maximum, Unit: "conditional_probability"},
			{Name: "minimum_required_probability_change", Value: upperDeficit, Unit: "conditional_probability"},
		}})
	}
	diagnostic := Diagnostic{
		Code: DiagnosticMedianInfeasible, Status: StatusInfeasibleModel,
		Message:     fmt.Sprintf("class %q %s", class.ID, strings.Join(parts, "; ")),
		SourcePaths: []string{lower.YAMLPath, upper.YAMLPath}, ConstraintIDs: []string{string(lower.ID), string(upper.ID)},
		Deficit: deficit, Representation: RepresentationAtomicBuckets,
		Causes: causes,
	}
	diagnostic.SourcePaths = appendUniqueStrings(diagnostic.SourcePaths, effectiveRiskSourcePaths(compiled, classIndex)...)
	return diagnostic, true, nil
}

// diagnoseMainProbability removes one Class's Main min/max rows and computes
// the achievable Main-total interval under mean, median, risk, CV, and semantic
// guardrails. This directly supports requested/achievable/deficit reporting.
func (e *IntentEngine) diagnoseMainProbability(
	ctx context.Context,
	compiled CompiledModel,
	source LinearProblem,
	classIndex int,
	options SolveOptions,
) (Diagnostic, bool, error) {
	class := compiled.Prepared.Classes[classIndex]
	row := findSemanticRow(source, "main_total", class.ID)
	if row == nil {
		return Diagnostic{}, false, nil
	}
	expression := sortedTerms(mainCoefficients(compiled, classIndex, 1))
	rangeResult, err := e.diagnosticRange(ctx, source, func(candidate LinearRow) bool {
		return candidate.Family == "main_total" && candidate.ClassID == class.ID
	}, expression, options)
	if err != nil || !rangeResult.proved {
		return Diagnostic{}, false, err
	}
	requested := class.Design.Subjective.MainExperience.Probability
	tolerance := scaledTolerance(options.FeasibilityTolerance, requested.Min, requested.Max, rangeResult.minimum, rangeResult.maximum)
	if requested.Min <= rangeResult.maximum+tolerance && requested.Max >= rangeResult.minimum-tolerance {
		return Diagnostic{}, false, nil
	}
	deficit := intervalDeficit(requested.Min, requested.Max, rangeResult.minimum, rangeResult.maximum)
	message := fmt.Sprintf("class %q Main probability [%.12g, %.12g] does not intersect achievable [%.12g, %.12g]", class.ID, requested.Min, requested.Max, rangeResult.minimum, rangeResult.maximum)
	if requested.Min > rangeResult.maximum {
		message = fmt.Sprintf("class %q Main probability minimum %.12g exceeds the maximum achievable %.12g; lower probability.min to at most %.12g (minimum change %.12g), or relax a limiting mean/median/support/risk rule", class.ID, requested.Min, rangeResult.maximum, rangeResult.maximum, deficit)
	} else if requested.Max < rangeResult.minimum {
		message = fmt.Sprintf("class %q Main probability maximum %.12g is below the minimum required %.12g; raise probability.max to at least %.12g (minimum change %.12g), or relax a limiting mean/median/support/risk rule", class.ID, requested.Max, rangeResult.minimum, rangeResult.minimum, deficit)
	}
	provenanceRow := row
	if requested.Min > rangeResult.maximum {
		if minimumRow := findRowByID(source, RowID(fmt.Sprintf("class:%04d:main:min", classIndex))); minimumRow != nil {
			provenanceRow = minimumRow
		}
	} else if requested.Max < rangeResult.minimum {
		if maximumRow := findRowByID(source, RowID(fmt.Sprintf("class:%04d:main:max", classIndex))); maximumRow != nil {
			provenanceRow = maximumRow
		}
	}
	diagnostic := modelBoundDiagnostic(
		DiagnosticMainProbabilityInfeasible,
		message,
		provenanceRow, Bound(requested), Bound{Min: rangeResult.minimum, Max: rangeResult.maximum},
		deficit,
	)
	diagnostic.Causes = []Cause{{
		Summary:     "configured Main probability interval does not intersect the range allowed by every other local hard rule",
		SourcePaths: effectiveRiskSourcePaths(compiled, classIndex),
		Metrics: []NamedValue{
			{Name: "requested_minimum_main_probability", Value: requested.Min, Unit: "conditional_probability"},
			{Name: "requested_maximum_main_probability", Value: requested.Max, Unit: "conditional_probability"},
			{Name: "minimum_achievable_main_probability", Value: rangeResult.minimum, Unit: "conditional_probability"},
			{Name: "maximum_achievable_main_probability", Value: rangeResult.maximum, Unit: "conditional_probability"},
			{Name: "minimum_required_change", Value: deficit, Unit: "conditional_probability"},
		},
	}}
	diagnostic.SourcePaths = appendUniqueStrings(diagnostic.SourcePaths, effectiveRiskSourcePaths(compiled, classIndex)...)
	return diagnostic, true, nil
}

// diagnoseMainGroupGuardrailCapacity proves that normalization cannot coexist
// with the derived rule requiring every configured Main Group to carry at
// least the average supported Other-bucket mass. Variable upper bounds already
// contain support and effective risk caps, so maximizing total mass after only
// normalization is removed yields the exact capacity and minimum shortfall.
func (e *IntentEngine) diagnoseMainGroupGuardrailCapacity(
	ctx context.Context,
	compiled CompiledModel,
	source LinearProblem,
	classIndex int,
	options SolveOptions,
) (Diagnostic, bool, error) {
	class := compiled.Prepared.Classes[classIndex]
	normalization := findSemanticRow(source, "normalization", class.ID)
	if normalization == nil || findSemanticRow(source, "main_group_guardrail", class.ID) == nil {
		return Diagnostic{}, false, nil
	}
	rangeResult, err := e.diagnosticRange(ctx, source, func(candidate LinearRow) bool {
		return candidate.Family == "normalization" && candidate.ClassID == class.ID
	}, normalization.Terms, options)
	if err != nil || !rangeResult.proved {
		return Diagnostic{}, false, err
	}
	tolerance := scaledTolerance(options.FeasibilityTolerance, 1, rangeResult.maximum)
	if rangeResult.maximum >= 1-tolerance {
		return Diagnostic{}, false, nil
	}
	deficit := 1 - rangeResult.maximum
	paths := make([]string, 0)
	constraintIDs := []string{string(normalization.ID)}
	for _, row := range source.Rows {
		if row.Family != "main_group_guardrail" || row.ClassID != class.ID {
			continue
		}
		paths = appendUniqueStrings(paths, row.YAMLPath)
		constraintIDs = append(constraintIDs, string(row.ID))
	}
	paths = appendUniqueStrings(paths, effectiveRiskSourcePaths(compiled, classIndex)...)
	return Diagnostic{
		Code: DiagnosticMainGroupGuardrailInfeasible, Status: StatusInfeasibleModel,
		Message: fmt.Sprintf(
			"class %q Main-group guardrails plus bucket support/risk caps can carry at most %.12g total conditional mass, below required normalization 1; increase compatible capacity by at least %.12g, or revise main_experience.groups/support/risk rules",
			class.ID, rangeResult.maximum, deficit,
		),
		SourcePaths: paths, ConstraintIDs: constraintIDs,
		Requested: &Bound{Min: 1, Max: 1}, Achievable: &Bound{Min: rangeResult.minimum, Max: rangeResult.maximum}, Deficit: deficit,
		Representation: RepresentationAtomicBuckets,
		Causes: []Cause{{
			Summary:     "derived Main-group visibility guardrails leave insufficient conditional-mass capacity",
			SourcePaths: paths,
			Metrics: []NamedValue{
				{Name: "required_total_conditional_mass", Value: 1, Unit: "conditional_probability"},
				{Name: "maximum_guardrail_compatible_capacity", Value: rangeResult.maximum, Unit: "conditional_probability"},
				{Name: "minimum_required_capacity_increase", Value: deficit, Unit: "conditional_probability"},
			},
		}},
	}, true, nil
}

// diagnoseGlobalCV removes the two linearized global second-moment rows and
// maps the achievable moment range back to dimensionless CV using fixed M.
func (e *IntentEngine) diagnoseGlobalCV(
	ctx context.Context,
	compiled CompiledModel,
	options SolveOptions,
) (Diagnostic, bool, error) {
	lower := findRowByID(compiled.Hard, "global:cv:min")
	upper := findRowByID(compiled.Hard, "global:cv:max")
	if lower == nil || upper == nil {
		return Diagnostic{}, false, nil
	}
	rangeResult, err := e.diagnosticRange(ctx, compiled.Hard, func(candidate LinearRow) bool {
		return candidate.Family == "overall_cv"
	}, lower.Terms, options)
	if err != nil || !rangeResult.proved {
		return Diagnostic{}, false, err
	}
	mean := compiled.Prepared.ExpectedRTP()
	requestedCV := compiled.Prepared.Plan.Intent.Overall.CV
	requestedLowerSecond := mean * mean * (1 + requestedCV.Min*requestedCV.Min)
	fixedSecond := requestedLowerSecond - lower.RHS
	achievableLowerSecond := rangeResult.minimum + fixedSecond
	achievableUpperSecond := rangeResult.maximum + fixedSecond
	achievableCV := Bound{
		Min: secondMomentCV(mean, achievableLowerSecond),
		Max: secondMomentCV(mean, achievableUpperSecond),
	}
	tolerance := scaledTolerance(options.FeasibilityTolerance, requestedCV.Min, requestedCV.Max, achievableCV.Min, achievableCV.Max)
	if requestedCV.Min <= achievableCV.Max+tolerance && requestedCV.Max >= achievableCV.Min-tolerance {
		return Diagnostic{}, false, nil
	}
	diagnostic := modelBoundDiagnostic(
		DiagnosticGlobalCVInfeasible,
		fmt.Sprintf("overall CV [%.12g, %.12g] does not intersect achievable [%.12g, %.12g]", requestedCV.Min, requestedCV.Max, achievableCV.Min, achievableCV.Max),
		lower, Bound(requestedCV), achievableCV,
		intervalDeficit(requestedCV.Min, requestedCV.Max, achievableCV.Min, achievableCV.Max),
	)
	diagnostic.ConstraintIDs = []string{string(lower.ID), string(upper.ID)}
	diagnostic.SourcePaths = []string{lower.YAMLPath, upper.YAMLPath}
	return diagnostic, true, nil
}

// diagnosticRange solves min and max semantic objectives after a selected row
// family is removed from a clone. Infeasible relaxed models simply mean this
// single family cannot explain the coupled conflict. Numerical/unbounded probes
// also decline proof and leave the original hard result authoritative.
func (e *IntentEngine) diagnosticRange(
	ctx context.Context,
	source LinearProblem,
	remove func(LinearRow) bool,
	terms []LinearTerm,
	options SolveOptions,
) (diagnosticRangeResult, error) {
	relaxed := cloneProblemWithoutRows(source, remove)
	if len(relaxed.Rows) == len(source.Rows) {
		return diagnosticRangeResult{}, nil
	}
	// A zero semantic expression has the exact range [0,0], but only if the
	// relaxed rows themselves are feasible. This occurs naturally for a median
	// lower endpoint equal to the support minimum, where P(X<L) is identically
	// zero. Treating an empty term list as "unprovable" would hide an unrelated
	// upper-median conflict in the same Class.
	if len(terms) == 0 {
		feasibility, err := e.solver.Solve(ctx, relaxed, LinearObjective{Name: "diagnostic-bound", Sense: Minimize, Origin: ObjectiveDiagnostic}, options)
		if err != nil {
			return diagnosticRangeResult{}, err
		}
		if feasibility.Status != SolveOptimal {
			return diagnosticRangeResult{}, nil
		}
		return diagnosticRangeResult{minimum: 0, maximum: 0, proved: true}, nil
	}
	minimum, err := e.solver.Solve(ctx, relaxed, LinearObjective{Name: "diagnostic-bound", Sense: Minimize, Origin: ObjectiveDiagnostic, Terms: append([]LinearTerm(nil), terms...)}, options)
	if err != nil {
		return diagnosticRangeResult{}, err
	}
	maximum, err := e.solver.Solve(ctx, relaxed, LinearObjective{Name: "diagnostic-bound", Sense: Maximize, Origin: ObjectiveDiagnostic, Terms: append([]LinearTerm(nil), terms...)}, options)
	if err != nil {
		return diagnosticRangeResult{}, err
	}
	if minimum.Status != SolveOptimal || maximum.Status != SolveOptimal {
		return diagnosticRangeResult{}, nil
	}
	return diagnosticRangeResult{minimum: minimum.ObjectiveValue, maximum: maximum.ObjectiveValue, proved: true}, nil
}

// cloneProblemWithoutRows preserves variable and surviving row order while
// making independent term copies for bounded diagnostic transforms.
func cloneProblemWithoutRows(source LinearProblem, remove func(LinearRow) bool) LinearProblem {
	result := LinearProblem{Variables: append([]LinearVariable(nil), source.Variables...)}
	for _, row := range source.Rows {
		if remove(row) {
			continue
		}
		row.Terms = append([]LinearTerm(nil), row.Terms...)
		result.Rows = append(result.Rows, row)
	}
	return result
}

// findSemanticRow returns the first compiler-ordered row with matching family
// and Class identity; callers use it for objective/source provenance.
func findSemanticRow(problem LinearProblem, family, classID string) *LinearRow {
	for i := range problem.Rows {
		if problem.Rows[i].Family == family && problem.Rows[i].ClassID == classID {
			return &problem.Rows[i]
		}
	}
	return nil
}

// findRowByID resolves an exact stable RowID without parsing backend indexes.
func findRowByID(problem LinearProblem, id RowID) *LinearRow {
	for i := range problem.Rows {
		if problem.Rows[i].ID == id {
			return &problem.Rows[i]
		}
	}
	return nil
}

// classLocalHardProblem preserves only one Class's semantic variables and
// rows. Global rows are deliberately excluded: this projection is not a Run
// relaxation or candidate model, but a proof that the Class is already
// impossible before any cross-Class constraint is considered.
func classLocalHardProblem(compiled CompiledModel, classIndex int) LinearProblem {
	if classIndex < 0 || classIndex >= len(compiled.Prepared.Classes) || classIndex >= len(compiled.ClassVariables) {
		return LinearProblem{}
	}
	classID := compiled.Prepared.Classes[classIndex].ID
	variableIDs := make(map[VariableID]struct{}, len(compiled.ClassVariables[classIndex]))
	for _, id := range compiled.ClassVariables[classIndex] {
		variableIDs[id] = struct{}{}
	}
	local := LinearProblem{}
	for _, variable := range compiled.Hard.Variables {
		if _, ok := variableIDs[variable.ID]; ok {
			local.Variables = append(local.Variables, variable)
		}
	}
	for _, row := range compiled.Hard.Rows {
		if row.ClassID != classID {
			continue
		}
		row.Terms = append([]LinearTerm(nil), row.Terms...)
		local.Rows = append(local.Rows, row)
	}
	return local
}

// classFocusedHardProblem retains normalization plus one semantic family. An
// infeasible focused problem is a sufficient conflict on its own, even when a
// second independent conflict in the same Class prevents the ordinary
// one-family relaxation from becoming feasible.
func classFocusedHardProblem(source LinearProblem, family string) LinearProblem {
	focused := LinearProblem{Variables: append([]LinearVariable(nil), source.Variables...)}
	for _, row := range source.Rows {
		if row.Family != "normalization" && row.Family != family {
			continue
		}
		row.Terms = append([]LinearTerm(nil), row.Terms...)
		focused.Rows = append(focused.Rows, row)
	}
	return focused
}

// mergeClassDiagnosticFindings turns multiple successful family relaxations
// into one Class-level conflict. Their thresholds are alternatives: if both
// exact mean and Main maximum can independently be moved to restore
// feasibility, presenting them as separate mandatory failures would mislead
// the Designer into changing both.
func mergeClassDiagnosticFindings(classID string, findings Diagnostics) Diagnostic {
	if len(findings) == 1 {
		return findings[0]
	}
	prefix := fmt.Sprintf("class %q ", classID)
	details := make([]string, 0, len(findings))
	sourcePaths := make([]string, 0)
	constraintIDs := make([]string, 0)
	causes := make([]Cause, 0, len(findings))
	for _, finding := range findings {
		details = append(details, strings.TrimPrefix(finding.Message, prefix))
		sourcePaths = appendUniqueStrings(sourcePaths, finding.SourcePaths...)
		constraintIDs = appendUniqueStrings(constraintIDs, finding.ConstraintIDs...)
		if len(finding.Causes) > 0 {
			causes = append(causes, finding.Causes...)
		} else {
			causes = append(causes, Cause{Summary: finding.Message})
		}
	}
	return Diagnostic{
		Code: DiagnosticHardModelInfeasible, Status: StatusInfeasibleModel,
		Message: fmt.Sprintf(
			"class %q has mutually incompatible hard rules. Separately proved adjustment alternatives (choose one, not all): %s",
			classID,
			strings.Join(details, "; alternatively, "),
		),
		SourcePaths: sourcePaths, ConstraintIDs: constraintIDs,
		Representation: RepresentationAtomicBuckets, Causes: causes,
	}
}

func diagnosticsWhenProved(diagnostic Diagnostic, proved bool) Diagnostics {
	if !proved {
		return nil
	}
	return Diagnostics{diagnostic}
}

func classHardSourcePaths(problem LinearProblem) []string {
	paths := make([]string, 0, len(problem.Rows))
	for _, row := range problem.Rows {
		if row.YAMLPath != "" {
			paths = appendUniqueStrings(paths, row.YAMLPath)
		}
	}
	return paths
}

func classHardConstraintIDs(problem LinearProblem) []string {
	ids := make([]string, 0, len(problem.Rows))
	for _, row := range problem.Rows {
		ids = append(ids, string(row.ID))
	}
	return ids
}

func appendUniqueStrings(destination []string, values ...string) []string {
	for _, value := range values {
		if value == "" {
			continue
		}
		found := false
		for _, existing := range destination {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			destination = append(destination, value)
		}
	}
	return destination
}

// effectiveRiskSourcePaths exposes the Designer-owned risk rule only when it
// actually tightens at least one semantic bucket below mass 1. A configured
// risk stanza whose derived caps are all >= 1 must not be blamed for a conflict.
func effectiveRiskSourcePaths(compiled CompiledModel, classIndex int) []string {
	if classIndex < 0 || classIndex >= len(compiled.Prepared.Classes) {
		return nil
	}
	class := compiled.Prepared.Classes[classIndex]
	if class.Design.Risk == nil {
		return nil
	}
	for _, bucket := range class.Buckets {
		if isFinite(bucket.RiskCap) && bucket.RiskCap < 1 {
			return []string{fmt.Sprintf("intents.%s.classes[%d].design.risk", compiled.Prepared.Plan.Plan.Intent, classIndex)}
		}
	}
	return nil
}

// modelBoundDiagnostic constructs a typed requested-versus-achievable hard
// model explanation using the compiler row's stable provenance.
func modelBoundDiagnostic(code DiagnosticCode, message string, row *LinearRow, requested, achievable Bound, deficit float64) Diagnostic {
	return Diagnostic{
		Code: code, Status: StatusInfeasibleModel, Message: message,
		SourcePaths: []string{row.YAMLPath}, ConstraintIDs: []string{string(row.ID)},
		Requested: &requested, Achievable: &achievable, Deficit: math.Max(0, deficit),
		Representation: RepresentationAtomicBuckets,
	}
}

// intervalDeficit returns the nonnegative gap between two disjoint inclusive
// intervals and zero when they overlap.
func intervalDeficit(requestedMin, requestedMax, achievableMin, achievableMax float64) float64 {
	if requestedMin > achievableMax {
		return requestedMin - achievableMax
	}
	if achievableMin > requestedMax {
		return achievableMin - requestedMax
	}
	return 0
}

// secondMomentCV converts an unconditional second moment back to CV for fixed
// positive mean, clamping only tiny negative round-off inside the square root.
func secondMomentCV(mean, secondMoment float64) float64 {
	if mean <= 0 || !isFinite(mean) || !isFinite(secondMoment) {
		return math.NaN()
	}
	return math.Sqrt(math.Max(0, secondMoment-mean*mean)) / mean
}
