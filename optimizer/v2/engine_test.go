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
	"reflect"
	"strings"
	"testing"
)

// solverStep describes one exact Engine-to-Solver interaction. Keeping the
// expected ObjectiveOrigin beside the backend result protects stage mapping:
// the same SolveInfeasible status has deliberately different meanings in hard
// feasibility, semantic preference refinements, and canonicalization.
type solverStep struct {
	origin ObjectiveOrigin
	name   OptimizationStageID
	status SolveStatus
}

// scriptedSolver is a strict, backend-independent Solver test double. It
// returns a correctly sized primal vector for optimal steps because the Engine
// maps canonical values by semantic variable ID after all stage transforms.
type scriptedSolver struct {
	t     *testing.T
	steps []solverStep
	next  int
}

// Solve consumes exactly one declared step and rejects an unexpected stage or
// an extra solve. This makes every test below document the intended stage
// protocol in addition to checking the final public Status.
func (s *scriptedSolver) Solve(
	_ context.Context,
	problem LinearProblem,
	objective LinearObjective,
	_ SolveOptions,
) (SolveResult, error) {
	s.t.Helper()
	if s.next >= len(s.steps) {
		return SolveResult{}, fmt.Errorf("unexpected solver call %d with origin %q", s.next, objective.Origin)
	}
	step := s.steps[s.next]
	s.next++
	if objective.Origin != step.origin {
		return SolveResult{}, fmt.Errorf(
			"solver call %d origin = %q, want %q",
			s.next-1,
			objective.Origin,
			step.origin,
		)
	}
	if step.name != "" && objective.Name != step.name {
		return SolveResult{}, fmt.Errorf(
			"solver call %d name = %q, want %q",
			s.next-1,
			objective.Name,
			step.name,
		)
	}
	result := SolveResult{
		Status: step.status,
		Evidence: SolverEvidence{
			Objective:       string(objective.Name),
			Backend:         "scripted-test-solver",
			BackendCode:     string(objective.Origin),
			SemanticColumns: len(problem.Variables),
			Rows:            len(problem.Rows),
		},
	}
	if step.status == SolveOptimal {
		result.Values = make([]float64, len(problem.Variables))
	}
	return result, nil
}

func engineTestMainGroupModel(mainVisibilityIterations int) CompiledModel {
	compiled := engineTestModel(false, 0, 0)
	compiled.Prepared.Plan.EngineOptions.MainGroupInternalVisibilityBisectionIterations = mainVisibilityIterations
	compiled.Hard.Variables = append(compiled.Hard.Variables, LinearVariable{ID: "p:0000:0001", Lower: 0, Upper: 1})
	compiled.Primary = append(compiled.Primary, PrimaryVariable{ID: "p:0000:0001", ClassIndex: 0, BucketIndex: 1})
	compiled.ClassVariables[0] = append(compiled.ClassVariables[0], "p:0000:0001")
	compiled.VariableIndex["p:0000:0001"] = 1
	compiled.Prepared.Classes[0].Buckets = []PreparedBucket{
		{Index: 0, Samples: []CollectedSample{{Snapshot: []byte{1}}}, RiskCap: math.Inf(1), MainGroup: 0},
		{Index: 1, Samples: []CollectedSample{{Snapshot: []byte{2}}}, RiskCap: math.Inf(1), MainGroup: 0},
	}
	compiled.Prepared.Classes[0].Groups[0].BucketIndexes = []int{0, 1}
	return compiled
}

// assertConsumed verifies that an early return did not silently skip a stage
// and that the Engine did not perform an undocumented additional solver call.
func (s *scriptedSolver) assertConsumed() {
	s.t.Helper()
	if s.next != len(s.steps) {
		s.t.Fatalf("solver consumed %d of %d scripted calls", s.next, len(s.steps))
	}
}

// engineTestModel builds the smallest semantic CompiledModel that exercises
// Main profile refinement and canonicalization. When withOther is true it adds
// one supported Other bucket so the model also exercises Other visibility. The hard rows are
// intentionally empty: these tests isolate Engine stage policy, while model
// compilation and numerical replay are covered by their own test suites.
func engineTestModel(withOther bool, profileIterations, visibilityIterations int) CompiledModel {
	variables := []LinearVariable{{ID: "p:0000:0000", Lower: 0, Upper: 1}}
	primary := []PrimaryVariable{{ID: "p:0000:0000", ClassIndex: 0, BucketIndex: 0}}
	classVariables := []VariableID{"p:0000:0000"}
	buckets := []PreparedBucket{{Index: 0, RiskCap: math.Inf(1)}}
	others := []int(nil)
	if withOther {
		variables = append(variables, LinearVariable{ID: "p:0000:0001", Lower: 0, Upper: 1})
		primary = append(primary, PrimaryVariable{ID: "p:0000:0001", ClassIndex: 0, BucketIndex: 1})
		classVariables = append(classVariables, "p:0000:0001")
		buckets = append(buckets, PreparedBucket{Index: 1, RiskCap: math.Inf(1)})
		others = []int{1}
	}
	return CompiledModel{
		Prepared: PreparedProblem{
			Plan: ResolvedPlan{Plan: RunPlan{Intent: "engine-stage-test"}, EngineOptions: EngineOptions{
				FeasibilityTolerance:               1e-9,
				OptimalityTolerance:                1e-9,
				ProfileTolerance:                   0.01,
				VisibilityTolerance:                0.01,
				ProfileBisectionIterations:         profileIterations,
				OtherVisibilityBisectionIterations: visibilityIterations,
			}},
			Classes: []PreparedClass{{
				ID: "class-0", Index: 0, Intent: true,
				Buckets: buckets,
				Groups:  []PreparedGroup{{Index: 0, BucketIndexes: []int{0}, PreferShare: 1}},
				Others:  others,
			}},
		},
		Hard:           LinearProblem{Variables: variables},
		Primary:        primary,
		ClassVariables: [][]VariableID{classVariables},
		VariableIndex: map[VariableID]int{
			"p:0000:0000": 0,
			"p:0000:0001": 1,
		},
	}
}

// assertNear compares bisection endpoints without coupling the tests to exact
// floating-point formatting in reports.
func assertNear(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("%s = %.17g, want %.17g", name, got, want)
	}
}

// TestIntentEngineMapsOnlyHardInfeasibleToRunInfeasible proves that the first
// hard-feasibility solve is the sole SolveInfeasible path that may produce the
// public INFEASIBLE_MODEL status and its stable diagnostic code.
func TestIntentEngineMapsOnlyHardInfeasibleToRunInfeasible(t *testing.T) {
	solver := &scriptedSolver{t: t, steps: []solverStep{
		{origin: ObjectiveHardFeasibility, status: SolveInfeasible},
	}}

	result, err := NewIntentEngine(solver).Solve(context.Background(), engineTestModel(false, 2, 2))
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	solver.assertConsumed()
	if result.Status != StatusInfeasibleModel {
		t.Fatalf("Status = %q, want %q", result.Status, StatusInfeasibleModel)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != DiagnosticHardModelInfeasible {
		t.Fatalf("Diagnostics = %+v, want one %q diagnostic", result.Diagnostics, DiagnosticHardModelInfeasible)
	}
}

// TestIntentEngineTreatsMainProfileProbeInfeasibleAsBracketSignal proves that infeasible
// delta probes tighten the minimizing bracket and do not terminate the Run.
// The scripted probes establish delta* in (1, 1.5], followed by a successful
// tolerance lock and canonical solve.
func TestIntentEngineTreatsMainProfileProbeInfeasibleAsBracketSignal(t *testing.T) {
	solver := &scriptedSolver{t: t, steps: []solverStep{
		{origin: ObjectiveHardFeasibility, status: SolveOptimal},
		{origin: ObjectiveMainProfileProbe, status: SolveOptimal},    // delta = 2 endpoint
		{origin: ObjectiveMainProfileProbe, status: SolveInfeasible}, // delta = 0 endpoint
		{origin: ObjectiveMainProfileProbe, status: SolveInfeasible}, // delta = 1
		{origin: ObjectiveMainProfileProbe, status: SolveOptimal},    // delta = 1.5
		{origin: ObjectiveIntentRefinement, status: SolveOptimal},    // fixed delta = 1.51
		{origin: ObjectiveCanonicalBucketProbability, status: SolveOptimal},
	}}

	result, err := NewIntentEngine(solver).Solve(context.Background(), engineTestModel(false, 2, 2))
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	solver.assertConsumed()
	if result.Status != StatusOptimal {
		t.Fatalf("Status = %q, want %q; diagnostics = %+v", result.Status, StatusOptimal, result.Diagnostics)
	}
	if result.MainProfileOptimization.Probes != 4 {
		t.Fatalf("MainProfileOptimization.Probes = %d, want 4", result.MainProfileOptimization.Probes)
	}
	if result.MainProfileOptimization.Objective != string(StageMinimizeMainProfileDeviation) || result.MainProfileOptimization.Metric != metricMainProfileDeviation || result.MainProfileOptimization.Direction != "minimize" {
		t.Fatalf("Main profile report identity = (%q, %q), want common delta/minimize", result.MainProfileOptimization.Objective, result.MainProfileOptimization.Direction)
	}
	assertNear(t, "MainProfileOptimization.Lower", result.MainProfileOptimization.Lower, 1)
	assertNear(t, "MainProfileOptimization.Upper", result.MainProfileOptimization.Upper, 1.5)
	assertNear(t, "MainProfileOptimization.FixedValue", result.MainProfileOptimization.FixedValue, 1.51)
	if result.OtherBucketVisibilityOptimization.Probes != 0 {
		t.Fatalf("OtherBucketVisibilityOptimization.Probes = %d, want 0 for a Class without Others", result.OtherBucketVisibilityOptimization.Probes)
	}
}

// TestIntentEngineTreatsOtherVisibilityProbeInfeasibleAsBracketSignal proves that infeasible
// rho probes tighten the maximizing bracket and preserve the known feasible
// Main profile witness. The scripted probes establish rho* in [0.25, 0.5), after
// which the tolerance lock and both primary canonical solves still run.
func TestIntentEngineTreatsOtherVisibilityProbeInfeasibleAsBracketSignal(t *testing.T) {
	solver := &scriptedSolver{t: t, steps: []solverStep{
		{origin: ObjectiveHardFeasibility, status: SolveOptimal},
		{origin: ObjectiveMainProfileProbe, status: SolveOptimal},              // delta = 2 endpoint
		{origin: ObjectiveMainProfileProbe, status: SolveOptimal},              // delta = 0 endpoint
		{origin: ObjectiveIntentRefinement, status: SolveOptimal},              // fixed delta = 0.01
		{origin: ObjectiveOtherBucketVisibilityProbe, status: SolveOptimal},    // rho = 0 endpoint
		{origin: ObjectiveOtherBucketVisibilityProbe, status: SolveInfeasible}, // rho = 1 endpoint
		{origin: ObjectiveOtherBucketVisibilityProbe, status: SolveInfeasible}, // rho = 0.5
		{origin: ObjectiveOtherBucketVisibilityProbe, status: SolveOptimal},    // rho = 0.25
		{origin: ObjectiveIntentRefinement, status: SolveOptimal},              // fixed rho = 0.24
		{origin: ObjectiveCanonicalBucketProbability, status: SolveOptimal},
		{origin: ObjectiveCanonicalBucketProbability, status: SolveOptimal},
	}}

	result, err := NewIntentEngine(solver).Solve(context.Background(), engineTestModel(true, 2, 2))
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	solver.assertConsumed()
	if result.Status != StatusOptimal {
		t.Fatalf("Status = %q, want %q; diagnostics = %+v", result.Status, StatusOptimal, result.Diagnostics)
	}
	if result.OtherBucketVisibilityOptimization.Probes != 4 {
		t.Fatalf("OtherBucketVisibilityOptimization.Probes = %d, want 4", result.OtherBucketVisibilityOptimization.Probes)
	}
	if result.OtherBucketVisibilityOptimization.Objective != string(StageMaximizeOtherBucketVisibility) || result.OtherBucketVisibilityOptimization.Metric != metricOtherBucketVisibility || result.OtherBucketVisibilityOptimization.Direction != "maximize" {
		t.Fatalf("Other visibility report identity = (%q, %q), want common rho/maximize", result.OtherBucketVisibilityOptimization.Objective, result.OtherBucketVisibilityOptimization.Direction)
	}
	assertNear(t, "OtherBucketVisibilityOptimization.Lower", result.OtherBucketVisibilityOptimization.Lower, 0.25)
	assertNear(t, "OtherBucketVisibilityOptimization.Upper", result.OtherBucketVisibilityOptimization.Upper, 0.5)
	assertNear(t, "OtherBucketVisibilityOptimization.FixedValue", result.OtherBucketVisibilityOptimization.FixedValue, 0.24)
}

// TestIntentEngineMapsCanonicalInfeasibleToNumericalFailure proves that once a
// feasible hard/intent witness exists, an unexpectedly infeasible canonical
// solve is a numerical or transform contradiction. It must never rewrite the
// already established fact into a false INFEASIBLE_MODEL Run status.
func TestIntentEngineMapsCanonicalInfeasibleToNumericalFailure(t *testing.T) {
	solver := &scriptedSolver{t: t, steps: []solverStep{
		{origin: ObjectiveHardFeasibility, status: SolveOptimal},
		{origin: ObjectiveMainProfileProbe, status: SolveOptimal},
		{origin: ObjectiveMainProfileProbe, status: SolveOptimal},
		{origin: ObjectiveIntentRefinement, status: SolveOptimal},
		{origin: ObjectiveCanonicalBucketProbability, status: SolveInfeasible},
	}}

	result, err := NewIntentEngine(solver).Solve(context.Background(), engineTestModel(false, 2, 2))
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	solver.assertConsumed()
	if result.Status != StatusNumericalFailure {
		t.Fatalf("Status = %q, want %q", result.Status, StatusNumericalFailure)
	}
	if result.Status == StatusInfeasibleModel {
		t.Fatal("canonical SolveInfeasible was incorrectly promoted to INFEASIBLE_MODEL")
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != DiagnosticSolverNumericalFailure {
		t.Fatalf("Diagnostics = %+v, want one %q diagnostic", result.Diagnostics, DiagnosticSolverNumericalFailure)
	}
}

func TestIntentEngineMaximizesMainGroupInternalVisibilityBeforeCanonicalSelection(t *testing.T) {
	steps := []solverStep{
		{origin: ObjectiveHardFeasibility, name: StageProveHardFeasibility, status: SolveOptimal},
		{origin: ObjectiveMainProfileProbe, name: StageMinimizeMainProfileDeviation, status: SolveOptimal},
		{origin: ObjectiveMainProfileProbe, name: StageMinimizeMainProfileDeviation, status: SolveOptimal},
		{origin: ObjectiveIntentRefinement, name: StageMinimizeMainProfileDeviation, status: SolveOptimal},
		{origin: ObjectiveMainGroupInternalVisibilityProbe, name: StageMaximizeMainGroupInternalVisibility, status: SolveOptimal},
		{origin: ObjectiveMainGroupInternalVisibilityProbe, name: StageMaximizeMainGroupInternalVisibility, status: SolveInfeasible},
		{origin: ObjectiveMainGroupInternalVisibilityProbe, name: StageMaximizeMainGroupInternalVisibility, status: SolveInfeasible},
		{origin: ObjectiveMainGroupInternalVisibilityProbe, name: StageMaximizeMainGroupInternalVisibility, status: SolveOptimal},
		{origin: ObjectiveIntentRefinement, name: StageMaximizeMainGroupInternalVisibility, status: SolveOptimal},
		{origin: ObjectiveCanonicalBucketProbability, name: StageSelectCanonicalBucketProbabilities, status: SolveOptimal},
		{origin: ObjectiveCanonicalBucketProbability, name: StageSelectCanonicalBucketProbabilities, status: SolveOptimal},
	}
	solver := &scriptedSolver{t: t, steps: steps}
	var events []OptimizationStageEvent
	result, err := NewIntentEngine(solver).SolveObserved(context.Background(), engineTestMainGroupModel(2), func(event OptimizationStageEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("SolveObserved: %v", err)
	}
	solver.assertConsumed()
	if result.Status != StatusOptimal {
		t.Fatalf("Status=%q diagnostics=%+v", result.Status, result.Diagnostics)
	}
	main := result.MainGroupInternalVisibilityOptimization
	if !main.Applicable || main.Probes != 4 || main.Objective != string(StageMaximizeMainGroupInternalVisibility) || main.Metric != metricMainGroupInternalVisibility {
		t.Fatalf("Main Group visibility report=%+v", main)
	}
	assertNear(t, "main visibility lower", main.Lower, 0.25)
	assertNear(t, "main visibility upper", main.Upper, 0.5)
	assertNear(t, "main visibility fixed", main.FixedValue, 0.24)
	if !reflect.DeepEqual(result.PhaseA, result.MainProfileOptimization) || !reflect.DeepEqual(result.PhaseB, result.OtherBucketVisibilityOptimization) {
		t.Fatal("deprecated EngineSolution aliases differ from canonical reports")
	}

	wantTerminal := []struct {
		stage OptimizationStageID
		state string
	}{
		{StageProveHardFeasibility, "completed"},
		{StageMinimizeMainProfileDeviation, "completed"},
		{StageMaximizeOtherBucketVisibility, "skipped"},
		{StageMaximizeMainGroupInternalVisibility, "completed"},
		{StageSelectCanonicalBucketProbabilities, "completed"},
	}
	var terminal []struct {
		stage OptimizationStageID
		state string
	}
	started := make(map[OptimizationStageID]int)
	for _, event := range events {
		if strings.Contains(string(event.Stage), "phase-") {
			t.Fatalf("semantic stage leaked legacy phase name: %q", event.Stage)
		}
		if event.State == "started" {
			started[event.Stage]++
		}
		if event.State == "completed" || event.State == "skipped" || event.State == "failed" {
			terminal = append(terminal, struct {
				stage OptimizationStageID
				state string
			}{event.Stage, event.State})
		}
	}
	if !reflect.DeepEqual(terminal, wantTerminal) {
		t.Fatalf("terminal events=%v want=%v", terminal, wantTerminal)
	}
	for _, item := range wantTerminal {
		if started[item.stage] != 1 {
			t.Fatalf("stage %q started %d times", item.stage, started[item.stage])
		}
	}
}

func TestIntentEngineMainGroupVisibilityRhoOneSkipsIntermediateBisection(t *testing.T) {
	solver := &scriptedSolver{t: t, steps: []solverStep{
		{origin: ObjectiveHardFeasibility, status: SolveOptimal},
		{origin: ObjectiveMainProfileProbe, status: SolveOptimal},
		{origin: ObjectiveMainProfileProbe, status: SolveOptimal},
		{origin: ObjectiveIntentRefinement, status: SolveOptimal},
		{origin: ObjectiveMainGroupInternalVisibilityProbe, status: SolveOptimal}, // rho=0 is an actual solve
		{origin: ObjectiveMainGroupInternalVisibilityProbe, status: SolveOptimal}, // rho=1
		{origin: ObjectiveIntentRefinement, status: SolveOptimal},
		{origin: ObjectiveCanonicalBucketProbability, status: SolveOptimal},
		{origin: ObjectiveCanonicalBucketProbability, status: SolveOptimal},
	}}
	result, err := NewIntentEngine(solver).Solve(context.Background(), engineTestMainGroupModel(7))
	if err != nil {
		t.Fatal(err)
	}
	solver.assertConsumed()
	main := result.MainGroupInternalVisibilityOptimization
	if main.Probes != 2 || main.Lower != 1 || main.Upper != 1 || main.FixedValue != 0.99 {
		t.Fatalf("rho=1 Main Group visibility report=%+v", main)
	}
}

func TestIntentEngineMainGroupVisibilityLockFailureIsNotHardInfeasibility(t *testing.T) {
	solver := &scriptedSolver{t: t, steps: []solverStep{
		{origin: ObjectiveHardFeasibility, status: SolveOptimal},
		{origin: ObjectiveMainProfileProbe, status: SolveOptimal},
		{origin: ObjectiveMainProfileProbe, status: SolveOptimal},
		{origin: ObjectiveIntentRefinement, status: SolveOptimal},
		{origin: ObjectiveMainGroupInternalVisibilityProbe, status: SolveOptimal},
		{origin: ObjectiveMainGroupInternalVisibilityProbe, status: SolveOptimal},
		{origin: ObjectiveIntentRefinement, status: SolveInfeasible},
	}}
	result, err := NewIntentEngine(solver).Solve(context.Background(), engineTestMainGroupModel(2))
	if err != nil {
		t.Fatal(err)
	}
	solver.assertConsumed()
	if result.Status != StatusNumericalFailure || result.Status == StatusInfeasibleModel {
		t.Fatalf("Main Group visibility lock failure status=%q diagnostics=%+v", result.Status, result.Diagnostics)
	}
}

func TestIntentEngineClassifiesMainGroupVisibilityProbeFailures(t *testing.T) {
	tests := []struct {
		name       string
		mainSteps  []solverStep
		wantStatus Status
	}{
		{
			name:       "rho zero known-witness contradiction",
			mainSteps:  []solverStep{{origin: ObjectiveMainGroupInternalVisibilityProbe, status: SolveInfeasible}},
			wantStatus: StatusNumericalFailure,
		},
		{
			name: "rho one numerical failure",
			mainSteps: []solverStep{
				{origin: ObjectiveMainGroupInternalVisibilityProbe, status: SolveOptimal},
				{origin: ObjectiveMainGroupInternalVisibilityProbe, status: SolveNumericalFailure},
			},
			wantStatus: StatusNumericalFailure,
		},
		{
			name: "rho one unbounded",
			mainSteps: []solverStep{
				{origin: ObjectiveMainGroupInternalVisibilityProbe, status: SolveOptimal},
				{origin: ObjectiveMainGroupInternalVisibilityProbe, status: SolveUnbounded},
			},
			wantStatus: StatusInternalError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			steps := []solverStep{
				{origin: ObjectiveHardFeasibility, status: SolveOptimal},
				{origin: ObjectiveMainProfileProbe, status: SolveOptimal},
				{origin: ObjectiveMainProfileProbe, status: SolveOptimal},
				{origin: ObjectiveIntentRefinement, status: SolveOptimal},
			}
			steps = append(steps, test.mainSteps...)
			solver := &scriptedSolver{t: t, steps: steps}
			result, err := NewIntentEngine(solver).Solve(context.Background(), engineTestMainGroupModel(2))
			if err != nil {
				t.Fatal(err)
			}
			solver.assertConsumed()
			if result.Status != test.wantStatus || result.Status == StatusInfeasibleModel {
				t.Fatalf("status=%q want=%q diagnostics=%+v", result.Status, test.wantStatus, result.Diagnostics)
			}
		})
	}
}
