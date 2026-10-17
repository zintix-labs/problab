// Copyright 2026 Zintix Labs
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

// Optional extended validation. All code below is intentionally commented out
// so ordinary go test and PR CI run only the representative regression sets.
// Remove the single block-comment pair and run:
//   go test ./optimizer/v2 -run 'Full500$|Extended$' -count=1 -v -timeout=20m
// For 100 trials per infeasibility margin instead of the archived 10:
//   PROBLAB_EVIDENCE_FULL=1 go test ./optimizer/v2 -run '^TestClassifyInfeasibleSetExtended$' -count=1 -v -timeout=20m
// Restore the comment before committing. The active copy is maintained in
// problab-p00/workers/_optimizer_v2/evidence_extended_worker_test.go.
// Never enable this copy and the P00 overlay copy simultaneously.

/*
import (
	"context"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestClassifyFeasibleFull500(t *testing.T) {
	sets := []struct{ trials, classes, buckets int }{{300, 2, 8}, {200, 3, 15}}
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

func TestClassifyInfeasibleSetExtended(t *testing.T) {
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

func TestFarkasVerifierSoundnessExtended(t *testing.T) {
	bad := oneVarProblem(0, 10, xRow("le", SenseLE, 1, 1), xRow("ge", SenseGE, 3, 1))
	model, _, err := buildEnvelope(bad, evidenceTestTau)
	if err != nil {
		t.Fatal(err)
	}
	if check := verifyFarkas(model, []*big.Rat{big.NewRat(1, 1), big.NewRat(1, 1)}); !check.valid || len(check.rows) != 2 || len(check.vars) != 0 {
		t.Fatalf("hand certificate: %+v", check)
	}
	signCase := oneVarProblem(1, 2, xRow("ge", SenseGE, 0, 1))
	signModel, _, _ := buildEnvelope(signCase, evidenceTestTau)
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		y := []float64{(r.Float64() - 0.5) * math.Pow(10, float64(r.Intn(20)-10))}
		if verifyFarkas(signModel, floatsToRats(y)).valid {
			t.Fatalf("false certificate with y=%v", y)
		}
	}
	for trial := 0; trial < 200; trial++ {
		problem, _ := plantedEvidenceProblem(r, 2, 6, true)
		m, _, err := buildEnvelope(problem, evidenceTestTau)
		if err != nil {
			t.Fatal(err)
		}
		for k := 0; k < 20; k++ {
			y := make([]float64, len(m.rows))
			for i := range y {
				if r.Float64() < 0.5 {
					y[i] = (r.Float64() - 0.2) * math.Pow(10, float64(r.Intn(12)-6))
				}
			}
			if verifyFarkas(m, floatsToRats(y)).valid {
				t.Fatalf("trial %d: corrupted multipliers certified a feasible model", trial)
			}
		}
	}
}

func TestExactCertificatesOnFuzzModelsExtended(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	for trial := 0; trial < 40; trial++ {
		problem, _ := plantedEvidenceProblem(r, 2, 6, true)
		m, _, _ := buildEnvelope(problem, evidenceTestTau)
		if acquireFarkas(context.Background(), m, defaultExactBudget).check.valid {
			t.Fatalf("trial %d: feasible model certified", trial)
		}
		m2, _, _ := buildEnvelope(makeEvidenceInfeasible(problem, 0.1), evidenceTestTau)
		if !acquireFarkas(context.Background(), m2, defaultExactBudget).check.valid {
			t.Fatalf("trial %d: wide-margin infeasible model not certified", trial)
		}
	}
}

// evidenceCVBindingModel is P00 E7: two Classes whose CV minimum binds in
// the probes (90% up the achievable second-moment range).
func evidenceCVBindingModel(t *testing.T) CompiledModel {
	classes := []PreparedClass{evidenceFixtureClass(0, 0.5), evidenceSkewedClass(1, 0.5)}
	wide := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, classes...)
	lo, hi := jointSigmaRange(t, wide)
	mu := wide.Prepared.ExpectedRTP()
	return evidenceCompile(t, NumericRange{Min: math.Sqrt((lo+0.9*(hi-lo))/(mu*mu) - 1), Max: 10}, classes...)
}

func extendedDeterminismModels(t *testing.T) map[string]CompiledModel {
	return map[string]CompiledModel{"skewed": skewedModel(t), "cv-binding": evidenceCVBindingModel(t)}
}

func TestEngineDeterminismExtended(t *testing.T) {
	for name, compiled := range extendedDeterminismModels(t) {
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

func TestEngineSubprocessRepeatableExtended(t *testing.T) {
	if os.Getenv("PROBLAB_EXTENDED_DETERMINISM_CHILD") == "1" {
		for _, name := range []string{"cv-binding", "skewed"} {
			fmt.Printf("DETERMINISM %s %s\n", name, solvePrimaryBits(t, extendedDeterminismModels(t)[name]))
		}
		return
	}
	var want string
	for i := 0; i < 2; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestEngineSubprocessRepeatableExtended$", "-test.v")
		cmd.Env = append(os.Environ(), "PROBLAB_EXTENDED_DETERMINISM_CHILD=1")
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
*/
