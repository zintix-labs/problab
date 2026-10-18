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
	"strconv"
)

// The hard global-CV explanation is built from the type-C certificate alone:
// no Solver is called after the certificate exists. Notation:
//
//   - T = Σσ_k is the variable part, the lhs of both CV rows;
//   - F is the fixed contribution of intent:false Classes;
//   - S = F + T is the full second moment.
//
// The certificate states, exactly, that every replay-acceptable point has
// T ≥ B_T (side "lower", against global:cv:max) or T ≤ B_T (side "upper",
// against global:cv:min), where B_T sums the Class σ outer bounds. B_T is a
// safe bound, not an achievable value. The strict claim exists only in
// second-moment units: CV = √(S−μ²)/μ uses the configured mean μ, while a
// replay-acceptable point's own mean equals μ only within tolerance, so CV
// figures are labeled approximations. S-scale values are always F plus the
// T-scale value (including S_limit = F + R_T), never re-derived from the CV
// range, so they agree with the numbers the certificate was verified on.

// errInvalidCVEvidence marks a type-C certificate that is inconsistent with
// itself. Such evidence must never be reported as INFEASIBLE_MODEL.
var errInvalidCVEvidence = errors.New("invalid type-C evidence")

// maxSafeDisplaySteps bounds the search for a float64 whose shortest decimal
// text lies on the safe side of an exact bound.
const maxSafeDisplaySteps = 8

const displayOutOfRangeNote = "; some display values exceed the float64 range and are omitted (exact values are in the evidence file)"

// cvConflict is the checked content of one type-C certificate.
type cvConflict struct {
	lower    bool // side "lower": B_T > R'_T against global:cv:max
	row      LinearRow
	boundT   *big.Rat // B_T
	limitT   *big.Rat // R_T
	relaxedT *big.Rat // R'_T
	margin   *big.Rat // B_T − R'_T (lower) or R'_T − B_T (upper), > 0
	evidence *cvDisjointEvidence
}

// checkCVEvidence performs every consistency check of the certificate before
// any display value is computed; a failure wraps errInvalidCVEvidence.
func checkCVEvidence(compiled CompiledModel, evidence *infeasibilityEvidence) (cvConflict, error) {
	invalid := func(format string, args ...any) (cvConflict, error) {
		return cvConflict{}, fmt.Errorf("%w: %s", errInvalidCVEvidence, fmt.Sprintf(format, args...))
	}
	if evidence == nil || evidence.kind != evidenceKindCVDisjoint || evidence.cv == nil {
		return invalid("missing type-C payload")
	}
	cv := evidence.cv
	if len(cv.classes) == 0 {
		return invalid("no Class bounds")
	}
	subs, _, err := splitByClass(compiled.Hard, compiled.Prepared.Classes)
	if err != nil {
		return invalid("split hard model: %v", err)
	}
	if len(cv.classes) != len(subs) {
		return invalid("%d Class bounds for %d Class subproblems", len(cv.classes), len(subs))
	}
	conflict := cvConflict{row: cv.cvRow, relaxedT: cv.relaxed, evidence: cv, boundT: new(big.Rat)}
	switch {
	case cv.side == "lower" && cv.cvRow.ID == cvMaxRowID && cv.cvRow.Sense == SenseLE:
		conflict.lower = true
	case cv.side == "upper" && cv.cvRow.ID == cvMinRowID && cv.cvRow.Sense == SenseGE:
	default:
		return invalid("side %q does not match CV row %q (sense %v)", cv.side, cv.cvRow.ID, cv.cvRow.Sense)
	}
	if cv.relaxed == nil || !isFinite(cv.cvRow.RHS) {
		return invalid("missing relaxed or limit value")
	}
	wantDirection := "upper"
	if conflict.lower {
		wantDirection = "lower"
	}
	for i, class := range cv.classes {
		if class.value == nil || class.direction != wantDirection {
			return invalid("Class bound %d is missing or has direction %q", i+1, class.direction)
		}
		if len(class.multipliers) != len(class.rows) {
			return invalid("Class bound %d has %d multipliers for %d rows", i+1, len(class.multipliers), len(class.rows))
		}
		for j, side := range class.rows {
			if side.row < 0 || side.row >= len(class.problem.Rows) {
				return invalid("Class bound %d references row %d outside its problem", i+1, side.row)
			}
			if y := class.multipliers[j]; y == nil || y.Sign() < 0 {
				return invalid("Class bound %d multiplier %d is not a nonnegative rational", i+1, j+1)
			}
		}
		conflict.boundT = ratAdd(conflict.boundT, class.value)
	}
	conflict.limitT = ratOf(cv.cvRow.RHS)
	if conflict.lower {
		conflict.margin = ratSub(conflict.boundT, conflict.relaxedT)
	} else {
		conflict.margin = ratSub(conflict.relaxedT, conflict.boundT)
	}
	if conflict.margin.Sign() <= 0 {
		return invalid("certificate separation %s is not positive", conflict.margin.RatString())
	}
	return conflict, nil
}

// fixedSecondMoment is F exactly. A failure is a broken compiled model.
func fixedSecondMoment(compiled CompiledModel) (*big.Rat, error) {
	_, fixed, err := buildSecondMomentExpression(compiled)
	if err != nil {
		return nil, fmt.Errorf("%w: fixed second moment: %v", errModelStructure, err)
	}
	if !isFinite(fixed) {
		return nil, fmt.Errorf("%w: fixed second moment is not finite", errModelStructure)
	}
	return ratOf(fixed), nil
}

// globalCVDiagnostic explains a hard type-C certificate without any solve.
// An errInvalidCVEvidence error means the evidence itself is broken; any
// other error means only the explanation could not be produced.
func globalCVDiagnostic(compiled CompiledModel, evidence *infeasibilityEvidence) (Diagnostic, error) {
	// Evidence checks come first: invalid evidence outranks a display failure.
	conflict, err := checkCVEvidence(compiled, evidence)
	if err != nil {
		return Diagnostic{}, err
	}
	fixed, err := fixedSecondMoment(compiled)
	if err != nil {
		return Diagnostic{}, err
	}
	requested := Bound(compiled.Prepared.Plan.Intent.Overall.CV)
	limitName, requestedLimit, direction := "overall.cv.min", requested.Min, "lower overall.cv.min"
	if conflict.lower {
		limitName, requestedLimit, direction = "overall.cv.max", requested.Max, "raise overall.cv.max"
	}
	boundS := ratAdd(fixed, conflict.boundT)
	limitS := ratAdd(fixed, conflict.limitT)

	var metrics []NamedValue
	omitted := false
	add := func(name, unit string, value float64, ok bool) {
		if !ok || !isFinite(value) {
			omitted = true
			return
		}
		metrics = append(metrics, NamedValue{Name: name, Value: value, Unit: unit})
	}
	fValue, fOK := nearestDisplay(fixed)
	add("fixed_second_moment_F", "second_moment", fValue, fOK)
	boundTValue, _, boundTOK := safeDisplay(conflict.boundT, conflict.lower)
	add("variable_second_moment_T_certified_bound", "second_moment", boundTValue, boundTOK)
	limitTValue, limitTOK := nearestDisplay(conflict.limitT)
	add("variable_second_moment_T_limit", "second_moment", limitTValue, limitTOK)
	relaxedValue, relaxedOK := nearestDisplay(conflict.relaxedT)
	add("variable_second_moment_T_relaxed_limit", "second_moment", relaxedValue, relaxedOK)
	boundSValue, boundSText, boundSOK := safeDisplay(boundS, conflict.lower)
	add("second_moment_S_certified_bound", "second_moment", boundSValue, boundSOK)
	limitSValue, limitSOK := nearestDisplay(limitS)
	add("second_moment_S_limit", "second_moment", limitSValue, limitSOK)
	marginValue, marginOK := nearestDisplay(conflict.margin)
	add("certificate_separation", "second_moment", marginValue, marginOK)
	mean := compiled.Prepared.ExpectedRTP()
	approxCV := math.NaN()
	if boundSOK {
		approxCV = secondMomentCV(mean, boundSValue)
	}
	add("approx_cv_of_certified_bound", "cv", approxCV, isFinite(approxCV))
	add("cv_requested_limit", "cv", requestedLimit, true)

	diagnostic := Diagnostic{
		Code: DiagnosticGlobalCVInfeasible, Status: StatusInfeasibleModel,
		Message:        globalCVMessage(conflict.lower, limitName, requestedLimit, boundSText, boundSOK, limitSValue, limitSOK, approxCV, direction),
		Requested:      &requested,
		Representation: RepresentationAtomicBuckets,
	}
	if conflict.row.YAMLPath != "" {
		diagnostic.SourcePaths = []string{conflict.row.YAMLPath}
	}
	diagnostic.ConstraintIDs, _ = participatingRows(evidence)
	summary := fmt.Sprintf("certificate %s, side %s (%s): the summed Class σ outer bounds miss the replay-relaxed limit; certificate_separation is the distance between the two, not an adjustment amount",
		evidenceKindCVDisjoint, conflict.evidence.side, limitName)
	if omitted {
		summary += displayOutOfRangeNote
	}
	diagnostic.Causes = []Cause{{Summary: summary, Metrics: metrics}}
	if paths := classBoundPaths(conflict.evidence); len(paths) > 0 {
		diagnostic.Causes = append(diagnostic.Causes, Cause{
			Summary:     "rows that bound each Class second moment in the certificate",
			SourcePaths: paths,
		})
	}
	return diagnostic, nil
}

// globalCVMessage states the strict second-moment conclusion, an approximate
// CV conversion, and the adjustment direction (never an amount).
func globalCVMessage(lower bool, limitName string, requestedLimit float64, boundText string, boundOK bool,
	limitValue float64, limitOK bool, approxCV float64, direction string,
) string {
	relation, extreme, cvRelation := "at least", "minimum may be higher", ">="
	beyond := "above"
	if !lower {
		relation, extreme, cvRelation, beyond = "at most", "maximum may be lower", "<=", "below"
	}
	var message string
	switch {
	case !boundOK:
		message = fmt.Sprintf("every replay-acceptable point violates %s = %.12g (verified certificate on the second moment; the bound exceeds the float64 display range, see the evidence file)",
			limitName, requestedLimit)
	case limitOK && separatedText(lower, boundText, strconv.FormatFloat(limitValue, 'g', -1, 64)):
		message = fmt.Sprintf("every replay-acceptable point has a full second moment of %s %s, %s the %s implied by %s = %.12g",
			relation, boundText, beyond, strconv.FormatFloat(limitValue, 'g', -1, 64), limitName, requestedLimit)
	default:
		message = fmt.Sprintf("every replay-acceptable point has a full second moment of %s %s; the verified certificate separates it from the limit implied by %s = %.12g, but the gap is below display precision, see the evidence file",
			relation, boundText, limitName, requestedLimit)
	}
	message += fmt.Sprintf(" (safe bound: the exact %s, so moving the limit to it does not guarantee feasibility)", extreme)
	if isFinite(approxCV) {
		message += fmt.Sprintf("; with the configured expected RTP this is approximately CV %s %.12g, and the strict conclusion is the second-moment bound in the evidence file", cvRelation, approxCV)
	}
	return message + fmt.Sprintf(". To resolve: %s, or relax the Class rules that bound each Class second moment (see causes).", direction)
}

// separatedText reports whether the two printed decimals, read exactly, are
// strictly separated in the conflict direction (bound above the limit for
// side "lower", below it for side "upper").
func separatedText(lower bool, boundText, limitText string) bool {
	bound, ok1 := new(big.Rat).SetString(boundText)
	limit, ok2 := new(big.Rat).SetString(limitText)
	if !ok1 || !ok2 {
		return false
	}
	if lower {
		return bound.Cmp(limit) > 0
	}
	return bound.Cmp(limit) < 0
}

// classBoundPaths lists, once each and in Class order, the YAML paths of the
// Class rows with a positive multiplier in the certificate.
func classBoundPaths(cv *cvDisjointEvidence) []string {
	var paths []string
	seen := map[string]bool{}
	for _, class := range cv.classes {
		for i, multiplier := range class.multipliers {
			if multiplier == nil || multiplier.Sign() <= 0 || i >= len(class.rows) {
				continue
			}
			path := class.problem.Rows[class.rows[i].row].YAMLPath
			if path != "" && !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	return paths
}

// nearestDisplay converts an exact value to the nearest float64; ok is false
// when the result is not finite.
func nearestDisplay(value *big.Rat) (float64, bool) {
	f, _ := value.Float64()
	return f, isFinite(f)
}

// safeDisplay returns a float64 and its shortest decimal text such that both,
// read exactly, lie on the safe side of bound: at most bound when lower is
// true (a lower bound may only be understated), at least bound otherwise.
// Shortest round-trip text alone is not enough: the float64 nearest 0.3 prints
// as "0.3" although its exact value is below 0.3. encoding/json prints the
// same shortest digits, so the check also covers the report's JSON text.
func safeDisplay(bound *big.Rat, lower bool) (float64, string, bool) {
	toward := math.Inf(1)
	if lower {
		toward = math.Inf(-1)
	}
	safe := func(r *big.Rat) bool {
		if lower {
			return r.Cmp(bound) <= 0
		}
		return r.Cmp(bound) >= 0
	}
	f, _ := bound.Float64()
	if isFinite(f) && !safe(new(big.Rat).SetFloat64(f)) {
		f = math.Nextafter(f, toward)
	}
	for step := 0; step <= maxSafeDisplaySteps && isFinite(f); step++ {
		text := strconv.FormatFloat(f, 'g', -1, 64)
		if exact, ok := new(big.Rat).SetString(text); ok && safe(exact) {
			return f, text, true
		}
		f = math.Nextafter(f, toward)
	}
	return 0, "", false
}
