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
	"math/big"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestStageTheoreticalEndpoints is A11: the P00 E4 three-Class model reaches
// δ = 0 and both ρ = 1 at their theoretical endpoints with a zero bound.
func TestStageTheoreticalEndpoints(t *testing.T) {
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10},
		evidenceFixtureClass(0, .5), evidenceFixtureClass(1, .3), evidenceFixtureClass(2, .2))
	solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), compiled)
	if err != nil || solution.Status != StatusOptimal {
		t.Fatalf("status=%s err=%v diagnostics=%+v", solution.Status, err, solution.Diagnostics)
	}
	for name, check := range map[string]struct {
		report BisectionReport
		value  float64
		basis  string
	}{
		"delta":     {solution.MainProfileOptimization, 0, solution.MainProfileOptimization.LowerBasis},
		"rho_other": {solution.OtherBucketVisibilityOptimization, 1, solution.OtherBucketVisibilityOptimization.UpperBasis},
		"rho_main":  {solution.MainGroupInternalVisibilityOptimization, 1, solution.MainGroupInternalVisibilityOptimization.UpperBasis},
	} {
		r := check.report
		if r.Lower != check.value || r.Upper != check.value || r.VerifiedBound != 0 || r.StopReason != StopEndpoint || check.basis != BasisTheoreticalEndpoint {
			t.Fatalf("%s: %+v", name, r)
		}
	}
}

// engcheckModel is problab-p00/cmd/engcheck: the E4 skewed Class (Main
// minimum 0.9, prefer 0.05/0.95, exact δ* = 0.4) on which the v2.9 engine
// returned NUMERICAL_FAILURE, with the default engine options.
func engcheckModel(t *testing.T) CompiledModel {
	enabled := true
	means := []float64{0, 1, 2, 3}
	buckets := make([]PreparedBucket, 4)
	for i := range buckets {
		buckets[i] = PreparedBucket{Index: i, Lower: float64(i), Upper: float64(i + 1),
			Samples: []CollectedSample{{Snapshot: []byte{1, byte(i + 1)}}},
			Mean:    means[i], SecondMoment: means[i] * means[i], Minimum: means[i], Maximum: means[i],
			CDFAtUpper: 1, RiskCap: math.Inf(1), MainGroup: -1}
	}
	buckets[0].MainGroup, buckets[1].MainGroup, buckets[2].MainGroup = 0, 0, 1
	class := PreparedClass{ID: "class-000", Weight: ClassWeightBase, Probability: 1, Intent: true, Buckets: buckets,
		Groups: []PreparedGroup{{Index: 0, BucketIndexes: []int{0, 1}, PreferShare: 0.05}, {Index: 1, BucketIndexes: []int{2}, PreferShare: 0.95}},
		Others: []int{3},
		Design: ClassDesign{Exp: 1.5, Median: ClosedInterval{0, 100}, Subjective: SubjectiveIntent{Intent: &enabled,
			MainExperience: &MainExperience{Probability: NumericRange{Min: 0.9, Max: 1}, Prefer: []float64{1, 1}}}}}
	prepared := PreparedProblem{Plan: ResolvedPlan{Version: ConfigVersion,
		Plan:          RunPlan{ID: "p00", Engine: EngineIntentLPV2, Intent: "p00", Target: Target{BetModes: []int{0}}},
		Intent:        MathIntent{Overall: OverallIntent{CV: NumericRange{Min: 0, Max: 10}}},
		EngineOptions: DefaultEngineOptions()},
		Classes: []PreparedClass{class}}
	compiled, diagnostics, err := CompileHardModel(prepared)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("compile: %v %+v", err, diagnostics)
	}
	return compiled
}

// TestStageEngcheckModelIsOptimal is A12.
func TestStageEngcheckModelIsOptimal(t *testing.T) {
	solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), engcheckModel(t))
	if err != nil || solution.Status != StatusOptimal {
		t.Fatalf("status=%s err=%v diagnostics=%+v", solution.Status, err, solution.Diagnostics)
	}
	for name, r := range map[string]BisectionReport{"delta": solution.MainProfileOptimization,
		"rho_other": solution.OtherBucketVisibilityOptimization, "rho_main": solution.MainGroupInternalVisibilityOptimization} {
		if r.Applicable && (r.VerifiedBound > 1e-8 || (r.StopReason != StopConverged && r.StopReason != StopEndpoint)) {
			t.Fatalf("%s: %+v", name, r)
		}
	}
	assertProfileBracket(t, solution.MainProfileOptimization, 0.4)
}

// TestStagePrecisionUnprovenReport is A15: too few iterations leave the
// bracket wider than 1e-8; the run reports PrecisionUnproven with the §9.1
// causes and the fixed Designer hint.
func TestStagePrecisionUnprovenReport(t *testing.T) {
	compiled := skewedModel(t)
	compiled.Prepared.Plan.EngineOptions.ProfileBisectionIterations = 5
	solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), compiled)
	if err != nil {
		t.Fatal(err)
	}
	if solution.Status != StatusNumericalFailure || len(solution.Diagnostics) != 1 {
		t.Fatalf("status=%s diagnostics=%+v", solution.Status, solution.Diagnostics)
	}
	d := solution.Diagnostics[0]
	if d.Code != DiagnosticPrecisionUnproven || !strings.Contains(d.Message, designerUncertainHint) ||
		!strings.Contains(d.Message, string(StageMinimizeMainProfileDeviation)) || len(d.Causes) == 0 {
		t.Fatalf("diagnostic=%+v", d)
	}
	cause := d.Causes[0]
	for _, want := range []string{string(StageMinimizeMainProfileDeviation), BasisVerifiedCertificate, BasisReplayedWitness, "stop reason " + StopBudget} {
		if !strings.Contains(cause.Summary, want) {
			t.Fatalf("cause %q lacks %q", cause.Summary, want)
		}
	}
	metrics := map[string]float64{}
	for _, m := range cause.Metrics {
		metrics[m.Name] = m.Value
	}
	if metrics["verified_bound"] <= 1e-8 || metrics["required_precision"] != 1e-8 || metrics["upper"]-metrics["lower"] != metrics["verified_bound"] {
		t.Fatalf("metrics=%+v", metrics)
	}
}

// TestStageHardCertificateKinds is A16 for the kinds a hard model produces
// through the engine: type A (Farkas on one Class) and type C (global CV
// disjoint). Both need the exact simplex, so a zero budget turns them into
// EvidenceUnavailable, never INFEASIBLE_MODEL. Type B needs no solve and is
// covered by TestClassifyZeroBudget.
func TestStageHardCertificateKinds(t *testing.T) {
	farkas := skewedModel(t)
	farkas.Hard = cloneLinearProblem(farkas.Hard)
	farkas.Hard.Rows = append(farkas.Hard.Rows, LinearRow{ID: "test:impossible", ClassID: farkas.Prepared.Classes[0].ID,
		Sense: SenseGE, RHS: 2, Terms: []LinearTerm{{Variable: farkas.Primary[0].ID, Coeff: 1}}})
	cv := evidenceCompile(t, NumericRange{Min: 0, Max: 0}, evidenceFixtureClass(0, .5), evidenceFixtureClass(1, .5))
	for _, test := range []struct {
		name     string
		compiled CompiledModel
		kind     string
	}{{"A", farkas, evidenceKindFarkas}, {"C", cv, evidenceKindCVDisjoint}} {
		run := NewIntentEngine(NewGonumSolver()).newRun(test.compiled, nil)
		decision, err := run.decideJoint(context.Background(), test.compiled.Hard, StageProveHardFeasibility, ObjectiveHardFeasibility, nil, false, nil)
		if err != nil || decision.class != evidenceInfeasible || decision.evidence.kind != test.kind || decision.evidence.rule != productRule(1e-9) {
			t.Fatalf("%s: class=%s evidence=%+v err=%v", test.name, decision.class, decision.evidence, err)
		}
		if test.kind == evidenceKindCVDisjoint {
			for _, class := range decision.evidence.cv.classes {
				if class.rule != decision.evidence.rule {
					t.Fatalf("type C mixes rules: %+v vs %+v", class.rule, decision.evidence.rule)
				}
			}
		}
		solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), test.compiled)
		if err != nil || solution.Status != StatusInfeasibleModel {
			t.Fatalf("%s: status=%s err=%v", test.name, solution.Status, err)
		}
		engine := NewIntentEngine(NewGonumSolver())
		engine.exactBudget = &exactBudget{maxPivots: 0, maxBits: 1 << 14}
		solution, err = engine.Solve(context.Background(), test.compiled)
		if err != nil || solution.Status != StatusNumericalFailure || solution.Diagnostics[0].Code != DiagnosticEvidenceUnavailable {
			t.Fatalf("%s zero budget: status=%s diagnostics=%+v err=%v", test.name, solution.Status, solution.Diagnostics, err)
		}
	}
}

// TestMinimumCVGapComparisonIsExact is A24 for Step 5: a gap whose float64
// value equals the requirement but exceeds it exactly is not met; an exact
// tie is met.
func TestMinimumCVGapComparisonIsExact(t *testing.T) {
	required := requiredMinimumCVGap(big.NewRat(1, 1)) // 1/10⁸ for S = 1
	over := ratAdd(required, new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Lsh(big.NewInt(1), 80)))
	if a, _ := over.Float64(); a != 1e-8 {
		t.Fatalf("fixture: float64(gap)=%.17g should round to 1e-8", a)
	}
	met := func(gap *big.Rat) bool {
		return jointDecision{classification: classification{class: evidenceFeasible}, gapS: gap, requiredS: required}.gapMet()
	}
	if met(over) {
		t.Fatal("gap above the requirement by 2⁻⁸⁰ was accepted")
	}
	if !met(new(big.Rat).Set(required)) {
		t.Fatal("gap equal to the requirement was rejected")
	}
}

// TestStageTwoSets is A25: the CV maximum sits a relative gap below the
// exact minimum second moment. Within the internal set the run is OPTIMAL
// and the final point passes both sets; feasible only in the external set it
// is EvidenceUnavailable; outside the external set it is INFEASIBLE_MODEL.
func TestStageTwoSets(t *testing.T) {
	for _, test := range []struct {
		gap  float64
		want Status
		code DiagnosticCode
	}{
		{0.5e-9, StatusOptimal, ""},
		{2e-9, StatusNumericalFailure, DiagnosticEvidenceUnavailable},
		{1e-8, StatusInfeasibleModel, ""},
	} {
		compiled := contractToleranceGap(t, test.gap)
		solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), compiled)
		if err != nil || solution.Status != test.want || (test.code != "" && solution.Diagnostics[0].Code != test.code) {
			t.Fatalf("gap %g: status=%s err=%v diagnostics=%+v", test.gap, solution.Status, err, solution.Diagnostics)
		}
		if solution.Status == StatusInfeasibleModel {
			// Every Class has a witness here (type C), so a Class-local
			// explanation would contradict the certificate.
			for _, d := range solution.Diagnostics {
				if d.Code != DiagnosticGlobalCVInfeasible && d.Code != DiagnosticHardModelInfeasible {
					t.Fatalf("gap %g: explanation disagrees with the type-C certificate: %+v", test.gap, d)
				}
			}
		}
		if solution.Status != StatusOptimal {
			continue
		}
		tau := compiled.Prepared.Plan.EngineOptions.FeasibilityTolerance
		_, _, external := replaySemanticSolution(compiled.Hard, solution.Primary, tau)
		if !replayWithRule(compiled.Hard, solution.Primary, internalRule(tau)) || !external {
			t.Fatalf("gap %g: final point fails a set", test.gap)
		}
	}
}

// determinismModels bisect (skewed) and bind the CV row in probes (E).
func determinismModels(t *testing.T) map[string]CompiledModel {
	return map[string]CompiledModel{"skewed": skewedModel(t), "cv-binding": contractCVBinding(t)}
}

func solvePrimaryBits(t *testing.T, compiled CompiledModel) string {
	t.Helper()
	solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), compiled)
	if err != nil || solution.Status != StatusOptimal {
		t.Fatalf("status=%s err=%v", solution.Status, err)
	}
	return fmt.Sprint(floatBits(solution.Primary))
}

// TestEngineDeterminism is A18 in one process: repeated runs and a model
// whose maps were filled in another order give the same primary bits.
func TestEngineDeterminism(t *testing.T) {
	for name, compiled := range determinismModels(t) {
		want := solvePrimaryBits(t, compiled)
		if got := solvePrimaryBits(t, compiled); got != want {
			t.Fatalf("%s: repeated run differs", name)
		}
		reordered := compiled
		reordered.VariableIndex = make(map[VariableID]int, len(compiled.VariableIndex))
		for j := len(compiled.Hard.Variables) - 1; j >= 0; j-- {
			id := compiled.Hard.Variables[j].ID
			if index, ok := compiled.VariableIndex[id]; ok {
				reordered.VariableIndex[id] = index
			}
		}
		if got := solvePrimaryBits(t, reordered); got != want {
			t.Fatalf("%s: map insertion order changed the primary", name)
		}
	}
}

// TestEngineSubprocessRepeatable is A18 across processes.
func TestEngineSubprocessRepeatable(t *testing.T) {
	if os.Getenv("PROBLAB_DETERMINISM_CHILD") == "1" {
		for _, name := range []string{"cv-binding", "skewed"} {
			fmt.Printf("DETERMINISM %s %s\n", name, solvePrimaryBits(t, determinismModels(t)[name]))
		}
		return
	}
	var want string
	for i := 0; i < 2; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestEngineSubprocessRepeatable$", "-test.v")
		cmd.Env = append(os.Environ(), "PROBLAB_DETERMINISM_CHILD=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		var got []string
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "DETERMINISM ") {
				got = append(got, line)
			}
		}
		if len(got) != 2 {
			t.Fatalf("child output:\n%s", out)
		}
		if i == 0 {
			want = strings.Join(got, "\n")
		} else if strings.Join(got, "\n") != want {
			t.Fatal("primary bits differ between processes")
		}
	}
}
