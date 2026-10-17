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
	where := solveContext{stage: StageProveHardFeasibility, class: 2, classCount: 2}
	witness, err := run.classify(context.Background(), subs[1].problem, where, ObjectiveMainProfileProbe)
	if err != nil || witness.class != evidenceFeasible {
		t.Fatalf("witness: %+v %v", witness, err)
	}
	interval, err := run.classInterval(context.Background(), subs[1], run.sigmaOf(compiled.Hard), witness.witness, where)
	if err != nil || interval.loOut == nil || interval.hiOut == nil {
		t.Fatalf("interval: %+v %v", interval, err)
	}
	for _, bound := range []objectiveBoundEvidence{*interval.loOut, *interval.hiOut} {
		// The bound problem is the hard model itself (no probe).
		bundle, err := run.objectiveBoundBundle(compiled.Hard, 2, bound, StageProveHardFeasibility, nil)
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
	file, optimal := validMinimumCVEvidence(t)
	best := file
	cv := *file.MinimumCV
	cv.Witness = encodeValues(optimal.witness)
	cv.CandidateSecond, cv.RequiredGap = optimal.candidateS.RatString(), optimal.requiredS.RatString()
	best.MinimumCV = &cv
	if err := VerifyNumericalEvidence(best); err == nil {
		t.Fatal("an optimal candidate verified as PrecisionUnproven")
	}
}

// validMinimumCVEvidence is a verified Step 5 PrecisionUnproven file and the
// optimal decision of the same lock problem.
func validMinimumCVEvidence(t *testing.T) (NumericalEvidence, jointDecision) {
	t.Helper()
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceFixtureClass(0, .5), evidenceFixtureClass(1, .5))
	run := NewIntentEngine(NewGonumSolver()).newRun(compiled, nil)
	run.evidenceDir = t.TempDir()
	terms, fixed, err := buildSecondMomentExpression(compiled)
	if err != nil {
		t.Fatal(err)
	}
	// A real lock chain with loose locks (δ = 2, ρ = 0), so σ keeps a range.
	run.locks = ProbeLocks{Delta: encodeFloat64(2), OtherRho: encodeFloat64(0), MainRho: encodeFloat64(0)}
	locked, err := BuildMainProfileDeviationProblem(compiled, 2)
	if err == nil {
		locked, err = AddOtherBucketVisibilityRows(locked, compiled, 0)
	}
	if err == nil {
		locked, err = AddMainGroupInternalVisibilityRows(locked, compiled, 0)
	}
	if err != nil {
		t.Fatal(err)
	}
	optimal, err := run.decideJoint(context.Background(), locked, StageSelectCanonicalBucketProbabilities, ObjectiveCanonicalBucketProbability, nil, true, nil)
	if err != nil || !optimal.gapMet() || len(optimal.lowerBounds) != 2 {
		t.Fatalf("optimal decision: %+v %v", optimal, err)
	}
	// A worse feasible candidate: every Class at its σ maximum.
	subs, _, err := splitByClass(locked, compiled.Prepared.Classes)
	if err != nil {
		t.Fatal(err)
	}
	sigma := run.sigmaOf(locked)
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
	high := assemble(locked, subs, his, his, 0)
	if !run.replayAccepts(locked, high) {
		t.Fatal("fixture: σ-maximal point fails the internal set")
	}
	worse := optimal
	worse.witness = high
	worse.candidateS = ratAdd(ratOf(fixed), sigmaValue(locked, sigma, high))
	worse.requiredS = requiredMinimumCVGap(worse.candidateS)
	if ratSub(worse.candidateS, optimal.safeLowerS).Cmp(worse.requiredS) <= 0 {
		t.Fatal("fixture: the σ-maximal point is within the gap requirement")
	}
	solution := run.minimumCVPrecisionUnproven(worse, locked, terms, fixed)
	var file NumericalEvidence
	readEvidence(t, solution.Diagnostics[0].EvidencePath, &file)
	if err := VerifyNumericalEvidence(file); err != nil {
		t.Fatal(err)
	}
	return file, optimal
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
	if base := report.EvidenceBase; base == nil || base.HardSHA256 == "" || base.StructureSHA256 == "" {
		t.Fatalf("evidence base=%+v", base)
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
		// Review finding: keep one Main term at −δ', drop the others.
		"Main membership": func(f *NumericalEvidence) {
			bundle := *f.BoundEvidence
			rebind(t, &bundle.EvidenceHeader, func(p *LinearProblem) {
				for i := range p.Rows {
					if p.Rows[i].Family != "main_profile_deviation_lock" {
						continue
					}
					var terms []LinearTerm
					kept := false
					for _, term := range p.Rows[i].Terms {
						switch {
						case strings.HasPrefix(string(term.Variable), "d:main:"):
							terms = append(terms, term)
						case !kept:
							terms, kept = append(terms, LinearTerm{Variable: term.Variable, Coeff: -(feasible - 0.0001)}), true
						}
					}
					p.Rows[i].Terms = sortedTerms(func() map[VariableID]float64 {
						m := map[VariableID]float64{}
						for _, term := range terms {
							m[term.Variable] = term.Coeff
						}
						return m
					}())
				}
			})
			bundle.Probe = relabelled
			f.BoundEnd, f.BoundEvidence = relabelled, &bundle
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

// TestProbeBindingMatchesBuilders: each stage problem binds exactly the value
// the builder chain was called with (including ρ = 0, where zero
// coefficients are dropped) and rejects the neighbouring float64 unless the
// builders produce the very same matrix for it (ρ/k can underflow to 0).
func TestProbeBindingMatchesBuilders(t *testing.T) {
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceFixtureClass(0, .5), evidenceFixtureClass(1, .5))
	const deltaFix, otherFix = 0.25, 0.5
	run := NewIntentEngine(NewGonumSolver()).newRun(compiled, nil)
	profile := func() LinearProblem {
		p, err := BuildMainProfileDeviationProblem(compiled, deltaFix)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	other := func() LinearProblem {
		p, err := AddOtherBucketVisibilityRows(profile(), compiled, otherFix)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	stages := []struct {
		stage OptimizationStageID
		locks ProbeLocks
		build func(float64) (LinearProblem, error)
	}{
		{StageMinimizeMainProfileDeviation, ProbeLocks{}, func(v float64) (LinearProblem, error) { return BuildMainProfileDeviationProblem(compiled, v) }},
		{StageMaximizeOtherBucketVisibility, ProbeLocks{Delta: encodeFloat64(deltaFix)}, func(v float64) (LinearProblem, error) {
			return AddOtherBucketVisibilityRows(profile(), compiled, v)
		}},
		{StageMaximizeMainGroupInternalVisibility, ProbeLocks{Delta: encodeFloat64(deltaFix), OtherRho: encodeFloat64(otherFix)}, func(v float64) (LinearProblem, error) {
			return AddMainGroupInternalVisibilityRows(other(), compiled, v)
		}},
	}
	for _, test := range stages {
		run.locks = test.locks
		base, err := run.probeBase()
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []float64{0, 1.0 / 3, 0.4375, 0.75, 1, 1e-300} {
			problem, err := test.build(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyStageProblem(base, string(test.stage), encodeFloat64(value), problem); err != nil {
				t.Fatalf("%s at %v: %v", test.stage, value, err)
			}
			next := math.Nextafter(value, 2)
			neighbour, err := test.build(next)
			if err != nil { // above the parameter range: use the value below
				next = math.Nextafter(value, -1)
				if neighbour, err = test.build(next); err != nil {
					t.Fatal(err)
				}
			}
			a, _ := SnapshotProblem(problem).SHA256()
			b, _ := SnapshotProblem(neighbour).SHA256()
			if err := verifyStageProblem(base, string(test.stage), encodeFloat64(next), problem); err == nil && a != b {
				t.Fatalf("%s: problem at %v verified as %v", test.stage, value, next)
			}
		}
		// Wrong earlier locks or another hard model never verify.
		wrong := *base
		wrong.Locks.Delta = encodeFloat64(deltaFix + 0.125)
		if problem, _ := test.build(0.5); test.stage != StageMinimizeMainProfileDeviation && verifyStageProblem(&wrong, string(test.stage), encodeFloat64(0.5), problem) == nil {
			t.Fatalf("%s verified with another δ lock", test.stage)
		}
		moved := *base
		moved.HardSHA256 = strings.Repeat("0", 64)
		if problem, _ := test.build(0.5); verifyStageProblem(&moved, string(test.stage), encodeFloat64(0.5), problem) == nil {
			t.Fatalf("%s verified on another hard model", test.stage)
		}
	}
	// The minimum-CV lock problem is the chain at every lock.
	run.locks = ProbeLocks{Delta: encodeFloat64(deltaFix), OtherRho: encodeFloat64(otherFix), MainRho: encodeFloat64(0.125)}
	base, _ := run.probeBase()
	locked, err := AddMainGroupInternalVisibilityRows(other(), compiled, 0.125)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyStageProblem(base, string(StageSelectCanonicalBucketProbabilities), "", locked); err != nil {
		t.Fatal(err)
	}
	if verifyStageProblem(base, string(StageSelectCanonicalBucketProbabilities), "", other()) == nil {
		t.Fatal("a problem without the Main Group lock verified as the minimum-CV lock problem")
	}
}

// expectRejected requires err to be a rejection for the intended reason.
// NaN is refused already by the number decoder.
func expectRejected(t *testing.T, name string, err error, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: accepted", name)
	}
	if !strings.Contains(err.Error(), reason) && !(strings.HasSuffix(name, "NaN") && strings.Contains(err.Error(), "NaN is not a valid encoded")) {
		t.Fatalf("%s: rejected for another reason: %v (want %q)", name, err, reason)
	}
}

// TestVerifiersRejectNonFiniteNumbers: every public verifier returns an
// error, never panics, on non-finite numbers. Each case changes one field of
// otherwise valid evidence and checks the rejection reason.
func TestVerifiersRejectNonFiniteNumbers(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("verifier panicked: %v", r)
		}
	}()
	compiled := evidenceCompile(t, NumericRange{Min: 0, Max: 10}, evidenceFixtureClass(0, .5), evidenceSkewedClass(1, .5))
	run := NewIntentEngine(NewGonumSolver()).newRun(compiled, nil)
	subs, _, err := splitByClass(compiled.Hard, compiled.Prepared.Classes)
	if err != nil {
		t.Fatal(err)
	}
	where := solveContext{stage: StageProveHardFeasibility, class: 1, classCount: 2}
	witness, err := run.classify(context.Background(), subs[0].problem, where, ObjectiveHardFeasibility)
	if err != nil {
		t.Fatal(err)
	}
	interval, err := run.classInterval(context.Background(), subs[0], run.sigmaOf(compiled.Hard), witness.witness, where)
	if err != nil || interval.hiOut == nil {
		t.Fatalf("interval: %+v %v", interval, err)
	}
	bound, err := run.objectiveBoundBundle(compiled.Hard, 1, *interval.hiOut, StageProveHardFeasibility, nil)
	if err != nil || VerifyObjectiveBound(bound) != nil {
		t.Fatalf("valid type D bound: %v", err)
	}
	for _, coeff := range []string{"+Inf", "-Inf", "NaN"} {
		copied := bound
		copied.Terms = append([]TermSnapshot(nil), bound.Terms...)
		copied.Terms[0].Coeff = coeff
		expectRejected(t, "objective coefficient "+coeff, VerifyObjectiveBound(copied), "not finite")
	}
	copied := bound
	copied.Tolerance = "+Inf"
	expectRejected(t, "type D tolerance", VerifyObjectiveBound(copied), "not finite")

	bracket := skewedModel(t)
	bracket.Prepared.Plan.EngineOptions.ProfileBisectionIterations = 5
	solution := solveWithEvidence(t, bracket, nil)
	var file NumericalEvidence
	readEvidence(t, solution.Diagnostics[0].EvidencePath, &file)
	for _, value := range []string{"+Inf", "NaN"} {
		for name, test := range map[string]struct {
			tamper func(*NumericalEvidence)
			reason string
		}{
			"bound end":    {func(f *NumericalEvidence) { f.BoundEnd = value }, "bound end is not finite"},
			"feasible end": {func(f *NumericalEvidence) { f.FeasibleEnd = value }, "is not finite"},
			"tolerance":    {func(f *NumericalEvidence) { f.Tolerance = value }, "tolerance is not finite"},
		} {
			copied := file
			test.tamper(&copied)
			expectRejected(t, name+" "+value, VerifyNumericalEvidence(copied), test.reason)
		}
	}

	cvFile, _ := validMinimumCVEvidence(t)
	for name, test := range map[string]struct {
		tamper func(*MinimumCVEvidence)
		reason string
	}{
		"fixed second": {func(cv *MinimumCVEvidence) { cv.FixedSecond = "+Inf" }, "fixed second moment is not finite"},
		"σ coefficient NaN": {func(cv *MinimumCVEvidence) {
			cv.SigmaTerms = append([]TermSnapshot(nil), cv.SigmaTerms...)
			cv.SigmaTerms[0].Coeff = "NaN"
		}, "σ terms"},
		// Review finding: two finite 1e308 terms on one variable used to sum
		// to +Inf; duplicates are rejected before any arithmetic.
		"σ duplicate": {func(cv *MinimumCVEvidence) {
			first := cv.SigmaTerms[0]
			first.Coeff = "1e308"
			cv.SigmaTerms = append([]TermSnapshot{first, first}, cv.SigmaTerms[1:]...)
		}, "σ terms"},
	} {
		copied := cvFile
		cv := *cvFile.MinimumCV
		test.tamper(&cv)
		copied.MinimumCV = &cv
		expectRejected(t, name, VerifyNumericalEvidence(copied), test.reason)
	}
}

// TestBracketEvidenceBindsStageSemantics: the stage fixes direction,
// theoretical endpoint, required precision and acceptance rule; a file that
// states otherwise fails (review finding: δ relabelled as maximize toward 1
// accepted [0.4375, 1] although δ = 0.41 is feasible).
func TestBracketEvidenceBindsStageSemantics(t *testing.T) {
	compiled := skewedModel(t)
	compiled.Prepared.Plan.EngineOptions.ProfileBisectionIterations = 5
	solution := solveWithEvidence(t, compiled, nil)
	var file NumericalEvidence
	readEvidence(t, solution.Diagnostics[0].EvidencePath, &file)
	for name, test := range map[string]struct {
		tamper func(*NumericalEvidence)
		reason string
	}{
		"direction": {func(f *NumericalEvidence) {
			f.Direction, f.BoundBasis, f.BoundEnd, f.BoundEvidence = "maximize", BasisTheoreticalEndpoint, "1", nil
		}, "is a minimize stage"},
		"theoretical endpoint": {func(f *NumericalEvidence) {
			f.BoundBasis, f.BoundEnd, f.BoundEvidence = BasisTheoreticalEndpoint, "0.25", nil
		}, "theoretical endpoint"},
		"required precision": {func(f *NumericalEvidence) { f.RequiredPrecision = "0" }, "not the engine's"},
		"external rule":      {func(f *NumericalEvidence) { f.Rule = encodeEvidenceRule(productRule(1e-9)) }, "internal acceptance rule"},
	} {
		copied := file
		test.tamper(&copied)
		expectRejected(t, name, VerifyNumericalEvidence(copied), test.reason)
	}
	// A probe certificate stated for the external set, or a hard certificate
	// for the internal set, is bound to the wrong stage set.
	external := *file.BoundEvidence
	external.Rule = encodeEvidenceRule(productRule(1e-9))
	expectRejected(t, "probe certificate on the external set", VerifyInfeasibilityEvidence(external), "must use rule")
}

// TestFailedRunReportAnchorsEvidence: a run that fails in the engine still
// records evidence_base and the acceptance rules (review finding).
func TestFailedRunReportAnchorsEvidence(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "failed-anchor")
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputOptimalArtifactV1})
	tuner.engine.solver = falseInfeasibleSolver{}
	tuner.engine.exactBudget = &exactBudget{maxPivots: 0, maxBits: 1 << 14}
	result, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusNumericalFailure || len(result.Diagnostics) == 0 || result.Diagnostics[0].EvidencePath == "" {
		t.Fatalf("status=%s diagnostics=%+v", result.Status, result.Diagnostics)
	}
	anchor := result.Report.Intent.EvidenceBase
	if anchor == nil || anchor.HardSHA256 != base.Report.Intent.EvidenceBase.HardSHA256 || result.Report.Intent.AcceptanceRules == nil {
		t.Fatalf("failed run report anchor=%+v rules=%+v", anchor, result.Report.Intent.AcceptanceRules)
	}
	var file NumericalEvidence
	readEvidence(t, result.Diagnostics[0].EvidencePath, &file)
	if file.Base == nil || file.Base.HardSHA256 != anchor.HardSHA256 || file.Base.StructureSHA256 != anchor.StructureSHA256 {
		t.Fatalf("evidence base %+v does not match the report anchor %+v", file.Base, anchor)
	}
}

// TestLaterStageEvidenceReverifies: a ρ_main PrecisionUnproven file from the
// engine carries the δ and ρ_other locks and verifies by rebuilding the
// whole chain; another δ lock does not.
func TestLaterStageEvidenceReverifies(t *testing.T) {
	compiled := skewedModel(t)
	compiled.Prepared.Plan.EngineOptions.MainGroupInternalVisibilityBisectionIterations = 5
	solution := solveWithEvidence(t, compiled, nil)
	if solution.Status != StatusNumericalFailure || !strings.Contains(solution.Diagnostics[0].Message, string(StageMaximizeMainGroupInternalVisibility)) {
		t.Fatalf("status=%s diagnostics=%+v", solution.Status, solution.Diagnostics)
	}
	var file NumericalEvidence
	readEvidence(t, solution.Diagnostics[0].EvidencePath, &file)
	if file.Base == nil || file.Base.Locks.Delta == "" || file.Base.Locks.OtherRho == "" || file.Base.Locks.MainRho != "" {
		t.Fatalf("locks=%+v", file.Base)
	}
	if err := VerifyNumericalEvidence(file); err != nil {
		t.Fatal(err)
	}
	wrong := file
	base := *file.Base
	base.Locks.Delta = encodeFloat64(0.5)
	wrong.Base = &base
	if err := VerifyNumericalEvidence(wrong); err == nil {
		t.Fatal("another δ lock verified")
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
