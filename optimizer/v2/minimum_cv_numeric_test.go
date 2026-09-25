// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"context"
	"fmt"
	"math"
	"testing"
)

// TestMinimumCVObjectiveNumericalScales is the M10 pre-implementation gate.
// A Class has single-valued buckets 0, a, 2a and exact mean a. Thus
// p0=p2=t, p1=1-2t, S_controlled=a²(1+2t). The analytic minimum is
// t=floor, independently of the backend. A second, fixed Class contributes
// q_fixed*b² to the unconditional objective without entering the LP.
func TestMinimumCVObjectiveNumericalScales(t *testing.T) {
	t.Run("simultaneous_scales", func(t *testing.T) {
		var p LinearProblem
		var terms []LinearTerm
		const fixed = 0.25 // A fourth, fixed Class has q=.25 and second moment=1.
		want := fixed
		for i, amplitude := range []float64{2e-4, 2, 2e4} {
			ids := []VariableID{VariableID(fmt.Sprintf("c%d:p0", i)), VariableID(fmt.Sprintf("c%d:p1", i)), VariableID(fmt.Sprintf("c%d:p2", i))}
			for _, id := range ids {
				p.Variables = append(p.Variables, LinearVariable{ID: id, Upper: 1})
			}
			coeff := .25 * amplitude * amplitude
			local := []LinearTerm{{Variable: ids[1], Coeff: coeff}, {Variable: ids[2], Coeff: 4 * coeff}}
			terms = append(terms, local...)
			p.Rows = append(p.Rows,
				LinearRow{ID: RowID(fmt.Sprintf("c%d:norm", i)), Sense: SenseEQ, RHS: 1, Terms: []LinearTerm{{Variable: ids[0], Coeff: 1}, {Variable: ids[1], Coeff: 1}, {Variable: ids[2], Coeff: 1}}},
				LinearRow{ID: RowID(fmt.Sprintf("c%d:mean", i)), Sense: SenseEQ, RHS: amplitude, Terms: []LinearTerm{{Variable: ids[1], Coeff: amplitude}, {Variable: ids[2], Coeff: 2 * amplitude}}},
				LinearRow{ID: RowID(fmt.Sprintf("c%d:floor", i)), Sense: SenseGE, RHS: 1.5 * coeff, Terms: local})
			want += 1.5 * coeff // Independent one-dimensional segments: each t >= .25.
		}
		r, err := NewGonumSolver().Solve(context.Background(), p, LinearObjective{Name: StageSelectCanonicalBucketProbabilities, Sense: Minimize, Origin: ObjectiveCanonicalBucketProbability, Terms: terms}, SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9})
		if err != nil || r.Status != SolveOptimal {
			t.Fatalf("simultaneous scales: %v %+v", err, r)
		}
		got := fixed + evaluateTerms(terms, p.Variables, r.Values)
		allowed := 1e-8 * math.Max(1, math.Abs(want))
		t.Logf("coeff nonzero=[1e-8,4e8] fixed=%g gap=%g allowed=%g", fixed, got-want, allowed)
		if math.Abs(got-want) > allowed {
			t.Fatalf("simultaneous objective gap: got=%g oracle=%g", got, want)
		}
		if 0.5e8 <= allowed {
			t.Fatal("worse feasible endpoint not separated from oracle")
		}
	})
	for _, amplitude := range []float64{1e-4, 1, 1e4} {
		for _, weight := range []float64{1, 0.1} {
			for _, floor := range []float64{0, 0.25} {
				t.Run(fmt.Sprintf("a=%g/q=%g/floor=%g", amplitude, weight, floor), func(t *testing.T) {
					p := LinearProblem{
						Variables: []LinearVariable{{ID: "p0", Upper: 1}, {ID: "p1", Upper: 1}, {ID: "p2", Upper: 1}},
						Rows: []LinearRow{
							{ID: "normalization", Sense: SenseEQ, RHS: 1, Terms: []LinearTerm{{Variable: "p0", Coeff: 1}, {Variable: "p1", Coeff: 1}, {Variable: "p2", Coeff: 1}}},
							{ID: "mean", Sense: SenseEQ, RHS: amplitude, Terms: []LinearTerm{{Variable: "p1", Coeff: amplitude}, {Variable: "p2", Coeff: 2 * amplitude}}},
						},
					}
					coeff := weight * amplitude * amplitude
					terms := []LinearTerm{{Variable: "p1", Coeff: coeff}, {Variable: "p2", Coeff: 4 * coeff}}
					if floor > 0 {
						p.Rows = append(p.Rows, LinearRow{ID: "cv_floor", Sense: SenseGE, RHS: coeff * (1 + 2*floor), Terms: terms})
					}
					fixed := (1 - weight) * 0.25
					want := fixed + coeff*(1+2*floor)
					allowed := 1e-8 * math.Max(1, math.Abs(want))
					result, err := NewGonumSolver().Solve(context.Background(), p, LinearObjective{
						Name: StageSelectCanonicalBucketProbabilities, Sense: Minimize, Origin: ObjectiveCanonicalBucketProbability, Terms: terms,
					}, SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9})
					if err != nil || result.Status != SolveOptimal {
						t.Fatalf("minimum-CV scale gate: status=%v err=%v evidence=%+v", result.Status, err, result.Evidence)
					}
					got := fixed + evaluateTerms(terms, p.Variables, result.Values)
					t.Logf("coeff=[0,%g,%g] fixed=%g got=%.17g oracle=%.17g gap=%g allowed=%g p=%v", coeff, 4*coeff, fixed, got, want, got-want, allowed, result.Values)
					if math.Abs(got-want) > allowed {
						t.Fatalf("objective gap %g exceeds %g", math.Abs(got-want), allowed)
					}
					// The upper end of the feasible segment is a known worse solution.
					// At ordinary/large scale it lies well outside the acceptance band.
					if amplitude >= 1 && coeff*(1-2*floor) <= allowed {
						t.Fatal("vacuous oracle fixture")
					}
				})
			}
		}
	}
}
