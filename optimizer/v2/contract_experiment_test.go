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

// Contract experiment worker (2026-10-17, round 2). One process runs exactly
// one case with one candidate mode; the external driver
// (problab-p00/tools/contract_driver.py) owns the hard time limit, collects a
// goroutine dump on timeout, kills the process group and records the real
// exit code. Nothing here abandons a running Gonum solve.
//
// Environment: PROBLAB_CONTRACT_CASE, PROBLAB_CONTRACT_MODE,
// PROBLAB_CONTRACT_OUT, optional PROBLAB_CONTRACT_CANCEL_AFTER (duration),
// PROBLAB_CONTRACT_LP (saved slow-solve record, L cases only).

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/spec"
)

type contractResult struct {
	Case, Mode, Variant string
	Status, Diagnostic  string
	Causes              []string
	Error               string
	ElapsedSeconds      float64
	ReturnAfterCancel   string // cancel variant: time from deadline to return
	SolvesAfterCancel   int
	Stages              map[string]string
	StageSeconds        map[string]float64
	SlowSolves          int
	Evidence            map[string]*EvidenceCounters
	MinCVGap            string
	// Residuals of the final point against Hard, in units of τ, and the
	// verdict of each acceptance set.
	RowResidualTau   float64
	BoundViolation   float64
	InternalAccepted bool // rows τ/3, bounds exact
	ExternalAccepted bool // replay(τ)
	NormalizedTau    float64
	SumMinusOne      string
	RTPPre, RTPPost  string
	TargetRTP        string
	Artifact         string // "pass", "fail: …", "n/a (synthetic wins)"
	PrimarySHA256    string // IEEE-754 bits of the selected primary vector
	SecondMoment     float64
	PipelineStatus   string
	LPResult         string
}

func TestContractWorker(t *testing.T) {
	name := os.Getenv("PROBLAB_CONTRACT_CASE")
	if name == "" {
		t.Skip("contract experiment worker: run through problab-p00/tools/contract_driver.py")
	}
	// The compared modes of round 2 are recorded in problab-p00; the
	// engine now has one contract, so the mode only labels the run.
	modeName := os.Getenv("PROBLAB_CONTRACT_MODE")
	out := os.Getenv("PROBLAB_CONTRACT_OUT")
	result := contractResult{Case: name, Mode: modeName, Stages: map[string]string{}, StageSeconds: map[string]float64{}, Evidence: map[string]*EvidenceCounters{}}
	tag := name + "-" + modeName
	ctx := context.Background()
	var deadline time.Time
	if after := os.Getenv("PROBLAB_CONTRACT_CANCEL_AFTER"); after != "" {
		d, err := time.ParseDuration(after)
		if err != nil {
			t.Fatal(err)
		}
		result.Variant = "cancel-after-" + after
		tag += "-" + result.Variant
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
		deadline, _ = ctx.Deadline()
	}
	logDir := filepath.Join(out, "lp-log", tag)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	solver := &loggingSolver{inner: NewGonumSolver(), dir: logDir, deadline: deadline}
	started := time.Now()
	switch {
	case strings.HasPrefix(name, "L"):
		result.LPResult = contractStuckLP(t, name, solver)
	case name == "A":
		contractRunPipeline(t, ctx, solver, &result)
	default:
		compiled := contractCompiled(t, name)
		engine := NewIntentEngine(solver)
		engine.evidenceDirectory = filepath.Join(logDir, "slow-solves")
		solution, err := engine.SolveObserved(ctx, compiled, contractStageObserver(&result))
		if err != nil {
			result.Error = err.Error()
		} else {
			contractRecord(compiled, solution, &result)
			if name == "B" {
				contractArtifact(compiled, solution, &result)
			} else {
				result.Artifact = "n/a (synthetic wins)"
			}
		}
	}
	result.ElapsedSeconds = time.Since(started).Seconds()
	if !deadline.IsZero() {
		result.ReturnAfterCancel = time.Since(deadline).String()
		result.SolvesAfterCancel = solver.afterDeadline
	}
	raw, _ := json.MarshalIndent(result, "", "  ")
	if err := os.MkdirAll(filepath.Join(out, "rows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "rows", tag+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: status=%s diagnostic=%q error=%q elapsed=%.2fs", tag, result.Status, result.Diagnostic, result.Error, result.ElapsedSeconds)
}

// loggingSolver writes each LP before the backend starts (atomic overwrite of
// current-lp.json plus an appended, closed log line) so a killed process still
// shows where it stopped. It calls the backend on the caller's goroutine.
type loggingSolver struct {
	inner         Solver
	dir           string
	deadline      time.Time
	count         int
	afterDeadline int
}

func (s *loggingSolver) Solve(ctx context.Context, p LinearProblem, o LinearObjective, opt SolveOptions) (SolveResult, error) {
	s.count++
	if !s.deadline.IsZero() && time.Now().After(s.deadline) {
		s.afterDeadline++
	}
	record, _ := json.Marshal(map[string]any{"index": s.count, "problem": SnapshotProblem(p), "objective": SnapshotObjective(o),
		"options": map[string]string{"feasibility_tolerance": encodeFloat64(opt.FeasibilityTolerance), "optimality_tolerance": encodeFloat64(opt.OptimalityTolerance)}})
	_, _ = writeFileAtomic(s.dir, "current-lp.json", record)
	appendLine(filepath.Join(s.dir, "log.txt"), fmt.Sprintf("%s start #%d %s vars=%d rows=%d", time.Now().Format(time.RFC3339Nano), s.count, o.Name, len(p.Variables), len(p.Rows)))
	started := time.Now()
	result, err := s.inner.Solve(ctx, p, o, opt)
	appendLine(filepath.Join(s.dir, "log.txt"), fmt.Sprintf("%s end #%d status=%s err=%v elapsed=%s", time.Now().Format(time.RFC3339Nano), s.count, solveStatusName(result.Status), err, time.Since(started)))
	return result, err
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line + "\n")
	_ = f.Sync()
	_ = f.Close()
}

func contractStageObserver(result *contractResult) OptimizationStageObserver {
	return func(event OptimizationStageEvent) {
		switch event.State {
		case "completed", "failed", "skipped":
			result.StageSeconds[string(event.Stage)] = event.Duration.Seconds()
		case SolveEventSlow:
			result.SlowSolves++
		}
	}
}

func contractCompiled(t *testing.T, name string) CompiledModel {
	gaps := map[string]float64{"F": 0.5e-9, "F1": 1e-9, "F2": 2e-9, "F3": 3e-9, "F5": 5e-9, "F10": 1e-8}
	if gap, ok := gaps[name]; ok {
		return contractToleranceGap(t, gap)
	}
	switch name {
	case "B":
		return contractClassWeightBase(t)
	case "C":
		return modelTestCompile(t, jointFairnessPreparedProblem())
	case "D":
		return evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceSkewedClass(0, 1))
	case "E":
		return contractCVBinding(t)
	case "G": // the 450-bucket fixture (100 Classes)
		return minimumCV450BucketsFixture(t)
	case "H": // 450 buckets where δ* = 0.4, so the profile stage really bisects
		classes := make([]PreparedClass, 100)
		for i := range classes {
			classes[i] = evidenceSkewedClass(i, .01)
		}
		return evidenceCompile(t, NumericRange{Min: 0, Max: 10}, classes...)
	}
	t.Fatalf("unknown case %q", name)
	return CompiledModel{}
}

func contractRecord(compiled CompiledModel, solution EngineSolution, result *contractResult) {
	result.Status = string(solution.Status)
	if len(solution.Diagnostics) > 0 {
		result.Diagnostic = string(solution.Diagnostics[0].Code) + ": " + solution.Diagnostics[0].Message
		for _, cause := range solution.Diagnostics[0].Causes {
			result.Causes = append(result.Causes, cause.Summary)
		}
	}
	for stage, report := range map[string]BisectionReport{"delta": solution.MainProfileOptimization,
		"rho_other": solution.OtherBucketVisibilityOptimization, "rho_main": solution.MainGroupInternalVisibilityOptimization} {
		switch {
		case report.StopReason != "":
			result.Stages[stage] = fmt.Sprintf("[%s, %s] w=%.3g %s/%s %s probes=%d", encodeFloat64(report.Lower), encodeFloat64(report.Upper), report.VerifiedBound, report.LowerBasis, report.UpperBasis, report.StopReason, report.Probes)
		case !report.Applicable && report.InapplicableReason != "":
			result.Stages[stage] = "skipped"
		}
		if report.Evidence != nil {
			result.Evidence[stage] = report.Evidence
		}
	}
	canonical := solution.CanonicalBucketProbabilitySelection
	if canonical.Evidence != nil {
		result.Evidence["min_cv"] = canonical.Evidence
	}
	if canonical.OptimalityGap != nil && canonical.RequiredGap != nil {
		result.MinCVGap = fmt.Sprintf("%.3g (req %.3g)", *canonical.OptimalityGap, *canonical.RequiredGap)
	}
	if solution.Status != StatusOptimal {
		return
	}
	bits := make([]byte, 0, 8*len(solution.Primary))
	for _, v := range solution.Primary {
		bits = binary.LittleEndian.AppendUint64(bits, math.Float64bits(v))
	}
	result.PrimarySHA256 = fmt.Sprintf("%x", sha256.Sum256(bits))
	if canonical.SecondMoment != nil {
		result.SecondMoment = *canonical.SecondMoment
	}
	tau := compiled.Prepared.Plan.EngineOptions.FeasibilityTolerance
	result.RowResidualTau = maxRelativeResidual(compiled.Hard, solution.Primary) / tau
	for j, v := range compiled.Hard.Variables {
		x := solution.Primary[j]
		result.BoundViolation = math.Max(result.BoundViolation, v.Lower-x)
		if !math.IsInf(v.Upper, 1) {
			result.BoundViolation = math.Max(result.BoundViolation, x-v.Upper)
		}
	}
	result.InternalAccepted = replayWithRule(compiled.Hard, solution.Primary, internalRule(tau))
	_, _, result.ExternalAccepted = replaySemanticSolution(compiled.Hard, solution.Primary, tau)
	sum := 0.0
	for _, class := range compiled.Prepared.Classes {
		if !class.Intent {
			sum += class.Probability
		}
	}
	for _, primary := range compiled.Primary {
		sum += compiled.Prepared.Classes[primary.ClassIndex].Probability * solution.Primary[compiled.VariableIndex[primary.ID]]
	}
	normalized := make([]float64, len(solution.Primary))
	for i, v := range solution.Primary {
		normalized[i] = math.Max(0, v) / sum
	}
	result.NormalizedTau = maxRelativeResidual(compiled.Hard, normalized) / tau
	result.SumMinusOne = fmt.Sprintf("%.3g", sum-1)
}

// contractArtifact runs the real expansion, materialization and full
// artifact verification (models with real sample wins only).
func contractArtifact(compiled CompiledModel, solution EngineSolution, result *contractResult) {
	if solution.Status != StatusOptimal {
		return
	}
	expanded, err := ExpandSolution(compiled, solution, compiled.Prepared.BetMode, compiled.Prepared.BetUnit)
	if err != nil {
		result.Artifact = "fail: expand: " + err.Error()
		return
	}
	var total, rtp compensatedSum
	for _, s := range expanded {
		total.Add(s.Probability)
		rtp.Add(s.Probability * s.Win)
	}
	result.SumMinusOne = fmt.Sprintf("%.17g", total.Value()-1)
	result.RTPPre = fmt.Sprintf("%.17g", rtp.Value())
	result.RTPPost = fmt.Sprintf("%.17g", rtp.Value()/total.Value())
	result.TargetRTP = fmt.Sprintf("%.17g", compiled.Prepared.ExpectedRTP())
	materialized, err := materializeModeWithNormalizationTolerance(compiled.Prepared.BetMode, compiled.Prepared.BetUnit, expanded, compiled.Prepared.Plan.EngineOptions.FeasibilityTolerance)
	if err != nil {
		result.Artifact = "fail: materialize: " + err.Error()
		return
	}
	report := VerifyMaterialized(compiled, solution, materialized)
	if report.Pass {
		result.Artifact = "pass"
		return
	}
	var failed []string
	for _, check := range report.Checks {
		if !check.Pass {
			failed = append(failed, fmt.Sprintf("%s (%v vs %v)", check.Name, check.Actual, check.Required))
		}
	}
	result.Artifact = "fail: " + strings.Join(failed, "; ")
}

// contractStuckLP re-solves one saved slow LP: "L…-gonum" with the
// production backend (the driver's time limit applies), "L…-exact" with the
// budgeted exact simplex on the internal rule's inner model of the same LP.
func contractStuckLP(t *testing.T, name string, solver *loggingSolver) string {
	raw, err := os.ReadFile(os.Getenv("PROBLAB_CONTRACT_LP"))
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
	optimality, err := decodeFloat64(record.Options["optimality_tolerance"])
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if strings.HasSuffix(name, "-gonum") {
		result, err := solver.Solve(context.Background(), problem, objective, SolveOptions{FeasibilityTolerance: tau, OptimalityTolerance: optimality})
		return fmt.Sprintf("gonum status=%s err=%v objective=%.17g elapsed=%s", solveStatusName(result.Status), err, result.ObjectiveValue, time.Since(started))
	}
	models, err := buildCandidateModels(problem, internalRule(tau))
	if err != nil {
		return "exact: " + err.Error()
	}
	index := map[VariableID]int{}
	for j, v := range problem.Variables {
		index[v.ID] = j
	}
	c := map[int]*big.Rat{}
	for _, term := range objective.Terms {
		a := ratOf(term.Coeff)
		if objective.Sense == Minimize {
			a = ratNeg(a)
		}
		c[index[term.Variable]] = a
	}
	usage := &EvidenceCounters{}
	budget := defaultExactBudget
	budget.usage = usage
	point, status := exactOptimum(context.Background(), models[0], c, budget)
	value, replayed := math.NaN(), false
	if point != nil {
		values := ratsToFloats(point)
		replayed = replayWithRule(problem, values, internalRule(tau))
		value = 0
		for _, term := range objective.Terms {
			value += term.Coeff * values[index[term.Variable]]
		}
	}
	return fmt.Sprintf("exact status=%s point=%v objective=%.17g internal-replay=%v pivots=%d bits=%d elapsed=%s",
		status, point != nil, value, replayed, usage.MaxExactPivots, usage.MaxExactBits, time.Since(started))
}

// contractRunPipeline runs the complete Tuner pipeline of
// TestTunerRunExecutesTheCompleteProductionPipeline.
func contractRunPipeline(t *testing.T, ctx context.Context, solver *loggingSolver, result *contractResult) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	seed := Int64Seed(4127483647)
	const samples uint64 = 256
	collection := CollectionOptions{Workers: 2, BatchSize: 64, MaxSpins: samples}
	fixture := collectionFixturePlan(0, collection.Workers, samples, collection.MaxSpins, collection.BatchSize)
	fixture.Plan.Seed = seed.clone()
	collected, _, err := NewCollector(lab).Collect(context.Background(), fixture, 0)
	if err != nil {
		t.Fatal(err)
	}
	minimum, maximum, mean, second := math.Inf(1), math.Inf(-1), 0.0, 0.0
	for _, s := range collected.Classes[0].Samples {
		minimum, maximum = math.Min(minimum, s.Win), math.Max(maximum, s.Win)
		mean += s.Win
		second += s.Win * s.Win
	}
	mean /= float64(samples)
	second /= float64(samples)
	cv := math.Sqrt(math.Max(0, second-mean*mean)) / mean
	midpoint := minimum + (maximum-minimum)/2
	enabled := true
	intent := MathIntent{
		Overall: OverallIntent{CV: NumericRange{Min: 0, Max: cv + 1}},
		Classes: []ClassIntent{{Name: "all_outcomes", Weight: ClassWeightBase,
			Collect: CollectIntent{Samples: samples, WinRange: ClosedInterval{minimum, maximum}},
			Design: ClassDesign{Exp: mean, Median: ClosedInterval{minimum, maximum},
				Subjective: SubjectiveIntent{Intent: &enabled, Buckets: []float64{minimum, midpoint, maximum},
					MainExperience: &MainExperience{Groups: []ClosedInterval{{minimum, maximum}}, Probability: NumericRange{Min: 1, Max: 1}, Prefer: []float64{1}}}}}},
	}
	options := DefaultEngineOptions()
	options.ProfileBisectionIterations, options.OtherVisibilityBisectionIterations, options.MainGroupInternalVisibilityBisectionIterations = 30, 30, 30
	planID := "complete-pipeline"
	config := Config{Version: ConfigVersion,
		Plans: []RunPlan{{ID: planID, Target: Target{Game: spec.GID(1), BetModes: []int{0}}, Engine: EngineIntentLPV2, Intent: planID, Seed: seed.clone(),
			Collection: collection, CandidateSelection: CandidateSelectionOptions{Evaluator: "none", MaxCandidates: 1},
			Output: OutputOptions{Format: []OutputFormat{OutputOptimalGacha, OutputOptimalArtifactV1}, Directory: t.TempDir()}}},
		Intents: map[string]MathIntent{planID: intent}, EngineOptions: options}
	tuner, err := NewTuner(config, lab)
	if err != nil {
		t.Fatal(err)
	}
	tuner.engine.solver = solver
	tuner.engine.evidenceDirectory = filepath.Join(solver.dir, "slow-solves")
	tuner.engine.observeResult = func(compiled CompiledModel, solution EngineSolution) {
		contractRecord(compiled, solution, result)
		contractArtifact(compiled, solution, result)
	}
	run, err := tuner.Run(ctx, RunRequest{PlanID: planID})
	if err != nil {
		result.Error = err.Error()
		return
	}
	result.PipelineStatus = string(run.Status)
	if run.Report.Verification.Pass {
		result.PipelineStatus += " (verification pass)"
	}
}

func contractClassWeightBase(t *testing.T) CompiledModel {
	t.Helper()
	config, err := ParseConfig([]byte(validConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	intent := MathIntent{Overall: OverallIntent{CV: NumericRange{Min: math.Sqrt(3/(1.3*1.3) - 1), Max: math.Sqrt(3/(1.3*1.3) - 1)}}}
	for i, weight := range []int{700000, 300000} {
		mean := float64(i + 1)
		intent.Classes = append(intent.Classes, ClassIntent{
			Name: fmt.Sprintf("class-%d", i), Weight: weight,
			Collect: CollectIntent{Samples: 3, WinRange: ClosedInterval{0, 2 * mean}},
			Design: ClassDesign{Exp: mean, Median: ClosedInterval{0, 2 * mean},
				Subjective: SubjectiveIntent{Intent: &enabled, Buckets: []float64{0, 0.5 * mean, 1.5 * mean, 2 * mean},
					MainExperience: &MainExperience{Groups: []ClosedInterval{{0, 2 * mean}}, Probability: NumericRange{Min: 1, Max: 1}, Prefer: []float64{1}}},
			},
		})
	}
	base := ClassWeightDenominator(1000000)
	intent.Overall.ClassWeightBase = &base
	config.Intents["high-win-v2"] = intent
	plan, err := config.ResolvePlan("demo-high-win-v2")
	if err != nil {
		t.Fatal(err)
	}
	prepared := PreparedProblem{Plan: plan, BetMode: 0, BetUnit: 1}
	for i, classIntent := range plan.Intent.Classes {
		var samples []CollectedSample
		for j := 0; j < 3; j++ {
			samples = append(samples, CollectedSample{ClassID: classIntent.Name, Win: float64(j) * classIntent.Design.Exp, Snapshot: []byte{byte(i*3 + j)}, Sequence: uint64(j)})
		}
		class, diagnostic, err := prepareClass(plan, i, classIntent, samples)
		if err != nil || diagnostic.StopsRun() {
			t.Fatalf("prepare: %v %v", diagnostic, err)
		}
		prepared.Classes = append(prepared.Classes, class)
	}
	compiled, diagnostics, err := CompileHardModel(prepared)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("compile: %v %v", diagnostics, err)
	}
	return compiled
}

func contractCVBinding(t *testing.T) CompiledModel {
	classes := []PreparedClass{evidenceFixtureClass(0, 0.5), evidenceSkewedClass(1, 0.5)}
	wide := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, classes...)
	lo, hi := jointSigmaRange(t, wide)
	mu := wide.Prepared.ExpectedRTP()
	return evidenceCompile(t, NumericRange{Min: math.Sqrt((lo+0.9*(hi-lo))/(mu*mu) - 1), Max: 10}, classes...)
}

// contractToleranceGap puts the CV maximum the given relative gap below the
// exact minimum second moment, so only replay tolerance can make it feasible.
func contractToleranceGap(t *testing.T, gap float64) CompiledModel {
	classes := []PreparedClass{evidenceFixtureClass(0, 0.5), evidenceFixtureClass(1, 0.3), evidenceFixtureClass(2, 0.2)}
	wide := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, classes...)
	lo := exactMinimumT(t, wide)
	mu := wide.Prepared.ExpectedRTP()
	return evidenceCompile(t, NumericRange{Min: 0, Max: math.Sqrt(lo*(1-gap)/(mu*mu) - 1)}, classes...)
}

// exactMinimumT is the exact minimum of the CV-row expression over the
// unrelaxed hard model (exact simplex, no tolerance).
func exactMinimumT(t *testing.T, compiled CompiledModel) float64 {
	t.Helper()
	row := modelTestRow(t, compiled.Hard, cvMinRowID)
	withoutCV := cloneLinearProblem(compiled.Hard)
	withoutCV.Rows = nil
	for _, r := range compiled.Hard.Rows {
		if r.ID != cvMinRowID && r.ID != cvMaxRowID {
			withoutCV.Rows = append(withoutCV.Rows, r)
		}
	}
	models, err := buildCandidateModels(withoutCV, productRule(1e-9))
	if err != nil || len(models) < 2 {
		t.Fatalf("exact model: %v", err)
	}
	unrelaxed := models[1]
	c := map[int]*big.Rat{}
	index := map[VariableID]int{}
	for j, v := range withoutCV.Variables {
		index[v.ID] = j
	}
	for _, term := range row.Terms {
		c[index[term.Variable]] = ratOf(term.Coeff)
	}
	result := acquireLowerBound(context.Background(), unrelaxed, c, defaultExactBudget)
	if !result.check.valid {
		t.Fatalf("exact minimum: %s", result.check.reason)
	}
	value, _ := result.check.value.Float64()
	return value
}

// maxRelativeResidual is the largest row violation divided by its replay
// scale max(1,|lhs|,|b|), so it compares directly with τ.
func maxRelativeResidual(problem LinearProblem, values []float64) float64 {
	index := make(map[VariableID]int, len(problem.Variables))
	for i, v := range problem.Variables {
		index[v.ID] = i
	}
	worst := 0.0
	for _, row := range problem.Rows {
		lhs := 0.0
		for _, term := range row.Terms {
			lhs += term.Coeff * values[index[term.Variable]]
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
		worst = math.Max(worst, violation/math.Max(1, math.Max(math.Abs(lhs), math.Abs(row.RHS))))
	}
	return worst
}
