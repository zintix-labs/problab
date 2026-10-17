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
	"math/big"
	"strings"
)

// designerUncertainHint is the fixed text every EvidenceUnavailable and
// PrecisionUnproven message carries (techspec §2): an engineering limit is
// never presented as a reason to change Designer parameters.
const designerUncertainHint = "目前無法判定，沒有證據要求調整設計參數。"

// stageFailure carries a terminal value result out of a stage.
type stageFailure struct {
	solution EngineSolution
}

// solveStages runs every stage of techspec §7 on per-Class decomposition and
// verified evidence. A non-nil error is cancellation or a broken dependency;
// every mathematical or numerical outcome is a value result.
func (r *engineRun) solveStages(ctx context.Context) (EngineSolution, error) {
	compiled := r.compiled
	failure, err := r.proveHardFeasibility(ctx)
	if err != nil || failure != nil {
		return failureOrError(failure, err)
	}
	profile, profileWitness, profileReport, failure, err := r.bisect(ctx, bisectSpec{
		stage: StageMinimizeMainProfileDeviation, origin: ObjectiveMainProfileProbe,
		metric: metricMainProfileDeviation, minimize: true, feasibleEnd: 2, otherEnd: 0,
		iterations: compiled.Prepared.Plan.EngineOptions.ProfileBisectionIterations,
		build:      func(delta float64) (LinearProblem, error) { return BuildMainProfileDeviationProblem(compiled, delta) },
		lock: func(best float64) float64 {
			return math.Min(2, best+compiled.Prepared.Plan.EngineOptions.ProfileTolerance)
		},
		lockAllowance: compiled.Prepared.Plan.EngineOptions.ProfileTolerance,
	})
	if err != nil || failure != nil {
		return failureOrError(failure, err)
	}
	other, otherWitness, otherReport := profile, profileWitness, BisectionReport{}
	if hasEligibleOthers(compiled.Prepared) {
		other, otherWitness, otherReport, failure, err = r.bisect(ctx, bisectSpec{
			stage: StageMaximizeOtherBucketVisibility, origin: ObjectiveOtherBucketVisibilityProbe,
			metric: metricOtherBucketVisibility, minimize: false, feasibleEnd: 0, otherEnd: 1,
			iterations: compiled.Prepared.Plan.EngineOptions.OtherVisibilityBisectionIterations,
			build:      func(rho float64) (LinearProblem, error) { return AddOtherBucketVisibilityRows(profile, compiled, rho) },
			lock: func(best float64) float64 {
				return math.Max(0, best-compiled.Prepared.Plan.EngineOptions.VisibilityTolerance)
			},
			lockAllowance: compiled.Prepared.Plan.EngineOptions.VisibilityTolerance,
		})
		if err != nil || failure != nil {
			return failureOrError(failure, err)
		}
	} else {
		otherReport = r.skippedStage(StageMaximizeOtherBucketVisibility, metricOtherBucketVisibility, reasonNoSupportedOtherBuckets)
	}
	group, groupWitness, groupReport := other, otherWitness, BisectionReport{}
	if hasEligibleMainGroupInternalVisibility(compiled.Prepared) {
		group, groupWitness, groupReport, failure, err = r.bisect(ctx, bisectSpec{
			stage: StageMaximizeMainGroupInternalVisibility, origin: ObjectiveMainGroupInternalVisibilityProbe,
			metric: metricMainGroupInternalVisibility, minimize: false, feasibleEnd: 0, otherEnd: 1,
			iterations: compiled.Prepared.Plan.EngineOptions.MainGroupInternalVisibilityBisectionIterations,
			build: func(rho float64) (LinearProblem, error) {
				return AddMainGroupInternalVisibilityRows(other, compiled, rho)
			},
			lock: func(best float64) float64 {
				return math.Max(0, best-compiled.Prepared.Plan.EngineOptions.VisibilityTolerance)
			},
			lockAllowance: compiled.Prepared.Plan.EngineOptions.VisibilityTolerance,
		})
		if err != nil || failure != nil {
			return failureOrError(failure, err)
		}
	} else {
		groupReport = r.skippedStage(StageMaximizeMainGroupInternalVisibility, metricMainGroupInternalVisibility, reasonNoMainGroupWithMultipleSupport)
	}
	values, canonicalReport, failure, err := r.selectMinimumCV(ctx, group, groupWitness)
	if err != nil || failure != nil {
		return failureOrError(failure, err)
	}
	primary, err := compiled.PrimaryValues(group, values)
	if err != nil {
		return internalEngineFailure(fmt.Sprintf("map canonical primary variables: %v", err), r.finalEvidence(group, values)), nil
	}
	return newOptimalEngineSolution(group, values, primary, profileReport, otherReport, groupReport, canonicalReport, r.finalEvidence(group, values)), nil
}

func failureOrError(failure *stageFailure, err error) (EngineSolution, error) {
	if err != nil {
		return EngineSolution{}, err
	}
	return failure.solution, nil
}

// stageError turns a broken model structure into an INTERNAL_ERROR value
// result; cancellation and dependency errors are propagated unchanged.
func stageError(_ context.Context, err error, stage OptimizationStageID) (*stageFailure, error) {
	if !errors.Is(err, errModelStructure) {
		return nil, err
	}
	return &stageFailure{solution: internalEngineFailure(fmt.Sprintf("%s: %v", stage, err), SolverEvidence{})}, nil
}

// proveHardFeasibility is §7.1: a witness, a verified certificate, or no
// evidence. Explanatory diagnostics run only after a certificate exists.
func (r *engineRun) proveHardFeasibility(ctx context.Context) (*stageFailure, error) {
	hardStage := beginOptimizationStage(r.emit, StageProveHardFeasibility, string(StageProveHardFeasibility), "")
	defer hardStage.ensureTerminal()
	decision, err := r.decideJoint(ctx, r.compiled.Hard, StageProveHardFeasibility, ObjectiveHardFeasibility, nil, false, nil)
	if err != nil {
		return stageError(ctx, err, StageProveHardFeasibility)
	}
	switch decision.class {
	case evidenceFeasible:
		hardStage.finish("completed", 1, nil, nil, nil, "feasible", "")
		return nil, nil
	case evidenceUnknown:
		solution := r.evidenceUnavailable(StageProveHardFeasibility, nil, r.compiled.Hard, decision.classification)
		hardStage.finish("failed", 1, nil, nil, nil, "unknown", solution.Diagnostics[0].Message)
		return &stageFailure{solution: solution}, nil
	}
	solution, err := r.hardInfeasible(ctx, decision.classification)
	if err != nil {
		return nil, err
	}
	hardStage.finish("failed", 1, nil, nil, nil, "infeasible", solution.Diagnostics[0].Message)
	return &stageFailure{solution: solution}, nil
}

// hardInfeasible reports INFEASIBLE_MODEL backed by a verified certificate,
// adding the existing family diagnostics as explanation only.
func (r *engineRun) hardInfeasible(ctx context.Context, evidence classification) (EngineSolution, error) {
	certificate := r.certificateDiagnostic(evidence)
	explainer := &IntentEngine{solver: r}
	r.at(solveContext{stage: StageProveHardFeasibility, source: "diagnostic"})
	explanation, err := explainer.diagnoseHardModelInfeasibility(ctx, r.compiled, r.options)
	if err != nil {
		if ctx.Err() != nil {
			return EngineSolution{}, err
		}
		explanation = nil
	}
	// The status rests on the certificate; the explanation only words it.
	// The explainer still trusts backend statuses, so only explanations that
	// agree with the certificate replace the generic certificate diagnostic,
	// whose evidence is kept in the evidence bundle (techspec §9.3).
	certified, err := r.certifiedClasses(ctx, evidence.evidence)
	if err != nil {
		return EngineSolution{}, err
	}
	var diagnostics Diagnostics
	for _, diagnostic := range explanation {
		if diagnostic.Status == StatusInfeasibleModel && explanationAgrees(diagnostic, evidence.evidence, certified) {
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	if len(diagnostics) == 0 {
		diagnostics = Diagnostics{certificate}
	}
	solution := EngineSolution{Status: StatusInfeasibleModel, Diagnostics: diagnostics, Evidence: r.finalEvidence(LinearProblem{}, nil)}
	bundle, err := r.infeasibilityBundle(evidence.evidence, StageProveHardFeasibility, nil)
	return r.attachEvidence(solution, "infeasibility-evidence-"+string(StageProveHardFeasibility)+".json", bundle, err), nil
}

// certifiedClasses returns the 0-based Classes whose own subproblem has a
// verified certificate on the external set. The hard decision stops at the
// first infeasible Class; explanations of later Classes need their own.
func (r *engineRun) certifiedClasses(ctx context.Context, evidence *infeasibilityEvidence) (map[int]bool, error) {
	certified := map[int]bool{}
	if evidence == nil || evidence.kind == evidenceKindCVDisjoint {
		return certified, nil
	}
	subs, _, err := splitByClass(r.compiled.Hard, r.compiled.Prepared.Classes)
	if err != nil {
		return certified, nil
	}
	for _, sub := range subs {
		where := solveContext{stage: StageProveHardFeasibility, class: sub.position, classCount: len(subs), source: "diagnostic"}
		result, err := r.classify(ctx, sub.problem, where, ObjectiveDiagnostic)
		if err != nil {
			return nil, err
		}
		if result.class == evidenceInfeasible {
			certified[sub.position-1] = true
		}
	}
	return certified, nil
}

// explanationAgrees reports whether an explanatory diagnostic names a
// conflict that has evidence. A type-C certificate exists only after every
// Class has a witness, so only a global CV explanation agrees with it; with
// Class certificates (type A or B) an explanation must name a certified Class.
func explanationAgrees(diagnostic Diagnostic, evidence *infeasibilityEvidence, certified map[int]bool) bool {
	if evidence == nil {
		return true
	}
	if evidence.kind == evidenceKindCVDisjoint {
		return diagnostic.Code == DiagnosticGlobalCVInfeasible
	}
	if diagnostic.Code == DiagnosticGlobalCVInfeasible {
		return false
	}
	for class := range certified {
		prefix := fmt.Sprintf("class:%04d:", class)
		for _, id := range diagnostic.ConstraintIDs {
			if strings.HasPrefix(id, prefix) {
				return true
			}
		}
	}
	return false
}

// certificateDiagnostic names the rows and bounds that participate in the
// verified infeasibility evidence.
func (r *engineRun) certificateDiagnostic(evidence classification) Diagnostic {
	diagnostic := Diagnostic{
		Code: DiagnosticHardModelInfeasible, Status: StatusInfeasibleModel,
		Message:        "the hard model has no replay-acceptable point (verified certificate); semantic preference optimizations were not applied",
		Representation: RepresentationAtomicBuckets,
	}
	if evidence.evidence == nil {
		return diagnostic
	}
	rows, paths := participatingRows(evidence.evidence)
	diagnostic.ConstraintIDs, diagnostic.SourcePaths = rows, paths
	diagnostic.Causes = []Cause{{Summary: "certificate " + evidence.evidence.kind}}
	return diagnostic
}

// participatingRows lists the semantic rows used by a certificate.
func participatingRows(evidence *infeasibilityEvidence) ([]string, []string) {
	var ids, paths []string
	seen := map[string]bool{}
	add := func(row LinearRow) {
		if seen[string(row.ID)] {
			return
		}
		seen[string(row.ID)] = true
		ids = append(ids, string(row.ID))
		if row.YAMLPath != "" {
			paths = append(paths, row.YAMLPath)
		}
	}
	switch evidence.kind {
	case evidenceKindFarkas:
		for i, multiplier := range evidence.farkas.multipliers {
			if multiplier.Sign() > 0 {
				add(evidence.problem.Rows[evidence.farkas.rows[i].row])
			}
		}
	case evidenceKindEmptyBox:
		if evidence.emptyBox.sourceRow >= 0 {
			add(evidence.problem.Rows[evidence.emptyBox.sourceRow])
		}
	case evidenceKindCVDisjoint:
		add(evidence.cv.cvRow)
		for _, class := range evidence.cv.classes {
			for i, multiplier := range class.multipliers {
				if multiplier.Sign() > 0 {
					add(class.problem.Rows[class.rows[i].row])
				}
			}
		}
	}
	return ids, paths
}

// parameterPrecisionMet is the exact completion test of a bracket width:
// width ≤ 1/10⁸, equality included (techspec §8.1).
func parameterPrecisionMet(width *big.Rat) bool {
	return width.Cmp(requiredParameterPrecision) <= 0
}

// bisectSpec describes one of the three common-parameter searches (§7.2).
type bisectSpec struct {
	stage         OptimizationStageID
	origin        ObjectiveOrigin
	metric        string
	minimize      bool // δ is minimized; both ρ are maximized
	feasibleEnd   float64
	otherEnd      float64
	iterations    int
	build         func(float64) (LinearProblem, error)
	lock          func(best float64) float64
	lockAllowance float64
}

// bisect runs the evidence-only bisection of techspec §7.2: only replayed
// witnesses and verified certificates move the bracket. After every bracket
// update it stops when the theoretical endpoint is feasible, when the exact
// bracket width reaches requiredParameterPrecision (iterations are a cap, not
// a count), or at the first UNKNOWN; completion requires the width to be
// within requiredParameterPrecision.
func (r *engineRun) bisect(ctx context.Context, spec bisectSpec) (LinearProblem, []float64, BisectionReport, *stageFailure, error) {
	direction := "maximize"
	if spec.minimize {
		direction = "minimize"
	}
	report := BisectionReport{Applicable: true, Objective: string(spec.stage), Metric: spec.metric, Direction: direction}
	before := r.counters.clone()
	stage := beginOptimizationStage(r.emit, spec.stage, report.Objective, spec.metric)
	defer stage.ensureTerminal()
	fail := func(failure *stageFailure, err error) (LinearProblem, []float64, BisectionReport, *stageFailure, error) {
		if failure != nil && len(failure.solution.Diagnostics) > 0 {
			stage.finish("failed", report.Probes, nil, nil, nil, "", failure.solution.Diagnostics[0].Message)
		}
		return LinearProblem{}, nil, report, failure, err
	}
	// The probe history, the feasible end's problem and the certificate of
	// the infeasible end feed the PrecisionUnproven evidence file.
	var history []EvidenceProbe
	var lastProblem, bestProblem LinearProblem
	var otherEvidence *infeasibilityEvidence
	probe := func(value float64) (jointDecision, error) {
		problem, err := spec.build(value)
		if err != nil {
			return jointDecision{}, fmt.Errorf("%w: %v", errModelStructure, err)
		}
		lastProblem = problem
		v := value
		decision, err := r.decideJoint(ctx, problem, spec.stage, spec.origin, &v, false, nil)
		if err == nil {
			report.Probes++
			history = append(history, EvidenceProbe{Value: encodeFloat64(value), Result: string(decision.class)})
		}
		return decision, err
	}

	feasible := spec.feasibleEnd
	start, err := probe(feasible)
	if err != nil {
		failure, err := stageError(ctx, err, spec.stage)
		return fail(failure, err)
	}
	switch start.class {
	case evidenceInfeasible:
		return fail(&stageFailure{solution: r.contradiction(spec.stage, "the feasible endpoint transform lost the previous stage's feasibility")}, nil)
	case evidenceUnknown:
		return fail(&stageFailure{solution: r.evidenceUnavailable(spec.stage, &feasible, lastProblem, start.classification)}, nil)
	}
	best, witness, bestProblem := feasible, start.witness, lastProblem
	other, otherBasis, stop := spec.otherEnd, BasisTheoreticalEndpoint, ""
	var lastUnknown classification
	stage.progress(report.Probes, minF(best, other), maxF(best, other), nil, string(start.class))

	end, err := probe(other)
	if err != nil {
		failure, err := stageError(ctx, err, spec.stage)
		return fail(failure, err)
	}
	width := func() *big.Rat {
		if best == other {
			return new(big.Rat)
		}
		return ratAbs(ratSub(ratOf(other), ratOf(best)))
	}
	converged := func() bool { return parameterPrecisionMet(width()) }
	switch end.class {
	case evidenceFeasible:
		best, witness, bestProblem, stop = other, end.witness, lastProblem, StopEndpoint
	case evidenceInfeasible:
		otherBasis, otherEvidence = BasisVerifiedCertificate, end.evidence
	case evidenceUnknown:
		stop, lastUnknown = StopEvidenceUnavailable, end.classification
	}
	if stop != StopEndpoint && converged() {
		stop = StopConverged
	}
	stage.progress(report.Probes, minF(best, other), maxF(best, other), nil, string(end.class))
	for i := 0; stop == "" && i < spec.iterations; i++ {
		mid := best + (other-best)/2
		decision, err := probe(mid)
		if err != nil {
			failure, err := stageError(ctx, err, spec.stage)
			return fail(failure, err)
		}
		switch decision.class {
		case evidenceFeasible:
			best, witness, bestProblem = mid, decision.witness, lastProblem
		case evidenceInfeasible:
			other, otherBasis, otherEvidence = mid, BasisVerifiedCertificate, decision.evidence
		default:
			stop, lastUnknown = StopEvidenceUnavailable, decision.classification
		}
		if stop == "" && converged() {
			stop = StopConverged
		}
		stage.progress(report.Probes, minF(best, other), maxF(best, other), nil, string(decision.class))
	}
	if stop == "" {
		stop = StopBudget
	}
	bestBasis := BasisReplayedWitness
	if stop == StopEndpoint {
		// The theoretical endpoint itself is feasible: the optimum is exact.
		otherBasis = BasisTheoreticalEndpoint
	}
	if spec.minimize {
		report.Lower, report.LowerBasis, report.Upper, report.UpperBasis = minF(best, other), otherBasis, best, bestBasis
		if stop == StopEndpoint {
			report.Lower = best
		}
	} else {
		report.Lower, report.LowerBasis, report.Upper, report.UpperBasis = best, bestBasis, maxF(best, other), otherBasis
		if stop == StopEndpoint {
			report.Upper = best
		}
	}
	widthFloat, _ := width().Float64()
	report.VerifiedBound = widthFloat
	report.VerifiedWithLock = widthFloat + spec.lockAllowance
	report.RequiredPrecision, _ = requiredParameterPrecision.Float64()
	report.StopReason = stop
	if !converged() {
		report.Evidence = r.counters.since(before)
		file := r.numericalEvidence(DiagnosticPrecisionUnproven, spec.stage)
		file.Metric, file.Direction, file.RequiredPrecision = spec.metric, direction, requiredParameterPrecision.RatString()
		file.Budget.Iterations, file.Probes = spec.iterations, history
		file.FeasibleEnd, file.Witness = encodeFloat64(best), encodeValues(witness)
		file.BoundEnd, file.BoundBasis, file.Reasons, file.Counters = encodeFloat64(other), otherBasis, lastUnknown.reasons, report.Evidence
		buildErr := setProblem(&file.FeasibleProblem, &file.FeasibleSHA256, bestProblem)
		if buildErr == nil && otherEvidence != nil {
			bundle, err := r.infeasibilityBundle(otherEvidence, spec.stage, &other)
			file.BoundEvidence, buildErr = &bundle, err
		}
		solution := r.attachEvidence(r.precisionUnproven(spec.stage, report, lastUnknown), numericalEvidenceName(spec.stage), file, buildErr)
		return fail(&stageFailure{solution: solution}, nil)
	}

	fixed := spec.lock(best)
	locked, err := spec.build(fixed)
	if err != nil {
		failure, err := stageError(ctx, fmt.Errorf("%w: %v", errModelStructure, err), spec.stage)
		return fail(failure, err)
	}
	lockedWitness := witness
	if !r.replayAccepts(locked, witness) {
		decision, err := r.decideJoint(ctx, locked, spec.stage, ObjectiveIntentRefinement, &fixed, false, nil)
		if err != nil {
			failure, err := stageError(ctx, err, spec.stage)
			return fail(failure, err)
		}
		switch decision.class {
		case evidenceInfeasible:
			return fail(&stageFailure{solution: r.contradiction(spec.stage, "the tolerance lock is provably infeasible although a feasible witness exists")}, nil)
		case evidenceUnknown:
			return fail(&stageFailure{solution: r.evidenceUnavailable(spec.stage, &fixed, locked, decision.classification)}, nil)
		}
		lockedWitness = decision.witness
	}
	report.FixedValue = fixed
	report.Evidence = r.counters.since(before)
	stage.finish("completed", report.Probes, floatPointer(report.Lower), floatPointer(report.Upper), floatPointer(fixed), "verified", "")
	return locked, lockedWitness, report, nil, nil
}

func (r *engineRun) skippedStage(stage OptimizationStageID, metric, reason string) BisectionReport {
	report := BisectionReport{Applicable: false, InapplicableReason: reason, Objective: string(stage), Metric: metric, Direction: "maximize"}
	lifecycle := beginOptimizationStage(r.emit, stage, report.Objective, metric)
	lifecycle.finish("skipped", 0, nil, nil, floatPointer(0), "", reason)
	return report
}

// selectMinimumCV is §7.3 on the final lock problem.
func (r *engineRun) selectMinimumCV(ctx context.Context, base LinearProblem, witness []float64) ([]float64, CanonicalizationReport, *stageFailure, error) {
	compiled := r.compiled
	report := CanonicalizationReport{
		Objective: string(StageSelectCanonicalBucketProbabilities),
		Metric:    metricCanonicalBucketProbabilities, Direction: "minimize",
		PrimaryVariables: len(compiled.Primary), GapUnit: "second_moment",
	}
	before := r.counters.clone()
	stage := beginOptimizationStage(r.emit, StageSelectCanonicalBucketProbabilities, report.Objective, report.Metric)
	defer stage.ensureTerminal()
	fail := func(solution EngineSolution) ([]float64, CanonicalizationReport, *stageFailure, error) {
		stage.finish("failed", report.Solves, nil, nil, nil, "", solution.Diagnostics[0].Message)
		return nil, report, &stageFailure{solution: solution}, nil
	}
	if err := validateFixedExpectation(compiled, base, r.tau()); err != nil {
		return fail(internalEngineFailure(err.Error(), SolverEvidence{}))
	}
	terms, fixed, err := buildSecondMomentExpression(compiled)
	if err != nil {
		return fail(internalEngineFailure(err.Error(), SolverEvidence{}))
	}
	values := witness
	if len(compiled.Primary) > 0 {
		decision, err := r.decideJoint(ctx, base, StageSelectCanonicalBucketProbabilities, ObjectiveCanonicalBucketProbability, nil, true, witness)
		if err != nil {
			failure, err := stageError(ctx, err, StageSelectCanonicalBucketProbabilities)
			if err != nil {
				return nil, report, nil, err
			}
			return fail(failure.solution)
		}
		stage.tick(1, 1, string(decision.class))
		report.Solves = r.counters.since(before).Solves
		switch decision.class {
		case evidenceInfeasible:
			return fail(r.contradiction(StageSelectCanonicalBucketProbabilities, "minimum CV selection became provably infeasible although a feasible witness exists"))
		case evidenceUnknown:
			return fail(r.evidenceUnavailable(StageSelectCanonicalBucketProbabilities, nil, base, decision.classification))
		}
		if decision.safeLowerS != nil {
			report.SafeLowerBound = ratFloatPointer(decision.safeLowerS)
			report.OptimalityGap = ratFloatPointer(decision.gapS)
		}
		report.RequiredGap = ratFloatPointer(decision.requiredS)
		if !decision.gapMet() {
			report.Evidence = r.counters.since(before)
			return fail(r.minimumCVPrecisionUnproven(decision, base, terms, fixed))
		}
		values = decision.witness
	}
	second, cv, err := minimumCVStatistics(compiled, base, values, terms, fixed, r.tau())
	if err != nil {
		return fail(numericalEngineFailure(err.Error(), SolverEvidence{}))
	}
	report.Method = "per-class-decomposition"
	report.SecondMoment = floatPointer(second)
	report.CV = cv
	report.CVDefined = cv != nil
	report.Evidence = r.counters.since(before)
	stage.finish("completed", report.Solves, nil, nil, nil, "verified", "")
	return values, report, nil, nil
}

// evidenceUnavailable is the NUMERICAL_FAILURE value for "no evidence".
func (r *engineRun) evidenceUnavailable(stage OptimizationStageID, probe *float64, problem LinearProblem, result classification) EngineSolution {
	where := string(stage)
	if probe != nil {
		where += fmt.Sprintf(" at %s", encodeFloat64(*probe))
	}
	causes := []Cause{{Summary: "stage " + string(stage)}}
	for _, reason := range result.reasons {
		causes = append(causes, Cause{Summary: reason})
	}
	diagnostic := Diagnostic{
		Code: DiagnosticEvidenceUnavailable, Status: StatusNumericalFailure,
		Message:        fmt.Sprintf("%s: no replay-accepted witness and no verified certificate (%s). %s", where, result.reasonText(), designerUncertainHint),
		Representation: RepresentationAtomicBuckets, Causes: causes,
	}
	solution := EngineSolution{Status: StatusNumericalFailure, Diagnostics: Diagnostics{diagnostic}, Evidence: r.finalEvidence(LinearProblem{}, nil)}
	file := r.numericalEvidence(DiagnosticEvidenceUnavailable, stage)
	counters := r.counters.clone()
	file.Reasons, file.Counters = result.reasons, &counters
	if probe != nil {
		file.Probe = encodeFloat64(*probe)
	}
	err := setProblem(&file.Problem, &file.ProblemSHA256, problem)
	return r.attachEvidence(solution, numericalEvidenceName(stage), file, err)
}

// precisionUnproven is the NUMERICAL_FAILURE value for a bracket wider than
// the required precision.
func (r *engineRun) precisionUnproven(stage OptimizationStageID, report BisectionReport, unknown classification) EngineSolution {
	diagnostic := Diagnostic{
		Code: DiagnosticPrecisionUnproven, Status: StatusNumericalFailure,
		Message: fmt.Sprintf("%s: verified bracket [%s, %s] (width %s) exceeds the required precision %s; stop reason %s. %s",
			stage, encodeFloat64(report.Lower), encodeFloat64(report.Upper), encodeFloat64(report.VerifiedBound),
			encodeFloat64(report.RequiredPrecision), report.StopReason, designerUncertainHint),
		Representation: RepresentationAtomicBuckets,
		Causes: []Cause{{
			Summary: fmt.Sprintf("stage %s: lower basis %s, upper basis %s, stop reason %s", stage, report.LowerBasis, report.UpperBasis, report.StopReason),
			Metrics: []NamedValue{
				{Name: "lower", Value: report.Lower}, {Name: "upper", Value: report.Upper},
				{Name: "verified_bound", Value: report.VerifiedBound}, {Name: "required_precision", Value: report.RequiredPrecision},
			},
		}},
	}
	for _, reason := range unknown.reasons {
		diagnostic.Causes = append(diagnostic.Causes, Cause{Summary: "last undecided probe: " + reason})
	}
	return EngineSolution{Status: StatusNumericalFailure, Diagnostics: Diagnostics{diagnostic}, Evidence: r.finalEvidence(LinearProblem{}, nil)}
}

func (r *engineRun) minimumCVPrecisionUnproven(decision jointDecision, base LinearProblem, terms []LinearTerm, fixed float64) EngineSolution {
	metrics := []NamedValue{{Name: "candidate_second_moment", Value: ratFloat(decision.candidateS)}, {Name: "required_gap", Value: ratFloat(decision.requiredS)}}
	gap := "unbounded (no reliable lower bound)"
	if decision.gapS != nil {
		metrics = append(metrics, NamedValue{Name: "safe_lower_second_moment", Value: ratFloat(decision.safeLowerS)}, NamedValue{Name: "gap", Value: ratFloat(decision.gapS)})
		gap = encodeFloat64(ratFloat(decision.gapS))
	}
	diagnostic := Diagnostic{
		Code: DiagnosticPrecisionUnproven, Status: StatusNumericalFailure,
		Message: fmt.Sprintf("%s: verified second-moment gap %s exceeds the required %s. %s",
			StageSelectCanonicalBucketProbabilities, gap, encodeFloat64(ratFloat(decision.requiredS)), designerUncertainHint),
		Representation: RepresentationAtomicBuckets,
		Causes:         []Cause{{Summary: "minimum CV optimality gap in second-moment units", Metrics: metrics}},
	}
	solution := EngineSolution{Status: StatusNumericalFailure, Diagnostics: Diagnostics{diagnostic}, Evidence: r.finalEvidence(LinearProblem{}, nil)}
	file := r.numericalEvidence(DiagnosticPrecisionUnproven, StageSelectCanonicalBucketProbabilities)
	file.Metric, file.Direction = metricCanonicalBucketProbabilities, "minimize"
	counters := r.counters.clone()
	file.Counters = &counters
	file.MinimumCV = &MinimumCVEvidence{FixedSecond: encodeFloat64(fixed), SigmaTerms: encodeTerms(terms),
		Witness: encodeValues(decision.witness), CandidateSecond: decision.candidateS.RatString(), RequiredGap: decision.requiredS.RatString()}
	for i, bound := range decision.lowerBounds {
		file.MinimumCV.LowerBounds = append(file.MinimumCV.LowerBounds, ClassBound{Class: i + 1, Direction: bound.direction,
			Multipliers: encodeMultipliers(bound.problem, bound.rows, bound.multipliers), Value: bound.value.String()})
	}
	err := setProblem(&file.Problem, &file.ProblemSHA256, base)
	return r.attachEvidence(solution, numericalEvidenceName(StageSelectCanonicalBucketProbabilities), file, err)
}

// contradiction is INTERNAL_ERROR: verified infeasibility after a witness.
func (r *engineRun) contradiction(stage OptimizationStageID, message string) EngineSolution {
	return internalEngineFailure(fmt.Sprintf("%s: %s", stage, message), r.finalEvidence(LinearProblem{}, nil))
}

// finalEvidence describes the verified result in the existing provenance
// shape; the maximum residuals come from the semantic replay of the result.
func (r *engineRun) finalEvidence(problem LinearProblem, values []float64) SolverEvidence {
	evidence := SolverEvidence{
		Objective: string(StageSelectCanonicalBucketProbabilities),
		Backend:   "class-decomposition+gonum/lp.Simplex+exact-simplex", BackendVersion: gonumBackendVersion,
		BackendCode: "verified", TransformVersion: splitRuleVersion,
		SemanticColumns: len(problem.Variables), Rows: len(problem.Rows),
	}
	if values != nil {
		evidence.MaxRowViolation, evidence.MaxBoundViolation, _ = replaySemanticSolution(problem, values, r.tau())
	}
	return evidence
}

// clone copies the counters (maps included).
func (c EvidenceCounters) clone() EvidenceCounters {
	out := c
	out.WitnessAttempts = copyCounts(c.WitnessAttempts)
	out.WitnessFound = copyCounts(c.WitnessFound)
	return out
}

// since returns the counters accumulated after before.
func (c EvidenceCounters) since(before EvidenceCounters) *EvidenceCounters {
	out := EvidenceCounters{
		Solves: c.Solves - before.Solves, SlowSolves: c.SlowSolves - before.SlowSolves,
		InfeasibilityCertificates:    c.InfeasibilityCertificates - before.InfeasibilityCertificates,
		InfeasibilityCertificateTime: c.InfeasibilityCertificateTime - before.InfeasibilityCertificateTime,
		ExactBounds:                  c.ExactBounds - before.ExactBounds,
		ExactBoundTime:               c.ExactBoundTime - before.ExactBoundTime,
		ExactWitnesses:               c.ExactWitnesses - before.ExactWitnesses,
		ExactWitnessTime:             c.ExactWitnessTime - before.ExactWitnessTime,
		SigmaEndpoints:               c.SigmaEndpoints - before.SigmaEndpoints,
		SigmaEndpointTime:            c.SigmaEndpointTime - before.SigmaEndpointTime,
		BudgetExhausted:              c.BudgetExhausted - before.BudgetExhausted,
		ExactCalls:                   c.ExactCalls - before.ExactCalls,
		MaxExactPivots:               c.MaxExactPivots,
		MaxExactBits:                 c.MaxExactBits,
		WitnessAttempts:              diffCounts(c.WitnessAttempts, before.WitnessAttempts),
		WitnessFound:                 diffCounts(c.WitnessFound, before.WitnessFound),
	}
	return &out
}

func copyCounts(source map[string]int) map[string]int {
	out := make(map[string]int, len(source))
	for k, v := range source {
		out[k] = v
	}
	return out
}

func diffCounts(now, before map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range now {
		if d := v - before[k]; d != 0 {
			out[k] = d
		}
	}
	return out
}

func ratFloat(value *big.Rat) float64 {
	if value == nil {
		return math.NaN()
	}
	f, _ := value.Float64()
	return f
}

func ratFloatPointer(value *big.Rat) *float64 {
	if value == nil {
		return nil
	}
	return floatPointer(ratFloat(value))
}

func minF(a, b float64) float64 { return math.Min(a, b) }
func maxF(a, b float64) float64 { return math.Max(a, b) }
