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
	"errors"
	"fmt"
	"math"

	"gonum.org/v1/gonum/mat"
	"gonum.org/v1/gonum/optimize/convex/lp"
)

const gonumTransformVersion = "intent-lp-v2/gonum-standard-form-v2"

var errInconsistentDependentEquality = errors.New("semantic equalities are linearly dependent with inconsistent right-hand sides")

// GonumSolver is the production adapter for Gonum's equality/nonnegative
// simplex API. It contains no product-stage policy; the Engine interprets an
// infeasible result according to ObjectiveOrigin.
type GonumSolver struct{}

// NewGonumSolver constructs the stateless canonical LP backend. Keeping this
// constructor explicit lets tests inject a fake Solver without importing Gonum
// anywhere else in the v2 package.
func NewGonumSolver() *GonumSolver {
	return &GonumSolver{}
}

type standardRow struct {
	id    RowID
	sense Sense
	rhs   float64
	coeff []float64
}

type standardForm struct {
	semanticColumns   int
	rows              []standardRow
	objective         []float64
	lower             []float64
	maximize          bool
	droppedEqualities int
}

// Solve converts the named semantic problem, calls Gonum exactly once, maps
// the result back to semantic coordinates, and independently replays every
// original row. A replay failure is numerical failure, never a usable optimum.
func (s *GonumSolver) Solve(
	ctx context.Context,
	problem LinearProblem,
	objective LinearObjective,
	options SolveOptions,
) (SolveResult, error) {
	if err := ctx.Err(); err != nil {
		return SolveResult{}, err
	}
	if err := validateLinearProblem(problem, objective, options); err != nil {
		return SolveResult{}, fmt.Errorf("validate semantic LP: %w", err)
	}
	form, err := buildStandardForm(problem, objective, options.FeasibilityTolerance)
	if err != nil {
		if errors.Is(err, errInconsistentDependentEquality) {
			return SolveResult{Status: SolveInfeasible, Evidence: SolverEvidence{
				Objective: string(objective.Name),
				Backend:   "gonum/lp.Simplex", BackendVersion: "gonum.org/v1/gonum/v0.16.0",
				BackendCode: "inconsistent_dependent_equality", TransformVersion: gonumTransformVersion,
				SemanticColumns: len(problem.Variables), Rows: len(problem.Rows),
			}}, nil
		}
		return SolveResult{}, fmt.Errorf("build Gonum standard form: %w", err)
	}
	evidence := SolverEvidence{
		Objective:        string(objective.Name),
		Backend:          "gonum/lp.Simplex",
		BackendVersion:   "gonum.org/v1/gonum/v0.16.0",
		TransformVersion: gonumTransformVersion,
		SemanticColumns:  form.semanticColumns,
		Rows:             len(problem.Rows),
	}
	data, rhs, backendObjective, columns := materializeStandardMatrix(form)
	evidence.AuxiliaryColumns = columns - form.semanticColumns
	if len(form.rows) == 0 {
		for _, coefficient := range backendObjective {
			if coefficient < -options.OptimalityTolerance {
				evidence.BackendCode = "unbounded_without_rows"
				return SolveResult{Status: SolveUnbounded, Evidence: evidence}, nil
			}
		}
		values := append([]float64(nil), form.lower...)
		objectiveValue := evaluateTerms(objective.Terms, problem.Variables, values)
		rowViolation, boundViolation, replayOK := replaySemanticSolution(problem, values, options.FeasibilityTolerance)
		evidence.MaxRowViolation = rowViolation
		evidence.MaxBoundViolation = boundViolation
		if !replayOK {
			evidence.BackendCode = "semantic_replay_failed"
			return SolveResult{Status: SolveNumericalFailure, Evidence: evidence}, nil
		}
		evidence.BackendCode = backendOptimalCode("optimal_at_lower_bounds", form.droppedEqualities)
		return SolveResult{Status: SolveOptimal, Values: values, ObjectiveValue: objectiveValue, Evidence: evidence}, nil
	}
	_, raw, solveErr := callSimplex(
		backendObjective,
		mat.NewDense(len(form.rows), columns, data),
		rhs,
		options.OptimalityTolerance,
	)
	if err := ctx.Err(); err != nil {
		return SolveResult{}, err
	}
	if solveErr != nil {
		evidence.BackendCode = solveErr.Error()
		switch {
		case errors.Is(solveErr, lp.ErrInfeasible):
			return SolveResult{Status: SolveInfeasible, Evidence: evidence}, nil
		case errors.Is(solveErr, lp.ErrUnbounded):
			return SolveResult{Status: SolveUnbounded, Evidence: evidence}, nil
		default:
			return SolveResult{Status: SolveNumericalFailure, Evidence: evidence}, nil
		}
	}
	if len(raw) < form.semanticColumns {
		return SolveResult{}, fmt.Errorf("Gonum returned %d columns, need %d", len(raw), form.semanticColumns)
	}
	values := make([]float64, form.semanticColumns)
	for i := range values {
		values[i] = raw[i] + form.lower[i]
	}
	objectiveValue := evaluateTerms(objective.Terms, problem.Variables, values)
	rowViolation, boundViolation, replayOK := replaySemanticSolution(problem, values, options.FeasibilityTolerance)
	evidence.MaxRowViolation = rowViolation
	evidence.MaxBoundViolation = boundViolation
	if !replayOK || !isFinite(objectiveValue) {
		evidence.BackendCode = "semantic_replay_failed"
		return SolveResult{Status: SolveNumericalFailure, Evidence: evidence}, nil
	}
	evidence.BackendCode = backendOptimalCode("optimal", form.droppedEqualities)
	return SolveResult{
		Status:         SolveOptimal,
		Values:         values,
		ObjectiveValue: objectiveValue,
		Evidence:       evidence,
	}, nil
}

// callSimplex converts a backend panic into an ordinary adapter error. Gonum
// may panic for unsupported matrix dimensions; a production Run must classify
// that as numerical/backend failure instead of crashing the process.
func callSimplex(objective []float64, matrix *mat.Dense, rhs []float64, tolerance float64) (optimum float64, values []float64, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("gonum simplex panic: %v", recovered)
		}
	}()
	return lp.Simplex(objective, matrix, rhs, tolerance, nil)
}

// buildStandardForm applies lower-bound shifts and creates deterministic bound
// rows. Inequality slack/surplus columns are added later in one stable pass.
func buildStandardForm(problem LinearProblem, objective LinearObjective, tolerance float64) (standardForm, error) {
	indices := make(map[VariableID]int, len(problem.Variables))
	form := standardForm{
		semanticColumns: len(problem.Variables),
		objective:       make([]float64, len(problem.Variables)),
		lower:           make([]float64, len(problem.Variables)),
		maximize:        objective.Sense == Maximize,
	}
	for i, variable := range problem.Variables {
		indices[variable.ID] = i
		form.lower[i] = variable.Lower
	}
	for _, term := range objective.Terms {
		form.objective[indices[term.Variable]] = term.Coeff
	}
	if form.maximize {
		for i := range form.objective {
			form.objective[i] = -form.objective[i]
		}
	}
	for _, row := range problem.Rows {
		converted := standardRow{id: row.ID, sense: row.Sense, rhs: row.RHS, coeff: make([]float64, form.semanticColumns)}
		for _, term := range row.Terms {
			column := indices[term.Variable]
			converted.coeff[column] = term.Coeff
			converted.rhs -= term.Coeff * form.lower[column]
		}
		form.rows = append(form.rows, converted)
	}
	var inconsistent bool
	form.rows, form.droppedEqualities, inconsistent = reduceDependentEqualities(form.rows, tolerance)
	if inconsistent {
		return standardForm{}, errInconsistentDependentEquality
	}
	for i, variable := range problem.Variables {
		if math.IsInf(variable.Upper, 1) {
			continue
		}
		width := variable.Upper - variable.Lower
		if width < 0 || !isFinite(width) {
			return standardForm{}, fmt.Errorf("variable %q has invalid shifted upper bound", variable.ID)
		}
		coeff := make([]float64, form.semanticColumns)
		coeff[i] = 1
		form.rows = append(form.rows, standardRow{
			id: RowID("bound:upper:" + string(variable.ID)), sense: SenseLE, rhs: width, coeff: coeff,
		})
	}
	return form, nil
}

type equalityBasisRow struct {
	pivot int
	coeff []float64
	rhs   float64
}

// reduceDependentEqualities removes only algebraically redundant equality rows
// before Gonum sees the standard-form matrix. Gonum requires full row rank and
// otherwise reports A as singular for valid semantic models or future
// contributors that independently imply the same equality. Inequalities remain
// untouched because their unique slack/surplus columns make them independent.
//
// The reduction runs in semantic row/column order with deterministic Gaussian
// elimination. A dependent row whose transformed RHS is nonzero is not dropped:
// it is reported as an explicit mathematical inconsistency. Solve still replays
// every original row after an optimum, so removed redundant rows remain part of
// the public semantic contract rather than disappearing from verification.
func reduceDependentEqualities(rows []standardRow, tolerance float64) ([]standardRow, int, bool) {
	kept := make([]standardRow, 0, len(rows))
	basis := make([]equalityBasisRow, 0)
	dropped := 0
	for _, row := range rows {
		if row.sense != SenseEQ {
			kept = append(kept, row)
			continue
		}
		coefficients := append([]float64(nil), row.coeff...)
		rhs := row.rhs
		scale := 1.0
		for _, coefficient := range coefficients {
			scale = math.Max(scale, math.Abs(coefficient))
		}
		for i := range coefficients {
			coefficients[i] /= scale
		}
		rhs /= scale
		for _, existing := range basis {
			factor := coefficients[existing.pivot]
			if math.Abs(factor) <= tolerance {
				coefficients[existing.pivot] = 0
				continue
			}
			for column := existing.pivot; column < len(coefficients); column++ {
				coefficients[column] -= factor * existing.coeff[column]
			}
			rhs -= factor * existing.rhs
		}
		pivot := -1
		for column, coefficient := range coefficients {
			if math.Abs(coefficient) > tolerance {
				pivot = column
				break
			}
		}
		if pivot < 0 {
			dropped++
			if math.Abs(rhs) > scaledTolerance(tolerance, rhs, row.rhs) {
				return nil, dropped, true
			}
			continue
		}
		pivotValue := coefficients[pivot]
		for column := pivot; column < len(coefficients); column++ {
			coefficients[column] /= pivotValue
		}
		rhs /= pivotValue
		basis = append(basis, equalityBasisRow{pivot: pivot, coeff: coefficients, rhs: rhs})
		kept = append(kept, row)
	}
	return kept, dropped, false
}

// backendOptimalCode records equality reduction in solver evidence without
// changing the stable SolveOptimal classification.
func backendOptimalCode(base string, dropped int) string {
	if dropped == 0 {
		return base
	}
	return fmt.Sprintf("%s;dropped_redundant_equalities=%d", base, dropped)
}

// materializeStandardMatrix converts EQ/LE/GE rows to A*x=b with nonnegative
// auxiliary columns. Row scaling is deterministic and applies to every term,
// RHS, and slack coefficient together.
func materializeStandardMatrix(form standardForm) (data, rhs, objective []float64, columns int) {
	inequalities := 0
	for _, row := range form.rows {
		if row.sense != SenseEQ {
			inequalities++
		}
	}
	columns = form.semanticColumns + inequalities
	data = make([]float64, len(form.rows)*columns)
	rhs = make([]float64, len(form.rows))
	objective = make([]float64, columns)
	copy(objective, form.objective)
	slack := form.semanticColumns
	for rowIndex, row := range form.rows {
		scale := math.Max(1, math.Abs(row.rhs))
		for _, coefficient := range row.coeff {
			scale = math.Max(scale, math.Abs(coefficient))
		}
		for column, coefficient := range row.coeff {
			data[rowIndex*columns+column] = coefficient / scale
		}
		rhs[rowIndex] = row.rhs / scale
		switch row.sense {
		case SenseLE:
			data[rowIndex*columns+slack] = 1 / scale
			slack++
		case SenseGE:
			data[rowIndex*columns+slack] = -1 / scale
			slack++
		}
	}
	return data, rhs, objective, columns
}

// replaySemanticSolution validates the values against original named rows and
// bounds rather than the transformed matrix. It returns maximum raw residuals
// for reports while applying scale-aware feasibility tolerance to pass/fail.
func replaySemanticSolution(problem LinearProblem, values []float64, tolerance float64) (float64, float64, bool) {
	if len(values) != len(problem.Variables) {
		return math.Inf(1), math.Inf(1), false
	}
	indices := make(map[VariableID]int, len(problem.Variables))
	maxBoundViolation := 0.0
	pass := true
	for i, variable := range problem.Variables {
		indices[variable.ID] = i
		value := values[i]
		if !isFinite(value) {
			return math.Inf(1), math.Inf(1), false
		}
		violation := math.Max(variable.Lower-value, 0)
		if !math.IsInf(variable.Upper, 1) {
			violation = math.Max(violation, value-variable.Upper)
		}
		maxBoundViolation = math.Max(maxBoundViolation, violation)
		if violation > scaledTolerance(tolerance, variable.Lower, variable.Upper) {
			pass = false
		}
	}
	maxRowViolation := 0.0
	for _, row := range problem.Rows {
		lhs := 0.0
		for _, term := range row.Terms {
			lhs += term.Coeff * values[indices[term.Variable]]
		}
		violation := 0.0
		switch row.Sense {
		case SenseEQ:
			violation = math.Abs(lhs - row.RHS)
		case SenseLE:
			violation = math.Max(lhs-row.RHS, 0)
		case SenseGE:
			violation = math.Max(row.RHS-lhs, 0)
		}
		maxRowViolation = math.Max(maxRowViolation, violation)
		if violation > scaledTolerance(tolerance, lhs, row.RHS) {
			pass = false
		}
	}
	return maxRowViolation, maxBoundViolation, pass
}

// evaluateTerms recomputes an objective from semantic values, deliberately
// ignoring backend slack columns and the backend-reported objective scalar.
func evaluateTerms(terms []LinearTerm, variables []LinearVariable, values []float64) float64 {
	indices := make(map[VariableID]int, len(variables))
	for i, variable := range variables {
		indices[variable.ID] = i
	}
	total := 0.0
	for _, term := range terms {
		total += term.Coeff * values[indices[term.Variable]]
	}
	return total
}

// scaledTolerance keeps a single absolute tolerance meaningful for large
// payout moments without claiming more precision than the solver can prove.
func scaledTolerance(base float64, values ...float64) float64 {
	scale := 1.0
	for _, value := range values {
		if isFinite(value) {
			scale = math.Max(scale, math.Abs(value))
		}
	}
	return base * scale
}
