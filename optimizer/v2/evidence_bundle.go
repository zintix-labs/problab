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
	"math/big"
)

// Evidence bundles (techspec §9.3) carry everything an independent verifier
// needs: the snapshot of the bound problem, τ, the acceptance rule and the
// Class order. The verifiers rebuild every envelope from the snapshot and
// never trust a derived quantity stored in the bundle.
const (
	evidenceBundleVersion   = "verified-lp-evidence/v1"
	evidenceVerifierVersion = "exact-envelope-verifier/v1"
)

// EvidenceRule names the acceptance set an envelope is rebuilt for.
type EvidenceRule struct {
	Version        string `json:"version"`
	RowTolerance   string `json:"row_tolerance"`
	BoundTolerance string `json:"bound_tolerance"` // "0": bounds must hold exactly
}

// AcceptanceRulesReport is the report form of the two sets.
type AcceptanceRulesReport struct {
	Internal EvidenceRule `json:"internal"`
	External EvidenceRule `json:"external"`
}

func acceptanceRulesReport(tau float64) *AcceptanceRulesReport {
	if !(tau > 0) {
		return nil
	}
	return &AcceptanceRulesReport{Internal: encodeEvidenceRule(internalRule(tau)), External: encodeEvidenceRule(productRule(tau))}
}

func encodeEvidenceRule(rule acceptanceRule) EvidenceRule {
	return EvidenceRule{Version: rule.version(), RowTolerance: encodeFloat64(rule.rowTau), BoundTolerance: encodeFloat64(rule.boundTau)}
}

// EvidenceHeader is the part every bundle shares.
type EvidenceHeader struct {
	Version         string          `json:"version"`
	EngineVersion   string          `json:"engine_version"`
	VerifierVersion string          `json:"verifier_version"`
	BackendVersion  string          `json:"backend_version"`
	SplitRule       string          `json:"split_rule"`
	Tolerance       string          `json:"tolerance"`
	Rule            EvidenceRule    `json:"rule"`
	Stage           string          `json:"stage"`
	Probe           string          `json:"probe,omitempty"`
	ClassIDs        []string        `json:"class_ids"`
	Problem         ProblemSnapshot `json:"problem"`
	ProblemSHA256   string          `json:"problem_sha256"`
}

// RowMultiplier is one exact multiplier on the "≤" form of a row: side "le"
// is the row as written (or an EQ row's upper half), "ge" its negation.
type RowMultiplier struct {
	Row   RowID  `json:"row"`
	Side  string `json:"side"`
	Value string `json:"value"` // exact rational "num/den"
}

// ClassBound is a type-D payload bound to one Class of the header problem.
type ClassBound struct {
	Class       int             `json:"class"` // 1-based Class position
	Direction   string          `json:"direction"`
	Multipliers []RowMultiplier `json:"multipliers"`
	Value       string          `json:"value"`
}

// InfeasibilityBundle proves that the header problem has no point in the
// rule's set (types A, B, C).
type InfeasibilityBundle struct {
	EvidenceHeader
	Kind string `json:"kind"`
	// Class is the 1-based Class whose subproblem types A and B refute; 0
	// means the whole problem.
	Class       int             `json:"class,omitempty"`
	Multipliers []RowMultiplier `json:"multipliers,omitempty"` // A
	Variable    VariableID      `json:"variable,omitempty"`    // B
	SourceRow   RowID           `json:"source_row,omitempty"`  // B, explanatory only
	Side        string          `json:"side,omitempty"`        // C
	Bounds      []ClassBound    `json:"bounds,omitempty"`      // C, one per Class
}

// ObjectiveBoundBundle proves an exact bound of one objective over the
// rule's envelope of the header problem, or of its Class subproblem (type D).
type ObjectiveBoundBundle struct {
	EvidenceHeader
	Class       int             `json:"class,omitempty"`
	Terms       []TermSnapshot  `json:"terms"`
	Direction   string          `json:"direction"` // "upper" or "lower"
	Multipliers []RowMultiplier `json:"multipliers"`
	Value       string          `json:"value"`
}

func sideName(side Sense) string {
	if side == SenseGE {
		return "ge"
	}
	return "le"
}

func encodeMultipliers(problem LinearProblem, rows []rowSide, multipliers []*big.Rat) []RowMultiplier {
	var out []RowMultiplier
	for i, y := range multipliers {
		if y == nil || y.Sign() == 0 {
			continue
		}
		out = append(out, RowMultiplier{Row: problem.Rows[rows[i].row].ID, Side: sideName(rows[i].side), Value: y.String()})
	}
	return out
}

// evidenceHeader binds a bundle to problem. Nothing is stored that the
// verifier would have to trust.
func (r *engineRun) evidenceHeader(problem LinearProblem, rule acceptanceRule, stage OptimizationStageID, probe *float64) (EvidenceHeader, error) {
	snapshot := SnapshotProblem(problem)
	sum, err := snapshot.SHA256()
	if err != nil {
		return EvidenceHeader{}, err
	}
	header := EvidenceHeader{
		Version: evidenceBundleVersion, EngineVersion: engineImplementationVersion, VerifierVersion: evidenceVerifierVersion,
		BackendVersion: gonumBackendVersion, SplitRule: splitRuleVersion, Tolerance: encodeFloat64(r.tau()),
		Rule: encodeEvidenceRule(rule), Stage: string(stage), ClassIDs: preparedClassIDs(r.compiled.Prepared.Classes),
		Problem: snapshot, ProblemSHA256: sum,
	}
	if probe != nil {
		header.Probe = encodeFloat64(*probe)
	}
	return header, nil
}

// infeasibilityBundle encodes evidence found for the joint problem of one
// decision (decideJoint attaches it).
func (r *engineRun) infeasibilityBundle(evidence *infeasibilityEvidence, stage OptimizationStageID, probe *float64) (InfeasibilityBundle, error) {
	if evidence == nil || evidence.joint == nil {
		return InfeasibilityBundle{}, errors.New("evidence without its joint problem")
	}
	header, err := r.evidenceHeader(*evidence.joint, evidence.rule, stage, probe)
	if err != nil {
		return InfeasibilityBundle{}, err
	}
	bundle := InfeasibilityBundle{EvidenceHeader: header, Kind: evidence.kind}
	switch evidence.kind {
	case evidenceKindFarkas:
		bundle.Class = evidence.class
		bundle.Multipliers = encodeMultipliers(evidence.problem, evidence.farkas.rows, evidence.farkas.multipliers)
	case evidenceKindEmptyBox:
		bundle.Class = evidence.class
		bundle.Variable = evidence.emptyBox.id
		if evidence.emptyBox.sourceRow >= 0 {
			bundle.SourceRow = evidence.problem.Rows[evidence.emptyBox.sourceRow].ID
		}
	case evidenceKindCVDisjoint:
		bundle.Side = evidence.cv.side
		for i, bound := range evidence.cv.classes {
			bundle.Bounds = append(bundle.Bounds, ClassBound{Class: i + 1, Direction: bound.direction,
				Multipliers: encodeMultipliers(bound.problem, bound.rows, bound.multipliers), Value: bound.value.String()})
		}
	default:
		return InfeasibilityBundle{}, fmt.Errorf("unknown evidence kind %q", evidence.kind)
	}
	return bundle, nil
}

// objectiveBoundBundle encodes a type-D bound found on Class class of joint.
func (r *engineRun) objectiveBoundBundle(joint LinearProblem, class int, bound objectiveBoundEvidence, stage OptimizationStageID, probe *float64) (ObjectiveBoundBundle, error) {
	header, err := r.evidenceHeader(joint, bound.rule, stage, probe)
	if err != nil {
		return ObjectiveBoundBundle{}, err
	}
	return ObjectiveBoundBundle{EvidenceHeader: header, Class: class, Terms: encodeTerms(bound.terms), Direction: bound.direction,
		Multipliers: encodeMultipliers(bound.problem, bound.rows, bound.multipliers), Value: bound.value.String()}, nil
}

// VerifyInfeasibilityEvidence re-verifies a type A, B or C bundle from its
// problem snapshot alone. It returns nil only when the rebuilt envelope of
// the bundle's rule is provably empty.
func VerifyInfeasibilityEvidence(bundle InfeasibilityBundle) error {
	problem, rule, err := bundle.EvidenceHeader.open()
	if err != nil {
		return err
	}
	switch bundle.Kind {
	case evidenceKindFarkas:
		sub, err := classSubproblemOf(problem, bundle.ClassIDs, bundle.Class)
		if err != nil {
			return err
		}
		model, _, err := buildEnvelopeRule(sub, rule)
		if err != nil {
			return fmt.Errorf("rebuild envelope: %w", err)
		}
		y, err := multiplierVector(sub, model, bundle.Multipliers)
		if err != nil {
			return err
		}
		if check := verifyFarkas(model, y); !check.valid {
			return fmt.Errorf("Farkas certificate rejected: %s", check.reason)
		}
		return nil
	case evidenceKindEmptyBox:
		sub, err := classSubproblemOf(problem, bundle.ClassIDs, bundle.Class)
		if err != nil {
			return err
		}
		_, _, err = buildEnvelopeRule(sub, rule)
		var empty *emptyBoxError
		if !errors.As(err, &empty) || empty.id != bundle.Variable {
			return fmt.Errorf("envelope box of %q is not empty (%v)", bundle.Variable, err)
		}
		return nil
	case evidenceKindCVDisjoint:
		return verifyCVDisjoint(problem, rule, bundle)
	}
	return fmt.Errorf("unknown evidence kind %q", bundle.Kind)
}

// VerifyObjectiveBound re-verifies a type-D bundle: the bound recomputed from
// the multipliers over the rebuilt envelope must equal the claimed value.
func VerifyObjectiveBound(bundle ObjectiveBoundBundle) error {
	problem, rule, err := bundle.EvidenceHeader.open()
	if err != nil {
		return err
	}
	sub, err := classSubproblemOf(problem, bundle.ClassIDs, bundle.Class)
	if err != nil {
		return err
	}
	terms, err := decodeTerms(bundle.Terms)
	if err != nil {
		return err
	}
	_, err = verifyBound(sub, rule, terms, bundle.Direction, bundle.Multipliers, bundle.Value)
	return err
}

// open decodes the header problem and rule and checks their consistency.
func (h EvidenceHeader) open() (LinearProblem, acceptanceRule, error) {
	if h.Version != evidenceBundleVersion {
		return LinearProblem{}, acceptanceRule{}, fmt.Errorf("unknown bundle version %q", h.Version)
	}
	if h.SplitRule != splitRuleVersion {
		return LinearProblem{}, acceptanceRule{}, fmt.Errorf("unknown split rule %q", h.SplitRule)
	}
	if sum, err := h.Problem.SHA256(); err != nil || sum != h.ProblemSHA256 {
		return LinearProblem{}, acceptanceRule{}, fmt.Errorf("problem snapshot does not match its SHA-256")
	}
	problem, err := h.Problem.Problem()
	if err != nil {
		return LinearProblem{}, acceptanceRule{}, err
	}
	rule, err := h.rule()
	return problem, rule, err
}

// rule decodes the acceptance rule and checks it is one the engine defines
// for the header's τ.
func (h EvidenceHeader) rule() (acceptanceRule, error) {
	tau, err := decodeFloat64(h.Tolerance)
	if err != nil {
		return acceptanceRule{}, fmt.Errorf("tolerance: %w", err)
	}
	row, err := decodeFloat64(h.Rule.RowTolerance)
	if err != nil {
		return acceptanceRule{}, fmt.Errorf("row tolerance: %w", err)
	}
	bound, err := decodeFloat64(h.Rule.BoundTolerance)
	if err != nil {
		return acceptanceRule{}, fmt.Errorf("bound tolerance: %w", err)
	}
	rule := acceptanceRule{rowTau: row, boundTau: bound}
	var want acceptanceRule
	switch h.Rule.Version {
	case productRule(tau).version():
		want = productRule(tau)
	case internalRuleVersion:
		want = internalRule(tau)
	default:
		return acceptanceRule{}, fmt.Errorf("unknown acceptance rule %q", h.Rule.Version)
	}
	if rule != want {
		return acceptanceRule{}, fmt.Errorf("rule %q does not match tolerance %s", h.Rule.Version, h.Tolerance)
	}
	return rule, nil
}

// classSubproblemOf returns Class class (1-based) of problem, or problem
// itself for class 0.
func classSubproblemOf(problem LinearProblem, classIDs []string, class int) (LinearProblem, error) {
	if class == 0 {
		return problem, nil
	}
	subs, _, err := splitByClassIDs(problem, classIDs)
	if err != nil {
		return LinearProblem{}, fmt.Errorf("split: %w", err)
	}
	if class < 1 || class > len(subs) {
		return LinearProblem{}, fmt.Errorf("class %d out of range", class)
	}
	return subs[class-1].problem, nil
}

// multiplierVector places every payload multiplier on its envelope row.
func multiplierVector(problem LinearProblem, model exactModel, multipliers []RowMultiplier) ([]*big.Rat, error) {
	index := make(map[string]int, len(model.rows))
	for i, row := range model.rows {
		index[string(problem.Rows[row.source].ID)+"\x00"+sideName(row.side)] = i
	}
	y := make([]*big.Rat, len(model.rows))
	for _, m := range multipliers {
		i, ok := index[string(m.Row)+"\x00"+m.Side]
		if !ok {
			return nil, fmt.Errorf("multiplier on unknown row side %q/%s", m.Row, m.Side)
		}
		if y[i] != nil {
			return nil, fmt.Errorf("duplicate multiplier on %q/%s", m.Row, m.Side)
		}
		value, ok := new(big.Rat).SetString(m.Value)
		if !ok || value.Sign() < 0 {
			return nil, fmt.Errorf("multiplier on %q/%s is not a nonnegative rational: %q", m.Row, m.Side, m.Value)
		}
		y[i] = value
	}
	return y, nil
}

// verifyBound recomputes a type-D bound and returns it when it equals the
// claimed value.
func verifyBound(problem LinearProblem, rule acceptanceRule, terms []LinearTerm, direction string, multipliers []RowMultiplier, claimed string) (*big.Rat, error) {
	model, _, err := buildEnvelopeRule(problem, rule)
	if err != nil {
		return nil, fmt.Errorf("rebuild envelope: %w", err)
	}
	index := make(map[VariableID]int, len(problem.Variables))
	for j, v := range problem.Variables {
		index[v.ID] = j
	}
	c := map[int]*big.Rat{}
	for _, term := range terms {
		j, ok := index[term.Variable]
		if !ok {
			return nil, fmt.Errorf("objective term on unknown variable %q", term.Variable)
		}
		c[j] = ratAdd(ratOrZero(c[j]), ratOf(term.Coeff))
	}
	y, err := multiplierVector(problem, model, multipliers)
	if err != nil {
		return nil, err
	}
	var check boundCheck
	switch direction {
	case "upper":
		check = upperBoundRat(model, c, y)
	case "lower":
		check = lowerBoundRat(model, c, y)
	default:
		return nil, fmt.Errorf("unknown bound direction %q", direction)
	}
	if !check.valid {
		return nil, fmt.Errorf("bound rejected: %s", check.reason)
	}
	want, ok := new(big.Rat).SetString(claimed)
	if !ok {
		return nil, fmt.Errorf("claimed bound %q is not rational", claimed)
	}
	if check.value.Cmp(want) != 0 {
		return nil, fmt.Errorf("recomputed %s bound %s differs from the claimed %s", direction, check.value.RatString(), want.RatString())
	}
	return check.value, nil
}

func ratOrZero(a *big.Rat) *big.Rat {
	if a == nil {
		return new(big.Rat)
	}
	return a
}

// verifyCVDisjoint checks type C: every Class bound is a valid type-D bound
// of σ on its own Class, and their sum misses the relaxed CV limit.
func verifyCVDisjoint(problem LinearProblem, rule acceptanceRule, bundle InfeasibilityBundle) error {
	limitID, direction := cvMaxRowID, "lower" // Σlo_out > U_relaxed
	if bundle.Side == "upper" {
		limitID, direction = cvMinRowID, "upper" // Σhi_out < L_relaxed
	} else if bundle.Side != "lower" {
		return fmt.Errorf("unknown CV side %q", bundle.Side)
	}
	var limit *LinearRow
	for i := range problem.Rows {
		if problem.Rows[i].ID == limitID {
			limit = &problem.Rows[i]
		}
	}
	if limit == nil {
		return fmt.Errorf("problem has no %s row", limitID)
	}
	relaxed, _, err := relaxCVRow(problem, *limit, rule)
	if err != nil {
		return err
	}
	subs, _, err := splitByClassIDs(problem, bundle.ClassIDs)
	if err != nil {
		return fmt.Errorf("split: %w", err)
	}
	if len(bundle.Bounds) != len(subs) {
		return fmt.Errorf("%d Class bounds for %d Classes", len(bundle.Bounds), len(subs))
	}
	sigma := map[VariableID]float64{}
	for _, term := range limit.Terms {
		sigma[term.Variable] += term.Coeff
	}
	total := new(big.Rat)
	for i, bound := range bundle.Bounds {
		if bound.Class != i+1 || bound.Direction != direction {
			return fmt.Errorf("Class bound %d is %d/%s, want %d/%s", i, bound.Class, bound.Direction, i+1, direction)
		}
		sub := subs[i].problem
		var terms []LinearTerm
		for _, v := range sub.Variables {
			if a, ok := sigma[v.ID]; ok && a != 0 {
				terms = append(terms, LinearTerm{Variable: v.ID, Coeff: a})
			}
		}
		value, err := verifyBound(sub, rule, terms, direction, bound.Multipliers, bound.Value)
		if err != nil {
			return fmt.Errorf("Class %d: %w", bound.Class, err)
		}
		total = ratAdd(total, value)
	}
	if direction == "lower" && total.Cmp(relaxed) <= 0 {
		return fmt.Errorf("Σ lower bounds %s does not exceed the relaxed CV maximum %s", total.RatString(), relaxed.RatString())
	}
	if direction == "upper" && total.Cmp(relaxed) >= 0 {
		return fmt.Errorf("Σ upper bounds %s is not below the relaxed CV minimum %s", total.RatString(), relaxed.RatString())
	}
	return nil
}
