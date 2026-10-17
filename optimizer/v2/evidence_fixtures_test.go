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
	"math/rand"
	"testing"
)

// plantedEvidenceProblem is the P00 fuzz generator (problab-p00 evidence/
// fixtures.go, from the 2026-10-16 review): an LP shaped like the hard model
// (normalization, exact mean, median and Main rows, unsupported buckets fixed
// at 0, global second-moment rows) around a planted feasible point. tight=true
// puts every inequality at the planted point, which reproduced Gonum's false
// "infeasible". The planted point is returned in variable order.
func plantedEvidenceProblem(r *rand.Rand, classes, buckets int, tight bool) (LinearProblem, []float64) {
	var p LinearProblem
	planted := map[VariableID]float64{}
	second := map[VariableID]float64{}
	secondValue := 0.0
	for k := 0; k < classes; k++ {
		ids := make([]VariableID, buckets)
		x := make([]float64, buckets)
		supported := make([]bool, buckets)
		var sup []int
		for i := range ids {
			ids[i] = VariableID(fmt.Sprintf("p:%04d:%04d", k, i))
			supported[i] = r.Float64() > 0.3
			if supported[i] {
				sup = append(sup, i)
			}
		}
		if len(sup) < 3 {
			supported[0], supported[1], supported[2] = true, true, true
			sup = []int{0, 1, 2}
		}
		total := 0.0
		for _, i := range sup {
			if r.Float64() < 0.4 {
				x[i] = r.Float64()
				total += x[i]
			}
		}
		if total == 0 {
			x[sup[0]] = 1
			total = 1
		}
		for i := range x {
			x[i] /= total
		}
		classID := fmt.Sprintf("class-%04d", k)
		for i := range ids {
			planted[ids[i]] = x[i]
			upper := 1.0
			if !supported[i] {
				upper = 0
			}
			p.Variables = append(p.Variables, LinearVariable{ID: ids[i], Lower: 0, Upper: upper})
		}
		mu := make([]float64, buckets)
		for i := range mu {
			mu[i] = math.Pow(10, 4*float64(i)/float64(buckets)-1) * (1 + r.Float64())
		}
		cls := 1.0 / float64(classes)
		addRow := func(name string, sense Sense, coef func(i int) float64, slack float64) {
			m := map[VariableID]float64{}
			v := 0.0
			for i := range ids {
				m[ids[i]] = coef(i)
				v += coef(i) * x[i]
			}
			rhs := v
			if !tight {
				switch sense {
				case SenseLE:
					rhs = v + slack
				case SenseGE:
					rhs = v - slack
				}
			}
			p.Rows = append(p.Rows, LinearRow{ID: RowID(fmt.Sprintf("c%04d:%s", k, name)), ClassID: classID, Sense: sense, RHS: rhs, Terms: sortedTerms(m)})
		}
		addRow("norm", SenseEQ, func(int) float64 { return 1 }, 0)
		addRow("mean", SenseEQ, func(i int) float64 { return mu[i] }, 0)
		a := make([]float64, buckets)
		b := make([]float64, buckets)
		cut := r.Intn(buckets)
		for i := range a {
			if i < cut {
				a[i], b[i] = 1, 1
			} else if i == cut {
				a[i] = float64(r.Intn(10)) / 10
				b[i] = 1
			}
		}
		addRow("medlo", SenseLE, func(i int) float64 { return a[i] }, 0.05)
		addRow("medhi", SenseGE, func(i int) float64 { return b[i] }, 0.05)
		mainLo, mainHi := r.Intn(buckets/2), buckets/2+r.Intn(buckets/2)
		inMain := func(i int) float64 {
			if i >= mainLo && i <= mainHi {
				return 1
			}
			return 0
		}
		addRow("mainmin", SenseGE, inMain, 0.05)
		addRow("mainmax", SenseLE, inMain, 0.05)
		for i := range ids {
			s := mu[i] * mu[i] * (1 + r.Float64())
			second[ids[i]] += cls * s
			secondValue += cls * s * x[i]
		}
	}
	lo, hi := secondValue, secondValue*1.001
	if !tight {
		lo, hi = secondValue*0.9, secondValue*1.1
	}
	p.Rows = append(p.Rows,
		LinearRow{ID: "global:cv:min", Sense: SenseGE, RHS: lo, Terms: sortedTerms(second)},
		LinearRow{ID: "global:cv:max", Sense: SenseLE, RHS: hi, Terms: sortedTerms(second)},
	)
	x := make([]float64, len(p.Variables))
	for j, v := range p.Variables {
		x[j] = planted[v.ID]
	}
	return p, x
}

// makeEvidenceInfeasible raises Class 0's Main minimum above 1 by margin.
func makeEvidenceInfeasible(p LinearProblem, margin float64) LinearProblem {
	out := cloneLinearProblem(p)
	for i, row := range out.Rows {
		if row.ID == "c0000:mainmin" {
			out.Rows[i].RHS = 1 + margin
		}
	}
	return out
}

// profileToyProblem mirrors BuildMainProfileDeviationProblem for one Class
// with two Main Groups, t = g1+g2 = 1, prefer ½/½ and Σd ≤ δ·t.
func profileToyProblem(delta float64) LinearProblem {
	term := func(id string, c float64) LinearTerm { return LinearTerm{Variable: VariableID(id), Coeff: c} }
	return LinearProblem{
		Variables: []LinearVariable{
			{ID: "d1", Lower: 0, Upper: math.Inf(1)}, {ID: "d2", Lower: 0, Upper: math.Inf(1)},
			{ID: "g1", Lower: 0, Upper: 1}, {ID: "g2", Lower: 0, Upper: 1},
		},
		Rows: []LinearRow{
			{ID: "norm", Sense: SenseEQ, RHS: 1, Terms: []LinearTerm{term("g1", 1), term("g2", 1)}},
			{ID: "pos1", Sense: SenseGE, RHS: 0, Terms: []LinearTerm{term("d1", 1), term("g1", -0.5), term("g2", 0.5)}},
			{ID: "neg1", Sense: SenseGE, RHS: 0, Terms: []LinearTerm{term("d1", 1), term("g1", 0.5), term("g2", -0.5)}},
			{ID: "pos2", Sense: SenseGE, RHS: 0, Terms: []LinearTerm{term("d2", 1), term("g1", 0.5), term("g2", -0.5)}},
			{ID: "neg2", Sense: SenseGE, RHS: 0, Terms: []LinearTerm{term("d2", 1), term("g1", -0.5), term("g2", 0.5)}},
			{ID: "lock", Sense: SenseLE, RHS: 0, Terms: []LinearTerm{term("d1", 1), term("d2", 1), term("g1", -delta), term("g2", -delta)}},
		},
	}
}

// evidenceFixtureClass is one Class of minimumCV450BucketsFixture.
func evidenceFixtureClass(i int, probability float64) PreparedClass {
	n := 4 + i%2
	means := make([]float64, n)
	seconds := make([]float64, n)
	for j := range means {
		means[j] = float64(j)
		seconds[j] = means[j] * means[j]
	}
	others := []int{3}
	if n == 5 {
		others = append(others, 4)
	}
	class := modelTestIntentClass(i, fmt.Sprintf("class-%03d", i), probability, means, seconds, [][]int{{0, 1}, {2}}, others)
	class.Design.Exp = float64(n-1) / 2
	class.Groups[0].PreferShare = 2.0 / 3
	class.Groups[1].PreferShare = 1.0 / 3
	return class
}

// evidenceSkewedClass is the P00 E4 model: Main minimum 0.9 and prefer
// 0.05/0.95; its exact Main-profile optimum is δ* = 0.4.
func evidenceSkewedClass(i int, probability float64) PreparedClass {
	class := evidenceFixtureClass(i, probability)
	class.Design.Subjective.MainExperience.Probability = NumericRange{Min: 0.9, Max: 1}
	class.Groups[0].PreferShare, class.Groups[1].PreferShare = 0.05, 0.95
	return class
}

func evidenceCompile(t *testing.T, cv NumericRange, classes ...PreparedClass) CompiledModel {
	t.Helper()
	return modelTestCompile(t, modelTestPrepared(OverallIntent{CV: cv}, classes...))
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

// evidenceToleranceGapModel puts the CV maximum the given relative gap below the
// exact minimum second moment, so only replay tolerance can make it feasible.
func evidenceToleranceGapModel(t *testing.T, gap float64) CompiledModel {
	classes := []PreparedClass{evidenceFixtureClass(0, 0.5), evidenceFixtureClass(1, 0.3), evidenceFixtureClass(2, 0.2)}
	wide := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, classes...)
	lo := exactMinimumSecondMoment(t, wide)
	mu := wide.Prepared.ExpectedRTP()
	return evidenceCompile(t, NumericRange{Min: 0, Max: math.Sqrt(lo*(1-gap)/(mu*mu) - 1)}, classes...)
}

// exactMinimumSecondMoment is the exact minimum of the CV-row expression over the
// unrelaxed hard model (exact simplex, no tolerance).
func exactMinimumSecondMoment(t *testing.T, compiled CompiledModel) float64 {
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
