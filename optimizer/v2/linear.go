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
)

// VariableID is the stable semantic identity of one LP variable.
//
// IDs are deliberately strings because reports and diagnostics must retain a
// human-readable bridge from the mathematical model back to a Class/bucket.
// Backend-created slack columns never receive a VariableID and therefore can
// never leak into candidate identity or product reporting.
type VariableID string

// RowID is the stable semantic identity of one compiled constraint row.
//
// A RowID is not a Gonum row number. It survives backend conversion so an
// infeasibility diagnostic can still identify the originating design rule.
type RowID string

// RuleOrigin records who owns a rule and whether it is hard or preferential.
type RuleOrigin string

const (
	OriginDesignerHard             RuleOrigin = "DesignerHard"
	OriginSystemInvariant          RuleOrigin = "SystemInvariant"
	OriginDerivedSafety            RuleOrigin = "DerivedSafety"
	OriginDerivedSemanticGuardrail RuleOrigin = "DerivedSemanticGuardrail"
	OriginDesignerPreference       RuleOrigin = "DesignerPreference"
	OriginSystemNeutralPreference  RuleOrigin = "SystemNeutralPreference"
	OriginCanonicalization         RuleOrigin = "Canonicalization"
)

// Sense is the semantic comparison operator used by a LinearRow.
type Sense uint8

const (
	SenseEQ Sense = iota
	SenseLE
	SenseGE
)

// String renders a stable operator name for hashes, diagnostics, and tests.
func (s Sense) String() string {
	switch s {
	case SenseEQ:
		return "EQ"
	case SenseLE:
		return "LE"
	case SenseGE:
		return "GE"
	default:
		return "UNKNOWN"
	}
}

// ObjectiveSense declares whether the semantic objective is minimized or
// maximized. The Gonum adapter owns any sign inversion needed by its API.
type ObjectiveSense uint8

const (
	Minimize ObjectiveSense = iota
	Maximize
)

// ObjectiveOrigin tells the Engine how an infeasible backend result should be
// interpreted. In particular, infeasible preference probes update a bisection
// bracket and are not Run-level failures.
type ObjectiveOrigin string

const (
	ObjectiveHardFeasibility                  ObjectiveOrigin = "HardFeasibility"
	ObjectiveMainProfileProbe                 ObjectiveOrigin = "MainProfileProbe"
	ObjectiveOtherBucketVisibilityProbe       ObjectiveOrigin = "OtherBucketVisibilityProbe"
	ObjectiveMainGroupInternalVisibilityProbe ObjectiveOrigin = "MainGroupInternalVisibilityProbe"
	ObjectiveIntentRefinement                 ObjectiveOrigin = "IntentRefinement"
	ObjectiveCanonicalBucketProbability       ObjectiveOrigin = "CanonicalBucketProbability"
	ObjectiveDiagnostic                       ObjectiveOrigin = "DiagnosticBound"
	ObjectiveCandidateProbe                   ObjectiveOrigin = "CandidateProbe"

	// Deprecated: use ObjectiveMainProfileProbe.
	ObjectivePhaseAProbe = ObjectiveMainProfileProbe
	// Deprecated: use ObjectiveOtherBucketVisibilityProbe.
	ObjectivePhaseBProbe = ObjectiveOtherBucketVisibilityProbe
	// Deprecated: use ObjectiveIntentRefinement.
	ObjectiveIntentRefine = ObjectiveIntentRefinement
	// Deprecated: use ObjectiveCanonicalBucketProbability.
	ObjectiveCanonical = ObjectiveCanonicalBucketProbability
)

// LinearVariable defines one semantic variable and its explicit bounds.
// Lower must be finite in v2. Upper may be +Inf; NaN is always invalid.
type LinearVariable struct {
	ID              VariableID
	Lower           float64
	Upper           float64
	LowerProvenance []BoundProvenance
	UpperProvenance []BoundProvenance
}

// BoundProvenance records every semantic rule that contributed a candidate
// variable bound. The effective Lower is the maximum lower contribution and
// Upper is the minimum upper contribution. Keeping all contributors means an
// inactive collision cap remains auditable even when the invariant p<=1 is
// tighter, while an active cap is explicitly attributable to DerivedSafety.
type BoundProvenance struct {
	Origin      RuleOrigin
	YAMLPath    string
	Description string
	Bound       float64
}

// LinearTerm is one sparse coefficient in a row or objective. Terms must be
// sorted by VariableID so model construction never depends on Go map order.
type LinearTerm struct {
	Variable VariableID
	Coeff    float64
}

// LinearRow is a backend-neutral semantic constraint with source provenance.
type LinearRow struct {
	ID          RowID
	Family      string
	Origin      RuleOrigin
	ClassID     string
	YAMLPath    string
	Description string
	Sense       Sense
	RHS         float64
	Terms       []LinearTerm
}

// LinearProblem is an immutable-by-convention named LP model. Variables and
// rows are kept in compiler order, which is part of deterministic rebuilding.
type LinearProblem struct {
	Variables []LinearVariable
	Rows      []LinearRow
}

// LinearObjective is supplied separately from the base model because preference
// probes, diagnostics, and canonicalization reuse the same semantic rows.
type LinearObjective struct {
	Name   OptimizationStageID
	Sense  ObjectiveSense
	Origin ObjectiveOrigin
	Terms  []LinearTerm
}

// SolveOptions contains developer-owned numerical controls. These values are
// not Designer intent and a backend must not silently relax them.
type SolveOptions struct {
	FeasibilityTolerance float64
	OptimalityTolerance  float64
}

// SolveStatus is a backend result, not a product Run status.
type SolveStatus uint8

const (
	SolveOptimal SolveStatus = iota
	SolveInfeasible
	SolveNumericalFailure
	SolveUnbounded
)

// SolverEvidence captures the backend facts needed to audit status mapping.
type SolverEvidence struct {
	Objective         string  `json:"objective,omitempty"`
	Backend           string  `json:"backend"`
	BackendVersion    string  `json:"backend_version"`
	BackendCode       string  `json:"backend_code"`
	TransformVersion  string  `json:"transform_version"`
	SemanticColumns   int     `json:"semantic_columns"`
	AuxiliaryColumns  int     `json:"auxiliary_columns"`
	Rows              int     `json:"rows"`
	MaxRowViolation   float64 `json:"max_row_violation"`
	MaxBoundViolation float64 `json:"max_bound_violation"`
}

// SolveResult returns values in LinearProblem.Variables order. Values is empty
// for every non-optimal status so callers cannot accidentally use a partial or
// stale primal vector.
type SolveResult struct {
	Status         SolveStatus
	Values         []float64
	ObjectiveValue float64
	Evidence       SolverEvidence
}

// Solver isolates all backend-specific matrix conversion and status handling.
// Mathematical outcomes are returned through SolveResult; error is reserved
// for cancellation and broken adapter contracts.
type Solver interface {
	Solve(context.Context, LinearProblem, LinearObjective, SolveOptions) (SolveResult, error)
}

// cloneLinearProblem makes a deep copy before a lexicographic phase adds rows.
// This prevents one candidate or bisection probe from mutating the shared base.
func cloneLinearProblem(source LinearProblem) LinearProblem {
	result := LinearProblem{
		Variables: append([]LinearVariable(nil), source.Variables...),
		Rows:      make([]LinearRow, len(source.Rows)),
	}
	for i := range result.Variables {
		result.Variables[i].LowerProvenance = append([]BoundProvenance(nil), source.Variables[i].LowerProvenance...)
		result.Variables[i].UpperProvenance = append([]BoundProvenance(nil), source.Variables[i].UpperProvenance...)
	}
	for i, row := range source.Rows {
		result.Rows[i] = row
		result.Rows[i].Terms = append([]LinearTerm(nil), row.Terms...)
	}
	return result
}

// sortedTerms converts a coefficient map into canonical sparse order. Model
// builders use this helper at the boundary and never retain the source map.
func sortedTerms(coefficients map[VariableID]float64) []LinearTerm {
	terms := make([]LinearTerm, 0, len(coefficients))
	for variable, coefficient := range coefficients {
		if coefficient != 0 {
			terms = append(terms, LinearTerm{Variable: variable, Coeff: coefficient})
		}
	}
	sort.Slice(terms, func(i, j int) bool { return terms[i].Variable < terms[j].Variable })
	return terms
}

// addVariable appends a semantic variable while rejecting duplicate IDs at the
// point of construction, where the compiler still knows the responsible rule.
func addVariable(problem *LinearProblem, variable LinearVariable) error {
	for _, existing := range problem.Variables {
		if existing.ID == variable.ID {
			return fmt.Errorf("duplicate LP variable %q", variable.ID)
		}
	}
	problem.Variables = append(problem.Variables, variable)
	return nil
}

// addRow appends one fully named row. Coefficients are copied and sorted so a
// caller cannot later mutate the model through a retained map or slice.
func addRow(problem *LinearProblem, row LinearRow) error {
	for _, existing := range problem.Rows {
		if existing.ID == row.ID {
			return fmt.Errorf("duplicate LP row %q", row.ID)
		}
	}
	row.Terms = append([]LinearTerm(nil), row.Terms...)
	sort.Slice(row.Terms, func(i, j int) bool { return row.Terms[i].Variable < row.Terms[j].Variable })
	problem.Rows = append(problem.Rows, row)
	return nil
}

// validateLinearProblem checks the semantic contract before any backend
// conversion. Invalid compiled data is an internal error, never infeasibility.
func validateLinearProblem(problem LinearProblem, objective LinearObjective, options SolveOptions) error {
	if !finitePositive(options.FeasibilityTolerance) || !finitePositive(options.OptimalityTolerance) {
		return fmt.Errorf("solver tolerances must be finite and positive")
	}
	variables := make(map[VariableID]struct{}, len(problem.Variables))
	for i, variable := range problem.Variables {
		if variable.ID == "" {
			return fmt.Errorf("variable[%d] has empty ID", i)
		}
		if _, duplicate := variables[variable.ID]; duplicate {
			return fmt.Errorf("duplicate variable ID %q", variable.ID)
		}
		if math.IsNaN(variable.Lower) || math.IsInf(variable.Lower, 0) || math.IsNaN(variable.Upper) || variable.Upper < variable.Lower {
			return fmt.Errorf("variable %q has invalid bounds", variable.ID)
		}
		if err := validateBoundProvenance(variable); err != nil {
			return fmt.Errorf("variable %q: %w", variable.ID, err)
		}
		variables[variable.ID] = struct{}{}
	}
	rows := make(map[RowID]struct{}, len(problem.Rows))
	for i, row := range problem.Rows {
		if row.ID == "" || !isFinite(row.RHS) {
			return fmt.Errorf("row[%d] has invalid identity or RHS", i)
		}
		if row.Sense > SenseGE {
			return fmt.Errorf("row %q has invalid sense", row.ID)
		}
		if _, duplicate := rows[row.ID]; duplicate {
			return fmt.Errorf("duplicate row ID %q", row.ID)
		}
		if err := validateTerms(row.Terms, variables); err != nil {
			return fmt.Errorf("row %q: %w", row.ID, err)
		}
		rows[row.ID] = struct{}{}
	}
	if objective.Sense > Maximize || objective.Origin == "" {
		return fmt.Errorf("objective has invalid sense or origin")
	}
	if err := validateTerms(objective.Terms, variables); err != nil {
		return fmt.Errorf("objective: %w", err)
	}
	return nil
}

func validateBoundProvenance(variable LinearVariable) error {
	for _, field := range []struct {
		side       string
		provenance []BoundProvenance
	}{
		{side: "lower", provenance: variable.LowerProvenance},
		{side: "upper", provenance: variable.UpperProvenance},
	} {
		for i, source := range field.provenance {
			if source.Origin == "" || !isFinite(source.Bound) || source.Description == "" {
				return fmt.Errorf("%s provenance[%d] has invalid origin, bound, or description", field.side, i)
			}
		}
	}
	if len(variable.LowerProvenance) > 0 {
		effective := variable.LowerProvenance[0].Bound
		for _, source := range variable.LowerProvenance[1:] {
			effective = math.Max(effective, source.Bound)
		}
		if effective != variable.Lower {
			return fmt.Errorf("lower provenance resolves to %.17g, want %.17g", effective, variable.Lower)
		}
	}
	if len(variable.UpperProvenance) > 0 {
		effective := variable.UpperProvenance[0].Bound
		for _, source := range variable.UpperProvenance[1:] {
			effective = math.Min(effective, source.Bound)
		}
		if effective != variable.Upper {
			return fmt.Errorf("upper provenance resolves to %.17g, want %.17g", effective, variable.Upper)
		}
	}
	return nil
}

// validateTerms verifies canonical sparse coefficients and declared variables.
func validateTerms(terms []LinearTerm, variables map[VariableID]struct{}) error {
	var previous VariableID
	for i, term := range terms {
		if !isFinite(term.Coeff) || term.Coeff == 0 {
			return fmt.Errorf("term[%d] has non-finite or zero coefficient", i)
		}
		if _, exists := variables[term.Variable]; !exists {
			return fmt.Errorf("term[%d] references unknown variable %q", i, term.Variable)
		}
		if i > 0 && term.Variable <= previous {
			return fmt.Errorf("terms are not strictly sorted by VariableID")
		}
		previous = term.Variable
	}
	return nil
}

// finitePositive centralizes numerical option validation without accepting NaN
// (whose comparisons otherwise produce surprising false results).
func finitePositive(value float64) bool {
	return isFinite(value) && value > 0
}

// isFinite is the shared guard used before values enter a model or artifact.
func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
