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
	"math"
	"math/big"
	"math/rand"
	"testing"
)

const evidenceTestTau = 1e-9

func oneVarProblem(lower, upper float64, rows ...LinearRow) LinearProblem {
	return LinearProblem{Variables: []LinearVariable{{ID: "x", Lower: lower, Upper: upper}}, Rows: rows}
}

func xRow(id string, sense Sense, rhs, coeff float64) LinearRow {
	return LinearRow{ID: RowID(id), Sense: sense, RHS: rhs, Terms: []LinearTerm{{Variable: "x", Coeff: coeff}}}
}

func floatsToRats(values []float64) []*big.Rat {
	out := make([]*big.Rat, len(values))
	for i, v := range values {
		if v > 0 {
			out[i] = ratOf(v)
		} else {
			out[i] = new(big.Rat)
		}
	}
	return out
}

func TestEvidenceToleranceValidation(t *testing.T) {
	for _, tau := range []float64{1e-9, 0.5, 1 - 0x1p-52} {
		if err := validateEvidenceTolerance(tau); err != nil {
			t.Fatalf("tau %v rejected: %v", tau, err)
		}
	}
	for _, tau := range []float64{0, math.SmallestNonzeroFloat64, 0x1p-1023, 1 - 0x1p-53, 1, math.Inf(1)} {
		if err := validateEvidenceTolerance(tau); err == nil {
			t.Fatalf("tau %v accepted", tau)
		}
	}
	options := DefaultEngineOptions()
	options.FeasibilityTolerance = math.SmallestNonzeroFloat64
	if err := validateEngineOptions(options); err == nil {
		t.Fatal("subnormal feasibility tolerance must be rejected by configuration")
	}
}

// A22: an overflowed lhs used to pass replay while the envelope proves the
// model infeasible. Both overflow forms (+Inf, and NaN from Inf−Inf without
// fused multiply-add) are now rejected.
func TestReplayRejectsOverflowAndEnvelopeAgrees(t *testing.T) {
	vars := []LinearVariable{{ID: "x", Lower: 0, Upper: 10}, {ID: "y", Lower: 0, Upper: 10}}
	geForm := LinearProblem{Variables: vars, Rows: []LinearRow{
		{ID: "big", Sense: SenseGE, RHS: 1e308, Terms: []LinearTerm{{Variable: "x", Coeff: 1e308}, {Variable: "y", Coeff: -1e308}}},
		{ID: "ge", Sense: SenseGE, RHS: 0, Terms: []LinearTerm{{Variable: "x", Coeff: -1}, {Variable: "y", Coeff: 1}}},
	}}
	leForm := LinearProblem{Variables: vars, Rows: []LinearRow{
		{ID: "big", Sense: SenseLE, RHS: -1e308, Terms: []LinearTerm{{Variable: "x", Coeff: 1e308}, {Variable: "y", Coeff: -1e308}}},
		{ID: "le", Sense: SenseLE, RHS: 0, Terms: []LinearTerm{{Variable: "x", Coeff: -1}, {Variable: "y", Coeff: 1}}},
	}}
	for name, problem := range map[string]LinearProblem{"ge": geForm, "le": leForm} {
		if _, _, ok := replaySemanticSolution(problem, []float64{10, 10}, evidenceTestTau); ok {
			t.Fatalf("%s: replay accepted an overflowed point", name)
		}
		model, _, err := buildEnvelope(problem, evidenceTestTau)
		if err != nil {
			t.Fatal(err)
		}
		if result := acquireFarkas(context.Background(), model, defaultExactBudget); !result.check.valid {
			t.Fatalf("%s: envelope certificate missing: %s", name, result.check.reason)
		}
	}
	// Explicit unfused evaluation also yields a non-finite sum.
	big, ten := 1e308, 10.0
	lhs := float64(big*ten) + float64(-big*ten)
	if !math.IsNaN(lhs) {
		t.Fatalf("expected NaN without fusion, got %v", lhs)
	}
}

func TestEnvelopeRejectsNonFiniteData(t *testing.T) {
	if _, _, err := buildEnvelope(oneVarProblem(0, 1, xRow("r", SenseLE, math.NaN(), 1)), evidenceTestTau); !errors.Is(err, errEnvelopeNonFiniteData) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := buildEnvelope(oneVarProblem(0, 1, xRow("r", SenseLE, 1, math.Inf(1))), evidenceTestTau); !errors.Is(err, errEnvelopeNonFiniteData) {
		t.Fatalf("err = %v", err)
	}
}

// A06: a hand certificate verifies; the sign counterexample and corrupted
// multipliers never produce a false certificate.
func TestFarkasVerifierSoundness(t *testing.T) {
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

// A07: an empty derived box is controlled evidence, never a panic.
func TestEnvelopeEmptyDerivedBox(t *testing.T) {
	problem := oneVarProblem(0, math.Inf(1), xRow("le", SenseLE, -1, 1))
	_, _, err := buildEnvelope(problem, evidenceTestTau)
	var empty *emptyBoxError
	if !errors.As(err, &empty) || empty.sourceRow != 0 || empty.id != "x" {
		t.Fatalf("expected emptyBoxError from row 0, got %v", err)
	}
}

// A08: replacing d by |g − r·t| breaks replay acceptance; the original point
// stays inside the envelope.
func TestCanonicalDeviationCounterexample(t *testing.T) {
	tau := math.Ldexp(1, -30)
	eps := 0.75 * tau
	problem := profileToyProblem(0)
	accepted := []float64{0, 0, 0.5 + eps, 0.5 - eps}
	if _, _, ok := replaySemanticSolution(problem, accepted, tau); !ok {
		t.Fatal("original point with d=0 should pass replay")
	}
	if _, _, ok := replaySemanticSolution(problem, []float64{eps, eps, 0.5 + eps, 0.5 - eps}, tau); ok {
		t.Fatal("canonical d should fail the sum row")
	}
	model, report, err := buildEnvelope(problem, tau)
	if err != nil {
		t.Fatal(err)
	}
	if !model.containsFloat(accepted) {
		t.Fatal("envelope excludes a replay-accepted point")
	}
	if len(report.derivedFrom) != 2 {
		t.Fatalf("expected derived bounds for both d, got %v", report.derivedFrom)
	}
}

// A05: every point that passes replay lies in the envelope, including points
// pushed to the edge of acceptance and models with subnormal data.
func TestEnvelopeContainsReplayAcceptedPoints(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	type sample struct {
		problem LinearProblem
		point   []float64
	}
	var samples []sample
	for trial := 0; trial < 120; trial++ {
		switch trial % 3 {
		case 0:
			p, x := plantedEvidenceProblem(r, 2, 6, true)
			samples = append(samples, sample{p, x})
		case 1:
			delta := float64(trial%5) * 0.1
			g := 0.5 + (r.Float64()-0.5)*delta/2
			samples = append(samples, sample{profileToyProblem(delta), []float64{math.Abs(g - 0.5), math.Abs(0.5 - g), g, 1 - g}})
		default:
			// Subnormal coefficients and values exercise the n·η term.
			sub := math.SmallestNonzeroFloat64 * float64(1+r.Intn(1000))
			p := LinearProblem{
				Variables: []LinearVariable{{ID: "x", Lower: 0, Upper: 1}, {ID: "y", Lower: 0, Upper: 1}},
				Rows: []LinearRow{
					{ID: "s", Sense: SenseLE, RHS: sub, Terms: []LinearTerm{{Variable: "x", Coeff: sub}, {Variable: "y", Coeff: sub}}},
					{ID: "n", Sense: SenseEQ, RHS: 1, Terms: []LinearTerm{{Variable: "x", Coeff: 1}, {Variable: "y", Coeff: 1}}},
				},
			}
			samples = append(samples, sample{p, []float64{0.5, 0.5}})
		}
	}
	checked := 0
	for i, s := range samples {
		model, _, err := buildEnvelope(s.problem, evidenceTestTau)
		if err != nil {
			t.Fatal(err)
		}
		for k := 0; k < 30; k++ {
			dir := make([]float64, len(s.point))
			for j := range dir {
				dir[j] = (r.Float64()*2 - 1) * evidenceTestTau * 4
			}
			at := func(scale float64) []float64 {
				out := make([]float64, len(s.point))
				for j := range out {
					out[j] = s.point[j] + scale*dir[j]
				}
				return out
			}
			lo, hi := 0.0, 1.0
			if _, _, ok := replaySemanticSolution(s.problem, at(hi), evidenceTestTau); ok {
				lo = hi
			} else {
				for it := 0; it < 60; it++ {
					mid := (lo + hi) / 2
					if _, _, ok := replaySemanticSolution(s.problem, at(mid), evidenceTestTau); ok {
						lo = mid
					} else {
						hi = mid
					}
				}
			}
			point := at(lo)
			if _, _, ok := replaySemanticSolution(s.problem, point, evidenceTestTau); !ok {
				continue
			}
			checked++
			if !model.containsFloat(point) {
				t.Fatalf("sample %d: replay-accepted point outside the envelope", i)
			}
		}
	}
	if checked < 2000 {
		t.Fatalf("too few accepted points checked: %d", checked)
	}
}

// A09: budget exhaustion and cancellation are statuses, never evidence.
func TestExactSimplexBudgetAndCancellation(t *testing.T) {
	bad := oneVarProblem(0, 10, xRow("le", SenseLE, 1, 1), xRow("ge", SenseGE, 3, 1))
	model, _, _ := buildEnvelope(bad, evidenceTestTau)
	if result := acquireFarkas(context.Background(), model, defaultExactBudget); !result.check.valid {
		t.Fatalf("certificate expected: %s", result.check.reason)
	}
	for _, budget := range []exactBudget{{maxPivots: 0, maxBits: 1 << 14}, {maxPivots: 100, maxBits: 0}} {
		result := acquireFarkas(context.Background(), model, budget)
		if result.status != exactBudgetHit || result.check.valid || result.nonEmpty {
			t.Fatalf("budget %+v: %+v", budget, result)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := acquireFarkas(ctx, model, defaultExactBudget); result.status != exactCanceled || result.check.valid {
		t.Fatalf("cancelled: %+v", result)
	}
	good := oneVarProblem(0, 10, xRow("le", SenseLE, 1, 1))
	goodModel, _, _ := buildEnvelope(good, evidenceTestTau)
	if result := acquireFarkas(context.Background(), goodModel, defaultExactBudget); result.check.valid || !result.nonEmpty {
		t.Fatalf("feasible model: %+v", result)
	}
}

// Exact certificates are complete on wide-margin infeasible fuzz models and
// never appear on feasible ones.
func TestExactCertificatesOnFuzzModels(t *testing.T) {
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

func TestExactBoundsPointsAndOptima(t *testing.T) {
	problem := LinearProblem{
		Variables: []LinearVariable{{ID: "x", Lower: 0, Upper: 10}, {ID: "y", Lower: 0, Upper: 10}},
		Rows: []LinearRow{
			{ID: "sum", Sense: SenseLE, RHS: 4, Terms: []LinearTerm{{Variable: "x", Coeff: 1}, {Variable: "y", Coeff: 1}}},
			{ID: "diff", Sense: SenseGE, RHS: -1, Terms: []LinearTerm{{Variable: "x", Coeff: 1}, {Variable: "y", Coeff: -1}}},
		},
	}
	model, _, err := buildEnvelope(problem, evidenceTestTau)
	if err != nil {
		t.Fatal(err)
	}
	c := map[int]*big.Rat{1: big.NewRat(1, 1)}
	upper := acquireUpperBound(context.Background(), model, c, defaultExactBudget)
	lower := acquireLowerBound(context.Background(), model, c, defaultExactBudget)
	up, _ := upper.check.value.Float64()
	low, _ := lower.check.value.Float64()
	if !upper.check.valid || math.Abs(up-2.5) > 1e-8 || !lower.check.valid || low < -2e-8 || low > 0 {
		t.Fatalf("bounds: upper=%v (%v) lower=%v (%v)", up, upper.check.reason, low, lower.check.reason)
	}
	inner, err := buildInnerModel(problem, evidenceTestTau)
	if err != nil {
		t.Fatal(err)
	}
	point, _ := exactOptimum(context.Background(), inner, c, defaultExactBudget)
	if point == nil || !inner.contains(point) {
		t.Fatalf("exact optimum missing: %v", point)
	}
	if y, _ := point[1].Float64(); math.Abs(y-2.5) > 1e-8 {
		t.Fatalf("max y = %v", y)
	}
	minPoint, _ := exactOptimum(context.Background(), inner, map[int]*big.Rat{1: big.NewRat(-1, 1)}, defaultExactBudget)
	if minPoint == nil || minPoint[1].Sign() > 0 && func() bool { v, _ := minPoint[1].Float64(); return v > 1e-8 }() {
		t.Fatalf("min y = %v", minPoint)
	}
	feasible, _ := exactPoint(context.Background(), inner, defaultExactBudget)
	if feasible == nil || !inner.contains(feasible) {
		t.Fatal("exact point missing")
	}
	if _, _, ok := replaySemanticSolution(problem, ratsToFloats(feasible), evidenceTestTau); !ok {
		t.Fatal("inner-model exact point must pass replay")
	}
}
