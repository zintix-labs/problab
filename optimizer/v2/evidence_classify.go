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
	"fmt"
	"math"
	"math/big"
	"time"
)

// evidenceClass is the three-way outcome of techspec §2: a replay-accepted
// witness, a verified certificate, or neither. A backend status alone never
// decides it.
type evidenceClass string

const (
	evidenceFeasible   evidenceClass = "FEASIBLE"
	evidenceInfeasible evidenceClass = "INFEASIBLE"
	evidenceUnknown    evidenceClass = "UNKNOWN"
)

// Witness sources, in their fixed order (techspec §6.2).
const (
	witnessPlain = "plain"
	witnessGap   = "gap"
	witnessExact = "exact"
)

var witnessOrder = []string{witnessPlain, witnessGap, witnessExact}

// Reasons recorded when no evidence is found.
const (
	reasonEnvelopeNonEmpty     = "envelope-nonempty"
	reasonBudget               = "budget"
	reasonUnboundedVariable    = "unbounded-variable"
	reasonRoundingPrecondition = "rounding-precondition"
)

// Infeasibility evidence kinds (techspec §9.3).
const (
	evidenceKindFarkas     = "A-farkas"
	evidenceKindEmptyBox   = "B-empty-box"
	evidenceKindCVDisjoint = "C-cv-disjoint"
)

// rowSide names one "≤" form of a semantic row: the row itself (LE side) or
// its negation (GE side). Equality rows have both.
type rowSide struct {
	row  int
	side Sense
}

// farkasEvidence is a type-A certificate: one exact multiplier per envelope
// row of the bound problem.
type farkasEvidence struct {
	rows        []rowSide
	multipliers []*big.Rat
}

// infeasibilityEvidence proves that one bound problem has no replay-accepted
// point. Exactly one of the kind-specific fields is set.
type infeasibilityEvidence struct {
	kind     string
	problem  LinearProblem
	farkas   *farkasEvidence
	emptyBox *emptyBoxError
	cv       *cvDisjointEvidence
	class    int // 1-based Class for kinds A/B found on a Class subproblem
}

// classification is the result of one evidence decision.
type classification struct {
	class    evidenceClass
	witness  []float64
	source   string
	evidence *infeasibilityEvidence
	reasons  []string
}

func (c classification) reasonText() string {
	if len(c.reasons) == 0 {
		return ""
	}
	text := c.reasons[0]
	for _, reason := range c.reasons[1:] {
		text += "; " + reason
	}
	return text
}

func (r *engineRun) budget() exactBudget {
	if r.engine.exactBudget != nil {
		return *r.engine.exactBudget
	}
	return defaultExactBudget
}

func (r *engineRun) tau() float64 { return r.options.FeasibilityTolerance }

// replayAccepts is the single acceptance test for every witness candidate.
func (r *engineRun) replayAccepts(problem LinearProblem, values []float64) bool {
	if len(values) != len(problem.Variables) {
		return false
	}
	_, _, ok := replaySemanticSolution(problem, values, r.tau())
	return ok
}

// classify decides one feasibility question with evidence only (techspec
// §6.2). The returned error is non-nil only for cancellation or a broken
// solver contract; every numerical outcome is a classification.
func (r *engineRun) classify(ctx context.Context, problem LinearProblem, where solveContext, origin ObjectiveOrigin) (classification, error) {
	var out classification
	for _, source := range witnessOrder {
		if err := ctx.Err(); err != nil {
			return classification{}, err
		}
		witness, reason, err := r.tryWitness(ctx, problem, where, origin, source)
		if err != nil {
			return classification{}, err
		}
		if reason != "" {
			out.reasons = append(out.reasons, source+": "+reason)
		}
		if witness != nil {
			r.counters.WitnessFound[source]++
			return classification{class: evidenceFeasible, witness: witness, source: source}, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return classification{}, err
	}
	evidence, reason, err := r.certifyInfeasible(ctx, problem)
	if err != nil {
		return classification{}, err
	}
	if evidence != nil {
		evidence.class = where.class
		return classification{class: evidenceInfeasible, evidence: evidence}, nil
	}
	out.class = evidenceUnknown
	out.reasons = append(out.reasons, "certificate: "+reason)
	return out, nil
}

// tryWitness runs one witness source and returns a replay-accepted point,
// or the reason it produced none.
func (r *engineRun) tryWitness(ctx context.Context, problem LinearProblem, where solveContext, origin ObjectiveOrigin, source string) ([]float64, string, error) {
	if r.counters.WitnessAttempts == nil {
		r.counters.WitnessAttempts, r.counters.WitnessFound = map[string]int{}, map[string]int{}
	}
	r.counters.WitnessAttempts[source]++
	switch source {
	case witnessPlain:
		where.source = witnessPlain
		r.at(where)
		result, err := r.Solve(ctx, problem, LinearObjective{Name: where.stage, Sense: Minimize, Origin: origin}, r.options)
		if err != nil {
			return nil, "", err
		}
		if result.Status != SolveOptimal {
			return nil, "backend " + solveStatusName(result.Status), nil
		}
		if !r.replayAccepts(problem, result.Values) {
			return nil, "witness failed replay", nil
		}
		return append([]float64(nil), result.Values...), "", nil
	case witnessGap:
		gap, objective, ok := gapProblem(problem, where.stage, origin)
		if !ok {
			return nil, "no inequality rows", nil
		}
		where.source = witnessGap
		r.at(where)
		result, err := r.Solve(ctx, gap, objective, r.options)
		if err != nil {
			return nil, "", err
		}
		if result.Status != SolveOptimal {
			return nil, "backend " + solveStatusName(result.Status), nil
		}
		candidate := append([]float64(nil), result.Values[:len(problem.Variables)]...)
		if !r.replayAccepts(problem, candidate) {
			return nil, "witness failed replay", nil
		}
		return candidate, "", nil
	case witnessExact:
		started := time.Now()
		defer func() {
			r.counters.ExactWitnesses++
			r.counters.ExactWitnessTime += time.Since(started)
		}()
		inner, err := buildInnerModel(problem, r.tau())
		if err != nil {
			return nil, envelopeReason(err), nil
		}
		point, status := exactPoint(ctx, inner, r.budget())
		switch status {
		case exactCanceled:
			return nil, "", ctx.Err()
		case exactBudgetHit:
			r.counters.BudgetExhausted++
			return nil, reasonBudget, nil
		}
		if point == nil {
			return nil, "inner model empty", nil
		}
		candidate := ratsToFloats(point)
		if !r.replayAccepts(problem, candidate) {
			return nil, "witness failed replay", nil
		}
		return candidate, "", nil
	}
	return nil, "", fmt.Errorf("unknown witness source %q", source)
}

// certifyInfeasible tries type-B (empty box) and type-A (exact Farkas)
// evidence on the outer envelope of problem.
func (r *engineRun) certifyInfeasible(ctx context.Context, problem LinearProblem) (*infeasibilityEvidence, string, error) {
	started := time.Now()
	defer func() { r.counters.InfeasibilityCertificateTime += time.Since(started) }()
	model, _, err := buildEnvelope(problem, r.tau())
	var empty *emptyBoxError
	if errors.As(err, &empty) {
		r.counters.InfeasibilityCertificates++
		return &infeasibilityEvidence{kind: evidenceKindEmptyBox, problem: problem, emptyBox: empty}, "", nil
	}
	if err != nil {
		return nil, envelopeReason(err), nil
	}
	result := acquireFarkas(ctx, model, r.budget())
	switch result.status {
	case exactCanceled:
		return nil, "", ctx.Err()
	case exactBudgetHit:
		r.counters.BudgetExhausted++
		return nil, reasonBudget, nil
	}
	if !result.check.valid {
		if result.nonEmpty {
			return nil, reasonEnvelopeNonEmpty, nil
		}
		return nil, result.check.reason, nil
	}
	r.counters.InfeasibilityCertificates++
	rows := make([]rowSide, len(model.rows))
	for i, row := range model.rows {
		rows[i] = rowSide{row: row.source, side: row.side}
	}
	return &infeasibilityEvidence{kind: evidenceKindFarkas, problem: problem,
		farkas: &farkasEvidence{rows: rows, multipliers: result.multipliers}}, "", nil
}

func envelopeReason(err error) string {
	switch {
	case errors.Is(err, errEnvelopeUnbounded):
		return reasonUnboundedVariable
	case errors.Is(err, errRoundingInapplicable):
		return reasonRoundingPrecondition
	default:
		return err.Error()
	}
}

// Gap variables sort after every semantic ID and cannot collide with them.
const (
	gapPlusID  VariableID = "~gap:w:plus"
	gapMinusID VariableID = "~gap:w:minus"
)

// gapProblem adds one free w = w⁺ − w⁻ to every inequality row (LE rows get
// a·x + w ≤ b, GE rows a·x − w ≥ b) and maximizes w. Equality rows and bounds
// are unchanged. Splitting w keeps every lower bound finite (solver contract)
// without inventing a large constant. ok is false without inequality rows.
func gapProblem(problem LinearProblem, name OptimizationStageID, origin ObjectiveOrigin) (LinearProblem, LinearObjective, bool) {
	out := cloneLinearProblem(problem)
	out.Variables = append(out.Variables,
		LinearVariable{ID: gapPlusID, Lower: 0, Upper: math.Inf(1)},
		LinearVariable{ID: gapMinusID, Lower: 0, Upper: math.Inf(1)})
	inequalities := 0
	for i, row := range out.Rows {
		var sign float64
		switch row.Sense {
		case SenseLE:
			sign = 1
		case SenseGE:
			sign = -1
		default:
			continue
		}
		inequalities++
		// Sorted order: semantic IDs, then "~gap:w:minus", then "~gap:w:plus".
		terms := append(row.Terms, LinearTerm{Variable: gapMinusID, Coeff: -sign}, LinearTerm{Variable: gapPlusID, Coeff: sign})
		out.Rows[i].Terms = terms
	}
	objective := LinearObjective{Name: name, Sense: Maximize, Origin: origin,
		Terms: []LinearTerm{{Variable: gapMinusID, Coeff: -1}, {Variable: gapPlusID, Coeff: 1}}}
	return out, objective, inequalities > 0
}
