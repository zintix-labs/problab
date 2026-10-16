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
	"errors"
	"fmt"
	"math"
	"math/big"
)

// The exact evidence layer (techspec class-decomposition-and-verified-lp-
// evidence §4) works on polyhedra with big.Rat data built from the float64
// semantic model. The outer envelope provably contains every point that
// replaySemanticSolution accepts, so an exact Farkas certificate on it proves
// that no point can pass replay.
//
// Floating-point model (binary64, round to nearest, with underflow):
//   - multiplication and FMA: fl(op) = op(1+δ) + e, |δ| ≤ u, |e| ≤ η/2;
//   - addition and subtraction: fl(a±b) = (a±b)(1+δ); a subnormal result of
//     an addition or subtraction is exact, so there is no e term;
//   - comparison, abs and max are exact.
// with u = 2⁻⁵³ and η = 2⁻¹⁰⁷⁴. Overflow needs no assumption: replay rejects
// every non-finite intermediate, and +Inf/NaN never become finite again, so an
// accepted point had finite intermediates throughout.
//
//  1. Bounds: replay accepts fl(l−x) ≤ fl(τ·M) with M ≥ 1. Subtraction has no
//     e term, and τ is normal with M ≥ 1 so τ·M cannot underflow; hence
//     l − x ≤ τ''·M with τ'' = τ(1+u)/(1−u). No η term is needed.
//  2. A row sum of n products, possibly fused: |fl(lhs) − lhs| ≤
//     γₙ·Σ|aⱼ||xⱼ| + (n·η/2)(1+u)ⁿ⁻¹ ≤ γₙ·Σ|aⱼ||xⱼ| + n·η, which needs
//     (1+u)ⁿ⁻¹ ≤ 2; the strict n·u < 1/2 guarantees it and also γₙ < 1.
//  3. Row violation: v = fl(lhs) − b > 0 is accepted when fl(v) ≤ fl(τ·Mᵣ),
//     Mᵣ = max(1,|fl(lhs)|,|b|) ≤ C + v with C = max(1,|b|). Both operations
//     carry no e term, so v ≤ τ''(C+v), i.e. v ≤ τ''·C/(1−τ'').
//  4. Derived upper bound of +Inf variables uses inequality 2 inside a row the
//     replay must accept (deriveUpperBounds).

var (
	ratUnitRoundoff = new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Lsh(big.NewInt(1), 53))
	ratUnderflowEta = new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Lsh(big.NewInt(1), 1074))
	ratOne          = big.NewRat(1, 1)
	ratHalf         = big.NewRat(1, 2)
	smallestNormal  = math.Float64frombits(0x0010000000000000) // 2⁻¹⁰²²
)

func ratOf(value float64) *big.Rat { return new(big.Rat).SetFloat64(value) }
func ratAdd(a, b *big.Rat) *big.Rat { return new(big.Rat).Add(a, b) }
func ratSub(a, b *big.Rat) *big.Rat { return new(big.Rat).Sub(a, b) }
func ratMul(a, b *big.Rat) *big.Rat { return new(big.Rat).Mul(a, b) }
func ratQuo(a, b *big.Rat) *big.Rat { return new(big.Rat).Quo(a, b) }
func ratAbs(a *big.Rat) *big.Rat    { return new(big.Rat).Abs(a) }
func ratNeg(a *big.Rat) *big.Rat    { return new(big.Rat).Neg(a) }

func ratMax(values ...*big.Rat) *big.Rat {
	out := new(big.Rat).Set(values[0])
	for _, value := range values[1:] {
		if value.Cmp(out) > 0 {
			out.Set(value)
		}
	}
	return out
}

// effectiveTau returns τ'' = τ(1+u)/(1−u) exactly.
func effectiveTau(tau float64) *big.Rat {
	return ratQuo(ratMul(ratOf(tau), ratAdd(ratOne, ratUnitRoundoff)), ratSub(ratOne, ratUnitRoundoff))
}

// validateEvidenceTolerance enforces the two preconditions of the envelope
// derivation on the configured feasibility tolerance: τ is a normal float64
// and τ'' < 1.
func validateEvidenceTolerance(tau float64) error {
	if !isFinite(tau) || tau < smallestNormal {
		return fmt.Errorf("must be a normal float64 (at least 2^-1022) for verified LP evidence")
	}
	if effectiveTau(tau).Cmp(ratOne) >= 0 {
		return fmt.Errorf("must satisfy tau*(1+u)/(1-u) < 1 for verified LP evidence")
	}
	return nil
}

// gammaN returns γₙ = n·u/(1−n·u) and whether the strict precondition
// n·u < 1/2 of the rounding derivation holds.
func gammaN(n int) (*big.Rat, bool) {
	nu := ratMul(big.NewRat(int64(n), 1), ratUnitRoundoff)
	if nu.Cmp(ratHalf) >= 0 {
		return nil, false
	}
	return ratQuo(nu, ratSub(ratOne, nu)), true
}

// exactRow is one "≤" inequality of an exact model. Equality rows of the
// semantic problem become one LE side and one GE side (negated).
type exactRow struct {
	source int // index into the semantic problem's rows
	side   Sense
	rhs    *big.Rat
	coef   map[int]*big.Rat
	terms  int
}

// exactModel is a polyhedron over the semantic variables. A nil upper means
// +Inf; lower is always finite (LinearProblem contract).
type exactModel struct {
	ids   []VariableID
	lower []*big.Rat
	upper []*big.Rat
	rows  []exactRow
}

// envelopeReport records derived bounds so diagnostics can name their rows.
type envelopeReport struct {
	derivedFrom map[int]int // variable -> source row index
	unbounded   []VariableID
}

// emptyBoxError proves infeasibility: the relaxed lower bound of one variable
// exceeds its (possibly derived) upper bound, so no point passes replay.
type emptyBoxError struct {
	variable  int
	id        VariableID
	sourceRow int // -1 when the upper bound is the variable's own
}

func (e *emptyBoxError) Error() string {
	return fmt.Sprintf("empty envelope box for variable %q", e.id)
}

var (
	errEnvelopeUnbounded     = errors.New("no finite safe upper bound for an unbounded variable")
	errRoundingInapplicable  = errors.New("rounding derivation precondition n*u < 1/2 does not hold")
	errEnvelopeNonFiniteData = errors.New("semantic problem contains non-finite data")
)

type envelopeKind struct {
	boundAllowance func(scale *big.Rat) *big.Rat
	rowSlack       func(c *big.Rat) *big.Rat
	rounding       bool
	fixedUpper     []*big.Rat
}

// buildEnvelope constructs the outer envelope M̄ for replay tolerance tau.
func buildEnvelope(problem LinearProblem, tau float64) (exactModel, envelopeReport, error) {
	if err := validateEvidenceTolerance(tau); err != nil {
		return exactModel{}, envelopeReport{}, err
	}
	tauEff := effectiveTau(tau)
	return buildExactModel(problem, envelopeKind{
		boundAllowance: func(scale *big.Rat) *big.Rat { return ratMul(tauEff, scale) },
		rowSlack:       func(c *big.Rat) *big.Rat { return ratQuo(ratMul(tauEff, c), ratSub(ratOne, tauEff)) },
		rounding:       true,
	})
}

// buildInnerModel is the diagnostic polyhedron slightly inside the replay
// set (slack τ(1−η')·max(1,|·|), no rounding allowance). Points found in it
// are candidates only and always go through replay. +Inf upper bounds reuse
// the envelope's derived bounds, which every replay-accepted point satisfies.
func buildInnerModel(problem LinearProblem, tau float64) (exactModel, error) {
	outer, _, err := buildEnvelope(problem, tau)
	if err != nil {
		return exactModel{}, err
	}
	shrunk := ratMul(ratOf(tau), ratSub(ratOne, big.NewRat(1, 10000)))
	model, _, err := buildExactModel(problem, envelopeKind{
		boundAllowance: func(scale *big.Rat) *big.Rat { return ratMul(shrunk, scale) },
		rowSlack:       func(c *big.Rat) *big.Rat { return ratMul(shrunk, c) },
		fixedUpper:     outer.upper,
	})
	return model, err
}

func buildExactModel(problem LinearProblem, kind envelopeKind) (exactModel, envelopeReport, error) {
	report := envelopeReport{derivedFrom: map[int]int{}}
	model := exactModel{
		ids:   make([]VariableID, len(problem.Variables)),
		lower: make([]*big.Rat, len(problem.Variables)),
		upper: make([]*big.Rat, len(problem.Variables)),
	}
	index := make(map[VariableID]int, len(problem.Variables))
	for j, variable := range problem.Variables {
		if !isFinite(variable.Lower) || math.IsNaN(variable.Upper) || math.IsInf(variable.Upper, -1) {
			return exactModel{}, report, errEnvelopeNonFiniteData
		}
		model.ids[j] = variable.ID
		index[variable.ID] = j
		scale := ratMax(ratOne, ratAbs(ratOf(variable.Lower)))
		if !math.IsInf(variable.Upper, 1) {
			scale = ratMax(scale, ratAbs(ratOf(variable.Upper)))
		}
		allowance := kind.boundAllowance(scale)
		model.lower[j] = ratSub(ratOf(variable.Lower), allowance)
		switch {
		case !math.IsInf(variable.Upper, 1):
			model.upper[j] = ratAdd(ratOf(variable.Upper), allowance)
		case kind.fixedUpper != nil && kind.fixedUpper[j] != nil:
			model.upper[j] = new(big.Rat).Set(kind.fixedUpper[j])
		}
	}
	for i, row := range problem.Rows {
		if !isFinite(row.RHS) {
			return exactModel{}, report, errEnvelopeNonFiniteData
		}
		coef := make(map[int]*big.Rat, len(row.Terms))
		for _, term := range row.Terms {
			if !isFinite(term.Coeff) {
				return exactModel{}, report, errEnvelopeNonFiniteData
			}
			j, ok := index[term.Variable]
			if !ok {
				return exactModel{}, report, fmt.Errorf("row %q references unknown variable %q", row.ID, term.Variable)
			}
			if previous, duplicate := coef[j]; duplicate {
				coef[j] = ratAdd(previous, ratOf(term.Coeff))
			} else {
				coef[j] = ratOf(term.Coeff)
			}
		}
		b := ratOf(row.RHS)
		slack := kind.rowSlack(ratMax(ratOne, ratAbs(b)))
		if row.Sense == SenseLE || row.Sense == SenseEQ {
			model.rows = append(model.rows, exactRow{source: i, side: SenseLE, rhs: ratAdd(b, slack), coef: coef, terms: len(row.Terms)})
		}
		if row.Sense == SenseGE || row.Sense == SenseEQ {
			negated := make(map[int]*big.Rat, len(coef))
			for j, a := range coef {
				negated[j] = ratNeg(a)
			}
			model.rows = append(model.rows, exactRow{source: i, side: SenseGE, rhs: ratAdd(ratNeg(b), slack), coef: negated, terms: len(row.Terms)})
		}
	}
	if kind.rounding {
		if err := deriveUpperBounds(&model, &report); err != nil {
			return exactModel{}, report, err
		}
	}
	for j := range model.upper {
		if model.upper[j] == nil {
			report.unbounded = append(report.unbounded, model.ids[j])
			continue
		}
		if model.upper[j].Cmp(model.lower[j]) < 0 {
			source, ok := report.derivedFrom[j]
			if !ok {
				source = -1
			}
			return exactModel{}, report, &emptyBoxError{variable: j, id: model.ids[j], sourceRow: source}
		}
	}
	if len(report.unbounded) > 0 {
		return exactModel{}, report, errEnvelopeUnbounded
	}
	if kind.rounding {
		for r := range model.rows {
			row := &model.rows[r]
			gamma, ok := gammaN(row.terms)
			if !ok {
				return exactModel{}, report, errRoundingInapplicable
			}
			sum := new(big.Rat)
			for j, a := range row.coef {
				sum = ratAdd(sum, ratMul(ratAbs(a), ratMax(ratAbs(model.lower[j]), ratAbs(model.upper[j]))))
			}
			eta := ratMul(big.NewRat(int64(row.terms), 1), ratUnderflowEta)
			row.rhs = ratAdd(row.rhs, ratAdd(ratMul(gamma, sum), eta))
		}
	}
	return model, report, nil
}

// deriveUpperBounds replaces +Inf upper bounds with bounds that every
// replay-accepted point satisfies. For a "≤" form Σ aⱼxⱼ ≤ R (R already holds
// the row slack) where every +Inf variable U has aⱼ > 0 and every other
// variable O is boxed, inequality 2 of the derivation gives
//
//	T + m_O ≤ R + γ(T + P + K_O) + n·η,  T = Σ_U aⱼxⱼ,
//	P = Σ_U 2aⱼ·max(0,−l̃ⱼ) (since |xⱼ| ≤ xⱼ + 2max(0,−l̃ⱼ)),
//	K_O = Σ_O |aⱼ|·Xⱼ,  m_O = box minimum of Σ_O aⱼxⱼ,
//
// so T ≤ (R + n·η + γ(P + K_O) − m_O)/(1 − γ), with γ < 1 guaranteed by
// n·u < 1/2, and xₖ ≤ (T_max − Σ_{U∖k} aⱼl̃ⱼ)/aₖ.
func deriveUpperBounds(model *exactModel, report *envelopeReport) error {
	for changed := true; changed; {
		changed = false
		for _, row := range model.rows {
			var unbounded []int
			usable := true
			for j, a := range row.coef {
				if a.Sign() == 0 || model.upper[j] != nil {
					continue
				}
				if a.Sign() < 0 {
					usable = false
					break
				}
				unbounded = append(unbounded, j)
			}
			if !usable || len(unbounded) == 0 {
				continue
			}
			gamma, ok := gammaN(row.terms)
			if !ok {
				return errRoundingInapplicable
			}
			isUnbounded := make(map[int]bool, len(unbounded))
			for _, j := range unbounded {
				isUnbounded[j] = true
			}
			p, k, minimum := new(big.Rat), new(big.Rat), new(big.Rat)
			for j, a := range row.coef {
				if a.Sign() == 0 {
					continue
				}
				if isUnbounded[j] {
					if model.lower[j].Sign() < 0 {
						p = ratAdd(p, ratMul(ratMul(big.NewRat(2, 1), a), ratAbs(model.lower[j])))
					}
					continue
				}
				k = ratAdd(k, ratMul(ratAbs(a), ratMax(ratAbs(model.lower[j]), ratAbs(model.upper[j]))))
				if a.Sign() > 0 {
					minimum = ratAdd(minimum, ratMul(a, model.lower[j]))
				} else {
					minimum = ratAdd(minimum, ratMul(a, model.upper[j]))
				}
			}
			eta := ratMul(big.NewRat(int64(row.terms), 1), ratUnderflowEta)
			numerator := ratSub(ratAdd(ratAdd(row.rhs, eta), ratMul(gamma, ratAdd(p, k))), minimum)
			tMax := ratQuo(numerator, ratSub(ratOne, gamma))
			lowerSum := new(big.Rat)
			for _, j := range unbounded {
				lowerSum = ratAdd(lowerSum, ratMul(row.coef[j], model.lower[j]))
			}
			for _, j := range unbounded {
				others := ratSub(lowerSum, ratMul(row.coef[j], model.lower[j]))
				bound := ratQuo(ratSub(tMax, others), row.coef[j])
				if model.upper[j] == nil || bound.Cmp(model.upper[j]) < 0 {
					model.upper[j] = bound
					report.derivedFrom[j] = row.source
					changed = true
				}
			}
		}
	}
	return nil
}

// containsFloat reports whether x lies in the model (exact check).
func (m exactModel) containsFloat(x []float64) bool {
	point := make([]*big.Rat, len(x))
	for j, value := range x {
		if !isFinite(value) {
			return false
		}
		point[j] = ratOf(value)
	}
	return m.contains(point)
}

// contains reports whether an exact point lies in the model.
func (m exactModel) contains(x []*big.Rat) bool {
	for j := range m.ids {
		if x[j].Cmp(m.lower[j]) < 0 || (m.upper[j] != nil && x[j].Cmp(m.upper[j]) > 0) {
			return false
		}
	}
	for _, row := range m.rows {
		lhs := new(big.Rat)
		for j, a := range row.coef {
			lhs = ratAdd(lhs, ratMul(a, x[j]))
		}
		if lhs.Cmp(row.rhs) > 0 {
			return false
		}
	}
	return true
}
