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
	metricCanonicalBucketProbabilities   = "canonical_bucket_probabilities"
	reasonNoSupportedOtherBuckets        = "no-supported-other-buckets"
	reasonNoMainGroupWithMultipleSupport = "no-main-group-with-multiple-supported-buckets"
)

// IntentEngine owns stage policy above a backend-neutral Solver. In particular,
// it decides whether SolveInfeasible is a hard-model result, a normal bisection
// signal, or a contradiction after a known feasible witness exists.
type IntentEngine struct {
	solver Solver
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
	options := SolveOptions{
		FeasibilityTolerance: compiled.Prepared.Plan.EngineOptions.FeasibilityTolerance,
		OptimalityTolerance:  compiled.Prepared.Plan.EngineOptions.OptimalityTolerance,
	}
	hardStage := beginOptimizationStage(observer, StageProveHardFeasibility, string(StageProveHardFeasibility), "")
	hard, err := e.solver.Solve(ctx, compiled.Hard, LinearObjective{
		Name: StageProveHardFeasibility, Sense: Minimize, Origin: ObjectiveHardFeasibility,
	}, options)
	if err != nil {
		hardStage.finish("failed", 1, nil, nil, nil, "error", err.Error())
		return EngineSolution{}, err
	}
	if hard.Status == SolveInfeasible && len(compiled.Hard.Rows) > 0 {
		diagnostics, err := e.diagnoseHardModelInfeasibility(ctx, compiled, options)
		if err != nil {
			hardStage.finish("failed", 1, nil, nil, nil, solveStatusName(hard.Status), err.Error())
			return EngineSolution{}, err
		}
		if diagnostics.StopsRun() {
			hardStage.finish("failed", 1, nil, nil, nil, solveStatusName(hard.Status), diagnostics[0].Message)
			return EngineSolution{
				Status: diagnostics[0].Status, Diagnostics: diagnostics, Evidence: hard.Evidence,
			}, nil
		}
	}
	if result, stop := mapHardSolveResult(hard); stop {
		hardStage.finish("failed", 1, nil, nil, nil, solveStatusName(hard.Status), result.Diagnostics[0].Message)
		return result, nil
	}
	hardStage.finish("completed", 1, nil, nil, nil, solveStatusName(hard.Status), "")

	mainProfileProblem, mainProfileWitness, mainProfileReport, result, err := e.minimizeMainProfileDeviation(ctx, compiled, options, observer)
	if err != nil || result.Status.Valid() {
		return result, err
	}
	otherProblem, otherWitness, otherReport, result, err := e.maximizeOtherBucketVisibility(ctx, compiled, mainProfileProblem, mainProfileWitness, options, observer)
	if err != nil || result.Status.Valid() {
		return result, err
	}
	mainGroupProblem, mainGroupWitness, mainGroupReport, result, err := e.maximizeMainGroupInternalVisibility(ctx, compiled, otherProblem, otherWitness, options, observer)
	if err != nil || result.Status.Valid() {
		return result, err
	}
	canonicalProblem, canonical, canonicalReport, result, err := e.selectCanonicalBucketProbabilities(ctx, compiled, mainGroupProblem, mainGroupWitness, options, observer)
	if err != nil || result.Status.Valid() {
		return result, err
	}
	primary, err := compiled.PrimaryValues(canonicalProblem, canonical.Values)
	if err != nil {
		return internalEngineFailure(fmt.Sprintf("map canonical primary variables: %v", err), canonical.Evidence), nil
	}
	return newOptimalEngineSolution(
		canonicalProblem,
		canonical.Values,
		primary,
		mainProfileReport,
		otherReport,
		mainGroupReport,
		canonicalReport,
		canonical.Evidence,
	), nil
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

// mapHardSolveResult is the only path that maps SolveInfeasible to the public
// INFEASIBLE_MODEL status. Numerical and unbounded backend outcomes retain
// their distinct public meanings and never masquerade as Designer conflict.
func mapHardSolveResult(result SolveResult) (EngineSolution, bool) {
	switch result.Status {
	case SolveOptimal:
		return EngineSolution{}, false
	case SolveInfeasible:
		diagnostic := Diagnostic{
			Code: DiagnosticHardModelInfeasible, Status: StatusInfeasibleModel,
			Message:        "the immutable hard LP is infeasible; semantic preference optimizations were not applied",
			Representation: RepresentationAtomicBuckets,
		}
		return EngineSolution{Status: StatusInfeasibleModel, Diagnostics: Diagnostics{diagnostic}, Evidence: result.Evidence}, true
	case SolveNumericalFailure:
		return numericalEngineFailure("hard-feasibility solve failed numerical or semantic replay checks", result.Evidence), true
	case SolveUnbounded:
		return internalEngineFailure("zero-objective hard-feasibility model was reported unbounded", result.Evidence), true
	default:
		return internalEngineFailure("solver returned an unknown hard-feasibility status", result.Evidence), true
	}
}

// minimizeMainProfileDeviation performs bounded feasibility bisection over
// delta in [0,2]. It
// explicitly proves the delta=2 endpoint and tests delta=0; intermediate
// SolveInfeasible results update the lower bracket and are normal algorithmic
// signals, never Run-level infeasibility.
func (e *IntentEngine) minimizeMainProfileDeviation(
	ctx context.Context,
	compiled CompiledModel,
	options SolveOptions,
	observer OptimizationStageObserver,
) (LinearProblem, SolveResult, BisectionReport, EngineSolution, error) {
	report := BisectionReport{
		Applicable: true, Objective: string(StageMinimizeMainProfileDeviation),
		Metric: metricMainProfileDeviation, Direction: "minimize",
	}
	stage := beginOptimizationStage(observer, StageMinimizeMainProfileDeviation, report.Objective, report.Metric)
	defer stage.ensureTerminal()
	upperProblem, err := BuildMainProfileDeviationProblem(compiled, 2)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, internalEngineFailure(err.Error(), SolverEvidence{}), nil
	}
	upperWitness, err := e.solveProbe(ctx, upperProblem, StageMinimizeMainProfileDeviation, ObjectiveMainProfileProbe, options)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, EngineSolution{}, err
	}
	report.Probes++
	if upperWitness.Status != SolveOptimal {
		return LinearProblem{}, SolveResult{}, report, contradictionFromKnownWitness("Main profile deviation delta=2 transform did not preserve hard feasibility", upperWitness), nil
	}
	lower, upper := 0.0, 2.0
	bestWitness := upperWitness
	stage.progress(report.Probes, lower, upper, nil, solveStatusName(upperWitness.Status))
	zeroProblem, err := BuildMainProfileDeviationProblem(compiled, 0)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, internalEngineFailure(err.Error(), upperWitness.Evidence), nil
	}
	zeroWitness, err := e.solveProbe(ctx, zeroProblem, StageMinimizeMainProfileDeviation, ObjectiveMainProfileProbe, options)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, EngineSolution{}, err
	}
	report.Probes++
	switch zeroWitness.Status {
	case SolveOptimal:
		upper = 0
		bestWitness = zeroWitness
	case SolveInfeasible:
		// A normal probe result: profile perfection is unavailable, so the
		// deterministic bracket remains (0, 2].
	case SolveNumericalFailure, SolveUnbounded:
		return LinearProblem{}, SolveResult{}, report, probeBackendFailure("Main profile deviation delta=0 probe", zeroWitness), nil
	default:
		return LinearProblem{}, SolveResult{}, report, internalEngineFailure("unknown Main profile deviation solver status", zeroWitness.Evidence), nil
	}
	stage.progress(report.Probes, lower, upper, nil, solveStatusName(zeroWitness.Status))
	if upper > 0 {
		for range compiled.Prepared.Plan.EngineOptions.ProfileBisectionIterations {
			midpoint := lower + (upper-lower)/2
			probe, err := BuildMainProfileDeviationProblem(compiled, midpoint)
			if err != nil {
				return LinearProblem{}, SolveResult{}, report, internalEngineFailure(err.Error(), bestWitness.Evidence), nil
			}
			witness, err := e.solveProbe(ctx, probe, StageMinimizeMainProfileDeviation, ObjectiveMainProfileProbe, options)
			if err != nil {
				return LinearProblem{}, SolveResult{}, report, EngineSolution{}, err
			}
			report.Probes++
			switch witness.Status {
			case SolveOptimal:
				upper = midpoint
				bestWitness = witness
			case SolveInfeasible:
				lower = midpoint
			case SolveNumericalFailure, SolveUnbounded:
				return LinearProblem{}, SolveResult{}, report, probeBackendFailure("Main profile deviation bisection probe", witness), nil
			default:
				return LinearProblem{}, SolveResult{}, report, internalEngineFailure("unknown Main profile deviation solver status", witness.Evidence), nil
			}
			stage.progress(report.Probes, lower, upper, nil, solveStatusName(witness.Status))
		}
	}
	fixed := math.Min(2, upper+compiled.Prepared.Plan.EngineOptions.ProfileTolerance)
	locked, err := BuildMainProfileDeviationProblem(compiled, fixed)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, internalEngineFailure(err.Error(), bestWitness.Evidence), nil
	}
	lockedWitness, err := e.solveProbe(ctx, locked, StageMinimizeMainProfileDeviation, ObjectiveIntentRefinement, options)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, EngineSolution{}, err
	}
	if lockedWitness.Status != SolveOptimal {
		return LinearProblem{}, SolveResult{}, report, contradictionFromKnownWitness("Main profile deviation tolerance lock rejected its known feasible witness", lockedWitness), nil
	}
	report.Lower, report.Upper, report.FixedValue = lower, upper, fixed
	stage.finish("completed", report.Probes, floatPointer(lower), floatPointer(upper), floatPointer(fixed), solveStatusName(lockedWitness.Status), "")
	return locked, lockedWitness, report, EngineSolution{}, nil
}

// maximizeOtherBucketVisibility maximizes common normalized rho in [0,1]
// while retaining the Main profile lock. Infeasible positive-rho probes tighten
// the upper bracket; rho=0 must remain feasible.
func (e *IntentEngine) maximizeOtherBucketVisibility(
	ctx context.Context,
	compiled CompiledModel,
	mainProfileBase LinearProblem,
	mainProfileWitness SolveResult,
	options SolveOptions,
	observer OptimizationStageObserver,
) (LinearProblem, SolveResult, BisectionReport, EngineSolution, error) {
	report := BisectionReport{
		Applicable: true, Objective: string(StageMaximizeOtherBucketVisibility),
		Metric: metricOtherBucketVisibility, Direction: "maximize",
	}
	stage := beginOptimizationStage(observer, StageMaximizeOtherBucketVisibility, report.Objective, report.Metric)
	defer stage.ensureTerminal()
	if !hasEligibleOthers(compiled.Prepared) {
		report.Applicable = false
		report.InapplicableReason = reasonNoSupportedOtherBuckets
		report.FixedValue = 0
		stage.finish("skipped", 0, nil, nil, floatPointer(0), "", report.InapplicableReason)
		return mainProfileBase, mainProfileWitness, report, EngineSolution{}, nil
	}
	zeroProblem, err := AddOtherBucketVisibilityRows(mainProfileBase, compiled, 0)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, internalEngineFailure(err.Error(), mainProfileWitness.Evidence), nil
	}
	zeroWitness, err := e.solveProbe(ctx, zeroProblem, StageMaximizeOtherBucketVisibility, ObjectiveOtherBucketVisibilityProbe, options)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, EngineSolution{}, err
	}
	report.Probes++
	if zeroWitness.Status != SolveOptimal {
		return LinearProblem{}, SolveResult{}, report, contradictionFromKnownWitness("Other bucket visibility rho=0 transform did not preserve the Main profile witness", zeroWitness), nil
	}
	lower, upper := 0.0, 1.0
	bestWitness := zeroWitness
	stage.progress(report.Probes, lower, upper, nil, solveStatusName(zeroWitness.Status))
	oneProblem, err := AddOtherBucketVisibilityRows(mainProfileBase, compiled, 1)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, internalEngineFailure(err.Error(), zeroWitness.Evidence), nil
	}
	oneWitness, err := e.solveProbe(ctx, oneProblem, StageMaximizeOtherBucketVisibility, ObjectiveOtherBucketVisibilityProbe, options)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, EngineSolution{}, err
	}
	report.Probes++
	switch oneWitness.Status {
	case SolveOptimal:
		lower = 1
		bestWitness = oneWitness
	case SolveInfeasible:
		// A normal probe result: perfect Other retention is unavailable.
	case SolveNumericalFailure, SolveUnbounded:
		return LinearProblem{}, SolveResult{}, report, probeBackendFailure("Other bucket visibility rho=1 probe", oneWitness), nil
	default:
		return LinearProblem{}, SolveResult{}, report, internalEngineFailure("unknown Other bucket visibility solver status", oneWitness.Evidence), nil
	}
	stage.progress(report.Probes, lower, upper, nil, solveStatusName(oneWitness.Status))
	if lower < 1 {
		for range compiled.Prepared.Plan.EngineOptions.OtherVisibilityBisectionIterations {
			midpoint := lower + (upper-lower)/2
			probe, err := AddOtherBucketVisibilityRows(mainProfileBase, compiled, midpoint)
			if err != nil {
				return LinearProblem{}, SolveResult{}, report, internalEngineFailure(err.Error(), bestWitness.Evidence), nil
			}
			witness, err := e.solveProbe(ctx, probe, StageMaximizeOtherBucketVisibility, ObjectiveOtherBucketVisibilityProbe, options)
			if err != nil {
				return LinearProblem{}, SolveResult{}, report, EngineSolution{}, err
			}
			report.Probes++
			switch witness.Status {
			case SolveOptimal:
				lower = midpoint
				bestWitness = witness
			case SolveInfeasible:
				upper = midpoint
			case SolveNumericalFailure, SolveUnbounded:
				return LinearProblem{}, SolveResult{}, report, probeBackendFailure("Other bucket visibility bisection probe", witness), nil
			default:
				return LinearProblem{}, SolveResult{}, report, internalEngineFailure("unknown Other bucket visibility solver status", witness.Evidence), nil
			}
			stage.progress(report.Probes, lower, upper, nil, solveStatusName(witness.Status))
		}
	}
	fixed := math.Max(0, lower-compiled.Prepared.Plan.EngineOptions.VisibilityTolerance)
	locked, err := AddOtherBucketVisibilityRows(mainProfileBase, compiled, fixed)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, internalEngineFailure(err.Error(), bestWitness.Evidence), nil
	}
	lockedWitness, err := e.solveProbe(ctx, locked, StageMaximizeOtherBucketVisibility, ObjectiveIntentRefinement, options)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, EngineSolution{}, err
	}
	if lockedWitness.Status != SolveOptimal {
		return LinearProblem{}, SolveResult{}, report, contradictionFromKnownWitness("Other bucket visibility tolerance lock rejected its known feasible witness", lockedWitness), nil
	}
	report.Lower, report.Upper, report.FixedValue = lower, upper, fixed
	stage.finish("completed", report.Probes, floatPointer(lower), floatPointer(upper), floatPointer(fixed), solveStatusName(lockedWitness.Status), "")
	return locked, lockedWitness, report, EngineSolution{}, nil
}

// maximizeMainGroupInternalVisibility protects supported sibling buckets inside
// every eligible Main Group with one common normalized rho.
func (e *IntentEngine) maximizeMainGroupInternalVisibility(
	ctx context.Context,
	compiled CompiledModel,
	otherVisibilityBase LinearProblem,
	otherVisibilityWitness SolveResult,
	options SolveOptions,
	observer OptimizationStageObserver,
) (LinearProblem, SolveResult, BisectionReport, EngineSolution, error) {
	report := BisectionReport{
		Applicable: true, Objective: string(StageMaximizeMainGroupInternalVisibility),
		Metric: metricMainGroupInternalVisibility, Direction: "maximize",
	}
	stage := beginOptimizationStage(observer, StageMaximizeMainGroupInternalVisibility, report.Objective, report.Metric)
	defer stage.ensureTerminal()
	if !hasEligibleMainGroupInternalVisibility(compiled.Prepared) {
		report.Applicable = false
		report.InapplicableReason = reasonNoMainGroupWithMultipleSupport
		stage.finish("skipped", 0, nil, nil, floatPointer(0), "", report.InapplicableReason)
		return otherVisibilityBase, otherVisibilityWitness, report, EngineSolution{}, nil
	}
	zeroProblem, err := AddMainGroupInternalVisibilityRows(otherVisibilityBase, compiled, 0)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, internalEngineFailure(err.Error(), otherVisibilityWitness.Evidence), nil
	}
	zeroWitness, err := e.solveProbe(ctx, zeroProblem, StageMaximizeMainGroupInternalVisibility, ObjectiveMainGroupInternalVisibilityProbe, options)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, EngineSolution{}, err
	}
	report.Probes++
	if zeroWitness.Status != SolveOptimal {
		return LinearProblem{}, SolveResult{}, report, contradictionFromKnownWitness("Main Group internal visibility rho=0 transform did not preserve the Other visibility witness", zeroWitness), nil
	}
	lower, upper := 0.0, 1.0
	bestWitness := zeroWitness
	stage.progress(report.Probes, lower, upper, nil, solveStatusName(zeroWitness.Status))
	oneProblem, err := AddMainGroupInternalVisibilityRows(otherVisibilityBase, compiled, 1)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, internalEngineFailure(err.Error(), zeroWitness.Evidence), nil
	}
	oneWitness, err := e.solveProbe(ctx, oneProblem, StageMaximizeMainGroupInternalVisibility, ObjectiveMainGroupInternalVisibilityProbe, options)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, EngineSolution{}, err
	}
	report.Probes++
	switch oneWitness.Status {
	case SolveOptimal:
		lower = 1
		bestWitness = oneWitness
	case SolveInfeasible:
	case SolveNumericalFailure, SolveUnbounded:
		return LinearProblem{}, SolveResult{}, report, probeBackendFailure("Main Group internal visibility rho=1 probe", oneWitness), nil
	default:
		return LinearProblem{}, SolveResult{}, report, internalEngineFailure("unknown Main Group internal visibility solver status", oneWitness.Evidence), nil
	}
	stage.progress(report.Probes, lower, upper, nil, solveStatusName(oneWitness.Status))
	if lower < 1 {
		for range compiled.Prepared.Plan.EngineOptions.MainGroupInternalVisibilityBisectionIterations {
			midpoint := lower + (upper-lower)/2
			probe, err := AddMainGroupInternalVisibilityRows(otherVisibilityBase, compiled, midpoint)
			if err != nil {
				return LinearProblem{}, SolveResult{}, report, internalEngineFailure(err.Error(), bestWitness.Evidence), nil
			}
			witness, err := e.solveProbe(ctx, probe, StageMaximizeMainGroupInternalVisibility, ObjectiveMainGroupInternalVisibilityProbe, options)
			if err != nil {
				return LinearProblem{}, SolveResult{}, report, EngineSolution{}, err
			}
			report.Probes++
			switch witness.Status {
			case SolveOptimal:
				lower = midpoint
				bestWitness = witness
			case SolveInfeasible:
				upper = midpoint
			case SolveNumericalFailure, SolveUnbounded:
				return LinearProblem{}, SolveResult{}, report, probeBackendFailure("Main Group internal visibility bisection probe", witness), nil
			default:
				return LinearProblem{}, SolveResult{}, report, internalEngineFailure("unknown Main Group internal visibility solver status", witness.Evidence), nil
			}
			stage.progress(report.Probes, lower, upper, nil, solveStatusName(witness.Status))
		}
	}
	fixed := math.Max(0, lower-compiled.Prepared.Plan.EngineOptions.VisibilityTolerance)
	locked, err := AddMainGroupInternalVisibilityRows(otherVisibilityBase, compiled, fixed)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, internalEngineFailure(err.Error(), bestWitness.Evidence), nil
	}
	lockedWitness, err := e.solveProbe(ctx, locked, StageMaximizeMainGroupInternalVisibility, ObjectiveIntentRefinement, options)
	if err != nil {
		return LinearProblem{}, SolveResult{}, report, EngineSolution{}, err
	}
	if lockedWitness.Status != SolveOptimal {
		return LinearProblem{}, SolveResult{}, report, contradictionFromKnownWitness("Main Group internal visibility tolerance lock rejected its known feasible witness", lockedWitness), nil
	}
	report.Lower, report.Upper, report.FixedValue = lower, upper, fixed
	stage.finish("completed", report.Probes, floatPointer(lower), floatPointer(upper), floatPointer(fixed), solveStatusName(lockedWitness.Status), "")
	return locked, lockedWitness, report, EngineSolution{}, nil
}

// selectCanonicalBucketProbabilities performs stable lexicographic minimization over primary bucket
// masses only. Each established minimum is locked with a one-sided numerical
// allowance before the next solve. This chooses a deterministic serialization
// representative and makes no player-experience optimality claim.
func (e *IntentEngine) selectCanonicalBucketProbabilities(
	ctx context.Context,
	compiled CompiledModel,
	base LinearProblem,
	witness SolveResult,
	options SolveOptions,
	observer OptimizationStageObserver,
) (LinearProblem, SolveResult, CanonicalizationReport, EngineSolution, error) {
	report := CanonicalizationReport{
		Objective: string(StageSelectCanonicalBucketProbabilities),
		Metric:    metricCanonicalBucketProbabilities, Direction: "lexicographically-minimize",
		PrimaryVariables: len(compiled.Primary),
	}
	stage := beginOptimizationStage(observer, StageSelectCanonicalBucketProbabilities, report.Objective, report.Metric)
	defer stage.ensureTerminal()
	problem := cloneLinearProblem(base)
	current := witness
	for primaryIndex, primary := range compiled.Primary {
		result, err := e.solver.Solve(ctx, problem, LinearObjective{
			Name: StageSelectCanonicalBucketProbabilities, Sense: Minimize, Origin: ObjectiveCanonicalBucketProbability,
			Terms: []LinearTerm{{Variable: primary.ID, Coeff: 1}},
		}, options)
		if err != nil {
			return LinearProblem{}, SolveResult{}, report, EngineSolution{}, err
		}
		report.Solves++
		if result.Status != SolveOptimal {
			return LinearProblem{}, SolveResult{}, report, contradictionFromKnownWitness(fmt.Sprintf("canonical bucket probability solve %d became infeasible or invalid", primaryIndex), result), nil
		}
		column := -1
		for i, variable := range problem.Variables {
			if variable.ID == primary.ID {
				column = i
				break
			}
		}
		if column < 0 {
			return LinearProblem{}, SolveResult{}, report, internalEngineFailure(fmt.Sprintf("canonical variable %q is missing", primary.ID), result.Evidence), nil
		}
		lock := result.Values[column] + math.Max(options.FeasibilityTolerance, options.OptimalityTolerance)
		if err := addRow(&problem, LinearRow{
			ID: RowID(fmt.Sprintf("canonical-bucket-probability:primary-%04d:lock", primaryIndex)), Family: "canonical_bucket_probability", Origin: OriginCanonicalization,
			ClassID: compiled.Prepared.Classes[primary.ClassIndex].ID, Description: "preserve the established lexicographic minimum of one primary bucket mass",
			Sense: SenseLE, RHS: lock, Terms: []LinearTerm{{Variable: primary.ID, Coeff: 1}},
		}); err != nil {
			return LinearProblem{}, SolveResult{}, report, internalEngineFailure(err.Error(), result.Evidence), nil
		}
		current = result
		stage.tick(report.Solves, solveStatusName(result.Status))
	}
	stage.finish("completed", report.Solves, nil, nil, nil, solveStatusName(current.Status), "")
	return problem, current, report, EngineSolution{}, nil
}

// solveProbe executes one zero-objective feasibility problem while preserving
// ObjectiveOrigin so the caller can apply the correct stage policy.
func (e *IntentEngine) solveProbe(ctx context.Context, problem LinearProblem, name OptimizationStageID, origin ObjectiveOrigin, options SolveOptions) (SolveResult, error) {
	return e.solver.Solve(ctx, problem, LinearObjective{Name: name, Sense: Minimize, Origin: origin}, options)
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

func (s *optimizationStageLifecycle) tick(probe int, status string) {
	if s == nil || s.observer == nil || s.terminal {
		return
	}
	s.observer(OptimizationStageEvent{
		Stage: s.stage, State: "progress", Objective: s.objective, Metric: s.metric,
		Probe: probe, Status: status, Duration: time.Since(s.started),
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

// probeBackendFailure classifies non-infeasibility failures from a bisection
// probe. Ordinary SolveInfeasible must be handled by the bracket owner before
// this helper is called.
func probeBackendFailure(stage string, result SolveResult) EngineSolution {
	if result.Status == SolveUnbounded {
		return internalEngineFailure(stage+" was unexpectedly unbounded", result.Evidence)
	}
	return numericalEngineFailure(stage+" failed numerical or semantic replay checks", result.Evidence)
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
