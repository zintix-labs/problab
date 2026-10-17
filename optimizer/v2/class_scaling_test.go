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

// Class-size scaling measurement (2026-10-17). One process measures one
// (Class count, bucket count) case; problab-p00/tools/class_scaling_driver.py
// runs the grid with a hard time limit per process. The concern is the cost
// of the exact simplex (dense big.Rat tableau) as one Class grows.
//
// Environment: PROBLAB_SCALING_CLASSES and PROBLAB_SCALING_BUCKETS (one
// size), or PROBLAB_SCALING_MIX such as "30x5,10x10,10x20,10x30" (Class
// count x buckets per group); PROBLAB_SCALING_OUT (directory for results).

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scalingClass is one Class with n buckets and moderately rich conditions:
// means spread over the buckets with within-bucket variance, three Main
// Groups with skewed prefer shares (so δ bisects), a bounded Main
// probability, Others, a binding median interval and a fixed mean.
func scalingClass(index, n int, probability float64) PreparedClass {
	means := make([]float64, n)
	seconds := make([]float64, n)
	for j := range means {
		means[j] = float64(j)
		seconds[j] = means[j]*means[j] + 0.25*float64(j%3)
	}
	mainCount := int(math.Ceil(0.7 * float64(n)))
	var groups [][]int
	cuts := []int{0, mainCount / 3, 2 * mainCount / 3, mainCount}
	for g := 0; g < 3; g++ {
		var group []int
		for j := cuts[g]; j < cuts[g+1]; j++ {
			group = append(group, j)
		}
		if len(group) > 0 {
			groups = append(groups, group)
		}
	}
	var others []int
	for j := mainCount; j < n; j++ {
		others = append(others, j)
	}
	class := modelTestIntentClass(index, fmt.Sprintf("class-%03d", index), probability, means, seconds, groups, others)
	shares := []float64{0.5, 0.3, 0.2}
	for g := range class.Groups {
		class.Groups[g].PreferShare = shares[g]
	}
	class.Design.Exp = 0.4 * float64(n-1)
	class.Design.Subjective.MainExperience.Probability = NumericRange{Min: 0.6, Max: 0.95}
	lower, upper := 0.2*float64(n), 0.6*float64(n)
	class.Design.Median = ClosedInterval{lower, upper}
	for j := range class.Buckets {
		bucket := &class.Buckets[j]
		bucket.CDFBeforeLower, bucket.CDFAtUpper = 0, 0
		if bucket.Upper <= lower {
			bucket.CDFBeforeLower = 1
		}
		if bucket.Upper <= upper {
			bucket.CDFAtUpper = 1
		}
	}
	return class
}

type scalingResult struct {
	Mix              string `json:",omitempty"`
	Classes, Buckets int
	Status, Error    string
	Diagnostic       string
	TotalSeconds     float64
	StageSeconds     map[string]float64
	Probes           map[string]int
	Brackets         map[string]string // stage bracket, verified width, stop reason
	Evidence         map[string]*EvidenceCounters
	GonumSolves      int
	MaxGonumSeconds  float64
	SlowSolves       int
	// One Class subproblem of the hard model, timed piece by piece.
	ClassVariables, ClassRows int
	WitnessSeconds            float64
	WitnessSource             string
	IntervalSeconds           float64
	SigmaEndpointSeconds      float64
	OuterBoundSeconds         float64
	ClassExactCalls           int
	ClassMaxPivots            int
	ClassMaxBits              int
	ClassBudgetExhausted      int
	// ρ_main CV-conflict calibration (PROBLAB_SCALING_CONFLICT=1).
	Calibration *scalingCalibration `json:",omitempty"`
	// Probe outcomes per stage (FEASIBLE, INFEASIBLE, UNKNOWN) and the
	// selected point, as a baseline for later engine changes.
	ProbeOutcomes map[string]map[string]int
	PrimarySHA256 string
}

// scalingSizes returns the bucket count of every Class.
func scalingSizes(t *testing.T) (string, []int) {
	if mix := os.Getenv("PROBLAB_SCALING_MIX"); mix != "" {
		var sizes []int
		for _, part := range strings.Split(mix, ",") {
			var count, buckets int
			if _, err := fmt.Sscanf(part, "%dx%d", &count, &buckets); err != nil || count <= 0 || buckets <= 0 {
				t.Fatalf("bad mix part %q", part)
			}
			for i := 0; i < count; i++ {
				sizes = append(sizes, buckets)
			}
		}
		return mix, sizes
	}
	classes, _ := strconv.Atoi(os.Getenv("PROBLAB_SCALING_CLASSES"))
	buckets, _ := strconv.Atoi(os.Getenv("PROBLAB_SCALING_BUCKETS"))
	sizes := make([]int, classes)
	for i := range sizes {
		sizes[i] = buckets
	}
	return "", sizes
}

// scalingConflictClass makes every Class feasible on its own at δ = 0,
// ρ_other = 1 and ρ_main = 1: the fixed mean is the mean of a reference point
// with Main mass 0.9 split by the prefer shares, every Group uniform inside
// and the Others uniform; the median is left unconstrained. Only the global
// CV maximum can then hold ρ_main back.
func scalingConflictClass(class *PreparedClass) {
	const mainMass = 0.9
	mean := 0.0
	for _, group := range class.Groups {
		for _, b := range group.BucketIndexes {
			mean += mainMass * group.PreferShare / float64(len(group.BucketIndexes)) * class.Buckets[b].Mean
		}
	}
	for _, b := range class.Others {
		mean += (1 - mainMass) / float64(len(class.Others)) * class.Buckets[b].Mean
	}
	class.Design.Exp = mean
	class.Design.Median = ClosedInterval{0, 100}
	for j := range class.Buckets {
		class.Buckets[j].CDFBeforeLower, class.Buckets[j].CDFAtUpper = 0, 1
	}
}

// scalingCalibration records how the ρ_main CV-conflict model was built.
type scalingCalibration struct {
	DeltaLock, OtherLock           float64
	ClassesInfeasibleAtRho1        int
	SigmaMinAtRho0, SigmaMinAtRho1 float64
	UpperT, CVMax                  float64
	CalibrationSeconds             float64
}

// scalingConflictModel builds the ρ_main stress case: every Class stays
// feasible on its own for the whole ρ_main range, but the global CV maximum
// lies between Σσ_min at ρ_main = 0 and at ρ_main = 1, so infeasible probes
// can only be proved by disjoint σ intervals (type C) after every Class's
// interval is computed.
func scalingConflictModel(t *testing.T, build func(NumericRange) CompiledModel, result *scalingResult) CompiledModel {
	started := time.Now()
	wide := build(NumericRange{Min: 0, Max: 10})
	solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), wide)
	if err != nil || solution.Status != StatusOptimal {
		t.Fatalf("calibration run: %s %v", solution.Status, err)
	}
	calibration := &scalingCalibration{DeltaLock: solution.MainProfileOptimization.FixedValue, OtherLock: solution.OtherBucketVisibilityOptimization.FixedValue}
	base, err := BuildMainProfileDeviationProblem(wide, calibration.DeltaLock)
	if err == nil && solution.OtherBucketVisibilityOptimization.Applicable {
		base, err = AddOtherBucketVisibilityRows(base, wide, calibration.OtherLock)
	}
	if err != nil {
		t.Fatal(err)
	}
	run := NewIntentEngine(NewGonumSolver()).newRun(wide, nil)
	sigmaMin := func(rho float64) (float64, int) {
		problem, err := AddMainGroupInternalVisibilityRows(base, wide, rho)
		if err != nil {
			t.Fatal(err)
		}
		subs, _, err := splitByClass(problem, wide.Prepared.Classes)
		if err != nil {
			t.Fatal(err)
		}
		sigma := run.sigmaOf(problem)
		total, infeasible := 0.0, 0
		for _, sub := range subs {
			where := solveContext{stage: StageMaximizeMainGroupInternalVisibility, class: sub.position, classCount: len(subs)}
			witness, err := run.classify(context.Background(), sub.problem, where, ObjectiveMainGroupInternalVisibilityProbe)
			if err != nil {
				t.Fatal(err)
			}
			if witness.class != evidenceFeasible {
				infeasible++
				continue
			}
			interval, err := run.classInterval(context.Background(), sub, sigma, witness.witness, where)
			if err != nil {
				t.Fatal(err)
			}
			value, _ := interval.loIn.Float64()
			total += value
		}
		return total, infeasible
	}
	calibration.SigmaMinAtRho0, _ = sigmaMin(0)
	calibration.SigmaMinAtRho1, calibration.ClassesInfeasibleAtRho1 = sigmaMin(1)
	calibration.UpperT = calibration.SigmaMinAtRho0 + 0.5*(calibration.SigmaMinAtRho1-calibration.SigmaMinAtRho0)
	_, fixed, err := buildSecondMomentExpression(wide)
	if err != nil {
		t.Fatal(err)
	}
	mu := wide.Prepared.ExpectedRTP()
	calibration.CVMax = math.Sqrt((fixed+calibration.UpperT)/(mu*mu) - 1)
	calibration.CalibrationSeconds = time.Since(started).Seconds()
	result.Calibration = calibration
	t.Logf("calibration: δ lock %g, ρ_other lock %g, Classes infeasible at ρ_main=1: %d, Σσ_min %g → %g, CV max %g (%.1fs)",
		calibration.DeltaLock, calibration.OtherLock, calibration.ClassesInfeasibleAtRho1, calibration.SigmaMinAtRho0, calibration.SigmaMinAtRho1, calibration.CVMax, calibration.CalibrationSeconds)
	return build(NumericRange{Min: 0, Max: calibration.CVMax})
}

// sigmaRangeByClass is the achievable range of T = Σσ_k: only the CV rows
// couple Classes, so it is the sum of each Class's σ range (exact simplex
// endpoints, no joint Gonum LP).
func sigmaRangeByClass(t *testing.T, compiled CompiledModel) (float64, float64) {
	run := NewIntentEngine(NewGonumSolver()).newRun(compiled, nil)
	subs, _, err := splitByClass(compiled.Hard, compiled.Prepared.Classes)
	if err != nil {
		t.Fatal(err)
	}
	sigma := run.sigmaOf(compiled.Hard)
	lo, hi := 0.0, 0.0
	for _, sub := range subs {
		where := solveContext{stage: StageProveHardFeasibility, class: sub.position, classCount: len(subs)}
		witness, err := run.classify(context.Background(), sub.problem, where, ObjectiveHardFeasibility)
		if err != nil || witness.class != evidenceFeasible {
			t.Fatalf("class %s: %s %v", sub.classID, witness.class, err)
		}
		interval, err := run.classInterval(context.Background(), sub, sigma, witness.witness, where)
		if err != nil {
			t.Fatal(err)
		}
		l, _ := interval.loIn.Float64()
		h, _ := interval.hiIn.Float64()
		lo, hi = lo+l, hi+h
	}
	return lo, hi
}

func TestClassScalingWorker(t *testing.T) {
	if os.Getenv("PROBLAB_SCALING_CLASSES") == "" && os.Getenv("PROBLAB_SCALING_MIX") == "" {
		t.Skip("class scaling worker: run through problab-p00/tools/class_scaling_driver.py")
	}
	mix, sizes := scalingSizes(t)
	classCount, buckets := len(sizes), sizes[0]
	result := scalingResult{Mix: mix, Classes: classCount, Buckets: buckets, StageSeconds: map[string]float64{}, Probes: map[string]int{}, Brackets: map[string]string{},
		Evidence: map[string]*EvidenceCounters{}, ProbeOutcomes: map[string]map[string]int{}}
	conflict := os.Getenv("PROBLAB_SCALING_CONFLICT") == "1"
	build := func(cv NumericRange) CompiledModel {
		classes := make([]PreparedClass, classCount)
		for i := range classes {
			classes[i] = scalingClass(i, sizes[i], 1/float64(classCount))
			if conflict {
				scalingConflictClass(&classes[i])
			}
		}
		return evidenceCompile(t, cv, classes...)
	}
	// Wrap the Classes in a binding CV minimum: the middle of the achievable
	// second-moment range of the wide model.
	wide := build(NumericRange{Min: 0, Max: 10})
	lo, hi := sigmaRangeByClass(t, wide)
	mu := wide.Prepared.ExpectedRTP()
	_, fixed, err := buildSecondMomentExpression(wide)
	if err != nil {
		t.Fatal(err)
	}
	target := fixed + lo + 0.5*(hi-lo)
	compiled := build(NumericRange{Min: math.Sqrt(target/(mu*mu) - 1), Max: 10})
	if os.Getenv("PROBLAB_SCALING_CONFLICT") == "1" {
		compiled = scalingConflictModel(t, build, &result)
	}

	// Single Class subproblem of the hard model.
	run := NewIntentEngine(NewGonumSolver()).newRun(compiled, nil)
	subs, _, err := splitByClass(compiled.Hard, compiled.Prepared.Classes)
	if err != nil {
		t.Fatal(err)
	}
	sub := subs[0]
	result.ClassVariables, result.ClassRows = len(sub.problem.Variables), len(sub.problem.Rows)
	where := solveContext{stage: StageProveHardFeasibility, class: 1, classCount: len(subs)}
	started := time.Now()
	witness, err := run.classify(context.Background(), sub.problem, where, ObjectiveHardFeasibility)
	result.WitnessSeconds, result.WitnessSource = time.Since(started).Seconds(), witness.source
	if err != nil || witness.class != evidenceFeasible {
		result.Error = fmt.Sprintf("class witness: %s %v %s", witness.class, err, witness.reasonText())
	} else {
		started = time.Now()
		if _, err := run.classInterval(context.Background(), sub, run.sigmaOf(compiled.Hard), witness.witness, where); err != nil {
			result.Error = "class interval: " + err.Error()
		}
		result.IntervalSeconds = time.Since(started).Seconds()
	}
	result.SigmaEndpointSeconds, result.OuterBoundSeconds = run.counters.SigmaEndpointTime.Seconds(), run.counters.ExactBoundTime.Seconds()
	result.ClassExactCalls, result.ClassMaxPivots, result.ClassMaxBits = run.counters.ExactCalls, run.counters.MaxExactPivots, run.counters.MaxExactBits
	result.ClassBudgetExhausted = run.counters.BudgetExhausted

	// The complete five-stage engine run.
	engine := NewIntentEngine(NewGonumSolver())
	engine.evidenceDirectory = t.TempDir()
	started = time.Now()
	solution, err := engine.SolveObserved(context.Background(), compiled, func(event OptimizationStageEvent) {
		switch event.State {
		case "completed", "failed", "skipped":
			result.StageSeconds[string(event.Stage)] = event.Duration.Seconds()
		case SolveEventFinished:
			result.GonumSolves++
			result.MaxGonumSeconds = math.Max(result.MaxGonumSeconds, event.Duration.Seconds())
		case SolveEventSlow:
			result.SlowSolves++
		case "progress":
			if event.Status == "FEASIBLE" || event.Status == "INFEASIBLE" || event.Status == "UNKNOWN" {
				if result.ProbeOutcomes[string(event.Stage)] == nil {
					result.ProbeOutcomes[string(event.Stage)] = map[string]int{}
				}
				result.ProbeOutcomes[string(event.Stage)][event.Status]++
			}
		}
	})
	result.TotalSeconds = time.Since(started).Seconds()
	if err != nil {
		result.Error = err.Error()
	} else {
		result.Status = string(solution.Status)
		if len(solution.Diagnostics) > 0 {
			result.Diagnostic = string(solution.Diagnostics[0].Code) + ": " + solution.Diagnostics[0].Message
		}
		for name, report := range map[string]BisectionReport{"delta": solution.MainProfileOptimization,
			"rho_other": solution.OtherBucketVisibilityOptimization, "rho_main": solution.MainGroupInternalVisibilityOptimization} {
			result.Probes[name] = report.Probes
			result.Brackets[name] = fmt.Sprintf("[%s, %s] width %.3g %s", encodeFloat64(report.Lower), encodeFloat64(report.Upper), report.VerifiedBound, report.StopReason)
			if report.Evidence != nil {
				result.Evidence[name] = report.Evidence
			}
		}
		if solution.CanonicalBucketProbabilitySelection.Evidence != nil {
			result.Evidence["min_cv"] = solution.CanonicalBucketProbabilitySelection.Evidence
		}
		if solution.Status == StatusOptimal {
			bits := make([]byte, 0, 8*len(solution.Primary))
			for _, v := range solution.Primary {
				bits = binary.LittleEndian.AppendUint64(bits, math.Float64bits(v))
			}
			result.PrimarySHA256 = fmt.Sprintf("%x", sha256.Sum256(bits))
		}
	}
	raw, _ := json.MarshalIndent(result, "", "  ")
	out := os.Getenv("PROBLAB_SCALING_OUT")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("c%d-b%d", classCount, buckets)
	if mix != "" {
		name = "mix-" + strings.ReplaceAll(mix, ",", "_")
	}
	if result.Calibration != nil {
		name += "-conflict"
	}
	if err := os.WriteFile(filepath.Join(out, name+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("classes=%d buckets=%d status=%s total=%.2fs class witness=%.3fs interval=%.3fs pivots=%d bits=%d", classCount, buckets, result.Status, result.TotalSeconds, result.WitnessSeconds, result.IntervalSeconds, result.ClassMaxPivots, result.ClassMaxBits)
}
