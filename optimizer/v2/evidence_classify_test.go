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
	"math/rand"
	"os"
	"strings"
	"testing"
)

func newEvidenceRun(t *testing.T, solver Solver) *engineRun {
	t.Helper()
	engine := &IntentEngine{solver: solver, evidenceDirectory: t.TempDir()}
	run := engine.newRun(CompiledModel{}, nil)
	run.options = SolveOptions{FeasibilityTolerance: evidenceTestTau, OptimalityTolerance: 1e-9}
	return run
}

func classifyForTest(t *testing.T, run *engineRun, problem LinearProblem) classification {
	t.Helper()
	result, err := run.classify(context.Background(), problem, solveContext{stage: StageProveHardFeasibility}, ObjectiveHardFeasibility)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// A02: the review's feasible-by-construction set, including the Gonum
// false-infeasible cases: never INFEASIBLE, and every witness passes replay.
func TestClassifyFeasibleFuzzSet(t *testing.T) {
	sets := []struct{ trials, classes, buckets int }{{300, 2, 8}, {200, 3, 15}}
	if testing.Short() {
		sets = []struct{ trials, classes, buckets int }{{40, 2, 8}, {20, 3, 15}}
	}
	run := newEvidenceRun(t, NewGonumSolver())
	total, feasible := 0, 0
	for _, set := range sets {
		for trial := 0; trial < set.trials; trial++ {
			problem, _ := plantedEvidenceProblem(rand.New(rand.NewSource(int64(trial))), set.classes, set.buckets, true)
			result := classifyForTest(t, run, problem)
			total++
			switch result.class {
			case evidenceInfeasible:
				t.Fatalf("%dx%d trial %d: feasible model classified INFEASIBLE", set.classes, set.buckets, trial)
			case evidenceFeasible:
				feasible++
				if _, _, ok := replaySemanticSolution(problem, result.witness, evidenceTestTau); !ok {
					t.Fatal("witness does not pass replay")
				}
			}
		}
	}
	t.Logf("witness %d/%d; by source %v", feasible, total, run.counters.WitnessFound)
	if feasible != total {
		t.Fatalf("witness found for %d/%d feasible models (P00: 500/500)", feasible, total)
	}
}

// A03: provably infeasible models. Wide margins are always certified; near
// the tolerance no wrong conclusion appears and every UNKNOWN is a non-empty
// envelope.
func TestClassifyInfeasibleSet(t *testing.T) {
	// The full P00 set (100 per margin) takes over a minute; it runs when
	// PROBLAB_EVIDENCE_FULL=1 and its result is recorded in the techspec.
	trials := 10
	if os.Getenv("PROBLAB_EVIDENCE_FULL") == "1" {
		trials = 100
	}
	run := newEvidenceRun(t, NewGonumSolver())
	for _, margin := range []float64{1e-1, 1e-3, 1e-6, 1e-8, 5e-9, 3e-9, 2e-9, 1e-9} {
		certified := 0
		for trial := 0; trial < trials; trial++ {
			base, _ := plantedEvidenceProblem(rand.New(rand.NewSource(int64(1000+trial))), 2, 8, false)
			problem := makeEvidenceInfeasible(base, margin)
			result := classifyForTest(t, run, problem)
			switch result.class {
			case evidenceInfeasible:
				certified++
			case evidenceFeasible:
				if _, _, ok := replaySemanticSolution(problem, result.witness, evidenceTestTau); !ok {
					t.Fatalf("margin %g: witness does not pass replay", margin)
				}
			case evidenceUnknown:
				if !strings.Contains(result.reasonText(), "certificate: "+reasonEnvelopeNonEmpty) {
					t.Fatalf("margin %g: unknown for another reason: %s", margin, result.reasonText())
				}
			}
		}
		if margin >= 1e-8 && certified != trials {
			t.Fatalf("margin %g: certified %d/%d", margin, certified, trials)
		}
		t.Logf("margin %g: certified %d/%d", margin, certified, trials)
	}
}

// A04: x ≤ s and x ≥ s + k·τ·max(1,|s|). Each row has its own τ allowance,
// so the gray zone sits at k = 2.
func TestClassifyToleranceBoundary(t *testing.T) {
	run := newEvidenceRun(t, NewGonumSolver())
	for _, s := range []float64{0, 1000} {
		for _, k := range []float64{0.5, 1.5, 1.9, 1.99, 2.01, 2.05, 2.5, 3, 5} {
			scale := math.Max(1, math.Abs(s))
			problem := oneVarProblem(-1e4, 1e4, xRow("le", SenseLE, s, 1), xRow("ge", SenseGE, s+k*evidenceTestTau*scale, 1))
			result := classifyForTest(t, run, problem)
			want := evidenceFeasible
			if k > 2 {
				want = evidenceInfeasible
			}
			if result.class != want {
				t.Fatalf("s=%g k=%g: %s (%s), want %s", s, k, result.class, result.reasonText(), want)
			}
		}
	}
}

// falseInfeasibleSolver reports every problem infeasible, like Gonum's false
// negatives: the exact witness must still take over, and the backend's word
// must never become an INFEASIBLE classification.
type falseInfeasibleSolver struct{}

func (falseInfeasibleSolver) Solve(context.Context, LinearProblem, LinearObjective, SolveOptions) (SolveResult, error) {
	return SolveResult{Status: SolveInfeasible}, nil
}

func TestClassifyIgnoresBackendInfeasible(t *testing.T) {
	run := newEvidenceRun(t, falseInfeasibleSolver{})
	problem, _ := plantedEvidenceProblem(rand.New(rand.NewSource(7)), 2, 6, true)
	result := classifyForTest(t, run, problem)
	if result.class != evidenceFeasible || result.source != witnessExact {
		t.Fatalf("got %s via %q (%s)", result.class, result.source, result.reasonText())
	}
	bad := oneVarProblem(0, 10, xRow("le", SenseLE, 1, 1), xRow("ge", SenseGE, 3, 1))
	if result := classifyForTest(t, run, bad); result.class != evidenceInfeasible || result.evidence.kind != evidenceKindFarkas {
		t.Fatalf("infeasible toy: %+v", result)
	}
}

// Zero exact budget: certificates that need the simplex become UNKNOWN, but
// an empty derived box needs no solve and stays INFEASIBLE.
func TestClassifyZeroBudget(t *testing.T) {
	run := newEvidenceRun(t, falseInfeasibleSolver{})
	run.engine.exactBudget = &exactBudget{maxPivots: 0, maxBits: 1 << 14}
	bad := oneVarProblem(0, 10, xRow("le", SenseLE, 1, 1), xRow("ge", SenseGE, 3, 1))
	if result := classifyForTest(t, run, bad); result.class != evidenceUnknown || !strings.Contains(result.reasonText(), reasonBudget) {
		t.Fatalf("type A under zero budget: %+v", result)
	}
	empty := oneVarProblem(0, math.Inf(1), xRow("le", SenseLE, -1, 1))
	if result := classifyForTest(t, run, empty); result.class != evidenceInfeasible || result.evidence.kind != evidenceKindEmptyBox {
		t.Fatalf("type B under zero budget: %+v", result)
	}
}

func TestGapProblemKeepsTermsSorted(t *testing.T) {
	problem, _ := plantedEvidenceProblem(rand.New(rand.NewSource(1)), 2, 6, true)
	gap, objective, ok := gapProblem(problem, StageProveHardFeasibility, ObjectiveHardFeasibility)
	if !ok {
		t.Fatal("expected inequality rows")
	}
	if err := validateLinearProblem(gap, objective, SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9}); err != nil {
		t.Fatal(err)
	}
}
