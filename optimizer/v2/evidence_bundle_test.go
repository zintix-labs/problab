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
	"math"
	"math/big"
	"os"
	"strings"
	"testing"
)

func readEvidence(t *testing.T, path string, value any) {
	t.Helper()
	if path == "" {
		t.Fatal("no evidence path recorded")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		t.Fatal(err)
	}
}

func solveWithEvidence(t *testing.T, compiled CompiledModel, budget *exactBudget) EngineSolution {
	t.Helper()
	engine := NewIntentEngine(NewGonumSolver())
	engine.evidenceDirectory, engine.exactBudget = t.TempDir(), budget
	solution, err := engine.Solve(context.Background(), compiled)
	if err != nil {
		t.Fatal(err)
	}
	return solution
}

// rebind replaces the bundle's problem by another one with a valid SHA-256,
// as an attacker reusing the payload for a different probe would.
func rebind(t *testing.T, header *EvidenceHeader, edit func(*LinearProblem)) {
	t.Helper()
	problem, err := header.Problem.Problem()
	if err != nil {
		t.Fatal(err)
	}
	edit(&problem)
	header.Problem = SnapshotProblem(problem)
	if header.ProblemSHA256, err = header.Problem.SHA256(); err != nil {
		t.Fatal(err)
	}
}

// TestInfeasibilityEvidenceReverifies is A16: the saved hard certificates of
// types A and C verify from their snapshot alone; a changed multiplier, a
// different bound problem, a changed rule or a broken digest fail.
func TestInfeasibilityEvidenceReverifies(t *testing.T) {
	farkas := skewedModel(t)
	farkas.Hard = cloneLinearProblem(farkas.Hard)
	farkas.Hard.Rows = append(farkas.Hard.Rows, LinearRow{ID: "test:impossible", ClassID: farkas.Prepared.Classes[0].ID,
		Sense: SenseGE, RHS: 2, Terms: []LinearTerm{{Variable: farkas.Primary[0].ID, Coeff: 1}}})
	cv := evidenceCompile(t, NumericRange{Min: 0, Max: 0}, evidenceFixtureClass(0, .5), evidenceFixtureClass(1, .5))
	for _, test := range []struct {
		name     string
		compiled CompiledModel
		kind     string
	}{{"A", farkas, evidenceKindFarkas}, {"C", cv, evidenceKindCVDisjoint}} {
		solution := solveWithEvidence(t, test.compiled, nil)
		if solution.Status != StatusInfeasibleModel {
			t.Fatalf("%s: status=%s", test.name, solution.Status)
		}
		var bundle InfeasibilityBundle
		readEvidence(t, solution.Diagnostics[0].EvidencePath, &bundle)
		if bundle.Kind != test.kind || bundle.Rule.Version != productRule(1e-9).version() || bundle.Stage != string(StageProveHardFeasibility) {
			t.Fatalf("%s: bundle kind=%s rule=%+v stage=%s", test.name, bundle.Kind, bundle.Rule, bundle.Stage)
		}
		if err := VerifyInfeasibilityEvidence(bundle); err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		tampered := []func(*InfeasibilityBundle){
			func(b *InfeasibilityBundle) { b.ProblemSHA256 = strings.Repeat("0", 64) },
			// Relabeling to the internal set is a weaker, still true claim;
			// an unknown rule or one that does not match τ must fail.
			func(b *InfeasibilityBundle) { b.Rule.Version = "rows-tau/bounds-tau/v0" },
			func(b *InfeasibilityBundle) { b.Rule.RowTolerance = encodeFloat64(2e-9) },
			func(b *InfeasibilityBundle) {
				rebind(t, &b.EvidenceHeader, func(p *LinearProblem) {
					for i := range p.Rows { // a feasible variant of the same problem
						switch p.Rows[i].ID {
						case "test:impossible":
							p.Rows[i].RHS = 0
						case cvMaxRowID:
							p.Rows[i].RHS += 10
						}
					}
				})
			},
		}
		if test.kind == evidenceKindFarkas {
			// Farkas certificates are scale-invariant; zeroing them is not.
			tampered = append(tampered, func(b *InfeasibilityBundle) {
				b.Multipliers = append([]RowMultiplier(nil), b.Multipliers...)
				for i := range b.Multipliers {
					b.Multipliers[i].Value = "0"
				}
			}, func(b *InfeasibilityBundle) {
				b.Multipliers = append([]RowMultiplier(nil), b.Multipliers...)
				b.Multipliers[0].Value = "-1"
			})
		} else {
			tampered = append(tampered, func(b *InfeasibilityBundle) {
				b.Bounds = append([]ClassBound(nil), b.Bounds...)
				value, _ := new(big.Rat).SetString(b.Bounds[0].Value)
				b.Bounds[0].Value = ratSub(value, big.NewRat(1, 1<<40)).String()
			})
		}
		for i, tamper := range tampered {
			copied := bundle
			tamper(&copied)
			if err := VerifyInfeasibilityEvidence(copied); err == nil {
				t.Fatalf("%s: tampered bundle %d verified", test.name, i)
			}
		}
	}
}

// TestEmptyBoxEvidenceReverifies covers type B, which needs no solve.
func TestEmptyBoxEvidenceReverifies(t *testing.T) {
	run := newEvidenceRun(t, falseInfeasibleSolver{})
	problem := oneVarProblem(0, math.Inf(1), xRow("le", SenseLE, -1, 1))
	result := classifyForTest(t, run, problem)
	if result.class != evidenceInfeasible || result.evidence.kind != evidenceKindEmptyBox {
		t.Fatalf("%+v", result)
	}
	result.evidence.joint = &problem
	bundle, err := run.infeasibilityBundle(result.evidence, StageProveHardFeasibility, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyInfeasibilityEvidence(bundle); err != nil {
		t.Fatal(err)
	}
	rebind(t, &bundle.EvidenceHeader, func(p *LinearProblem) { p.Rows[0].RHS = 1 })
	if err := VerifyInfeasibilityEvidence(bundle); err == nil {
		t.Fatal("type B verified on a non-empty box")
	}
}

// TestObjectiveBoundEvidenceReverifies is A23: σ lower and upper bounds of
// a Class verify from the snapshot; a changed value, direction or bound
// problem fails.
func TestObjectiveBoundEvidenceReverifies(t *testing.T) {
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceFixtureClass(0, .5), evidenceSkewedClass(1, .5))
	run := NewIntentEngine(NewGonumSolver()).newRun(compiled, nil)
	subs, _, err := splitByClass(compiled.Hard, compiled.Prepared.Classes)
	if err != nil {
		t.Fatal(err)
	}
	where := solveContext{stage: StageMinimizeMainProfileDeviation, class: 2, classCount: 2}
	witness, err := run.classify(context.Background(), subs[1].problem, where, ObjectiveMainProfileProbe)
	if err != nil || witness.class != evidenceFeasible {
		t.Fatalf("witness: %+v %v", witness, err)
	}
	interval, err := run.classInterval(context.Background(), subs[1], run.sigmaOf(compiled.Hard), witness.witness, where)
	if err != nil || interval.loOut == nil || interval.hiOut == nil {
		t.Fatalf("interval: %+v %v", interval, err)
	}
	for _, bound := range []objectiveBoundEvidence{*interval.loOut, *interval.hiOut} {
		bundle, err := run.objectiveBoundBundle(compiled.Hard, 2, bound, where.stage, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyObjectiveBound(bundle); err != nil {
			t.Fatalf("%s: %v", bound.direction, err)
		}
		for name, tamper := range map[string]func(*ObjectiveBoundBundle){
			"value": func(b *ObjectiveBoundBundle) {
				value, _ := new(big.Rat).SetString(b.Value)
				b.Value = ratAdd(value, big.NewRat(1, 1<<50)).String()
			},
			"direction": func(b *ObjectiveBoundBundle) {
				b.Direction = map[string]string{"lower": "upper", "upper": "lower"}[b.Direction]
			},
			"class": func(b *ObjectiveBoundBundle) { b.Class = 1 },
			"problem": func(b *ObjectiveBoundBundle) {
				rebind(t, &b.EvidenceHeader, func(p *LinearProblem) { scaleClassRows(p, "class-001") })
			},
		} {
			copied := bundle
			tamper(&copied)
			if err := VerifyObjectiveBound(copied); err == nil {
				t.Fatalf("%s bound verified after changing the %s", bound.direction, name)
			}
		}
	}
}

// scaleClassRows changes one Class's mean target, a different bound problem.
func scaleClassRows(p *LinearProblem, classID string) {
	for i := range p.Rows {
		if p.Rows[i].ClassID == classID && strings.Contains(string(p.Rows[i].ID), "mean") {
			p.Rows[i].RHS *= 0.9
		}
	}
}

// TestPrecisionUnprovenEvidenceReverifies is A21 for a bracket: the saved
// file re-verifies the feasible end, the bound and their distance; values
// survive the round trip bit for bit; a moved witness fails.
func TestPrecisionUnprovenEvidenceReverifies(t *testing.T) {
	compiled := skewedModel(t)
	compiled.Prepared.Plan.EngineOptions.ProfileBisectionIterations = 5
	solution := solveWithEvidence(t, compiled, nil)
	if solution.Status != StatusNumericalFailure || solution.Diagnostics[0].Code != DiagnosticPrecisionUnproven {
		t.Fatalf("status=%s diagnostics=%+v", solution.Status, solution.Diagnostics)
	}
	var file NumericalEvidence
	readEvidence(t, solution.Diagnostics[0].EvidencePath, &file)
	if err := VerifyNumericalEvidence(file); err != nil {
		t.Fatal(err)
	}
	if file.BoundBasis != BasisVerifiedCertificate || file.BoundEvidence == nil || len(file.Probes) != 2+5 || file.Budget.Iterations != 5 {
		t.Fatalf("file: basis=%s probes=%d budget=%+v", file.BoundBasis, len(file.Probes), file.Budget)
	}
	raw, err := os.ReadFile(solution.Diagnostics[0].EvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	again, err := json.MarshalIndent(file, "", "  ")
	if err != nil || string(again) != string(raw) {
		t.Fatal("evidence file does not round-trip byte for byte")
	}
	witness, err := decodeValues(file.Witness)
	if err != nil || encodeValues(witness)[0] != file.Witness[0] {
		t.Fatal("witness values do not round-trip")
	}
	moved := file
	moved.Witness = append([]string(nil), file.Witness...)
	moved.Witness[0] = encodeFloat64(witness[0] + 0.25)
	if err := VerifyNumericalEvidence(moved); err == nil {
		t.Fatal("moved witness verified")
	}
	loose := file
	loose.RequiredPrecision = "1/1"
	if err := VerifyNumericalEvidence(loose); err == nil {
		t.Fatal("a bracket within a loose precision verified as unproven")
	}
}

// TestMinimumCVEvidenceReverifies is A21 for Step 5: a candidate that is
// truly farther from the safe lower bound than required verifies; the same
// file with the optimal candidate does not.
func TestMinimumCVEvidenceReverifies(t *testing.T) {
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceFixtureClass(0, .5), evidenceFixtureClass(1, .5))
	run := NewIntentEngine(NewGonumSolver()).newRun(compiled, nil)
	run.evidenceDir = t.TempDir()
	terms, fixed, err := buildSecondMomentExpression(compiled)
	if err != nil {
		t.Fatal(err)
	}
	optimal, err := run.decideJoint(context.Background(), compiled.Hard, StageSelectCanonicalBucketProbabilities, ObjectiveCanonicalBucketProbability, nil, true, nil)
	if err != nil || !optimal.gapMet() || len(optimal.lowerBounds) != 2 {
		t.Fatalf("optimal decision: %+v %v", optimal, err)
	}
	// A worse feasible candidate: every Class at its σ maximum.
	subs, _, err := splitByClass(compiled.Hard, compiled.Prepared.Classes)
	if err != nil {
		t.Fatal(err)
	}
	sigma := run.sigmaOf(compiled.Hard)
	his := make([][]float64, len(subs))
	for i, sub := range subs {
		where := solveContext{stage: StageSelectCanonicalBucketProbabilities, class: sub.position, classCount: len(subs)}
		w, err := run.classify(context.Background(), sub.problem, where, ObjectiveCanonicalBucketProbability)
		if err != nil {
			t.Fatal(err)
		}
		interval, err := run.classInterval(context.Background(), sub, sigma, w.witness, where)
		if err != nil {
			t.Fatal(err)
		}
		his[i] = interval.hiX
	}
	high := assemble(compiled.Hard, subs, his, his, 0)
	if !run.replayAccepts(compiled.Hard, high) {
		t.Fatal("fixture: σ-maximal point fails the internal set")
	}
	worse := optimal
	worse.witness = high
	worse.candidateS = ratAdd(ratOf(fixed), sigmaValue(compiled.Hard, sigma, high))
	worse.requiredS = requiredMinimumCVGap(worse.candidateS)
	if ratSub(worse.candidateS, optimal.safeLowerS).Cmp(worse.requiredS) <= 0 {
		t.Fatal("fixture: the σ-maximal point is within the gap requirement")
	}
	solution := run.minimumCVPrecisionUnproven(worse, compiled.Hard, terms, fixed)
	var file NumericalEvidence
	readEvidence(t, solution.Diagnostics[0].EvidencePath, &file)
	if err := VerifyNumericalEvidence(file); err != nil {
		t.Fatal(err)
	}
	best := file
	cv := *file.MinimumCV
	cv.Witness = encodeValues(optimal.witness)
	cv.CandidateSecond, cv.RequiredGap = optimal.candidateS.RatString(), optimal.requiredS.RatString()
	best.MinimumCV = &cv
	if err := VerifyNumericalEvidence(best); err == nil {
		t.Fatal("an optimal candidate verified as PrecisionUnproven")
	}
}

// TestEvidenceUnavailableFileReproduces is A21: the file loads, and the
// saved problem is again undecided under the same budget.
func TestEvidenceUnavailableFileReproduces(t *testing.T) {
	compiled := skewedModel(t)
	compiled.Hard = cloneLinearProblem(compiled.Hard)
	compiled.Hard.Rows = append(compiled.Hard.Rows, LinearRow{ID: "test:impossible", ClassID: compiled.Prepared.Classes[0].ID,
		Sense: SenseGE, RHS: 2, Terms: []LinearTerm{{Variable: compiled.Primary[0].ID, Coeff: 1}}})
	budget := &exactBudget{maxPivots: 0, maxBits: 1 << 14}
	solution := solveWithEvidence(t, compiled, budget)
	if solution.Diagnostics[0].Code != DiagnosticEvidenceUnavailable {
		t.Fatalf("diagnostics=%+v", solution.Diagnostics)
	}
	var file NumericalEvidence
	readEvidence(t, solution.Diagnostics[0].EvidencePath, &file)
	if err := VerifyNumericalEvidence(file); err != nil || file.Budget.ExactPivots != 0 || len(file.Reasons) == 0 {
		t.Fatalf("file=%+v err=%v", file, err)
	}
	problem, err := openSnapshot(file.Problem, file.ProblemSHA256)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewIntentEngine(NewGonumSolver())
	engine.exactBudget = budget
	run := engine.newRun(compiled, nil)
	decision, err := run.decideJoint(context.Background(), problem, StageProveHardFeasibility, ObjectiveHardFeasibility, nil, false, nil)
	if err != nil || decision.class != evidenceUnknown {
		t.Fatalf("reproduced class=%s err=%v", decision.class, err)
	}
}

// TestReportCarriesEvidenceFields is A20 (fields): every §9.2 field of a
// completed run has a value.
func TestReportCarriesEvidenceFields(t *testing.T) {
	compiled := skewedModel(t)
	solution := solveWithEvidence(t, compiled, nil)
	report := BuildIntentQualityReport(compiled, solution)
	delta := report.MainProfileOptimization
	if delta.LowerBasis == "" || delta.UpperBasis == "" || delta.StopReason == "" || delta.RequiredPrecision != 1e-8 ||
		delta.Evidence == nil || delta.Evidence.ExactCalls == 0 || delta.Evidence.SigmaEndpoints == 0 || delta.VerifiedWithLock <= delta.VerifiedBound {
		t.Fatalf("bisection report=%+v", delta)
	}
	canonical := report.CanonicalBucketProbabilitySelection
	if canonical.Method != "per-class-decomposition" || canonical.GapUnit != "second_moment" || canonical.SafeLowerBound == nil ||
		canonical.OptimalityGap == nil || canonical.RequiredGap == nil || canonical.Evidence == nil {
		t.Fatalf("canonical report=%+v", canonical)
	}
	rules := report.AcceptanceRules
	if rules == nil || rules.Internal.Version != internalRuleVersion || rules.Internal.BoundTolerance != "0" ||
		rules.External.RowTolerance != encodeFloat64(1e-9) {
		t.Fatalf("acceptance rules=%+v", rules)
	}
}

// TestBracketEvidenceBindsProbeValues: relabelling either bracket end,
// moving the certificate to another stage, or pairing ends that do not share
// their common constraints fails (review finding: a label of 0.4374 kept the
// certificate of 0.4375 although 0.4374 is feasible).
func TestBracketEvidenceBindsProbeValues(t *testing.T) {
	compiled := skewedModel(t)
	compiled.Prepared.Plan.EngineOptions.ProfileBisectionIterations = 5
	solution := solveWithEvidence(t, compiled, nil)
	var file NumericalEvidence
	readEvidence(t, solution.Diagnostics[0].EvidencePath, &file)
	if err := VerifyNumericalEvidence(file); err != nil {
		t.Fatal(err)
	}
	feasible, _ := decodeFloat64(file.FeasibleEnd)
	relabelled := encodeFloat64(feasible - 0.0001)
	cases := map[string]func(*NumericalEvidence){
		"bound label": func(f *NumericalEvidence) {
			bundle := *f.BoundEvidence
			f.BoundEnd, bundle.Probe = relabelled, relabelled
			f.BoundEvidence = &bundle
		},
		"feasible label": func(f *NumericalEvidence) { f.FeasibleEnd = encodeFloat64(feasible + 0.01) },
		"bound stage": func(f *NumericalEvidence) {
			bundle := *f.BoundEvidence
			bundle.Stage = string(StageProveHardFeasibility)
			f.BoundEvidence = &bundle
		},
		"common constraints": func(f *NumericalEvidence) {
			bundle := *f.BoundEvidence
			rebind(t, &bundle.EvidenceHeader, func(p *LinearProblem) {
				p.Rows = append(p.Rows, LinearRow{ID: "review:extra", ClassID: compiled.Prepared.Classes[0].ID, Sense: SenseGE, RHS: 2,
					Terms: []LinearTerm{{Variable: compiled.Primary[0].ID, Coeff: 1}}})
			})
			f.BoundEvidence = &bundle
		},
	}
	for name, tamper := range cases {
		copied := file
		tamper(&copied)
		if err := VerifyNumericalEvidence(copied); err == nil {
			t.Fatalf("%s: tampered bracket verified", name)
		}
	}
	// The relabelled value really is feasible: the certificate cannot move.
	probe := feasible - 0.0001
	problem, err := BuildMainProfileDeviationProblem(compiled, probe)
	if err != nil {
		t.Fatal(err)
	}
	run := NewIntentEngine(NewGonumSolver()).newRun(compiled, nil)
	if decision, err := run.decideJoint(context.Background(), problem, StageMinimizeMainProfileDeviation, ObjectiveMainProfileProbe, &probe, false, nil); err != nil || decision.class != evidenceFeasible {
		t.Fatalf("fixture: %g should be feasible, got %s %v", probe, decision.class, err)
	}
}

// TestProbeBindingMatchesBuilders: each stage's probe rows bind exactly the
// value the builder was called with, including ρ = 0 where zero coefficients
// are dropped, and reject the neighbouring float64.
func TestProbeBindingMatchesBuilders(t *testing.T) {
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceFixtureClass(0, .5), evidenceFixtureClass(1, .5))
	builders := map[OptimizationStageID]func(float64) (LinearProblem, error){
		StageMinimizeMainProfileDeviation: func(v float64) (LinearProblem, error) { return BuildMainProfileDeviationProblem(compiled, v) },
		StageMaximizeOtherBucketVisibility: func(v float64) (LinearProblem, error) {
			return AddOtherBucketVisibilityRows(compiled.Hard, compiled, v)
		},
		StageMaximizeMainGroupInternalVisibility: func(v float64) (LinearProblem, error) {
			return AddMainGroupInternalVisibilityRows(compiled.Hard, compiled, v)
		},
	}
	for stage, build := range builders {
		for _, value := range []float64{0, 1.0 / 3, 0.4375, 0.75, 1, 1e-300} {
			problem, err := build(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyProbeBinding(string(stage), problem, encodeFloat64(value)); err != nil {
				t.Fatalf("%s at %v: %v", stage, value, err)
			}
			// The neighbouring float64 must be rejected unless the builder
			// produces the very same matrix for it (ρ/k can underflow to 0).
			next := math.Nextafter(value, 2)
			other, err := build(next)
			if err != nil { // above the parameter range: use the value below
				next = math.Nextafter(value, -1)
				if other, err = build(next); err != nil {
					t.Fatal(err)
				}
			}
			a, _ := SnapshotProblem(problem).SHA256()
			b, _ := SnapshotProblem(other).SHA256()
			if err := verifyProbeBinding(string(stage), problem, encodeFloat64(next)); err == nil && a != b {
				t.Fatalf("%s: problem at %v verified as %v", stage, value, next)
			}
		}
	}
}

// TestVerifiersRejectNonFiniteNumbers: every public verifier returns an
// error, never panics, on non-finite numbers.
func TestVerifiersRejectNonFiniteNumbers(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("verifier panicked: %v", r)
		}
	}()
	run := NewIntentEngine(NewGonumSolver()).newRun(skewedModel(t), nil)
	header, err := run.evidenceHeader(oneVarProblem(0, 1), productRule(1e-9), StageProveHardFeasibility, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, coeff := range []string{"+Inf", "-Inf", "NaN"} {
		bundle := ObjectiveBoundBundle{EvidenceHeader: header, Terms: []TermSnapshot{{Variable: "x", Coeff: coeff}}, Direction: "upper", Value: "0"}
		if err := VerifyObjectiveBound(bundle); err == nil {
			t.Fatalf("objective coefficient %s accepted", coeff)
		}
	}
	bad := header
	bad.Tolerance = "+Inf"
	if err := VerifyObjectiveBound(ObjectiveBoundBundle{EvidenceHeader: bad, Direction: "upper", Value: "0"}); err == nil {
		t.Fatal("non-finite tolerance accepted")
	}

	compiled := skewedModel(t)
	compiled.Prepared.Plan.EngineOptions.ProfileBisectionIterations = 5
	solution := solveWithEvidence(t, compiled, nil)
	var file NumericalEvidence
	readEvidence(t, solution.Diagnostics[0].EvidencePath, &file)
	for _, value := range []string{"+Inf", "NaN"} {
		for name, tamper := range map[string]func(*NumericalEvidence){
			"bound end":    func(f *NumericalEvidence) { f.BoundEnd = value },
			"feasible end": func(f *NumericalEvidence) { f.FeasibleEnd = value },
			"tolerance":    func(f *NumericalEvidence) { f.Tolerance = value },
		} {
			copied := file
			tamper(&copied)
			if err := VerifyNumericalEvidence(copied); err == nil {
				t.Fatalf("%s %s accepted", name, value)
			}
		}
	}
	cvFile := NumericalEvidence{Version: numericalEvidenceVersion, Code: DiagnosticPrecisionUnproven, Tolerance: file.Tolerance, Rule: file.Rule,
		Problem: file.FeasibleProblem, ProblemSHA256: file.FeasibleSHA256,
		MinimumCV: &MinimumCVEvidence{FixedSecond: "+Inf", Witness: file.Witness, SigmaTerms: []TermSnapshot{{Variable: compiled.Primary[0].ID, Coeff: "NaN"}}}}
	if err := VerifyNumericalEvidence(cvFile); err == nil {
		t.Fatal("non-finite minimum-CV numbers accepted")
	}
}

// TestEvidenceIsNotOverwrittenAcrossRuns: two runs sharing an output
// directory keep separate evidence (review finding).
func TestEvidenceIsNotOverwrittenAcrossRuns(t *testing.T) {
	compiled := skewedModel(t)
	compiled.Hard = cloneLinearProblem(compiled.Hard)
	compiled.Prepared.Plan.Plan.Output.Directory = t.TempDir()
	compiled.Hard.Rows = append(compiled.Hard.Rows, LinearRow{ID: "review:impossible", ClassID: compiled.Prepared.Classes[0].ID,
		Sense: SenseGE, RHS: 2, Terms: []LinearTerm{{Variable: compiled.Primary[0].ID, Coeff: 1}}})
	engine := NewIntentEngine(NewGonumSolver())
	first, err := engine.Solve(context.Background(), compiled)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(first.Diagnostics[0].EvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	compiled.Hard.Rows[len(compiled.Hard.Rows)-1].RHS = 3
	second, err := engine.Solve(context.Background(), compiled)
	if err != nil {
		t.Fatal(err)
	}
	if second.Diagnostics[0].EvidencePath == first.Diagnostics[0].EvidencePath {
		t.Fatal("two runs share one evidence path")
	}
	after, err := os.ReadFile(first.Diagnostics[0].EvidencePath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("first run's evidence changed: %v", err)
	}
}
