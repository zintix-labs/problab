// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"fmt"
	"math"
	"slices"
)

// buildSecondMomentExpression is shared by the hard CV bounds and selection.
// Preserve declaration-order arithmetic: changing it can change hard RHS bits.
func buildSecondMomentExpression(compiled CompiledModel) ([]LinearTerm, float64, error) {
	coefficients := make(map[VariableID]float64)
	fixed := 0.0
	for ci, c := range compiled.Prepared.Classes {
		if !isFinite(c.Probability) || c.Probability < 0 {
			return nil, 0, fmt.Errorf("class %q has invalid probability", c.ID)
		}
		if !c.Intent {
			if len(c.Buckets) != 1 {
				return nil, 0, fmt.Errorf("fixed class %q requires one bucket", c.ID)
			}
			if !isFinite(c.Buckets[0].SecondMoment) || c.Buckets[0].SecondMoment < 0 {
				return nil, 0, fmt.Errorf("fixed class %q has invalid second moment", c.ID)
			}
			fixed += c.Probability * c.Buckets[0].SecondMoment
			if !isFinite(fixed) {
				return nil, 0, fmt.Errorf("fixed second moment overflow")
			}
			continue
		}
		if ci >= len(compiled.ClassVariables) || len(compiled.ClassVariables[ci]) != len(c.Buckets) {
			return nil, 0, fmt.Errorf("class %q variable mapping mismatch", c.ID)
		}
		for bi, b := range c.Buckets {
			coefficient := c.Probability * b.SecondMoment
			if !isFinite(b.SecondMoment) || b.SecondMoment < 0 || !isFinite(coefficient) {
				return nil, 0, fmt.Errorf("class %q bucket %d invalid second moment", c.ID, bi)
			}
			addCoefficient(coefficients, compiled.ClassVariables[ci][bi], coefficient)
		}
	}
	return sortedTerms(coefficients), fixed, nil
}

// validateFixedExpectation checks the semantic contract, not a backend basis.
// Dependent equalities may subsequently be eliminated by the solver adapter.
func validateFixedExpectation(compiled CompiledModel, base LinearProblem, tol float64) error {
	var total compensatedSum
	primary := make(map[VariableID]bool)
	for ci, c := range compiled.Prepared.Classes {
		if !isFinite(c.Probability) || c.Probability <= 0 || !isFinite(c.Design.Exp) || c.Design.Exp < 0 {
			return fmt.Errorf("minimum CV requires fixed finite probability and expectation for class %q", c.ID)
		}
		total.Add(c.Probability)
		if !c.Intent {
			if len(c.Buckets) != 1 || len(c.Buckets[0].Samples) == 0 {
				return fmt.Errorf("fixed class %q has no empirical distribution", c.ID)
			}
			var sum compensatedSum
			for _, s := range c.Buckets[0].Samples {
				if !isFinite(s.Win) {
					return fmt.Errorf("invalid empirical win")
				}
				sum.Add(s.Win)
			}
			mean := sum.Value() / float64(len(c.Buckets[0].Samples))
			if !isFinite(mean) || !isFinite(c.Buckets[0].Mean) || math.Abs(mean-c.Design.Exp) > scaledTolerance(tol, mean, c.Design.Exp) || math.Abs(mean-c.Buckets[0].Mean) > scaledTolerance(tol, mean, c.Buckets[0].Mean) {
				return fmt.Errorf("fixed class %q empirical mean differs from declared expectation", c.ID)
			}
			continue
		}
		if ci >= len(compiled.ClassVariables) || len(compiled.ClassVariables[ci]) != len(c.Buckets) {
			return fmt.Errorf("class %q variable mapping mismatch", c.ID)
		}
		norm := make(map[VariableID]float64)
		mean := make(map[VariableID]float64)
		for bi, b := range c.Buckets {
			id := compiled.ClassVariables[ci][bi]
			if id == "" || primary[id] || !isFinite(b.Mean) {
				return fmt.Errorf("invalid fixed-mean variable mapping")
			}
			primary[id] = true
			norm[id] = 1
			mean[id] = b.Mean
		}
		for _, want := range []LinearRow{
			{ID: RowID(fmt.Sprintf("class:%04d:normalization", ci)), Sense: SenseEQ, RHS: 1, Terms: sortedTerms(norm)},
			{ID: RowID(fmt.Sprintf("class:%04d:mean", ci)), Sense: SenseEQ, RHS: c.Design.Exp, Terms: sortedTerms(mean)},
		} {
			for _, p := range []LinearProblem{compiled.Hard, base} {
				found := 0
				for _, r := range p.Rows {
					if r.ID == want.ID {
						found++
						if r.Sense != SenseEQ || r.RHS != want.RHS || !slices.Equal(r.Terms, want.Terms) {
							return fmt.Errorf("minimum CV requires unchanged exact %s", want.ID)
						}
					}
				}
				if found != 1 {
					return fmt.Errorf("minimum CV requires one exact %s", want.ID)
				}
			}
		}
	}
	if !isFinite(total.Value()) || math.Abs(total.Value()-1) > scaledTolerance(tol, total.Value(), 1) {
		return fmt.Errorf("class probabilities do not sum to one")
	}
	if len(primary) != len(compiled.Primary) || len(compiled.Hard.Variables) != len(primary) {
		return fmt.Errorf("unexpected free variable in fixed-expectation hard model")
	}
	for _, v := range compiled.Hard.Variables {
		if !primary[v.ID] {
			return fmt.Errorf("non-bucket hard variable %q", v.ID)
		}
	}
	for _, v := range compiled.Primary {
		if !primary[v.ID] || v.ClassIndex < 0 || v.ClassIndex >= len(compiled.ClassVariables) || v.BucketIndex < 0 || v.BucketIndex >= len(compiled.ClassVariables[v.ClassIndex]) || compiled.ClassVariables[v.ClassIndex][v.BucketIndex] != v.ID {
			return fmt.Errorf("invalid primary variable mapping")
		}
		delete(primary, v.ID)
	}
	return nil
}

func minimumCVStatistics(compiled CompiledModel, p LinearProblem, values []float64, terms []LinearTerm, fixed, tol float64) (float64, *float64, error) {
	if len(values) != len(p.Variables) {
		return 0, nil, fmt.Errorf("minimum CV returned malformed vector")
	}
	for _, v := range values {
		if !isFinite(v) {
			return 0, nil, fmt.Errorf("minimum CV returned nonfinite value")
		}
	}
	if _, _, ok := replaySemanticSolution(p, values, tol); !ok {
		return 0, nil, fmt.Errorf("minimum CV semantic replay failed")
	}
	indices := make(map[VariableID]int, len(p.Variables))
	for i, v := range p.Variables {
		indices[v.ID] = i
	}
	var actual compensatedSum
	for ci, c := range compiled.Prepared.Classes {
		if !c.Intent {
			actual.Add(c.Probability * c.Buckets[0].Mean)
			continue
		}
		for bi, b := range c.Buckets {
			idx, ok := indices[compiled.ClassVariables[ci][bi]]
			if !ok {
				return 0, nil, fmt.Errorf("missing mean variable")
			}
			actual.Add(c.Probability * values[idx] * b.Mean)
		}
	}
	mean := compiled.Prepared.ExpectedRTP()
	if !isFinite(mean) || !isFinite(actual.Value()) || math.Abs(actual.Value()-mean) > scaledTolerance(tol, actual.Value(), mean) {
		return 0, nil, fmt.Errorf("minimum CV requires fixed expectation: actual=%g expected=%g", actual.Value(), mean)
	}
	second := fixed + evaluateTerms(terms, p.Variables, values)
	if !isFinite(second) || second < 0 {
		return 0, nil, fmt.Errorf("invalid selected second moment")
	}
	if mean == 0 {
		return second, nil, nil
	}
	variance := second - mean*mean
	if !isFinite(variance) || variance < -scaledTolerance(tol, second, mean*mean) {
		return 0, nil, fmt.Errorf("invalid selected variance")
	}
	cv := math.Sqrt(math.Max(0, variance)) / mean
	if !isFinite(cv) {
		return 0, nil, fmt.Errorf("invalid selected CV")
	}
	return second, &cv, nil
}
