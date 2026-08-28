package v2

import (
	"context"
	"math"
	"testing"
)

// TestGonumSolverSemanticContract verifies the adapter features that are easy
// to lose when changing matrix libraries: lower shifts, finite upper bounds,
// all three row senses, maximize sign restoration, and semantic replay.
func TestGonumSolverSemanticContract(t *testing.T) {
	problem := LinearProblem{
		Variables: []LinearVariable{
			{ID: "x", Lower: 2, Upper: 5},
			{ID: "y", Lower: 0, Upper: math.Inf(1)},
		},
		Rows: []LinearRow{
			{ID: "sum", Sense: SenseEQ, RHS: 6, Terms: []LinearTerm{{Variable: "x", Coeff: 1}, {Variable: "y", Coeff: 1}}},
			{ID: "x_min", Sense: SenseGE, RHS: 3, Terms: []LinearTerm{{Variable: "x", Coeff: 1}}},
			{ID: "y_max", Sense: SenseLE, RHS: 2, Terms: []LinearTerm{{Variable: "y", Coeff: 1}}},
		},
	}
	result, err := NewGonumSolver().Solve(context.Background(), problem, LinearObjective{
		Name: "diagnostic-bound", Sense: Maximize, Origin: ObjectiveDiagnostic,
		Terms: []LinearTerm{{Variable: "y", Coeff: 1}},
	}, SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if result.Status != SolveOptimal {
		t.Fatalf("status = %v, evidence = %+v", result.Status, result.Evidence)
	}
	if math.Abs(result.Values[0]-4) > 1e-8 || math.Abs(result.Values[1]-2) > 1e-8 {
		t.Fatalf("values = %v, want [4 2]", result.Values)
	}
	if math.Abs(result.ObjectiveValue-2) > 1e-8 {
		t.Fatalf("objective = %g, want 2", result.ObjectiveValue)
	}
	if result.Evidence.Objective != "diagnostic-bound" {
		t.Fatalf("semantic objective evidence=%q", result.Evidence.Objective)
	}
}

// TestGonumSolverSeparatesMathematicalStatuses ensures an infeasible phase
// probe and an unbounded model remain backend statuses for Engine-level policy.
func TestGonumSolverSeparatesMathematicalStatuses(t *testing.T) {
	options := SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9}
	solver := NewGonumSolver()
	infeasible := LinearProblem{
		Variables: []LinearVariable{{ID: "x", Lower: 0, Upper: math.Inf(1)}},
		Rows: []LinearRow{
			{ID: "lo", Sense: SenseGE, RHS: 2, Terms: []LinearTerm{{Variable: "x", Coeff: 1}}},
			{ID: "hi", Sense: SenseLE, RHS: 1, Terms: []LinearTerm{{Variable: "x", Coeff: 1}}},
		},
	}
	got, err := solver.Solve(context.Background(), infeasible, LinearObjective{Sense: Minimize, Origin: ObjectiveMainProfileProbe}, options)
	if err != nil {
		t.Fatalf("infeasible Solve: %v", err)
	}
	if got.Status != SolveInfeasible || len(got.Values) != 0 {
		t.Fatalf("infeasible result = %+v", got)
	}

	unbounded := LinearProblem{Variables: []LinearVariable{{ID: "x", Lower: 0, Upper: math.Inf(1)}}}
	got, err = solver.Solve(context.Background(), unbounded, LinearObjective{
		Sense: Maximize, Origin: ObjectiveDiagnostic,
		Terms: []LinearTerm{{Variable: "x", Coeff: 1}},
	}, options)
	if err != nil {
		t.Fatalf("unbounded Solve: %v", err)
	}
	if got.Status != SolveUnbounded || len(got.Values) != 0 {
		t.Fatalf("unbounded result = %+v", got)
	}
}

// TestGonumSolverReducesOnlyConsistentDependentEqualities protects the generic
// transform contract: independently contributed equivalent equalities must not
// become a singular-matrix failure. Changing only the duplicate RHS must instead
// prove infeasibility.
func TestGonumSolverReducesOnlyConsistentDependentEqualities(t *testing.T) {
	options := SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9}
	objective := LinearObjective{Sense: Minimize, Origin: ObjectiveHardFeasibility}
	base := LinearProblem{
		Variables: []LinearVariable{{ID: "x", Lower: 0, Upper: 1}, {ID: "y", Lower: 0, Upper: 1}},
		Rows: []LinearRow{
			{ID: "class_mean", Sense: SenseEQ, RHS: 1, Terms: []LinearTerm{{Variable: "x", Coeff: 1}, {Variable: "y", Coeff: 1}}},
			{ID: "same_sum_from_another_contributor", Sense: SenseEQ, RHS: 1, Terms: []LinearTerm{{Variable: "x", Coeff: 1}, {Variable: "y", Coeff: 1}}},
		},
	}
	result, err := NewGonumSolver().Solve(context.Background(), base, objective, options)
	if err != nil {
		t.Fatalf("consistent Solve: %v", err)
	}
	if result.Status != SolveOptimal {
		t.Fatalf("consistent status = %v, evidence = %+v", result.Status, result.Evidence)
	}
	if result.Evidence.BackendCode != "optimal;dropped_redundant_equalities=1" {
		t.Fatalf("backend code = %q", result.Evidence.BackendCode)
	}

	contradiction := cloneLinearProblem(base)
	contradiction.Rows[1].RHS = 2
	result, err = NewGonumSolver().Solve(context.Background(), contradiction, objective, options)
	if err != nil {
		t.Fatalf("contradictory Solve: %v", err)
	}
	if result.Status != SolveInfeasible || len(result.Values) != 0 {
		t.Fatalf("contradictory result = %+v", result)
	}
	if result.Evidence.BackendCode != "inconsistent_dependent_equality" {
		t.Fatalf("contradictory backend code = %q", result.Evidence.BackendCode)
	}
}

// TestLinearProblemRejectsNonCanonicalTerms protects deterministic column
// construction from accidental map iteration or duplicate sparse terms.
func TestLinearProblemRejectsNonCanonicalTerms(t *testing.T) {
	problem := LinearProblem{
		Variables: []LinearVariable{{ID: "a", Lower: 0, Upper: 1}, {ID: "b", Lower: 0, Upper: 1}},
		Rows: []LinearRow{{
			ID: "bad", Sense: SenseEQ, RHS: 1,
			Terms: []LinearTerm{{Variable: "b", Coeff: 1}, {Variable: "a", Coeff: 1}},
		}},
	}
	_, err := NewGonumSolver().Solve(context.Background(), problem,
		LinearObjective{Sense: Minimize, Origin: ObjectiveHardFeasibility},
		SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9})
	if err == nil {
		t.Fatal("Solve accepted non-canonical term order")
	}
}
