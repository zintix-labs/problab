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
// Environment: PROBLAB_SCALING_CLASSES, PROBLAB_SCALING_BUCKETS,
// PROBLAB_SCALING_OUT (directory for the JSON result).

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
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
	Classes, Buckets int
	Status, Error    string
	Diagnostic       string
	TotalSeconds     float64
	StageSeconds     map[string]float64
	Probes           map[string]int
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
}

func TestClassScalingWorker(t *testing.T) {
	classesText := os.Getenv("PROBLAB_SCALING_CLASSES")
	if classesText == "" {
		t.Skip("class scaling worker: run through problab-p00/tools/class_scaling_driver.py")
	}
	classCount, _ := strconv.Atoi(classesText)
	buckets, _ := strconv.Atoi(os.Getenv("PROBLAB_SCALING_BUCKETS"))
	result := scalingResult{Classes: classCount, Buckets: buckets, StageSeconds: map[string]float64{}, Probes: map[string]int{}, Evidence: map[string]*EvidenceCounters{}}
	build := func(cv NumericRange) CompiledModel {
		classes := make([]PreparedClass, classCount)
		for i := range classes {
			classes[i] = scalingClass(i, buckets, 1/float64(classCount))
		}
		return evidenceCompile(t, cv, classes...)
	}
	// Wrap the Classes in a binding CV minimum: the middle of the achievable
	// second-moment range of the wide model.
	wide := build(NumericRange{Min: 0, Max: 10})
	lo, hi := jointSigmaRange(t, wide)
	mu := wide.Prepared.ExpectedRTP()
	_, fixed, err := buildSecondMomentExpression(wide)
	if err != nil {
		t.Fatal(err)
	}
	target := fixed + lo + 0.5*(hi-lo)
	compiled := build(NumericRange{Min: math.Sqrt(target/(mu*mu) - 1), Max: 10})

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
			if report.Evidence != nil {
				result.Evidence[name] = report.Evidence
			}
		}
		if solution.CanonicalBucketProbabilitySelection.Evidence != nil {
			result.Evidence["min_cv"] = solution.CanonicalBucketProbabilitySelection.Evidence
		}
	}
	raw, _ := json.MarshalIndent(result, "", "  ")
	out := os.Getenv("PROBLAB_SCALING_OUT")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, fmt.Sprintf("c%d-b%d.json", classCount, buckets)), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("classes=%d buckets=%d status=%s total=%.2fs class witness=%.3fs interval=%.3fs pivots=%d bits=%d", classCount, buckets, result.Status, result.TotalSeconds, result.WitnessSeconds, result.IntervalSeconds, result.ClassMaxPivots, result.ClassMaxBits)
}
