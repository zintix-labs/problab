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
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// NumericalEvidence is the evidence file of a PrecisionUnproven or
// EvidenceUnavailable result (numerical-evidence-<stage>.json, techspec
// §9.3). For PrecisionUnproven an independent program can re-verify that the
// feasible end is feasible, that the reliable bound holds, and that the two
// are farther apart than required (VerifyNumericalEvidence).
type NumericalEvidence struct {
	Version             string         `json:"version"`
	Code                DiagnosticCode `json:"code"`
	EngineVersion       string         `json:"engine_version"`
	VerifierVersion     string         `json:"verifier_version"`
	BackendVersion      string         `json:"backend_version"`
	Stage               string         `json:"stage"`
	Metric              string         `json:"metric,omitempty"`
	Direction           string         `json:"direction,omitempty"`
	Tolerance           string         `json:"tolerance"`
	OptimalityTolerance string         `json:"optimality_tolerance"`
	Rule                EvidenceRule   `json:"rule"`
	RequiredPrecision   string         `json:"required_precision,omitempty"` // exact rational
	Budget              EvidenceBudget `json:"budget"`
	ClassIDs            []string       `json:"class_ids"`

	// Bisection (PrecisionUnproven of δ, ρ_other, ρ_main).
	Probes          []EvidenceProbe      `json:"probes,omitempty"`
	FeasibleEnd     string               `json:"feasible_end,omitempty"`
	FeasibleProblem *ProblemSnapshot     `json:"feasible_problem,omitempty"`
	FeasibleSHA256  string               `json:"feasible_problem_sha256,omitempty"`
	Witness         []string             `json:"witness,omitempty"`
	BoundEnd        string               `json:"bound_end,omitempty"`
	BoundBasis      string               `json:"bound_basis,omitempty"`
	BoundEvidence   *InfeasibilityBundle `json:"bound_evidence,omitempty"`

	// Minimum CV (PrecisionUnproven of Step 5).
	MinimumCV *MinimumCVEvidence `json:"minimum_cv,omitempty"`

	// The undecided (EvidenceUnavailable) or lock (minimum CV) problem.
	Probe         string            `json:"probe,omitempty"`
	Problem       *ProblemSnapshot  `json:"problem,omitempty"`
	ProblemSHA256 string            `json:"problem_sha256,omitempty"`
	Reasons       []string          `json:"reasons,omitempty"`
	Counters      *EvidenceCounters `json:"counters,omitempty"`
}

// EvidenceBudget records the §8 budget constants in force.
type EvidenceBudget struct {
	ExactPivots int `json:"exact_pivots"`
	ExactBits   int `json:"exact_bits"`
	Iterations  int `json:"iterations,omitempty"`
}

// EvidenceProbe is one bisection probe and its evidence class.
type EvidenceProbe struct {
	Value  string `json:"value"`
	Result string `json:"result"`
}

// MinimumCVEvidence is the Step 5 part: S = F + σᵀx and the safe lower
// bound of T = σᵀx from the relaxed CV minimum and the Class lower bounds.
type MinimumCVEvidence struct {
	FixedSecond     string         `json:"fixed_second"` // F, float64 encoding
	SigmaTerms      []TermSnapshot `json:"sigma_terms"`
	Witness         []string       `json:"witness"`
	CandidateSecond string         `json:"candidate_second"` // exact S_candidate
	RequiredGap     string         `json:"required_gap"`     // exact
	LowerBounds     []ClassBound   `json:"lower_bounds,omitempty"`
}

const numericalEvidenceVersion = "numerical-evidence/v1"

func (r *engineRun) numericalEvidence(code DiagnosticCode, stage OptimizationStageID) NumericalEvidence {
	budget := r.budget()
	return NumericalEvidence{
		Version: numericalEvidenceVersion, Code: code,
		EngineVersion: engineImplementationVersion, VerifierVersion: evidenceVerifierVersion, BackendVersion: gonumBackendVersion,
		Stage: string(stage), Tolerance: encodeFloat64(r.tau()), OptimalityTolerance: encodeFloat64(r.options.OptimalityTolerance),
		Rule: encodeEvidenceRule(r.accept), Budget: EvidenceBudget{ExactPivots: budget.maxPivots, ExactBits: budget.maxBits},
		ClassIDs: preparedClassIDs(r.compiled.Prepared.Classes),
	}
}

func setProblem(snapshot **ProblemSnapshot, sum *string, problem LinearProblem) error {
	s := SnapshotProblem(problem)
	digest, err := s.SHA256()
	if err != nil {
		return err
	}
	*snapshot, *sum = &s, digest
	return nil
}

// writeEvidence saves one evidence file atomically and returns its path; no
// evidence directory means nothing is written.
func (r *engineRun) writeEvidence(name string, value any) (string, error) {
	if r.evidenceDir == "" {
		return "", nil
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return writeFileAtomic(r.evidenceDir, name, raw)
}

// attachEvidence writes value and records its path, or the failure, on the
// solution's diagnostics. Evidence never changes the result.
func (r *engineRun) attachEvidence(solution EngineSolution, name string, value any, buildErr error) EngineSolution {
	path, err := "", buildErr
	if err == nil {
		path, err = r.writeEvidence(name, value)
	}
	for i := range solution.Diagnostics {
		if err != nil {
			solution.Diagnostics[i].Causes = append(solution.Diagnostics[i].Causes, Cause{Summary: "evidence not saved: " + err.Error()})
			continue
		}
		solution.Diagnostics[i].EvidencePath = path
	}
	return solution
}

func numericalEvidenceName(stage OptimizationStageID) string {
	return fmt.Sprintf("numerical-evidence-%s.json", stage)
}

// VerifyNumericalEvidence re-verifies a PrecisionUnproven file: the feasible
// end passes the recorded rule, the reliable bound is verified evidence, and
// their exact distance exceeds the required precision. An EvidenceUnavailable
// file only has to load: it records what could not be decided.
func VerifyNumericalEvidence(file NumericalEvidence) error {
	if file.Version != numericalEvidenceVersion {
		return fmt.Errorf("unknown numerical evidence version %q", file.Version)
	}
	rule, err := (EvidenceHeader{Tolerance: file.Tolerance, Rule: file.Rule}).rule()
	if err != nil {
		return err
	}
	switch file.Code {
	case DiagnosticEvidenceUnavailable:
		_, err := openSnapshot(file.Problem, file.ProblemSHA256)
		return err
	case DiagnosticPrecisionUnproven:
		if file.MinimumCV != nil {
			return verifyMinimumCVEvidence(file, rule)
		}
		return verifyBracketEvidence(file, rule)
	}
	return fmt.Errorf("unknown evidence code %q", file.Code)
}

func openSnapshot(snapshot *ProblemSnapshot, sum string) (LinearProblem, error) {
	if snapshot == nil {
		return LinearProblem{}, errors.New("missing problem snapshot")
	}
	if digest, err := snapshot.SHA256(); err != nil || digest != sum {
		return LinearProblem{}, errors.New("problem snapshot does not match its SHA-256")
	}
	return snapshot.Problem()
}

func verifyBracketEvidence(file NumericalEvidence, rule acceptanceRule) error {
	problem, err := openSnapshot(file.FeasibleProblem, file.FeasibleSHA256)
	if err != nil {
		return fmt.Errorf("feasible end: %w", err)
	}
	witness, err := decodeValues(file.Witness)
	if err != nil {
		return err
	}
	if !replayWithRule(problem, witness, rule) {
		return errors.New("feasible-end witness fails the recorded rule")
	}
	feasible, err := decodeFloat64(file.FeasibleEnd)
	if err != nil {
		return err
	}
	bound, err := decodeFloat64(file.BoundEnd)
	if err != nil {
		return err
	}
	switch file.BoundBasis {
	case BasisTheoreticalEndpoint:
		theoretical := 1.0
		if file.Direction == "minimize" {
			theoretical = 0
		}
		if bound != theoretical {
			return fmt.Errorf("theoretical endpoint %s is not %g", file.BoundEnd, theoretical)
		}
	case BasisVerifiedCertificate:
		if file.BoundEvidence == nil || file.BoundEvidence.Probe != file.BoundEnd || file.BoundEvidence.Rule != file.Rule {
			return errors.New("bound certificate missing or bound to another probe or rule")
		}
		if err := VerifyInfeasibilityEvidence(*file.BoundEvidence); err != nil {
			return fmt.Errorf("bound certificate: %w", err)
		}
	default:
		return fmt.Errorf("unknown bound basis %q", file.BoundBasis)
	}
	if (file.Direction == "minimize" && bound > feasible) || (file.Direction == "maximize" && bound < feasible) {
		return errors.New("bound lies on the wrong side of the feasible end")
	}
	required, ok := new(big.Rat).SetString(file.RequiredPrecision)
	if !ok {
		return fmt.Errorf("required precision %q is not rational", file.RequiredPrecision)
	}
	if ratAbs(ratSub(ratOf(bound), ratOf(feasible))).Cmp(required) <= 0 {
		return errors.New("the verified bracket is within the required precision")
	}
	return nil
}

func verifyMinimumCVEvidence(file NumericalEvidence, rule acceptanceRule) error {
	problem, err := openSnapshot(file.Problem, file.ProblemSHA256)
	if err != nil {
		return err
	}
	cv := file.MinimumCV
	witness, err := decodeValues(cv.Witness)
	if err != nil {
		return err
	}
	if !replayWithRule(problem, witness, rule) {
		return errors.New("minimum-CV candidate fails the recorded rule")
	}
	terms, err := decodeTerms(cv.SigmaTerms)
	if err != nil {
		return err
	}
	sigma := map[VariableID]float64{}
	for _, term := range terms {
		sigma[term.Variable] += term.Coeff
	}
	var lowerRow *LinearRow
	for i, row := range problem.Rows {
		if row.ID == cvMinRowID || row.ID == cvMaxRowID {
			if !sameTerms(row.Terms, sigma) {
				return fmt.Errorf("σ terms differ from row %q", row.ID)
			}
			if row.ID == cvMinRowID {
				lowerRow = &problem.Rows[i]
			}
		}
	}
	fixed, err := decodeFloat64(cv.FixedSecond)
	if err != nil {
		return err
	}
	candidateT := sigmaValue(problem, sigma, witness)
	candidateS := ratAdd(ratOf(fixed), candidateT)
	if claimed, ok := new(big.Rat).SetString(cv.CandidateSecond); !ok || claimed.Cmp(candidateS) != 0 {
		return errors.New("candidate second moment does not match the witness")
	}
	required := requiredMinimumCVGap(candidateS)
	if claimed, ok := new(big.Rat).SetString(cv.RequiredGap); !ok || claimed.Cmp(required) != 0 {
		return errors.New("required gap does not match the candidate")
	}
	var safeT *big.Rat
	if lowerRow != nil {
		if safeT, _, err = relaxCVRow(problem, *lowerRow, rule); err != nil {
			return err
		}
	}
	if len(cv.LowerBounds) > 0 {
		subs, _, err := splitByClassIDs(problem, file.ClassIDs)
		if err != nil {
			return fmt.Errorf("split: %w", err)
		}
		if len(cv.LowerBounds) != len(subs) {
			return fmt.Errorf("%d Class bounds for %d Classes", len(cv.LowerBounds), len(subs))
		}
		total := new(big.Rat)
		for i, bound := range cv.LowerBounds {
			if bound.Class != i+1 || bound.Direction != "lower" {
				return fmt.Errorf("Class bound %d is %d/%s", i, bound.Class, bound.Direction)
			}
			var classTerms []LinearTerm
			for _, v := range subs[i].problem.Variables {
				if a, ok := sigma[v.ID]; ok && a != 0 {
					classTerms = append(classTerms, LinearTerm{Variable: v.ID, Coeff: a})
				}
			}
			value, err := verifyBound(subs[i].problem, rule, classTerms, "lower", bound.Multipliers, bound.Value)
			if err != nil {
				return fmt.Errorf("Class %d: %w", bound.Class, err)
			}
			total = ratAdd(total, value)
		}
		if safeT == nil || total.Cmp(safeT) > 0 {
			safeT = total
		}
	}
	if safeT == nil {
		return nil // no reliable lower bound: the gap is unbounded
	}
	if ratSub(candidateT, safeT).Cmp(required) <= 0 {
		return errors.New("the verified gap is within the requirement")
	}
	return nil
}

func sameTerms(terms []LinearTerm, sigma map[VariableID]float64) bool {
	seen := map[VariableID]float64{}
	for _, term := range terms {
		seen[term.Variable] += term.Coeff
	}
	if len(seen) != len(sigma) {
		return false
	}
	for id, a := range seen {
		if sigma[id] != a {
			return false
		}
	}
	return true
}
