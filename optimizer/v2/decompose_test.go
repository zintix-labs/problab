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
	"math"
	"math/big"
	"strings"
	"testing"
)

func decompositionRun(t *testing.T, compiled CompiledModel) *engineRun {
	t.Helper()
	engine := &IntentEngine{solver: NewGonumSolver(), evidenceDirectory: t.TempDir()}
	return engine.newRun(compiled, nil)
}

// jointSigmaRange solves the joint hard model with Gonum for the T range (an
// oracle used only by tests).
func jointSigmaRange(t *testing.T, compiled CompiledModel) (float64, float64) {
	t.Helper()
	row := modelTestRow(t, compiled.Hard, cvMinRowID)
	options := SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9}
	var out [2]float64
	for i, sense := range []ObjectiveSense{Minimize, Maximize} {
		result, err := NewGonumSolver().Solve(context.Background(), compiled.Hard, LinearObjective{Name: "oracle", Sense: sense, Origin: ObjectiveDiagnostic, Terms: row.Terms}, options)
		if err != nil || result.Status != SolveOptimal {
			t.Fatalf("oracle %v: %v %v", sense, result.Status, err)
		}
		out[i] = evaluateTerms(row.Terms, compiled.Hard.Variables, result.Values)
	}
	return out[0], out[1]
}

func TestSplitByClassContract(t *testing.T) {
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceFixtureClass(0, 0.5), evidenceFixtureClass(1, 0.5))
	subs, global, err := splitByClass(compiled.Hard, compiled.Prepared.Classes)
	if err != nil || len(subs) != 2 || len(global) != 2 {
		t.Fatalf("split: %d subs %d global %v", len(subs), len(global), err)
	}
	total := 0
	for i, sub := range subs {
		if sub.position != i+1 || sub.classID != compiled.Prepared.Classes[i].ID {
			t.Fatalf("sub %d: %+v", i, sub)
		}
		total += len(sub.problem.Variables)
	}
	if total != len(compiled.Hard.Variables) {
		t.Fatal("every variable must belong to exactly one Class")
	}
	broken := cloneLinearProblem(compiled.Hard)
	broken.Rows = append(broken.Rows, LinearRow{ID: "stray", Sense: SenseLE, RHS: 1, Terms: broken.Rows[0].Terms})
	if _, _, err := splitByClass(broken, compiled.Prepared.Classes); err == nil {
		t.Fatal("a non-CV global row must be rejected")
	}
	shared := cloneLinearProblem(compiled.Hard)
	shared.Rows[0].Terms = append(append([]LinearTerm(nil), shared.Rows[0].Terms...), subs[1].problem.Rows[0].Terms[0])
	if _, _, err := splitByClass(shared, compiled.Prepared.Classes); err == nil || !strings.Contains(err.Error(), "shared") {
		t.Fatalf("shared variable: %v", err)
	}
}

// A13/A14 core: P00 E5 scenarios on the hard model. Decomposition agrees with
// the joint oracle; minimum S is within the exact required gap.
func TestDecideJointMatchesJointModel(t *testing.T) {
	classes := []PreparedClass{evidenceFixtureClass(0, 0.5), evidenceFixtureClass(1, 0.3), evidenceFixtureClass(2, 0.2)}
	wide := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, classes...)
	lo, hi := jointSigmaRange(t, wide)
	mu := wide.Prepared.ExpectedRTP()
	cvFor := func(second float64) float64 { return math.Sqrt(second/(mu*mu) - 1) }
	width := hi - lo
	scenarios := []struct {
		name string
		cv   NumericRange
		want evidenceClass
	}{
		{"not binding", NumericRange{Min: 0, Max: 10}, evidenceFeasible},
		{"lower binding 30%", NumericRange{Min: cvFor(lo + 0.3*width), Max: 10}, evidenceFeasible},
		{"lower binding 99%", NumericRange{Min: cvFor(lo + 0.99*width), Max: 10}, evidenceFeasible},
		{"upper below minimum", NumericRange{Min: 0, Max: cvFor(lo - 0.1*width)}, evidenceInfeasible},
		{"upper 1e-12 below minimum", NumericRange{Min: 0, Max: cvFor(lo * (1 - 1e-12))}, evidenceFeasible},
	}
	for _, sc := range scenarios {
		compiled := evidenceCompile(t, sc.cv, classes...)
		run := decompositionRun(t, compiled)
		decision, err := run.decideJoint(context.Background(), compiled.Hard, StageSelectCanonicalBucketProbabilities, ObjectiveDiagnostic, nil, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		if decision.class != sc.want {
			t.Fatalf("%s: %s (%s)", sc.name, decision.class, decision.reasonText())
		}
		if sc.want == evidenceInfeasible {
			if decision.evidence == nil || decision.evidence.kind != evidenceKindCVDisjoint || decision.evidence.cv.side != "lower" {
				t.Fatalf("%s: expected type-C lower-side evidence, got %+v", sc.name, decision.evidence)
			}
			continue
		}
		if !decision.gapMet() {
			t.Fatalf("%s: gap %v not within %v", sc.name, decision.gapS, decision.requiredS)
		}
		if _, _, ok := replaySemanticSolution(compiled.Hard, decision.witness, 1e-9); !ok {
			t.Fatalf("%s: final joint replay failed", sc.name)
		}
		// Oracle independent of Gonum (which reports the last scenario's joint
		// model infeasible): the minimum is max(unconstrained minimum, L_T).
		oracle := math.Max(lo, modelTestRow(t, compiled.Hard, cvMinRowID).RHS)
		candidate, _ := decision.candidateS.Float64()
		if math.Abs(candidate-oracle) > 1e-8*math.Max(1, oracle) {
			t.Fatalf("%s: decomposition S %v vs oracle %v", sc.name, candidate, oracle)
		}
	}
}

// Mixed fixed and intent Classes: S = F + T, the safe lower bound includes F
// and the reported S matches minimumCVStatistics.
func TestDecideJointMixedFixedAndIntentClasses(t *testing.T) {
	fixed := modelTestFixedClass(2, "fixed", 0.2, 3, 12)
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceFixtureClass(0, 0.5), evidenceFixtureClass(1, 0.3), fixed)
	terms, fixedSecond, err := buildSecondMomentExpression(compiled)
	if err != nil || fixedSecond == 0 {
		t.Fatalf("expected a fixed contribution, got %v (%v)", fixedSecond, err)
	}
	run := decompositionRun(t, compiled)
	decision, err := run.decideJoint(context.Background(), compiled.Hard, StageSelectCanonicalBucketProbabilities, ObjectiveDiagnostic, nil, true, nil)
	if err != nil || decision.class != evidenceFeasible || !decision.gapMet() {
		t.Fatalf("decision %s gap %v req %v err %v", decision.class, decision.gapS, decision.requiredS, err)
	}
	second, _, err := minimumCVStatistics(compiled, compiled.Hard, decision.witness, terms, fixedSecond, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	candidate, _ := decision.candidateS.Float64()
	if math.Abs(candidate-second) > 1e-12*math.Max(1, second) {
		t.Fatalf("candidate S %v vs minimumCVStatistics %v", candidate, second)
	}
	if decision.safeLowerS.Cmp(ratOf(fixedSecond)) <= 0 {
		t.Fatalf("safe lower bound %v must include F = %v", decision.safeLowerS, fixedSecond)
	}
}

// All Classes fixed: no variables, no CV rows, S = F and the gap is zero.
func TestDecideJointAllFixedClasses(t *testing.T) {
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, modelTestFixedClass(0, "a", 0.5, 1, 2), modelTestFixedClass(1, "b", 0.5, 2, 5))
	run := decompositionRun(t, compiled)
	decision, err := run.decideJoint(context.Background(), compiled.Hard, StageSelectCanonicalBucketProbabilities, ObjectiveDiagnostic, nil, true, nil)
	if err != nil || decision.class != evidenceFeasible || decision.gapS == nil || decision.gapS.Sign() != 0 {
		t.Fatalf("all fixed: %+v err %v", decision, err)
	}
	_, fixedSecond, _ := buildSecondMomentExpression(compiled)
	if decision.candidateS.Cmp(ratOf(fixedSecond)) != 0 {
		t.Fatalf("S = %v, want F = %v", decision.candidateS, fixedSecond)
	}
}

func TestRequiredMinimumCVGapIsExact(t *testing.T) {
	small := requiredMinimumCVGap(big.NewRat(1, 100_000))
	if small.Cmp(requiredGapAbsolute) != 0 {
		t.Fatalf("small S must use the absolute floor, got %v", small)
	}
	large := requiredMinimumCVGap(big.NewRat(5, 1))
	if large.Cmp(big.NewRat(5, 100_000_000)) != 0 {
		t.Fatalf("relative requirement = %v", large)
	}
}
