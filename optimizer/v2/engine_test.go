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
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// engineTestModel builds the smallest semantic CompiledModel that exercises
// Main profile refinement and canonicalization. When withOther is true it adds
// one supported Other bucket so the model also exercises Other visibility. The hard rows are
// limited to normalization and the exact zero mean: stage-policy fixtures
// must still satisfy the minimum-CV fixed-expectation contract.
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
				ID: "class-0", Index: 0, Intent: true, Probability: 1,
				Buckets: buckets,
				Groups:  []PreparedGroup{{Index: 0, BucketIndexes: []int{0}, PreferShare: 1}},
				Others:  others,
			}},
		},
		Hard: LinearProblem{Variables: variables, Rows: []LinearRow{
			{ID: "class:0000:normalization", ClassID: "class-0", Sense: SenseEQ, RHS: 1, Terms: func() []LinearTerm {
				terms := make([]LinearTerm, len(classVariables))
				for i, id := range classVariables {
					terms[i] = LinearTerm{Variable: id, Coeff: 1}
				}
				return terms
			}()},
			{ID: "class:0000:mean", ClassID: "class-0", Sense: SenseEQ, RHS: 0},
		}},
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

// statusSolver answers every solve with one backend status and no values, so
// every decision must come from the exact path.
type statusSolver struct{ status SolveStatus }

func (s statusSolver) Solve(context.Context, LinearProblem, LinearObjective, SolveOptions) (SolveResult, error) {
	return SolveResult{Status: s.status}, nil
}

// countingSolver counts backend calls and records the calls made after the
// caller's context was cancelled.
type countingSolver struct {
	inner           Solver
	calls           int
	afterCancelling int
}

func (s *countingSolver) Solve(ctx context.Context, p LinearProblem, o LinearObjective, opt SolveOptions) (SolveResult, error) {
	s.calls++
	if ctx.Err() != nil {
		s.afterCancelling++
	}
	return s.inner.Solve(ctx, p, o, opt)
}

// skewedModel is P00 E4: one Class whose exact Main-profile optimum is
// δ* = 0.4, so the profile stage really bisects.
func skewedModel(t *testing.T) CompiledModel {
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceSkewedClass(0, 1))
	compiled.Prepared.Plan.EngineOptions.ProfileBisectionIterations = 60
	return compiled
}

// TestIntentEngineBackendStatusNeverDecides: a backend that reports every
// problem infeasible, numerically failed or unbounded neither produces
// INFEASIBLE_MODEL on a feasible model nor stops the run; the exact path
// supplies every witness (PRD 0010 §3.2).
func TestIntentEngineBackendStatusNeverDecides(t *testing.T) {
	for _, status := range []SolveStatus{SolveInfeasible, SolveNumericalFailure, SolveUnbounded} {
		solution, err := NewIntentEngine(statusSolver{status}).Solve(context.Background(), skewedModel(t))
		if err != nil {
			t.Fatal(err)
		}
		if solution.Status != StatusOptimal {
			t.Fatalf("backend %s: status=%s diagnostics=%+v", solveStatusName(status), solution.Status, solution.Diagnostics)
		}
		assertProfileBracket(t, solution.MainProfileOptimization, 0.4)
	}
}

// TestIntentEngineHardInfeasibleNeedsCertificate: only a verified certificate
// on the external set produces INFEASIBLE_MODEL.
func TestIntentEngineHardInfeasibleNeedsCertificate(t *testing.T) {
	compiled := skewedModel(t)
	compiled.Hard = cloneLinearProblem(compiled.Hard)
	compiled.Hard.Rows = append(compiled.Hard.Rows, LinearRow{ID: "test:impossible", ClassID: compiled.Prepared.Classes[0].ID,
		Sense: SenseGE, RHS: 2, Terms: []LinearTerm{{Variable: compiled.Primary[0].ID, Coeff: 1}}})
	solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), compiled)
	if err != nil {
		t.Fatal(err)
	}
	if solution.Status != StatusInfeasibleModel || len(solution.Diagnostics) == 0 {
		t.Fatalf("status=%s diagnostics=%+v", solution.Status, solution.Diagnostics)
	}
	// Without exact budget no certificate exists: the same model is UNKNOWN.
	engine := NewIntentEngine(NewGonumSolver())
	engine.exactBudget = &exactBudget{maxPivots: 0, maxBits: 1 << 14}
	if solution, err = engine.Solve(context.Background(), compiled); err != nil {
		t.Fatal(err)
	}
	if solution.Status != StatusNumericalFailure || solution.Diagnostics[0].Code != DiagnosticEvidenceUnavailable {
		t.Fatalf("zero budget: status=%s diagnostics=%+v", solution.Status, solution.Diagnostics)
	}
}

// assertProfileBracket checks a minimizing bracket against the exact optimum
// of the unrelaxed model. The bracket is relative to the internal set, which
// is larger, so its optimum may sit slightly below (within the τ/3 slack).
func assertProfileBracket(t *testing.T, report BisectionReport, exact float64) {
	t.Helper()
	if !(report.Lower <= exact && exact-report.Upper <= 1e-8) {
		t.Fatalf("bracket [%.17g, %.17g] inconsistent with exact optimum %.17g (%+v)", report.Lower, report.Upper, exact, report)
	}
}

// TestBisectionStopsAtRequiredPrecision is A27: the bracket stops the first
// time its exact width reaches 1/10⁸, well before the iteration cap.
func TestBisectionStopsAtRequiredPrecision(t *testing.T) {
	solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), skewedModel(t))
	if err != nil {
		t.Fatal(err)
	}
	if solution.Status != StatusOptimal {
		t.Fatalf("status=%s diagnostics=%+v", solution.Status, solution.Diagnostics)
	}
	report := solution.MainProfileOptimization
	assertProfileBracket(t, report, 0.4)
	if report.StopReason != StopConverged || report.LowerBasis != BasisVerifiedCertificate || report.UpperBasis != BasisReplayedWitness {
		t.Fatalf("report=%+v", report)
	}
	// Halving [0, 2] first reaches 1e-8 after 28 midpoints: 2/2²⁸ ≈ 7.45e-9.
	if report.Probes != 2+28 || report.VerifiedBound > 1e-8 || report.VerifiedBound <= 0.5e-8 {
		t.Fatalf("probes=%d width=%g, want 30 probes and the first width ≤ 1e-8", report.Probes, report.VerifiedBound)
	}
}

func TestParameterPrecisionIsInclusiveAndExact(t *testing.T) {
	if !parameterPrecisionMet(big.NewRat(1, 100_000_000)) {
		t.Fatal("width exactly 1/10⁸ must complete")
	}
	if parameterPrecisionMet(ratAdd(big.NewRat(1, 100_000_000), big.NewRat(1, 1<<62))) {
		t.Fatal("width just above 1/10⁸ must not complete")
	}
	// float64(1e-8) is slightly above 1/10⁸: an exact comparison rejects it.
	if parameterPrecisionMet(ratOf(1e-8)) == (ratOf(1e-8).Cmp(big.NewRat(1, 100_000_000)) > 0) {
		t.Fatal("float64(1e-8) must be compared exactly")
	}
}

// TestBisectionUnknownBeforePrecisionIsPrecisionUnproven: with no exact
// budget the δ = 0 endpoint has no certificate; the search stops on UNKNOWN
// with the bracket still wide, which is PrecisionUnproven, never infeasible.
func TestBisectionUnknownBeforePrecisionIsPrecisionUnproven(t *testing.T) {
	engine := NewIntentEngine(NewGonumSolver())
	engine.exactBudget = &exactBudget{maxPivots: 0, maxBits: 1 << 14}
	solution, err := engine.Solve(context.Background(), skewedModel(t))
	if err != nil {
		t.Fatal(err)
	}
	if solution.Status != StatusNumericalFailure || solution.Diagnostics[0].Code != DiagnosticPrecisionUnproven ||
		!strings.Contains(solution.Diagnostics[0].Message, "stop reason "+StopEvidenceUnavailable) {
		t.Fatalf("status=%s diagnostics=%+v", solution.Status, solution.Diagnostics)
	}
	if !strings.Contains(solution.Diagnostics[0].Message, designerUncertainHint) {
		t.Fatalf("message lacks the fixed hint: %q", solution.Diagnostics[0].Message)
	}
}

// TestBisectionCancellationTakesPriority: cancelling during the profile
// bisection returns the context error and starts no further solve.
func TestBisectionCancellationTakesPriority(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	solver := &countingSolver{inner: NewGonumSolver()}
	_, err := NewIntentEngine(solver).SolveObserved(ctx, skewedModel(t), func(event OptimizationStageEvent) {
		if event.Stage == StageMinimizeMainProfileDeviation && event.State == "progress" && event.Probe == 5 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if solver.afterCancelling != 0 {
		t.Fatalf("%d solves started after cancellation", solver.afterCancelling)
	}
}

// TestBisectionLockCertificateIsContradiction: a lock that is provably
// infeasible although a witness exists is an INTERNAL_ERROR, never a hard
// infeasibility.
func TestBisectionLockCertificateIsContradiction(t *testing.T) {
	compiled := skewedModel(t)
	run := NewIntentEngine(NewGonumSolver()).newRun(compiled, nil)
	_, _, _, failure, err := run.bisect(context.Background(), bisectSpec{
		stage: StageMinimizeMainProfileDeviation, origin: ObjectiveMainProfileProbe,
		metric: metricMainProfileDeviation, minimize: true, feasibleEnd: 2, otherEnd: 0, iterations: 60,
		build: func(delta float64) (LinearProblem, error) { return BuildMainProfileDeviationProblem(compiled, delta) },
		lock:  func(float64) float64 { return 0 }, // δ = 0 is infeasible (δ* = 0.4)
	})
	if err != nil {
		t.Fatal(err)
	}
	if failure == nil || failure.solution.Status != StatusInternalError {
		t.Fatalf("failure=%+v", failure)
	}
}

// TestIntentEngineTheoreticalEndpointSkipsBisection: ρ = 1 feasible stops
// after the two endpoint probes with a zero-width bracket.
func TestIntentEngineTheoreticalEndpointSkipsBisection(t *testing.T) {
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceFixtureClass(0, 1))
	solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), compiled)
	if err != nil {
		t.Fatal(err)
	}
	main := solution.MainGroupInternalVisibilityOptimization
	if solution.Status != StatusOptimal || main.Probes != 2 || main.Lower != 1 || main.Upper != 1 || main.StopReason != StopEndpoint ||
		main.FixedValue != 1-compiled.Prepared.Plan.EngineOptions.VisibilityTolerance {
		t.Fatalf("status=%s Main Group visibility=%+v", solution.Status, main)
	}
}

// TestIntentEngineStageEventsAndReports: every stage starts once and ends
// once in declaration order; deprecated aliases mirror the canonical reports.
func TestIntentEngineStageEventsAndReports(t *testing.T) {
	var events []OptimizationStageEvent
	solution, err := NewIntentEngine(NewGonumSolver()).SolveObserved(context.Background(), skewedModel(t), func(event OptimizationStageEvent) {
		events = append(events, event)
	})
	if err != nil || solution.Status != StatusOptimal {
		t.Fatalf("status=%s err=%v", solution.Status, err)
	}
	if !reflect.DeepEqual(solution.PhaseA, solution.MainProfileOptimization) || !reflect.DeepEqual(solution.PhaseB, solution.OtherBucketVisibilityOptimization) {
		t.Fatal("deprecated EngineSolution aliases differ from canonical reports")
	}
	want := []OptimizationStageID{StageProveHardFeasibility, StageMinimizeMainProfileDeviation, StageMaximizeOtherBucketVisibility,
		StageMaximizeMainGroupInternalVisibility, StageSelectCanonicalBucketProbabilities}
	var terminal []OptimizationStageID
	started := map[OptimizationStageID]int{}
	for _, event := range events {
		if strings.Contains(string(event.Stage), "phase-") {
			t.Fatalf("semantic stage leaked legacy phase name: %q", event.Stage)
		}
		switch event.State {
		case "started":
			started[event.Stage]++
		case "completed", "skipped", "failed":
			terminal = append(terminal, event.Stage)
		case "progress":
			if event.Status != "FEASIBLE" && event.Status != "INFEASIBLE" && event.Status != "UNKNOWN" && event.Stage != StageSelectCanonicalBucketProbabilities {
				t.Fatalf("progress status=%q, want an evidence class", event.Status)
			}
		}
	}
	if !reflect.DeepEqual(terminal, want) {
		t.Fatalf("terminal events=%v want=%v", terminal, want)
	}
	for _, stage := range want {
		if started[stage] != 1 {
			t.Fatalf("stage %q started %d times", stage, started[stage])
		}
	}
}

// TestSigmaEndpointsNeverCallSolver is A26: a coupling decision calls the
// backend only for the per-Class witnesses; both σ endpoints of every Class
// come from the exact simplex.
func TestSigmaEndpointsNeverCallSolver(t *testing.T) {
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceFixtureClass(0, .5), evidenceSkewedClass(1, .5))
	solver := &countingSolver{inner: NewGonumSolver()}
	run := NewIntentEngine(solver).newRun(compiled, nil)
	decision, err := run.decideJoint(context.Background(), compiled.Hard, StageProveHardFeasibility, ObjectiveHardFeasibility, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.class != evidenceFeasible || solver.calls != 2 || run.counters.SigmaEndpoints != 4 {
		t.Fatalf("class=%s backend calls=%d σ endpoints=%d (%s)", decision.class, solver.calls, run.counters.SigmaEndpoints, decision.reasonText())
	}
}

// TestSigmaEndpointsSolveSavedGonumHangs is A26: the σ-endpoint LPs on which
// Gonum did not return (problab-p00 contract experiments) are solved by the
// exact simplex within budget, accepted by the internal set.
func TestSigmaEndpointsSolveSavedGonumHangs(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "stuck-sigma-lp", "*.json"))
	if err != nil || len(paths) != 4 {
		t.Fatalf("saved LPs: %v %v", paths, err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var record slowSolveRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		problem, err := record.Problem.Problem()
		if err != nil {
			t.Fatal(err)
		}
		objective, err := record.Objective.Objective()
		if err != nil {
			t.Fatal(err)
		}
		tau, err := decodeFloat64(record.Options["feasibility_tolerance"])
		if err != nil {
			t.Fatal(err)
		}
		run := NewIntentEngine(statusSolver{SolveNumericalFailure}).newRun(CompiledModel{}, nil)
		run.useTolerance(tau)
		index := map[VariableID]int{}
		for j, v := range problem.Variables {
			index[v.ID] = j
		}
		coefficients := map[int]*big.Rat{}
		for _, term := range objective.Terms {
			coefficients[index[term.Variable]] = ratOf(term.Coeff)
		}
		x, err := run.sigmaEndpoint(context.Background(), problem, coefficients, objective.Sense)
		if err != nil || x == nil || !replayWithRule(problem, x, internalRule(tau)) {
			t.Fatalf("%s: endpoint=%v err=%v", path, x, err)
		}
		if run.counters.BudgetExhausted != 0 || run.counters.MaxExactPivots > 100 {
			t.Fatalf("%s: exact usage %+v", path, run.counters)
		}
	}
}
