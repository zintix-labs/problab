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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

// brokenSecondMoment returns a copy of compiled whose prepared second moments
// buildSecondMomentExpression rejects. The compiled rows (including the CV
// rows) and every builder input stay intact, so only F and σ are affected.
func brokenSecondMoment(compiled CompiledModel) CompiledModel {
	classes := append([]PreparedClass(nil), compiled.Prepared.Classes...)
	for i := range classes {
		if classes[i].Intent {
			classes[i].Buckets = append([]PreparedBucket(nil), classes[i].Buckets...)
			classes[i].Buckets[0].SecondMoment = -1
			break
		}
	}
	compiled.Prepared.Classes = classes
	return compiled
}

// TestSecondMomentFailureIsNotSwallowed is B09: a second-moment expression
// that cannot be built is a broken model, never F = 0 or a fully fixed σ.
func TestSecondMomentFailureIsNotSwallowed(t *testing.T) {
	compiled := brokenSecondMoment(evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceFixtureClass(0, .5), evidenceFixtureClass(1, .5)))
	run := NewIntentEngine(NewGonumSolver()).newRun(compiled, nil)
	if _, err := run.fixedSecond(); !errors.Is(err, errModelStructure) {
		t.Fatalf("fixedSecond err=%v, want errModelStructure", err)
	}
	withoutCV := cloneProblemWithoutRows(compiled.Hard, func(row LinearRow) bool { return row.ClassID == "" })
	if _, err := run.sigmaOf(withoutCV); !errors.Is(err, errModelStructure) {
		t.Fatalf("sigmaOf err=%v, want errModelStructure", err)
	}
	// With CV rows σ comes from the rows; the minimum-CV decision still needs
	// F and must fail instead of reporting S = T.
	if _, err := run.decideJoint(context.Background(), compiled.Hard, StageSelectCanonicalBucketProbabilities,
		ObjectiveCanonicalBucketProbability, nil, true, nil); !errors.Is(err, errModelStructure) {
		t.Fatalf("minimum-CV decideJoint err=%v, want errModelStructure", err)
	}
	if _, err := run.decideJoint(context.Background(), withoutCV, StageSelectCanonicalBucketProbabilities,
		ObjectiveCanonicalBucketProbability, nil, true, nil); !errors.Is(err, errModelStructure) {
		t.Fatalf("minimum-CV decideJoint without CV rows err=%v, want errModelStructure", err)
	}
	solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), compiled)
	if err != nil || solution.Status != StatusInternalError {
		t.Fatalf("status=%s err=%v, want INTERNAL_ERROR", solution.Status, err)
	}
}

// recordingSolver records every backend call so tests can prove that no
// diagnostic or joint solve happens after a type-C certificate.
type recordingSolver struct {
	inner Solver
	calls []recordedSolve
}

type recordedSolve struct {
	origin  ObjectiveOrigin
	classes map[string]bool // Class indices referenced by the problem's variables
}

func (s *recordingSolver) Solve(ctx context.Context, p LinearProblem, o LinearObjective, opt SolveOptions) (SolveResult, error) {
	call := recordedSolve{origin: o.Origin, classes: map[string]bool{}}
	for _, v := range p.Variables {
		id := string(v.ID)
		switch {
		case strings.HasPrefix(id, "p:") && len(id) >= 6:
			call.classes[id[2:6]] = true
		case strings.HasPrefix(id, "d:main:") && len(id) >= 11:
			call.classes[id[7:11]] = true
		}
	}
	s.calls = append(s.calls, call)
	return s.inner.Solve(ctx, p, o, opt)
}

// typeCCase is one hard model refuted by a type-C certificate.
type typeCCase struct {
	name     string
	compiled func(*testing.T) CompiledModel
	side     string
	row      RowID
	path     string
}

func typeCCases() []typeCCase {
	return []typeCCase{
		{"lower", func(t *testing.T) CompiledModel {
			return evidenceCompile(t, NumericRange{Min: 0, Max: 0}, evidenceFixtureClass(0, .5), evidenceFixtureClass(1, .5))
		}, "lower", cvMaxRowID, "overall.cv.max"},
		{"upper", func(t *testing.T) CompiledModel {
			return evidenceCompile(t, NumericRange{Min: 50, Max: 60}, evidenceFixtureClass(0, .5), evidenceFixtureClass(1, .5))
		}, "upper", cvMinRowID, "overall.cv.min"},
		// F ≠ 0: a fixed Class contributes to S but not to T (B04).
		{"mixed", func(t *testing.T) CompiledModel {
			return evidenceCompile(t, NumericRange{Min: 0, Max: 0}, evidenceFixtureClass(0, .5), evidenceFixtureClass(1, .3),
				modelTestFixedClass(2, "fixed", 0.2, 3, 12))
		}, "lower", cvMaxRowID, "overall.cv.max"},
	}
}

// hardTypeC returns the hard type-C classification of compiled.
func hardTypeC(t *testing.T, compiled CompiledModel) classification {
	t.Helper()
	run := NewIntentEngine(NewGonumSolver()).newRun(compiled, nil)
	decision, err := run.decideJoint(context.Background(), compiled.Hard, StageProveHardFeasibility, ObjectiveHardFeasibility, nil, false, nil)
	if err != nil || decision.class != evidenceInfeasible || decision.evidence.kind != evidenceKindCVDisjoint {
		t.Fatalf("want a hard type-C certificate: class=%s evidence=%+v err=%v", decision.class, decision.evidence, err)
	}
	return decision.classification
}

// TestTypeCDiagnosticCallsNoSolver is B02: after the type-C certificate the
// engine calls no Solver, so the whole run makes exactly the certificate's
// calls, none of them diagnostic and none spanning two Classes.
func TestTypeCDiagnosticCallsNoSolver(t *testing.T) {
	for _, test := range typeCCases() {
		compiled := test.compiled(t)
		certify := &recordingSolver{inner: NewGonumSolver()}
		if _, err := NewIntentEngine(certify).newRun(compiled, nil).decideJoint(context.Background(), compiled.Hard,
			StageProveHardFeasibility, ObjectiveHardFeasibility, nil, false, nil); err != nil {
			t.Fatal(err)
		}
		solver := &recordingSolver{inner: NewGonumSolver()}
		engine := NewIntentEngine(solver)
		engine.evidenceDirectory = t.TempDir()
		solution, err := engine.Solve(context.Background(), compiled)
		if err != nil || solution.Status != StatusInfeasibleModel {
			t.Fatalf("%s: status=%s err=%v", test.name, solution.Status, err)
		}
		if len(solver.calls) != len(certify.calls) {
			t.Fatalf("%s: %d backend calls, the certificate alone needs %d", test.name, len(solver.calls), len(certify.calls))
		}
		for i, call := range solver.calls {
			if call.origin == ObjectiveDiagnostic || len(call.classes) > 1 {
				t.Fatalf("%s: call %d origin=%s spans Classes %v", test.name, i, call.origin, call.classes)
			}
		}
	}
}

// TestTypeCDiagnosticMatchesEvidence is B03 and B04: the diagnostic names the
// conflicting CV row, never presents the safe bound as achievable, and every
// figure is the §3.5 conversion of the exact values the saved bundle
// re-verifies, with S = F + T exact (F ≠ 0 in the mixed case).
func TestTypeCDiagnosticMatchesEvidence(t *testing.T) {
	for _, test := range typeCCases() {
		compiled := test.compiled(t)
		solution := solveWithEvidence(t, compiled, nil)
		if solution.Status != StatusInfeasibleModel || len(solution.Diagnostics) != 1 {
			t.Fatalf("%s: solution=%+v", test.name, solution)
		}
		diagnostic := solution.Diagnostics[0]
		if diagnostic.Code != DiagnosticGlobalCVInfeasible || diagnostic.Achievable != nil || diagnostic.Deficit != 0 {
			t.Fatalf("%s: diagnostic=%+v", test.name, diagnostic)
		}
		if len(diagnostic.SourcePaths) != 1 || diagnostic.SourcePaths[0] != test.path || diagnostic.ConstraintIDs[0] != string(test.row) {
			t.Fatalf("%s: paths=%v ids=%v", test.name, diagnostic.SourcePaths, diagnostic.ConstraintIDs)
		}
		var bundle InfeasibilityBundle
		readEvidence(t, diagnostic.EvidencePath, &bundle)
		if err := VerifyInfeasibilityEvidence(bundle); err != nil || bundle.Side != test.side {
			t.Fatalf("%s: side=%s verify=%v", test.name, bundle.Side, err)
		}
		// Recompute the exact values from the bundle alone.
		problem, rule, err := bundle.EvidenceHeader.open()
		if err != nil {
			t.Fatal(err)
		}
		row := findRowByID(problem, test.row)
		relaxed, _, err := relaxCVRow(problem, *row, rule)
		if err != nil {
			t.Fatal(err)
		}
		boundT := new(big.Rat)
		for _, bound := range bundle.Bounds {
			value, ok := new(big.Rat).SetString(bound.Value)
			if !ok {
				t.Fatalf("bound value %q", bound.Value)
			}
			boundT = ratAdd(boundT, value)
		}
		lower := test.side == "lower"
		margin := ratSub(boundT, relaxed)
		if !lower {
			margin = ratNeg(margin)
		}
		_, fixedFloat, err := buildSecondMomentExpression(compiled)
		if err != nil {
			t.Fatal(err)
		}
		if (test.name == "mixed") != (fixedFloat != 0) {
			t.Fatalf("%s: F=%g", test.name, fixedFloat)
		}
		fixed := ratOf(fixedFloat)
		safe := func(value *big.Rat) float64 {
			f, _, ok := safeDisplay(value, lower)
			if !ok {
				t.Fatalf("no safe display for %s", value.RatString())
			}
			return f
		}
		nearest := func(value *big.Rat) float64 { f, _ := value.Float64(); return f }
		for name, want := range map[string]float64{
			"fixed_second_moment_F":                    fixedFloat,
			"variable_second_moment_T_certified_bound": safe(boundT),
			"variable_second_moment_T_limit":           row.RHS,
			"variable_second_moment_T_relaxed_limit":   nearest(relaxed),
			"second_moment_S_certified_bound":          safe(ratAdd(fixed, boundT)),
			"second_moment_S_limit":                    nearest(ratAdd(fixed, ratOf(row.RHS))),
			"certificate_separation":                   nearest(margin),
		} {
			if got := causeMetric(t, diagnostic, name); math.Float64bits(got) != math.Float64bits(want) {
				t.Fatalf("%s: %s=%.17g want %.17g", test.name, name, got, want)
			}
		}
		direction := "raise overall.cv.max"
		if !lower {
			direction = "lower overall.cv.min"
		}
		if !strings.Contains(diagnostic.Message, direction) || !strings.Contains(diagnostic.Message, "approximately CV") ||
			strings.Contains(diagnostic.Message, "required") {
			t.Fatalf("%s: message=%s", test.name, diagnostic.Message)
		}
		if _, err := json.Marshal(diagnostic); err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
	}
}

// TestSafeDisplayDirection is B05: both the float64 and its printed decimal
// lie on the safe side of the exact bound, and the text round-trips.
func TestSafeDisplayDirection(t *testing.T) {
	for _, test := range []struct {
		bound string
		lower bool
		text  string
	}{
		// The nearest float64 prints as "0.3" (> bound) and "0.1" (< bound).
		{"2999999999999999999/10000000000000000000", true, "0.29999999999999993"},
		{"1000000000000000001/10000000000000000000", false, "0.10000000000000002"},
	} {
		bound, _ := new(big.Rat).SetString(test.bound)
		value, text, ok := safeDisplay(bound, test.lower)
		if !ok || text != test.text {
			t.Fatalf("%s: text=%q ok=%v, want %q", test.bound, text, ok, test.text)
		}
		exact, _ := new(big.Rat).SetString(text)
		floatExact := new(big.Rat).SetFloat64(value)
		if test.lower && (floatExact.Cmp(bound) > 0 || exact.Cmp(bound) > 0) || !test.lower && (floatExact.Cmp(bound) < 0 || exact.Cmp(bound) < 0) {
			t.Fatalf("%s: float or text on the unsafe side", test.bound)
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil || math.Float64bits(parsed) != math.Float64bits(value) {
			t.Fatalf("%s: text %q does not round-trip to %v", test.bound, text, value)
		}
	}
	// Bound and limit printing as the same number are not called separated.
	message := globalCVMessage(true, "overall.cv.max", 1, "5", true, 5, true, math.NaN(), "raise overall.cv.max")
	if strings.Contains(message, "above the") || !strings.Contains(message, "below display precision") {
		t.Fatalf("message=%s", message)
	}
}

// TestTypeCExplanationFailureKeepsCertificate is B06: when only the display
// data is broken (F cannot be built), the verified certificate still decides
// INFEASIBLE_MODEL with the generic certificate diagnostic, no Solver runs,
// and the saved bundle re-verifies.
func TestTypeCExplanationFailureKeepsCertificate(t *testing.T) {
	compiled := typeCCases()[0].compiled(t)
	evidence := hardTypeC(t, compiled)
	broken := brokenSecondMoment(compiled)
	if _, err := globalCVDiagnostic(broken, evidence.evidence); err == nil || errors.Is(err, errInvalidCVEvidence) {
		t.Fatalf("want an explanation failure, got %v", err)
	}
	solver := &recordingSolver{inner: NewGonumSolver()}
	engine := NewIntentEngine(solver)
	engine.evidenceDirectory = t.TempDir()
	run := engine.newRun(broken, nil)
	solution, err := run.hardInfeasible(context.Background(), evidence)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := solution.Diagnostics[0]
	if solution.Status != StatusInfeasibleModel || diagnostic.Code != DiagnosticHardModelInfeasible || len(solver.calls) != 0 {
		t.Fatalf("status=%s diagnostic=%+v calls=%d", solution.Status, diagnostic, len(solver.calls))
	}
	if last := diagnostic.Causes[len(diagnostic.Causes)-1]; !strings.HasPrefix(last.Summary, "global CV explanation unavailable") {
		t.Fatalf("causes=%+v", diagnostic.Causes)
	}
	var bundle InfeasibilityBundle
	readEvidence(t, diagnostic.EvidencePath, &bundle)
	if err := VerifyInfeasibilityEvidence(bundle); err != nil {
		t.Fatal(err)
	}
}

// forgeCV returns a copy of a type-C classification whose payload edit may
// change; the original stays untouched.
func forgeCV(source classification, edit func(*cvDisjointEvidence)) classification {
	evidence := *source.evidence
	cv := *evidence.cv
	cv.classes = append([]objectiveBoundEvidence(nil), cv.classes...)
	edit(&cv)
	evidence.cv = &cv
	source.evidence = &evidence
	return source
}

// TestInvalidTypeCEvidenceIsNeverInfeasible is B07, through hardInfeasible
// (the entry proveHardFeasibility uses): evidence inconsistent with itself is
// INTERNAL_ERROR without panicking, also when F fails at the same time
// (evidence checks come first), and no Solver runs.
func TestInvalidTypeCEvidenceIsNeverInfeasible(t *testing.T) {
	compiled := typeCCases()[0].compiled(t)
	valid := hardTypeC(t, compiled)
	sum := func(cv *cvDisjointEvidence) *big.Rat {
		total := new(big.Rat)
		for _, class := range cv.classes {
			total = ratAdd(total, class.value)
		}
		return total
	}
	cases := map[string]func(*cvDisjointEvidence){
		"non-positive separation": func(cv *cvDisjointEvidence) { cv.relaxed = sum(cv) },
		"no Class bounds":         func(cv *cvDisjointEvidence) { cv.classes = nil },
		"wrong CV row":            func(cv *cvDisjointEvidence) { cv.cvRow.ID = cvMinRowID },
		"side mismatch":           func(cv *cvDisjointEvidence) { cv.side = "upper" },
		"bound count":             func(cv *cvDisjointEvidence) { cv.classes = cv.classes[:1] },
		"missing bound value":     func(cv *cvDisjointEvidence) { cv.classes[0].value = nil },
		"row index out of range": func(cv *cvDisjointEvidence) {
			class := cv.classes[0]
			class.rows = append([]rowSide(nil), class.rows...)
			class.rows[0].row = len(class.problem.Rows)
			cv.classes[0] = class
		},
		"nil multiplier": func(cv *cvDisjointEvidence) {
			class := cv.classes[0]
			class.multipliers = append([]*big.Rat(nil), class.multipliers...)
			class.multipliers[0] = nil
			cv.classes[0] = class
		},
		"negative multiplier": func(cv *cvDisjointEvidence) {
			class := cv.classes[0]
			class.multipliers = append([]*big.Rat(nil), class.multipliers...)
			class.multipliers[0] = big.NewRat(-1, 1)
			cv.classes[0] = class
		},
		"multiplier count": func(cv *cvDisjointEvidence) {
			class := cv.classes[0]
			class.multipliers = class.multipliers[:len(class.multipliers)-1]
			cv.classes[0] = class
		},
	}
	forgeries := map[string]classification{}
	for name, edit := range cases {
		forgeries[name] = forgeCV(valid, edit)
	}
	missing := valid
	payload := *valid.evidence
	payload.cv = nil
	missing.evidence = &payload
	forgeries["missing type-C payload"] = missing
	for name, forged := range forgeries {
		for _, model := range []struct {
			label    string
			compiled CompiledModel
		}{{"valid model", compiled}, {"F also broken", brokenSecondMoment(compiled)}} {
			solver := &recordingSolver{inner: NewGonumSolver()}
			engine := NewIntentEngine(solver)
			engine.evidenceDirectory = t.TempDir()
			run := engine.newRun(model.compiled, nil)
			solution, err := run.hardInfeasible(context.Background(), forged)
			if err != nil || solution.Status != StatusInternalError || len(solver.calls) != 0 {
				t.Errorf("%s, %s: status=%s calls=%d diagnostics=%+v", name, model.label, solution.Status, len(solver.calls), solution.Diagnostics)
			}
		}
	}
}

// TestTypeCDisplayValuesStayFinite is B08: a finite certificate value beyond
// the float64 range is omitted instead of breaking JSON, and a non-positive
// mean omits the CV conversion.
func TestTypeCDisplayValuesStayFinite(t *testing.T) {
	compiled := typeCCases()[0].compiled(t)
	valid := hardTypeC(t, compiled)
	huge := new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), 1100))
	forged := forgeCV(valid, func(cv *cvDisjointEvidence) { cv.classes[0].value = huge })
	diagnostic, err := globalCVDiagnostic(compiled, forged.evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range diagnostic.Causes[0].Metrics {
		if !isFinite(metric.Value) {
			t.Fatalf("non-finite metric %+v", metric)
		}
		if strings.Contains(metric.Name, "certified_bound") || metric.Name == "certificate_separation" {
			t.Fatalf("out-of-range metric %s was not omitted", metric.Name)
		}
	}
	if !strings.Contains(diagnostic.Causes[0].Summary, "exceed the float64 range") || !strings.Contains(diagnostic.Message, "evidence file") {
		t.Fatalf("summary=%s message=%s", diagnostic.Causes[0].Summary, diagnostic.Message)
	}
	if _, err := json.Marshal(diagnostic); err != nil {
		t.Fatal(err)
	}

	zeroMean := compiled
	zeroMean.Prepared.Classes = append([]PreparedClass(nil), compiled.Prepared.Classes...)
	for i := range zeroMean.Prepared.Classes {
		zeroMean.Prepared.Classes[i].Design.Exp = 0
	}
	diagnostic, err = globalCVDiagnostic(zeroMean, valid.evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range diagnostic.Causes[0].Metrics {
		if metric.Name == "approx_cv_of_certified_bound" {
			t.Fatalf("CV conversion with a non-positive mean: %+v", metric)
		}
	}
	if strings.Contains(diagnostic.Message, "approximately CV") {
		t.Fatalf("message=%s", diagnostic.Message)
	}
}

// TestStageCountersAreStageScoped is B10: stage reports and per-stage
// evidence files carry the running stage's totals and its own exact-simplex
// peaks; the run-level counters keep the run maxima.
func TestStageCountersAreStageScoped(t *testing.T) {
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceFixtureClass(0, .5), evidenceFixtureClass(1, .5))
	subs, _, err := splitByClass(compiled.Hard, compiled.Prepared.Classes)
	if err != nil {
		t.Fatal(err)
	}
	large, _, err := buildEnvelope(subs[1].problem, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	small, _, err := buildEnvelope(LinearProblem{
		Variables: []LinearVariable{{ID: "x", Lower: 0, Upper: 1}},
		Rows:      []LinearRow{{ID: "r", Sense: SenseLE, RHS: 1, Terms: []LinearTerm{{Variable: "x", Coeff: 1}}}},
	}, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	peakOf := func(model exactModel) exactPeak {
		var peak exactPeak
		acquireFarkas(context.Background(), model, exactBudget{maxPivots: 20000, maxBits: 1 << 14, stage: &peak})
		return peak
	}
	largePeak, smallPeak := peakOf(large), peakOf(small)
	if largePeak.pivots <= smallPeak.pivots || largePeak.bits <= smallPeak.bits {
		t.Fatalf("fixture peaks do not separate: large=%+v small=%+v", largePeak, smallPeak)
	}

	run := NewIntentEngine(NewGonumSolver()).newRun(compiled, nil)
	run.evidenceDir = t.TempDir()
	run.beginStageCounters()
	acquireFarkas(context.Background(), large, run.budget())
	run.beginStageCounters()
	acquireFarkas(context.Background(), small, run.budget())
	stage := run.stageCounters()
	if stage.MaxExactPivots != smallPeak.pivots || stage.MaxExactBits != smallPeak.bits || stage.ExactCalls != 1 {
		t.Fatalf("stage counters=%+v, want the second stage's own peak %+v", stage, smallPeak)
	}
	if run.counters.MaxExactPivots != largePeak.pivots || run.counters.MaxExactBits != largePeak.bits || run.counters.ExactCalls != 2 {
		t.Fatalf("run counters=%+v, want run peak %+v over 2 calls", run.counters, largePeak)
	}

	// Both evidence files written by a failing later stage are stage-scoped.
	unavailable := run.evidenceUnavailable(StageMaximizeOtherBucketVisibility, nil, compiled.Hard,
		classification{class: evidenceUnknown, reasons: []string{"test"}})
	precision := run.minimumCVPrecisionUnproven(jointDecision{candidateS: big.NewRat(1, 1), requiredS: big.NewRat(1, 100)},
		compiled.Hard, nil, 0)
	for name, solution := range map[string]EngineSolution{"EvidenceUnavailable": unavailable, "PrecisionUnproven": precision} {
		var file NumericalEvidence
		readEvidence(t, solution.Diagnostics[0].EvidencePath, &file)
		if file.Counters == nil || file.Counters.ExactCalls != 1 || file.Counters.MaxExactPivots != smallPeak.pivots || file.Counters.MaxExactBits != smallPeak.bits {
			t.Fatalf("%s counters=%+v, want only the failing stage", name, file.Counters)
		}
	}
}

// TestExactPeakBitsCoverIntermediateTableaux is B10 for the bit peak: the
// recorded peak is the largest tableau of the whole call, which can exceed the
// final one. The reference replays the same deterministic Bland path with a
// pivot cap of n and measures the tableau left after n pivots.
func TestExactPeakBitsCoverIntermediateTableaux(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	objective := map[int]*big.Rat{0: big.NewRat(1, 1), 1: big.NewRat(2, 1), 2: big.NewRat(3, 1)}
	intermediateLarger := 0
	for trial := 0; trial < 200; trial++ {
		var problem LinearProblem
		for i := 0; i < 3; i++ {
			problem.Variables = append(problem.Variables, LinearVariable{ID: VariableID(fmt.Sprint("x", i)), Lower: 0, Upper: 10})
		}
		for i := 0; i < 4; i++ {
			row := LinearRow{ID: RowID(fmt.Sprint("r", i)), Sense: SenseLE, RHS: float64(rng.Intn(20) + 1)}
			for _, v := range problem.Variables {
				row.Terms = append(row.Terms, LinearTerm{Variable: v.ID, Coeff: float64(rng.Intn(11) + 1)})
			}
			problem.Rows = append(problem.Rows, row)
		}
		model, _, err := buildEnvelope(problem, 1e-9)
		if err != nil {
			t.Fatal(err)
		}
		var usage EvidenceCounters
		var peak exactPeak
		full := multiplierTableau(model, objective, false)
		_, _, pivots := full.solve(context.Background(), exactBudget{maxPivots: 20000, maxBits: 1 << 14, usage: &usage, stage: &peak})
		want := 0
		for n := 0; n <= pivots; n++ {
			replay := multiplierTableau(model, objective, false)
			replay.solve(context.Background(), exactBudget{maxPivots: n, maxBits: 1 << 14})
			want = max(want, replay.maxBits())
		}
		if peak.bits != want || usage.MaxExactBits != want {
			t.Fatalf("trial %d: stage peak %d, run peak %d, want %d over %d pivots", trial, peak.bits, usage.MaxExactBits, want, pivots)
		}
		if want > full.maxBits() {
			intermediateLarger++
		}
	}
	if intermediateLarger == 0 {
		t.Fatal("no trial has an intermediate tableau larger than the final one; the test cannot tell the two apart")
	}
	t.Logf("%d of 200 trials peak above the final tableau", intermediateLarger)
}

// stageSolveCounter counts finished backend solves per stage from the
// observer stream, independently of the engine's own counters.
type stageSolveCounter map[OptimizationStageID]int

func (c stageSolveCounter) observe(event OptimizationStageEvent) {
	if event.State == SolveEventFinished {
		c[event.Stage]++
	}
}

// TestStageCountersFollowTheProductionFlow is B10 for the stage wiring: in a
// real run every stage report counts only its own solves (matched against the
// observer stream), the reports and the hard stage add up to the run, and a
// bisection stage that fails mid-run writes an evidence file with only its
// own counters.
func TestStageCountersFollowTheProductionFlow(t *testing.T) {
	compiled := skewedModel(t)
	counts := stageSolveCounter{}
	run := NewIntentEngine(NewGonumSolver()).newRun(compiled, counts.observe)
	solution, err := run.solveStages(context.Background())
	if err != nil || solution.Status != StatusOptimal {
		t.Fatalf("status=%s err=%v", solution.Status, err)
	}
	reports := map[OptimizationStageID]*EvidenceCounters{
		StageMinimizeMainProfileDeviation:        solution.MainProfileOptimization.Evidence,
		StageMaximizeOtherBucketVisibility:       solution.OtherBucketVisibilityOptimization.Evidence,
		StageMaximizeMainGroupInternalVisibility: solution.MainGroupInternalVisibilityOptimization.Evidence,
		StageSelectCanonicalBucketProbabilities:  solution.CanonicalBucketProbabilitySelection.Evidence,
	}
	total, exactCalls := counts[StageProveHardFeasibility], 0
	for stage, report := range reports {
		if report == nil {
			continue // stage skipped by this model
		}
		if report.Solves != counts[stage] {
			t.Fatalf("%s report counts %d solves, the stage made %d", stage, report.Solves, counts[stage])
		}
		if report.MaxExactPivots > run.counters.MaxExactPivots || report.MaxExactBits > run.counters.MaxExactBits {
			t.Fatalf("%s stage peak %d/%d exceeds the run peak %d/%d", stage, report.MaxExactPivots, report.MaxExactBits,
				run.counters.MaxExactPivots, run.counters.MaxExactBits)
		}
		total += report.Solves
		exactCalls += report.ExactCalls
	}
	if total != run.counters.Solves || exactCalls > run.counters.ExactCalls {
		t.Fatalf("stage solves %d (exact %d) vs run %d (exact %d)", total, exactCalls, run.counters.Solves, run.counters.ExactCalls)
	}

	// A bisection stage failing mid-run: its PrecisionUnproven evidence file
	// carries only that stage's counters.
	for _, test := range []struct {
		stage OptimizationStageID
		limit func(*EngineOptions)
	}{
		{StageMinimizeMainProfileDeviation, func(o *EngineOptions) { o.ProfileBisectionIterations = 5 }},
		{StageMaximizeMainGroupInternalVisibility, func(o *EngineOptions) { o.MainGroupInternalVisibilityBisectionIterations = 5 }},
	} {
		failing := skewedModel(t)
		test.limit(&failing.Prepared.Plan.EngineOptions)
		counts := stageSolveCounter{}
		engine := NewIntentEngine(NewGonumSolver())
		engine.evidenceDirectory = t.TempDir()
		run := engine.newRun(failing, counts.observe)
		solution, err := run.solveStages(context.Background())
		if err != nil || solution.Status != StatusNumericalFailure || solution.Diagnostics[0].Code != DiagnosticPrecisionUnproven {
			t.Fatalf("%s: status=%s diagnostics=%+v err=%v", test.stage, solution.Status, solution.Diagnostics, err)
		}
		var file NumericalEvidence
		readEvidence(t, solution.Diagnostics[0].EvidencePath, &file)
		if file.Stage != string(test.stage) || file.Counters == nil || file.Counters.Solves != counts[test.stage] || file.Counters.Solves == run.counters.Solves {
			t.Fatalf("%s: file stage %s counters=%+v, stage solves %d, run solves %d", test.stage, file.Stage, file.Counters, counts[test.stage], run.counters.Solves)
		}
	}
}
