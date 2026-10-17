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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// A probe label means nothing unless the matrix carries it, and which
// variables a probe row must contain (Main membership, Group membership,
// supported buckets, Others) is not visible in a LinearProblem. Evidence of
// a stage problem therefore carries the builder inputs (ProbeStructure) and
// the earlier stages' lock values; the verifier strips every stage-added
// variable and row to recover the hard model, rebuilds the stage chain with
// the production builders, and requires the snapshot to match bit for bit.

// ProbeStructure is the part of the prepared model the stage builders read.
type ProbeStructure struct {
	Intent  string       `json:"intent"` // plan intent name, part of row YAML paths
	Classes []ProbeClass `json:"classes"`
}

// ProbeClass is one prepared Class as the builders see it.
type ProbeClass struct {
	ID        string       `json:"id"`
	Intent    bool         `json:"intent"`
	Variables []VariableID `json:"variables"` // ClassVariables, bucket order
	Supported []bool       `json:"supported"`
	Groups    []ProbeGroup `json:"groups,omitempty"`
	Others    []int        `json:"others,omitempty"`
}

// ProbeGroup is one Main Group.
type ProbeGroup struct {
	Index       int    `json:"index"`
	Buckets     []int  `json:"buckets"`
	PreferShare string `json:"prefer_share"`
}

// ProbeLocks are the fixed values of the stages before the bound one.
type ProbeLocks struct {
	Delta    string `json:"delta,omitempty"`
	OtherRho string `json:"other_rho,omitempty"`
	MainRho  string `json:"main_rho,omitempty"`
}

// ProbeBase binds a stage problem to the hard model and the builders.
type ProbeBase struct {
	Structure       ProbeStructure `json:"structure"`
	StructureSHA256 string         `json:"structure_sha256"`
	HardSHA256      string         `json:"hard_sha256"` // snapshot SHA-256 of the hard model
	Locks           ProbeLocks     `json:"locks"`
}

// stageFamilies are the row families the stage builders add.
var stageFamilies = map[string]bool{
	"main_profile_deviation": true, "main_profile_deviation_lock": true,
	"other_bucket_visibility": true, "main_group_internal_visibility": true,
}

// stageDirection is each bisection stage's fixed direction and theoretical
// endpoint: δ is minimized toward 0, both ρ are maximized toward 1.
func stageDirection(stage string) (direction string, theoretical float64, ok bool) {
	switch stage {
	case string(StageMinimizeMainProfileDeviation):
		return "minimize", 0, true
	case string(StageMaximizeOtherBucketVisibility), string(StageMaximizeMainGroupInternalVisibility):
		return "maximize", 1, true
	}
	return "", 0, false
}

// stageRule is the acceptance set evidence of a stage must be stated for:
// hard certificates refute the external set (INFEASIBLE_MODEL), every other
// stage works on the internal set.
func stageRule(stage string, tau float64) acceptanceRule {
	if stage == string(StageProveHardFeasibility) {
		return productRule(tau)
	}
	return internalRule(tau)
}

// probeStages are the stages whose problems carry a probe value.
var probeStages = map[string]bool{
	string(StageMinimizeMainProfileDeviation):        true,
	string(StageMaximizeOtherBucketVisibility):       true,
	string(StageMaximizeMainGroupInternalVisibility): true,
}

func probeStructure(compiled CompiledModel) ProbeStructure {
	structure := ProbeStructure{Intent: compiled.Prepared.Plan.Plan.Intent}
	for i, class := range compiled.Prepared.Classes {
		pc := ProbeClass{ID: class.ID, Intent: class.Intent, Others: append([]int(nil), class.Others...)}
		if i < len(compiled.ClassVariables) {
			pc.Variables = append([]VariableID(nil), compiled.ClassVariables[i]...)
		}
		for _, bucket := range class.Buckets {
			pc.Supported = append(pc.Supported, bucket.Supported())
		}
		for _, group := range class.Groups {
			pc.Groups = append(pc.Groups, ProbeGroup{Index: group.Index, Buckets: append([]int(nil), group.BucketIndexes...), PreferShare: encodeFloat64(group.PreferShare)})
		}
		structure.Classes = append(structure.Classes, pc)
	}
	return structure
}

// SHA256 is the hex digest of the structure's JSON encoding.
func (s ProbeStructure) SHA256() (string, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// compiled rebuilds the minimal CompiledModel the builders read, with hard
// as its hard model.
func (s ProbeStructure) compiled(hard LinearProblem) (CompiledModel, error) {
	compiled := CompiledModel{Hard: hard, VariableIndex: map[VariableID]int{}}
	compiled.Prepared.Plan.Plan.Intent = s.Intent
	for i, pc := range s.Classes {
		if len(pc.Supported) != len(pc.Variables) {
			return CompiledModel{}, fmt.Errorf("class %q: %d buckets but %d variables", pc.ID, len(pc.Supported), len(pc.Variables))
		}
		class := PreparedClass{ID: pc.ID, Index: i, Intent: pc.Intent, Others: pc.Others}
		for b, supported := range pc.Supported {
			bucket := PreparedBucket{Index: b}
			if supported {
				bucket.Samples = []CollectedSample{{}}
			}
			class.Buckets = append(class.Buckets, bucket)
		}
		for _, group := range pc.Groups {
			share, err := decodeFinite(group.PreferShare, "prefer share")
			if err != nil {
				return CompiledModel{}, err
			}
			for _, b := range group.Buckets {
				if b < 0 || b >= len(pc.Variables) {
					return CompiledModel{}, fmt.Errorf("class %q group %d: bucket %d out of range", pc.ID, group.Index, b)
				}
			}
			class.Groups = append(class.Groups, PreparedGroup{Index: group.Index, BucketIndexes: group.Buckets, PreferShare: share})
		}
		for _, b := range pc.Others {
			if b < 0 || b >= len(pc.Variables) {
				return CompiledModel{}, fmt.Errorf("class %q: Other bucket %d out of range", pc.ID, b)
			}
		}
		compiled.Prepared.Classes = append(compiled.Prepared.Classes, class)
		compiled.ClassVariables = append(compiled.ClassVariables, pc.Variables)
	}
	return compiled, nil
}

// EvidenceBaseReport is the run-report anchor of every ProbeBase.
type EvidenceBaseReport struct {
	HardSHA256      string `json:"hard_sha256"`
	StructureSHA256 string `json:"structure_sha256"`
}

func evidenceBaseReport(compiled CompiledModel) *EvidenceBaseReport {
	hard, err := SnapshotProblem(compiled.Hard).SHA256()
	if err != nil {
		return nil
	}
	structure, err := probeStructure(compiled).SHA256()
	if err != nil {
		return nil
	}
	return &EvidenceBaseReport{HardSHA256: hard, StructureSHA256: structure}
}

// probeBase records the builder inputs of this run and the locks so far.
func (r *engineRun) probeBase() (*ProbeBase, error) {
	structure := probeStructure(r.compiled)
	sum, err := structure.SHA256()
	if err != nil {
		return nil, err
	}
	hard, err := SnapshotProblem(r.compiled.Hard).SHA256()
	if err != nil {
		return nil, err
	}
	return &ProbeBase{Structure: structure, StructureSHA256: sum, HardSHA256: hard, Locks: r.locks}, nil
}

// stripStages removes every stage-added variable and row, leaving the hard
// model (stage builders only append).
func stripStages(problem LinearProblem) LinearProblem {
	var hard LinearProblem
	for _, v := range problem.Variables {
		if !strings.HasPrefix(string(v.ID), "d:main:") {
			hard.Variables = append(hard.Variables, v)
		}
	}
	for _, row := range problem.Rows {
		if !stageFamilies[row.Family] {
			hard.Rows = append(hard.Rows, row)
		}
	}
	return hard
}

// verifyStageProblem checks that problem is exactly the production stage
// chain: hard model, δ problem at the δ lock (or probe), Other visibility at
// its lock (or probe), Main Group visibility at its probe or lock. stage is
// the stage the problem belongs to; label is its probe value, or "" for the
// minimum-CV lock problem, which uses every applicable lock.
func verifyStageProblem(base *ProbeBase, stage, label string, problem LinearProblem) error {
	if base == nil {
		return errors.New("stage evidence lacks its probe base")
	}
	if sum, err := base.Structure.SHA256(); err != nil || sum != base.StructureSHA256 {
		return errors.New("probe structure does not match its SHA-256")
	}
	hard := stripStages(problem)
	if sum, err := SnapshotProblem(hard).SHA256(); err != nil || sum != base.HardSHA256 {
		return errors.New("stage problem is not built on the recorded hard model")
	}
	compiled, err := base.Structure.compiled(hard)
	if err != nil {
		return err
	}
	value := func(own, lock, name string) (float64, error) {
		text := lock
		if stage == own {
			text = label
		}
		if text == "" {
			return 0, fmt.Errorf("missing %s value", name)
		}
		return decodeFinite(text, name)
	}
	delta, err := value(string(StageMinimizeMainProfileDeviation), base.Locks.Delta, "δ")
	if err != nil {
		return err
	}
	rebuilt, err := BuildMainProfileDeviationProblem(compiled, delta)
	if err != nil {
		return err
	}
	if stage != string(StageMinimizeMainProfileDeviation) {
		if hasEligibleOthers(compiled.Prepared) {
			rho, err := value(string(StageMaximizeOtherBucketVisibility), base.Locks.OtherRho, "ρ_other")
			if err != nil {
				return err
			}
			if rebuilt, err = AddOtherBucketVisibilityRows(rebuilt, compiled, rho); err != nil {
				return err
			}
		} else if stage == string(StageMaximizeOtherBucketVisibility) || base.Locks.OtherRho != "" {
			return errors.New("Other visibility does not apply to this structure")
		}
	}
	if stage != string(StageMinimizeMainProfileDeviation) && stage != string(StageMaximizeOtherBucketVisibility) {
		if hasEligibleMainGroupInternalVisibility(compiled.Prepared) {
			rho, err := value(string(StageMaximizeMainGroupInternalVisibility), base.Locks.MainRho, "ρ_main")
			if err != nil {
				return err
			}
			if rebuilt, err = AddMainGroupInternalVisibilityRows(rebuilt, compiled, rho); err != nil {
				return err
			}
		} else if stage == string(StageMaximizeMainGroupInternalVisibility) || base.Locks.MainRho != "" {
			return errors.New("Main Group visibility does not apply to this structure")
		}
	}
	want, err := SnapshotProblem(rebuilt).SHA256()
	if err != nil {
		return err
	}
	got, err := SnapshotProblem(problem).SHA256()
	if err != nil {
		return err
	}
	if want != got {
		label := label
		if label == "" {
			label = "the recorded locks"
		}
		return fmt.Errorf("problem is not the %s problem at %s", stage, label)
	}
	return nil
}

// sameProbeBase requires two pieces of evidence to share one base.
func sameProbeBase(a, b *ProbeBase) error {
	if a == nil || b == nil {
		return errors.New("missing probe base")
	}
	if a.StructureSHA256 != b.StructureSHA256 || a.HardSHA256 != b.HardSHA256 || a.Locks != b.Locks {
		return errors.New("the two bracket ends have different probe bases")
	}
	return nil
}

// decodeFinite decodes a float64 field that must be finite.
func decodeFinite(text, what string) (float64, error) {
	value, err := decodeFloat64(text)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", what, err)
	}
	if !isFinite(value) {
		return 0, fmt.Errorf("%s is not finite: %q", what, text)
	}
	return value, nil
}

// finiteTerms rejects objective terms that cannot be converted exactly.
func finiteTerms(terms []LinearTerm) error {
	for _, term := range terms {
		if math.IsNaN(term.Coeff) || math.IsInf(term.Coeff, 0) {
			return fmt.Errorf("objective coefficient of %q is not finite", term.Variable)
		}
	}
	return nil
}

// validateEvidenceProblem applies the solver's structural contract to a
// decoded snapshot: unique IDs, finite RHS and coefficients, known
// variables, strictly sorted (so unique) terms.
func validateEvidenceProblem(problem LinearProblem) error {
	return validateLinearProblem(problem, LinearObjective{Origin: ObjectiveDiagnostic}, SolveOptions{FeasibilityTolerance: 1, OptimalityTolerance: 1})
}
