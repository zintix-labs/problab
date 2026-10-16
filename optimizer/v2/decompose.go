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
	"math/big"
	"time"
)

// Exact completion thresholds (techspec §8.1). They are rationals: 1e-8 as a
// float64 is not 10⁻⁸, and completion is never decided in floating point.
var (
	requiredParameterPrecision = big.NewRat(1, 100_000_000)
	requiredGapAbsolute        = new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(12), nil))
	requiredGapRelative        = big.NewRat(1, 100_000_000)
)

// requiredMinimumCVGap is max(10⁻¹², 10⁻⁸·S_candidate) with the full second
// moment S_candidate.
func requiredMinimumCVGap(candidate *big.Rat) *big.Rat {
	return ratMax(requiredGapAbsolute, ratMul(requiredGapRelative, ratAbs(candidate)))
}

const (
	cvMinRowID RowID = "global:cv:min"
	cvMaxRowID RowID = "global:cv:max"

	sigmaMinSource = "sigma-min"
	sigmaMaxSource = "sigma-max"

	splitRuleVersion = "class-id-rows-v1"
)

// classSubproblem is one Class block of a joint problem (techspec §6.1).
type classSubproblem struct {
	classID  string
	position int // 1-based among Classes that own rows
	problem  LinearProblem
	index    []int // joint variable position of each local variable
}

// splitByClass partitions a joint problem: rows by ClassID, variables by the
// Class whose rows reference them. Rows without a ClassID may only be the two
// global CV rows; anything else, or a variable shared by two Classes, is a
// broken model and returned as an error.
func splitByClass(problem LinearProblem, classes []PreparedClass) ([]classSubproblem, []int, error) {
	position := make(map[VariableID]int, len(problem.Variables))
	for j, variable := range problem.Variables {
		position[variable.ID] = j
	}
	owner := make([]int, len(problem.Variables))
	for j := range owner {
		owner[j] = -1
	}
	rowsOf := make(map[string][]int)
	var global []int
	for i, row := range problem.Rows {
		if row.ClassID == "" {
			if row.ID != cvMinRowID && row.ID != cvMaxRowID {
				return nil, nil, fmt.Errorf("row %q has no Class and is not a global CV row", row.ID)
			}
			global = append(global, i)
			continue
		}
		rowsOf[row.ClassID] = append(rowsOf[row.ClassID], i)
	}
	var subs []classSubproblem
	for _, class := range classes {
		rows := rowsOf[class.ID]
		if len(rows) == 0 {
			continue
		}
		delete(rowsOf, class.ID)
		sub := classSubproblem{classID: class.ID}
		used := make(map[int]bool)
		for _, i := range rows {
			sub.problem.Rows = append(sub.problem.Rows, problem.Rows[i])
			for _, term := range problem.Rows[i].Terms {
				j, ok := position[term.Variable]
				if !ok {
					return nil, nil, fmt.Errorf("row %q references unknown variable %q", problem.Rows[i].ID, term.Variable)
				}
				if owner[j] >= 0 && owner[j] != len(subs) {
					return nil, nil, fmt.Errorf("variable %q is shared by two Classes", term.Variable)
				}
				owner[j] = len(subs)
				used[j] = true
			}
		}
		for j, variable := range problem.Variables {
			if used[j] {
				sub.index = append(sub.index, j)
				sub.problem.Variables = append(sub.problem.Variables, variable)
			}
		}
		subs = append(subs, sub)
	}
	for classID := range rowsOf {
		return nil, nil, fmt.Errorf("rows reference unknown Class %q", classID)
	}
	for j, variable := range problem.Variables {
		if owner[j] < 0 {
			return nil, nil, fmt.Errorf("variable %q belongs to no Class", variable.ID)
		}
	}
	for i := range subs {
		subs[i].position = i + 1
	}
	return subs, global, nil
}

// objectiveBoundEvidence is a type-D certificate: an exact upper or lower
// bound of one linear objective over the envelope of its bound problem.
type objectiveBoundEvidence struct {
	problem     LinearProblem
	terms       []LinearTerm // objective over problem's variables
	direction   string       // "upper" or "lower"
	rows        []rowSide
	multipliers []*big.Rat
	value       *big.Rat
}

// cvDisjointEvidence is a type-C certificate: the summed outer σ intervals of
// every Class miss the relaxed global CV range on one side (techspec §6.3).
type cvDisjointEvidence struct {
	side    string // "lower": Σlo_out > U_relaxed; "upper": Σhi_out < L_relaxed
	cvRow   LinearRow
	cvVars  []LinearVariable
	relaxed *big.Rat
	classes []objectiveBoundEvidence
}

// classInterval holds one Class's σ interval evidence.
type classInterval struct {
	loIn, hiIn   *big.Rat
	loX, hiX     []float64
	loOut, hiOut *objectiveBoundEvidence
}

// jointDecision is the decision for one joint problem.
type jointDecision struct {
	classification
	// Minimum-CV extras, all exact; S is the full second moment F + T.
	// safeLowerS and gapS are nil when no reliable lower bound exists.
	candidateS *big.Rat
	safeLowerS *big.Rat
	gapS       *big.Rat
	requiredS  *big.Rat
	improved   bool
}

// gapMet reports whether the minimum-CV gap is proven within its requirement.
func (d jointDecision) gapMet() bool {
	return d.class == evidenceFeasible && d.gapS != nil && d.requiredS != nil && d.gapS.Cmp(d.requiredS) <= 0
}

// betterGap reports whether d has a proven gap strictly smaller than other's.
func (d jointDecision) betterGap(other jointDecision) bool {
	if d.class != evidenceFeasible || d.gapS == nil {
		return false
	}
	return other.class != evidenceFeasible || other.gapS == nil || d.gapS.Cmp(other.gapS) < 0
}

// decideJoint decides one joint problem by Class decomposition (techspec
// §6.2–§6.3). With minCV it also computes the exact minimum-CV candidate,
// safe lower bound and gap of §7.3, starting from the given witness.
func (r *engineRun) decideJoint(ctx context.Context, problem LinearProblem, stage OptimizationStageID, origin ObjectiveOrigin, probe *float64, minCV bool, start []float64) (jointDecision, error) {
	subs, global, err := splitByClass(problem, r.compiled.Prepared.Classes)
	if err != nil {
		return jointDecision{}, err
	}
	witnesses := make([][]float64, len(subs))
	for i, sub := range subs {
		where := solveContext{stage: stage, class: sub.position, classCount: len(subs), probe: probe}
		result, err := r.classify(ctx, sub.problem, where, origin)
		if err != nil {
			return jointDecision{}, err
		}
		switch result.class {
		case evidenceInfeasible:
			return jointDecision{classification: result}, nil
		case evidenceUnknown:
			result.reasons = prefixReasons(fmt.Sprintf("class %s", sub.classID), result.reasons)
			return jointDecision{classification: result}, nil
		}
		witnesses[i] = result.witness
	}
	if len(global) == 0 {
		joint := assemble(problem, subs, witnesses, witnesses, 0)
		if !r.replayAccepts(problem, joint) {
			return jointDecision{classification: classification{class: evidenceUnknown, reasons: []string{"assembled joint witness failed replay"}}}, nil
		}
		decision := jointDecision{classification: classification{class: evidenceFeasible, witness: joint, source: "decomposition"}}
		if minCV {
			// Without CV rows every contribution is fixed by Class rows: S = F.
			fixed := r.fixedSecond()
			decision.candidateS, decision.safeLowerS, decision.gapS = fixed, fixed, new(big.Rat)
			decision.requiredS = requiredMinimumCVGap(fixed)
		}
		return decision, nil
	}
	decision, err := r.couple(ctx, problem, subs, witnesses, stage, probe, minCV, start, false)
	if err != nil {
		return jointDecision{}, err
	}
	if decision.class != evidenceUnknown && (!minCV || decision.class != evidenceFeasible || decision.gapMet()) {
		return decision, nil
	}
	r.counters.SigmaEndpointImprovements++
	improved, err := r.couple(ctx, problem, subs, witnesses, stage, probe, minCV, start, true)
	if err != nil {
		return jointDecision{}, err
	}
	improved.improved = true
	decision.improved = true
	if decision.class == evidenceUnknown {
		if improved.class != evidenceUnknown {
			return improved, nil
		}
		return decision, nil
	}
	if improved.betterGap(decision) {
		return improved, nil
	}
	return decision, nil
}

func (r *engineRun) fixedSecond() *big.Rat {
	_, fixed, err := buildSecondMomentExpression(r.compiled)
	if err != nil || !isFinite(fixed) {
		return new(big.Rat)
	}
	return ratOf(fixed)
}

// couple is one pass of the §6.3 coupling decision with σ endpoints from
// Gonum (preferExact=false) or from the exact inner optimum (true).
func (r *engineRun) couple(ctx context.Context, problem LinearProblem, subs []classSubproblem, witnesses [][]float64,
	stage OptimizationStageID, probe *float64, minCV bool, start []float64, preferExact bool,
) (jointDecision, error) {
	sigma := sigmaCoefficients(problem)
	intervals := make([]classInterval, len(subs))
	for i, sub := range subs {
		where := solveContext{stage: stage, class: sub.position, classCount: len(subs), probe: probe}
		interval, err := r.classInterval(ctx, sub, sigma, witnesses[i], where, preferExact)
		if err != nil {
			return jointDecision{}, err
		}
		intervals[i] = interval
	}
	lower, upper, err := r.relaxedCVRange(problem)
	if err != nil {
		return jointDecision{}, err
	}
	sumLoIn, sumHiIn := new(big.Rat), new(big.Rat)
	sumLoOut, sumHiOut := new(big.Rat), new(big.Rat)
	outerComplete := true
	for _, interval := range intervals {
		sumLoIn = ratAdd(sumLoIn, interval.loIn)
		sumHiIn = ratAdd(sumHiIn, interval.hiIn)
		if interval.loOut == nil || interval.hiOut == nil {
			outerComplete = false
			continue
		}
		sumLoOut = ratAdd(sumLoOut, interval.loOut.value)
		sumHiOut = ratAdd(sumHiOut, interval.hiOut.value)
	}
	if outerComplete && upper.present && sumLoOut.Cmp(upper.relaxed) > 0 {
		return disjoint("lower", upper, intervals, func(c classInterval) objectiveBoundEvidence { return *c.loOut }), nil
	}
	if outerComplete && lower.present && sumHiOut.Cmp(lower.relaxed) < 0 {
		return disjoint("upper", lower, intervals, func(c classInterval) objectiveBoundEvidence { return *c.hiOut }), nil
	}
	// Aim at max(L_T, Σlo_in) clamped into the inner range; acceptance is the
	// joint replay, not exact interval arithmetic.
	target := new(big.Rat).Set(sumLoIn)
	if lower.present && lower.limit.Cmp(target) > 0 {
		target = new(big.Rat).Set(lower.limit)
	}
	if target.Cmp(sumHiIn) > 0 {
		target = sumHiIn
	}
	theta := 0.0
	if width := ratSub(sumHiIn, sumLoIn); width.Sign() > 0 {
		theta, _ = ratQuo(ratSub(target, sumLoIn), width).Float64()
	}
	los, his := make([][]float64, len(intervals)), make([][]float64, len(intervals))
	for i, interval := range intervals {
		los[i], his[i] = interval.loX, interval.hiX
	}
	joint := assemble(problem, subs, los, his, theta)
	decision := jointDecision{}
	if r.replayAccepts(problem, joint) {
		decision.classification = classification{class: evidenceFeasible, witness: joint, source: "decomposition"}
	} else {
		decision.classification = classification{class: evidenceUnknown, reasons: []string{"assembled joint point failed replay"}}
	}
	if !minCV {
		return decision, nil
	}
	// Minimum CV (§7.3): keep the better replayed candidate; the start witness
	// from the previous stage is always a valid fallback.
	candidate, candidateT := []float64(nil), (*big.Rat)(nil)
	if start != nil && r.replayAccepts(problem, start) {
		candidate, candidateT = start, sigmaValue(problem, sigma, start)
	}
	if decision.class == evidenceFeasible {
		if t := sigmaValue(problem, sigma, joint); candidate == nil || t.Cmp(candidateT) < 0 {
			candidate, candidateT = joint, t
		}
	}
	if candidate == nil {
		return jointDecision{classification: classification{class: evidenceUnknown, reasons: []string{"no replayed minimum-CV candidate"}}}, nil
	}
	fixed := r.fixedSecond()
	decision.classification = classification{class: evidenceFeasible, witness: candidate, source: "decomposition"}
	decision.candidateS = ratAdd(fixed, candidateT)
	decision.requiredS = requiredMinimumCVGap(decision.candidateS)
	var safeT *big.Rat
	if lower.present {
		safeT = lower.relaxed
	}
	if outerComplete && (safeT == nil || sumLoOut.Cmp(safeT) > 0) {
		safeT = sumLoOut
	}
	if safeT == nil {
		decision.reasons = []string{"no reliable lower bound for the second moment"}
		return decision, nil
	}
	decision.safeLowerS = ratAdd(fixed, safeT)
	decision.gapS = ratSub(decision.candidateS, decision.safeLowerS)
	return decision, nil
}

func disjoint(side string, limit relaxedLimit, intervals []classInterval, pick func(classInterval) objectiveBoundEvidence) jointDecision {
	evidence := &cvDisjointEvidence{side: side, cvRow: limit.row, cvVars: limit.vars, relaxed: limit.relaxed}
	for _, interval := range intervals {
		evidence.classes = append(evidence.classes, pick(interval))
	}
	return jointDecision{classification: classification{class: evidenceInfeasible,
		evidence: &infeasibilityEvidence{kind: evidenceKindCVDisjoint, cv: evidence}}}
}

// classInterval computes inner (replayed witnesses) and outer (exact dual
// bounds, type D) σ intervals of one Class subproblem.
func (r *engineRun) classInterval(ctx context.Context, sub classSubproblem, sigma map[VariableID]float64,
	fallback []float64, where solveContext, preferExact bool,
) (classInterval, error) {
	var terms []LinearTerm
	coefficients := map[int]*big.Rat{}
	for j, variable := range sub.problem.Variables {
		if a, ok := sigma[variable.ID]; ok && a != 0 {
			terms = append(terms, LinearTerm{Variable: variable.ID, Coeff: a})
			coefficients[j] = ratOf(a)
		}
	}
	value := func(x []float64) *big.Rat {
		total := new(big.Rat)
		for j, a := range coefficients {
			total = ratAdd(total, ratMul(a, ratOf(x[j])))
		}
		return total
	}
	var interval classInterval
	for _, sense := range []ObjectiveSense{Minimize, Maximize} {
		x, err := r.sigmaEndpoint(ctx, sub.problem, terms, coefficients, sense, where, preferExact)
		if err != nil {
			return classInterval{}, err
		}
		if x == nil {
			x = fallback // degenerate inner interval: any replayed point
		}
		if sense == Minimize {
			interval.loX, interval.loIn = x, value(x)
		} else {
			interval.hiX, interval.hiIn = x, value(x)
		}
	}
	started := time.Now()
	defer func() { r.counters.ExactBoundTime += time.Since(started) }()
	model, _, err := buildEnvelope(sub.problem, r.tau())
	if err != nil {
		return interval, nil // no outer interval: decisions stay on the safe side
	}
	for _, direction := range []string{"lower", "upper"} {
		var result exactResult
		if direction == "lower" {
			result = acquireLowerBound(ctx, model, coefficients, r.budget())
		} else {
			result = acquireUpperBound(ctx, model, coefficients, r.budget())
		}
		r.counters.ExactBounds++
		switch result.status {
		case exactCanceled:
			return classInterval{}, ctx.Err()
		case exactBudgetHit:
			r.counters.BudgetExhausted++
			continue
		}
		if !result.check.valid {
			continue
		}
		bound := &objectiveBoundEvidence{problem: sub.problem, terms: terms, direction: direction,
			rows: modelRowSides(model), multipliers: result.multipliers, value: result.check.value}
		if direction == "lower" {
			interval.loOut = bound
		} else {
			interval.hiOut = bound
		}
	}
	return interval, nil
}

func modelRowSides(model exactModel) []rowSide {
	rows := make([]rowSide, len(model.rows))
	for i, row := range model.rows {
		rows[i] = rowSide{row: row.source, side: row.side}
	}
	return rows
}

// sigmaEndpoint returns a replayed minimizer or maximizer of σ on one Class,
// from Gonum or from the exact optimum of the inner model.
func (r *engineRun) sigmaEndpoint(ctx context.Context, problem LinearProblem, terms []LinearTerm, coefficients map[int]*big.Rat,
	sense ObjectiveSense, where solveContext, preferExact bool,
) ([]float64, error) {
	gonum := func() ([]float64, error) {
		where.source = sigmaMinSource
		if sense == Maximize {
			where.source = sigmaMaxSource
		}
		r.at(where)
		result, err := r.Solve(ctx, problem, LinearObjective{Name: where.stage, Sense: sense, Origin: ObjectiveDiagnostic, Terms: terms}, r.options)
		if err != nil {
			return nil, err
		}
		if result.Status == SolveOptimal && r.replayAccepts(problem, result.Values) {
			return append([]float64(nil), result.Values...), nil
		}
		return nil, nil
	}
	exact := func() ([]float64, error) {
		started := time.Now()
		defer func() {
			r.counters.ExactWitnesses++
			r.counters.ExactWitnessTime += time.Since(started)
		}()
		inner, err := buildInnerModel(problem, r.tau())
		if err != nil {
			return nil, nil
		}
		direction := coefficients
		if sense == Minimize {
			direction = make(map[int]*big.Rat, len(coefficients))
			for j, a := range coefficients {
				direction[j] = ratNeg(a)
			}
		}
		point, status := exactOptimum(ctx, inner, direction, r.budget())
		switch status {
		case exactCanceled:
			return nil, ctx.Err()
		case exactBudgetHit:
			r.counters.BudgetExhausted++
			return nil, nil
		}
		if point == nil {
			return nil, nil
		}
		candidate := ratsToFloats(point)
		if !r.replayAccepts(problem, candidate) {
			return nil, nil
		}
		return candidate, nil
	}
	first, second := gonum, exact
	if preferExact {
		first, second = exact, gonum
	}
	x, err := first()
	if err != nil || x != nil {
		return x, err
	}
	return second()
}

// relaxedLimit is one global CV row and its replay-relaxed limit in T scale.
type relaxedLimit struct {
	row     LinearRow
	vars    []LinearVariable
	present bool
	limit   *big.Rat // the row's exact RHS (L_T or U_T)
	relaxed *big.Rat // L_T' (lower) or U_T' (upper)
}

// relaxedCVRange computes the replay-relaxed CV limits from the two CV rows
// alone: each row with its own variables forms a one-row problem whose
// envelope row carries exactly the slack and rounding allowance the joint
// envelope would (CV rows reference only bounded primary variables).
func (r *engineRun) relaxedCVRange(problem LinearProblem) (relaxedLimit, relaxedLimit, error) {
	var lower, upper relaxedLimit
	for _, row := range problem.Rows {
		if row.ID != cvMinRowID && row.ID != cvMaxRowID {
			continue
		}
		relaxed, vars, err := relaxCVRow(problem, row, r.tau())
		if err != nil {
			return relaxedLimit{}, relaxedLimit{}, err
		}
		limit := relaxedLimit{row: row, vars: vars, present: true, limit: ratOf(row.RHS), relaxed: relaxed}
		if row.ID == cvMinRowID {
			lower = limit
		} else {
			upper = limit
		}
	}
	return lower, upper, nil
}

// relaxCVRow returns the relaxed limit of one CV row (L_T' for the GE row,
// U_T' for the LE row) and the variables it references.
func relaxCVRow(problem LinearProblem, row LinearRow, tau float64) (*big.Rat, []LinearVariable, error) {
	position := make(map[VariableID]int, len(problem.Variables))
	for j, variable := range problem.Variables {
		position[variable.ID] = j
	}
	mini := LinearProblem{Rows: []LinearRow{row}}
	for _, term := range row.Terms {
		j, ok := position[term.Variable]
		if !ok {
			return nil, nil, fmt.Errorf("row %q references unknown variable %q", row.ID, term.Variable)
		}
		mini.Variables = append(mini.Variables, problem.Variables[j])
	}
	model, _, err := buildEnvelope(mini, tau)
	if err != nil {
		return nil, nil, fmt.Errorf("relax %s: %w", row.ID, err)
	}
	for _, form := range model.rows {
		if row.Sense == SenseGE && form.side == SenseGE {
			return ratNeg(form.rhs), mini.Variables, nil
		}
		if row.Sense == SenseLE && form.side == SenseLE {
			return form.rhs, mini.Variables, nil
		}
	}
	return nil, nil, fmt.Errorf("CV row %q has unexpected sense %v", row.ID, row.Sense)
}

func sigmaCoefficients(problem LinearProblem) map[VariableID]float64 {
	sigma := map[VariableID]float64{}
	for _, row := range problem.Rows {
		if row.ID == cvMinRowID || (row.ID == cvMaxRowID && len(sigma) == 0) {
			for _, term := range row.Terms {
				sigma[term.Variable] = term.Coeff
			}
		}
	}
	return sigma
}

// sigmaValue is T(x) = Σ σⱼxⱼ evaluated exactly.
func sigmaValue(problem LinearProblem, sigma map[VariableID]float64, x []float64) *big.Rat {
	total := new(big.Rat)
	for j, variable := range problem.Variables {
		if a, ok := sigma[variable.ID]; ok {
			total = ratAdd(total, ratMul(ratOf(a), ratOf(x[j])))
		}
	}
	return total
}

// assemble builds the joint point (1−θ)·lo + θ·hi per Class.
func assemble(problem LinearProblem, subs []classSubproblem, lo, hi [][]float64, theta float64) []float64 {
	joint := make([]float64, len(problem.Variables))
	for i, sub := range subs {
		for local, j := range sub.index {
			joint[j] = (1-theta)*lo[i][local] + theta*hi[i][local]
		}
	}
	return joint
}

func prefixReasons(prefix string, reasons []string) []string {
	out := make([]string, len(reasons))
	for i, reason := range reasons {
		out[i] = prefix + ": " + reason
	}
	return out
}
