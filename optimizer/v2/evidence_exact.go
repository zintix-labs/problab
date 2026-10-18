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
	"math/big"
)

// exactBudget bounds one exact simplex call (techspec §8.2). Termination
// under Bland's rule is only a theoretical guarantee; these limits make the
// work bounded in practice. Exhaustion is "no evidence", never infeasibility.
type exactBudget struct {
	maxPivots int
	maxBits   int
	// usage, when set, records the run's call count and largest pivot count
	// and number size, so reports can show how much of the budget real
	// models consume.
	usage *EvidenceCounters
	// stage, when set, records the same peaks for the current stage only:
	// a maximum cannot be recovered by subtracting two run maxima.
	stage *exactPeak
}

// exactPeak is the largest pivot count and number size of a set of calls.
type exactPeak struct {
	pivots int
	bits   int
}

func (p *exactPeak) observe(pivots, bits int) {
	p.pivots = max(p.pivots, pivots)
	p.bits = max(p.bits, bits)
}

var defaultExactBudget = exactBudget{maxPivots: 20000, maxBits: 1 << 14}

// boundCheck is the exact result of evaluating one multiplier vector.
type boundCheck struct {
	valid  bool
	reason string
	value  *big.Rat // exact upper bound of max cᵀx over the model
	rows   []int    // model rows with y > 0
	vars   []int    // variables whose bound is used (z ≠ 0)
}

// upperBoundRat verifies y against an all-"≤" model: for y ≥ 0 every row
// gives yᵢaᵢᵀx ≤ yᵢbᵢ, so with z = c − Aᵀy,
// cᵀx ≤ Σⱼ max(zⱼl̃ⱼ, zⱼũⱼ) + yᵀb for every x in the model. Non-positive
// entries of y are ignored, which is itself a valid multiplier choice, so the
// result never depends on where y came from.
func upperBoundRat(model exactModel, c map[int]*big.Rat, y []*big.Rat) boundCheck {
	if len(y) != len(model.rows) {
		return boundCheck{reason: "multiplier length mismatch"}
	}
	z := make([]*big.Rat, len(model.ids))
	for j := range z {
		z[j] = new(big.Rat)
		if cj, ok := c[j]; ok {
			z[j].Set(cj)
		}
	}
	value := new(big.Rat)
	var rows []int
	for i, row := range model.rows {
		if y[i] == nil || y[i].Sign() <= 0 {
			continue
		}
		rows = append(rows, i)
		value = ratAdd(value, ratMul(y[i], row.rhs))
		for j, a := range row.coef {
			z[j] = ratSub(z[j], ratMul(y[i], a))
		}
	}
	var vars []int
	for j, zj := range z {
		switch zj.Sign() {
		case 1:
			if model.upper[j] == nil {
				return boundCheck{reason: "positive reduced coefficient on an unbounded variable"}
			}
			value = ratAdd(value, ratMul(zj, model.upper[j]))
			vars = append(vars, j)
		case -1:
			value = ratAdd(value, ratMul(zj, model.lower[j]))
			vars = append(vars, j)
		}
	}
	return boundCheck{valid: true, value: value, rows: rows, vars: vars}
}

// lowerBoundRat is the exact lower bound of min cᵀx: −upperBoundRat(M, −c, y).
func lowerBoundRat(model exactModel, c map[int]*big.Rat, y []*big.Rat) boundCheck {
	negated := make(map[int]*big.Rat, len(c))
	for j, a := range c {
		negated[j] = ratNeg(a)
	}
	check := upperBoundRat(model, negated, y)
	if check.valid {
		check.value = ratNeg(check.value)
	}
	return check
}

// verifyFarkas reports whether y proves the model empty (c = 0, bound < 0).
func verifyFarkas(model exactModel, y []*big.Rat) boundCheck {
	check := upperBoundRat(model, nil, y)
	if check.valid && check.value.Sign() >= 0 {
		check.valid = false
		check.reason = "bound is not strictly negative"
	}
	return check
}

type exactStatus string

const (
	exactOptimal   exactStatus = "optimal"
	exactUnbounded exactStatus = "unbounded"
	exactBudgetHit exactStatus = "budget"
	exactCanceled  exactStatus = "canceled"
)

// exactTableau is a dense min-cost simplex over big.Rat with Bland's rule.
type exactTableau struct {
	rows  [][]*big.Rat
	rhs   []*big.Rat
	cost  []*big.Rat
	basis []int
}

func (t *exactTableau) reducedCost(k int) *big.Rat {
	r := new(big.Rat).Set(t.cost[k])
	for i := range t.rows {
		if t.rows[i][k].Sign() != 0 {
			r.Sub(r, new(big.Rat).Mul(t.cost[t.basis[i]], t.rows[i][k]))
		}
	}
	return r
}

func (t *exactTableau) maxBits() int {
	worst := 0
	check := func(x *big.Rat) {
		if b := x.Num().BitLen(); b > worst {
			worst = b
		}
		if b := x.Denom().BitLen(); b > worst {
			worst = b
		}
	}
	for i := range t.rows {
		check(t.rhs[i])
		for _, x := range t.rows[i] {
			check(x)
		}
	}
	return worst
}

// solve runs primal simplex from a feasible identity basis.
func (t *exactTableau) solve(ctx context.Context, budget exactBudget) (status exactStatus, x []*big.Rat, pivots int) {
	// bits is the size of the current tableau, measured once per tableau;
	// peakBits is the largest over every tableau this call produced, which
	// can exceed the final one.
	bits := t.maxBits()
	peakBits := bits
	if budget.usage != nil || budget.stage != nil {
		defer func() {
			if budget.usage != nil {
				budget.usage.ExactCalls++
				budget.usage.MaxExactPivots = max(budget.usage.MaxExactPivots, pivots)
				budget.usage.MaxExactBits = max(budget.usage.MaxExactBits, peakBits)
			}
			if budget.stage != nil {
				budget.stage.observe(pivots, peakBits)
			}
		}()
	}
	for {
		if ctx.Err() != nil {
			return exactCanceled, nil, pivots
		}
		if pivots >= budget.maxPivots || bits > budget.maxBits {
			return exactBudgetHit, nil, pivots
		}
		enter := -1
		for k := range t.cost {
			if t.reducedCost(k).Sign() < 0 {
				enter = k // Bland: smallest index
				break
			}
		}
		if enter < 0 {
			x := make([]*big.Rat, len(t.cost))
			for k := range x {
				x[k] = new(big.Rat)
			}
			for i, b := range t.basis {
				x[b].Set(t.rhs[i])
			}
			return exactOptimal, x, pivots
		}
		leave := -1
		var best *big.Rat
		for i := range t.rows {
			if t.rows[i][enter].Sign() <= 0 {
				continue
			}
			ratio := new(big.Rat).Quo(t.rhs[i], t.rows[i][enter])
			if leave < 0 || ratio.Cmp(best) < 0 || (ratio.Cmp(best) == 0 && t.basis[i] < t.basis[leave]) {
				leave, best = i, ratio
			}
		}
		if leave < 0 {
			return exactUnbounded, nil, pivots
		}
		t.pivot(leave, enter)
		pivots++
		bits = t.maxBits()
		peakBits = max(peakBits, bits)
	}
}

func (t *exactTableau) pivot(r, k int) {
	p := new(big.Rat).Set(t.rows[r][k])
	for j := range t.rows[r] {
		if t.rows[r][j].Sign() != 0 {
			t.rows[r][j].Quo(t.rows[r][j], p)
		}
	}
	t.rhs[r].Quo(t.rhs[r], p)
	for i := range t.rows {
		if i == r || t.rows[i][k].Sign() == 0 {
			continue
		}
		f := new(big.Rat).Set(t.rows[i][k])
		for j := range t.rows[i] {
			if t.rows[r][j].Sign() != 0 {
				t.rows[i][j].Sub(t.rows[i][j], new(big.Rat).Mul(f, t.rows[r][j]))
			}
		}
		t.rhs[i].Sub(t.rhs[i], new(big.Rat).Mul(f, t.rhs[r]))
	}
	t.basis[r] = k
}

// multiplierTableau builds min Σ ũⱼz⁺ⱼ − l̃ⱼz⁻ⱼ + bᵀy over y, z⁺, z⁻ ≥ 0 with
// Aᵀy + z⁺ − z⁻ = c [and Σy + s = 1 when normalized]. Rows with cⱼ < 0 are
// negated so z⁻ⱼ is basic; the identity start basis is feasible.
func multiplierTableau(model exactModel, c map[int]*big.Rat, normalize bool) *exactTableau {
	mr, nv := len(model.rows), len(model.ids)
	cols := mr + 2*nv
	if normalize {
		cols++
	}
	t := &exactTableau{cost: make([]*big.Rat, cols)}
	for k := range t.cost {
		t.cost[k] = new(big.Rat)
	}
	for i, row := range model.rows {
		t.cost[i].Set(row.rhs)
	}
	for j := 0; j < nv; j++ {
		t.cost[mr+j].Set(model.upper[j])
		t.cost[mr+nv+j].Neg(model.lower[j])
	}
	newRow := func() []*big.Rat {
		row := make([]*big.Rat, cols)
		for k := range row {
			row[k] = new(big.Rat)
		}
		return row
	}
	for j := 0; j < nv; j++ {
		row := newRow()
		for i, modelRow := range model.rows {
			if a, ok := modelRow.coef[j]; ok {
				row[i].Set(a)
			}
		}
		row[mr+j].SetInt64(1)
		row[mr+nv+j].SetInt64(-1)
		rhs := new(big.Rat)
		if cj, ok := c[j]; ok {
			rhs.Set(cj)
		}
		basic := mr + j
		if rhs.Sign() < 0 {
			for k := range row {
				row[k].Neg(row[k])
			}
			rhs.Neg(rhs)
			basic = mr + nv + j
		}
		t.rows = append(t.rows, row)
		t.rhs = append(t.rhs, rhs)
		t.basis = append(t.basis, basic)
	}
	if normalize {
		row := newRow()
		for i := 0; i < mr; i++ {
			row[i].SetInt64(1)
		}
		row[cols-1].SetInt64(1)
		t.rows = append(t.rows, row)
		t.rhs = append(t.rhs, big.NewRat(1, 1))
		t.basis = append(t.basis, cols-1)
	}
	return t
}

// exactResult is one exact acquisition. A failed acquisition is always safe
// to report as "no evidence": correctness rests on the verifier.
type exactResult struct {
	status      exactStatus
	check       boundCheck
	multipliers []*big.Rat // y, one per model row, when optimal
	pivots      int
	// nonEmpty is set when the normalized Farkas optimum is ≥ 0: by LP
	// duality no certificate exists, i.e. the model itself is non-empty.
	nonEmpty bool
}

// acquireFarkas solves the normalized Farkas LP exactly.
func acquireFarkas(ctx context.Context, model exactModel, budget exactBudget) exactResult {
	tableau := multiplierTableau(model, nil, true)
	status, x, pivots := tableau.solve(ctx, budget)
	result := exactResult{status: status, pivots: pivots}
	if status != exactOptimal {
		// With Σy ≤ 1 the LP is bounded unless a box is empty, which
		// buildEnvelope already reports; any other status is no evidence.
		result.check = boundCheck{reason: "exact Farkas LP " + string(status)}
		return result
	}
	result.multipliers = x[:len(model.rows)]
	result.check = verifyFarkas(model, result.multipliers)
	if !result.check.valid {
		result.nonEmpty = true
	}
	return result
}

// acquireUpperBound returns the exact optimum of max cᵀx over the model via
// its dual, with the multipliers that certify it.
func acquireUpperBound(ctx context.Context, model exactModel, c map[int]*big.Rat, budget exactBudget) exactResult {
	tableau := multiplierTableau(model, c, false)
	status, x, pivots := tableau.solve(ctx, budget)
	result := exactResult{status: status, pivots: pivots}
	if status != exactOptimal {
		result.check = boundCheck{reason: "exact dual LP " + string(status)}
		return result
	}
	result.multipliers = x[:len(model.rows)]
	result.check = upperBoundRat(model, c, result.multipliers)
	return result
}

// acquireLowerBound returns the exact optimum of min cᵀx over the model.
func acquireLowerBound(ctx context.Context, model exactModel, c map[int]*big.Rat, budget exactBudget) exactResult {
	negated := make(map[int]*big.Rat, len(c))
	for j, a := range c {
		negated[j] = ratNeg(a)
	}
	result := acquireUpperBound(ctx, model, negated, budget)
	if result.check.valid {
		result.check = lowerBoundRat(model, c, result.multipliers)
	}
	return result
}

// exactPoint returns an exact point of a non-empty model from the simplex
// multipliers of the optimal normalized Farkas LP, whose dual is
// max −t s.t. Ax ≤ b + t, l ≤ x ≤ u, t ≥ 0: a zero optimum gives a point with
// t = 0, and row j (rhs 0, basis z⁺ⱼ) has multiplier xⱼ = ũⱼ − r(z⁺ⱼ).
func exactPoint(ctx context.Context, model exactModel, budget exactBudget) ([]*big.Rat, exactStatus) {
	tableau := multiplierTableau(model, nil, true)
	status, x, _ := tableau.solve(ctx, budget)
	if status != exactOptimal {
		return nil, status
	}
	if check := verifyFarkas(model, x[:len(model.rows)]); check.valid {
		return nil, exactOptimal // model empty: no point exists
	}
	mr, nv := len(model.rows), len(model.ids)
	point := make([]*big.Rat, nv)
	for j := 0; j < nv; j++ {
		point[j] = ratSub(model.upper[j], tableau.reducedCost(mr+j))
	}
	if !model.contains(point) {
		return nil, exactOptimal
	}
	return point, exactOptimal
}

// exactOptimum returns an exact maximizer of cᵀx over a non-empty model. The
// dual LP of acquireUpperBound has max cᵀx s.t. Ax ≤ b, l ≤ x ≤ u as its own
// dual: a row kept as is (basis z⁺ⱼ) gives xⱼ = ũⱼ − r(z⁺ⱼ); a row negated
// because cⱼ < 0 (basis z⁻ⱼ) gives xⱼ = l̃ⱼ + r(z⁻ⱼ).
func exactOptimum(ctx context.Context, model exactModel, c map[int]*big.Rat, budget exactBudget) ([]*big.Rat, exactStatus) {
	tableau := multiplierTableau(model, c, false)
	status, _, _ := tableau.solve(ctx, budget)
	if status != exactOptimal {
		return nil, status
	}
	mr, nv := len(model.rows), len(model.ids)
	point := make([]*big.Rat, nv)
	for j := 0; j < nv; j++ {
		if cj, ok := c[j]; ok && cj.Sign() < 0 {
			point[j] = ratAdd(model.lower[j], tableau.reducedCost(mr+nv+j))
		} else {
			point[j] = ratSub(model.upper[j], tableau.reducedCost(mr+j))
		}
	}
	if !model.contains(point) {
		return nil, exactOptimal
	}
	return point, exactOptimal
}

// ratsToFloats converts an exact point to float64 (round to nearest).
func ratsToFloats(point []*big.Rat) []float64 {
	out := make([]float64, len(point))
	for j, x := range point {
		out[j], _ = x.Float64()
	}
	return out
}
