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
	"time"
)

// EngineSolution is the verified semantic witness selected by the ordered
// semantic LP sequence. Values correspond to Problem.Variables; Primary
// contains only p[k,i] in CompiledModel.Primary order for hashing and
// materialization.
type EngineSolution struct {
	Status                                  Status
	Diagnostics                             Diagnostics
	Problem                                 LinearProblem
	Values                                  []float64
	Primary                                 []float64
	MainProfileOptimization                 BisectionReport
	OtherBucketVisibilityOptimization       BisectionReport
	MainGroupInternalVisibilityOptimization BisectionReport
	CanonicalBucketProbabilitySelection     CanonicalizationReport
	// Deprecated: use MainProfileOptimization.
	PhaseA BisectionReport
	// Deprecated: use OtherBucketVisibilityOptimization.
	PhaseB   BisectionReport
	Evidence SolverEvidence
}

const (
	metricMainProfileDeviation           = "main_profile_deviation_delta"
	metricOtherBucketVisibility          = "other_bucket_visibility_rho"
	metricMainGroupInternalVisibility    = "main_group_internal_visibility_rho"
	metricCanonicalBucketProbabilities   = "unconditional_second_moment"
	reasonNoSupportedOtherBuckets        = "no-supported-other-buckets"
	reasonNoMainGroupWithMultipleSupport = "no-main-group-with-multiple-supported-buckets"
)

// IntentEngine owns stage policy above a backend-neutral Solver. In particular,
// it decides whether SolveInfeasible is a hard-model result, a normal bisection
// signal, or a contradiction after a known feasible witness exists.
type IntentEngine struct {
	solver Solver

	// Operational knobs; zero values select the documented defaults. They
	// never change the mathematics and are not configuration intent.
	evidenceDirectory  string
	slowSolveThreshold time.Duration
	slowSolveHeartbeat time.Duration
	exactBudget        *exactBudget // nil selects defaultExactBudget

	// observeResult, when set, receives every engine result (test hook).
	observeResult func(CompiledModel, EngineSolution)
}

// NewIntentEngine constructs the deterministic v2 engine with an injectable
// solver. A nil solver is rejected by Solve as an internal dependency error.
func NewIntentEngine(solver Solver) *IntentEngine {
	return &IntentEngine{solver: solver}
}

// Solve preserves the historical observer-free entry point.
func (e *IntentEngine) Solve(ctx context.Context, compiled CompiledModel) (EngineSolution, error) {
	return e.SolveObserved(ctx, compiled, nil)
}

// SolveObserved first proves hard feasibility, minimizes one common relative
// Main profile delta, maximizes common normalized Other visibility, maximizes
// common supported-sibling visibility inside Main Groups, and finally chooses a
// deterministic primary vector. Hard rows are never relaxed.
//
// Status mapping is intentionally stage-aware: an infeasible HardFeasibility
// solve becomes INFEASIBLE_MODEL after a typed diagnostic; infeasible preference
// probes merely update their deterministic brackets. Once a feasible
// witness exists, unexpected infeasibility in a lock/canonical solve is treated
// as numerical/transform contradiction and never overwrites the witness with a
// false hard-model diagnosis.
func (e *IntentEngine) SolveObserved(ctx context.Context, compiled CompiledModel, observer OptimizationStageObserver) (EngineSolution, error) {
	if e == nil || e.solver == nil {
		return EngineSolution{}, fmt.Errorf("intent engine requires a Solver dependency")
	}
	if err := ctx.Err(); err != nil {
		return EngineSolution{}, err
	}
	solution, err := e.newRun(compiled, observer).solveStages(ctx)
	if e.observeResult != nil && err == nil {
		e.observeResult(compiled, solution)
	}
	return solution, err
}

// newOptimalEngineSolution is the single compatibility boundary that keeps
// deprecated report aliases byte-for-byte equal to their canonical fields.
func newOptimalEngineSolution(
	problem LinearProblem,
	values, primary []float64,
	mainProfile, otherVisibility, mainGroupVisibility BisectionReport,
	canonicalReport CanonicalizationReport,
	evidence SolverEvidence,
) EngineSolution {
	solution := EngineSolution{
		Status: StatusOptimal, Problem: problem,
		Values: append([]float64(nil), values...), Primary: append([]float64(nil), primary...),
		MainProfileOptimization:                 mainProfile,
		OtherBucketVisibilityOptimization:       otherVisibility,
		MainGroupInternalVisibilityOptimization: mainGroupVisibility,
		CanonicalBucketProbabilitySelection:     canonicalReport,
		Evidence:                                evidence,
	}
	solution.PhaseA = solution.MainProfileOptimization
	solution.PhaseB = solution.OtherBucketVisibilityOptimization
	return solution
}

// hasEligibleOthers reports whether Other visibility has at least one intent Class with
// supported non-Main atomic buckets. A Class with no Others is vacuous.
func hasEligibleOthers(prepared PreparedProblem) bool {
	for _, class := range prepared.Classes {
		if class.Intent && len(class.Others) > 0 {
			return true
		}
	}
	return false
}

// hasEligibleMainGroupInternalVisibility reports whether at least one intent
// Class has a Main Group containing two or more supported atomic buckets.
func hasEligibleMainGroupInternalVisibility(prepared PreparedProblem) bool {
	for _, class := range prepared.Classes {
		if !class.Intent {
			continue
		}
		for _, group := range class.Groups {
			count := 0
			seen := make(map[int]struct{}, len(group.BucketIndexes))
			for _, bucketIndex := range group.BucketIndexes {
				if bucketIndex < 0 || bucketIndex >= len(class.Buckets) {
					// Force the builder path so malformed prepared membership is
					// returned as an internal model error instead of a silent skip.
					return true
				}
				if _, duplicate := seen[bucketIndex]; duplicate {
					return true
				}
				seen[bucketIndex] = struct{}{}
				if class.Buckets[bucketIndex].Supported() {
					count++
				}
			}
			if count >= 2 {
				return true
			}
		}
	}
	return false
}

type optimizationStageLifecycle struct {
	observer  OptimizationStageObserver
	stage     OptimizationStageID
	objective string
	metric    string
	started   time.Time
	terminal  bool
}

func beginOptimizationStage(observer OptimizationStageObserver, stage OptimizationStageID, objective, metric string) *optimizationStageLifecycle {
	lifecycle := &optimizationStageLifecycle{observer: observer, stage: stage, objective: objective, metric: metric, started: time.Now()}
	if observer != nil {
		observer(OptimizationStageEvent{Stage: stage, State: "started", Objective: objective, Metric: metric})
	}
	return lifecycle
}

func (s *optimizationStageLifecycle) progress(probe int, lower, upper float64, fixed *float64, status string) {
	if s == nil || s.observer == nil || s.terminal {
		return
	}
	s.observer(OptimizationStageEvent{
		Stage: s.stage, State: "progress", Objective: s.objective, Metric: s.metric,
		Probe: probe, Lower: floatPointer(lower), Upper: floatPointer(upper), FixedValue: fixed,
		Status: status, Duration: time.Since(s.started),
	})
}

func (s *optimizationStageLifecycle) tick(probe, total int, status string) {
	if s == nil || s.observer == nil || s.terminal {
		return
	}
	s.observer(OptimizationStageEvent{
		Stage: s.stage, State: "progress", Objective: s.objective, Metric: s.metric,
		Probe: probe, Total: total, Status: status, Duration: time.Since(s.started),
	})
}

func (s *optimizationStageLifecycle) finish(state string, probe int, lower, upper, fixed *float64, status, message string) {
	if s == nil || s.terminal {
		return
	}
	s.terminal = true
	if s.observer != nil {
		s.observer(OptimizationStageEvent{
			Stage: s.stage, State: state, Objective: s.objective, Metric: s.metric,
			Probe: probe, Lower: lower, Upper: upper, FixedValue: fixed,
			Status: status, Message: message, Duration: time.Since(s.started),
		})
	}
}

func (s *optimizationStageLifecycle) ensureTerminal() {
	if s != nil && !s.terminal {
		s.finish("failed", 0, nil, nil, nil, "", "optimization stage terminated before completion")
	}
}

func floatPointer(value float64) *float64 {
	copy := value
	return &copy
}

func solveStatusName(status SolveStatus) string {
	switch status {
	case SolveOptimal:
		return "optimal"
	case SolveInfeasible:
		return "infeasible"
	case SolveNumericalFailure:
		return "numerical-failure"
	case SolveUnbounded:
		return "unbounded"
	default:
		return "unknown"
	}
}

// contradictionFromKnownWitness converts any non-optimal refinement result to
// numerical or internal failure without erasing the previously proved witness.
func contradictionFromKnownWitness(message string, result SolveResult) EngineSolution {
	if result.Status == SolveUnbounded {
		return internalEngineFailure(message+": unexpected unbounded model", result.Evidence)
	}
	return numericalEngineFailure(message, result.Evidence)
}

// numericalEngineFailure constructs the stable NUMERICAL_FAILURE value result.
func numericalEngineFailure(message string, evidence SolverEvidence) EngineSolution {
	diagnostic := Diagnostic{
		Code: DiagnosticSolverNumericalFailure, Status: StatusNumericalFailure,
		Message: message, Representation: RepresentationAtomicBuckets,
	}
	return EngineSolution{Status: StatusNumericalFailure, Diagnostics: Diagnostics{diagnostic}, Evidence: evidence}
}

// internalEngineFailure constructs the stable INTERNAL_ERROR value result for
// broken transforms or impossible canonical-model backend classifications.
func internalEngineFailure(message string, evidence SolverEvidence) EngineSolution {
	diagnostic := Diagnostic{
		Code: DiagnosticInternalModelError, Status: StatusInternalError,
		Message: message, Representation: RepresentationAtomicBuckets,
	}
	return EngineSolution{Status: StatusInternalError, Diagnostics: Diagnostics{diagnostic}, Evidence: evidence}
}
