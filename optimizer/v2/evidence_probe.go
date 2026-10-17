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
	"fmt"
	"math"
	"strings"
)

// A probe label means nothing unless the matrix carries it. These checks
// bind a bisection probe value to its problem snapshot: the stage's probe
// rows must hold exactly the coefficients the model builders compute for that
// value (same float64 operations), so a certificate of one probe cannot be
// presented as the certificate of another.

// probeFamily is the row family each bisection stage adds per probe.
var probeFamily = map[string]string{
	string(StageMinimizeMainProfileDeviation):        "main_profile_deviation_lock",
	string(StageMaximizeOtherBucketVisibility):       "other_bucket_visibility",
	string(StageMaximizeMainGroupInternalVisibility): "main_group_internal_visibility",
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

// verifyProbeBinding checks that problem is the probe problem of stage at
// the labelled value. Stages without probe rows need no binding.
func verifyProbeBinding(stage string, problem LinearProblem, label string) error {
	family, ok := probeFamily[stage]
	if !ok {
		return nil
	}
	value, err := decodeFinite(label, "probe")
	if err != nil {
		return err
	}
	var rows []LinearRow
	for _, row := range problem.Rows {
		if row.Family == family {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return fmt.Errorf("stage %s: problem has no %s rows", stage, family)
	}
	switch stage {
	case string(StageMinimizeMainProfileDeviation):
		return verifyDeltaRows(rows, value)
	case string(StageMaximizeOtherBucketVisibility):
		return verifyOtherVisibilityRows(rows, value)
	default:
		return verifyMainVisibilityRows(rows, value)
	}
}

// verifyDeltaRows: Σ d ≤ δ·(Main mass), written as Σ d − δ·p ≤ 0
// (BuildMainProfileDeviationProblem).
func verifyDeltaRows(rows []LinearRow, delta float64) error {
	for _, row := range rows {
		if row.Sense != SenseLE || row.RHS != 0 {
			return fmt.Errorf("row %q is not a δ lock row", row.ID)
		}
		mainTerms := 0
		for _, term := range row.Terms {
			want := -delta
			if strings.HasPrefix(string(term.Variable), "d:main:") {
				want = 1
			} else {
				mainTerms++
			}
			if term.Coeff != want {
				return fmt.Errorf("row %q: coefficient %v of %q does not match δ = %v", row.ID, term.Coeff, term.Variable, delta)
			}
		}
		// δ = 0 drops the Main terms; any other δ must carry them.
		if delta != 0 && mainTerms == 0 {
			return fmt.Errorf("row %q has no Main terms for δ = %v", row.ID, delta)
		}
	}
	return nil
}

// verifyOtherVisibilityRows: per Class with k supported Others, share = ρ/k;
// each row is p_other + share·(Main mass) ≥ share (AddOtherBucketVisibilityRows).
func verifyOtherVisibilityRows(rows []LinearRow, rho float64) error {
	perClass := map[string]int{}
	for _, row := range rows {
		perClass[row.ClassID]++
	}
	for _, row := range rows {
		share := rho / float64(perClass[row.ClassID])
		if row.Sense != SenseGE || row.RHS != share {
			return fmt.Errorf("row %q: RHS %v does not match ρ = %v", row.ID, row.RHS, rho)
		}
		ones := 0
		for _, term := range row.Terms {
			switch term.Coeff {
			case share:
			case 1:
				ones++
			default:
				return fmt.Errorf("row %q: coefficient %v of %q does not match ρ = %v", row.ID, term.Coeff, term.Variable, rho)
			}
		}
		if share != 1 && ones != 1 { // with share = 1 the Other term is indistinguishable
			return fmt.Errorf("row %q has %d Other bucket terms", row.ID, ones)
		}
	}
	return nil
}

// verifyMainVisibilityRows: per Main Group with k supported buckets,
// share = ρ/k; each row is (1 − share)·p_self − share·Σ p_sibling ≥ 0
// (AddMainGroupInternalVisibilityRows; 1 − share is computed as 1 + (−share)).
func verifyMainVisibilityRows(rows []LinearRow, rho float64) error {
	group := func(id RowID) string {
		text := string(id)
		if i := strings.LastIndex(text, ":bucket-"); i >= 0 {
			return text[:i]
		}
		return text
	}
	perGroup := map[string]int{}
	for _, row := range rows {
		perGroup[group(row.ID)]++
	}
	for _, row := range rows {
		k := perGroup[group(row.ID)]
		share := rho / float64(k)
		self := 1 + (-share)
		if row.Sense != SenseGE || row.RHS != 0 || k < 2 {
			return fmt.Errorf("row %q is not a Main Group visibility row", row.ID)
		}
		selves, siblings := 0, 0
		for _, term := range row.Terms {
			switch term.Coeff {
			case self:
				selves++
			case -share:
				siblings++
			default:
				return fmt.Errorf("row %q: coefficient %v of %q does not match ρ = %v", row.ID, term.Coeff, term.Variable, rho)
			}
		}
		wantSiblings := k - 1
		if share == 0 {
			wantSiblings = 0 // zero coefficients are dropped
		}
		if selves != 1 || siblings != wantSiblings {
			return fmt.Errorf("row %q has %d self and %d sibling terms for %d buckets", row.ID, selves, siblings, k)
		}
	}
	return nil
}

// verifySameExceptProbe checks that two probe problems of one stage share
// every variable and every row except the stage's probe rows (the common
// constraints of the bracket).
func verifySameExceptProbe(stage string, a, b LinearProblem) error {
	family := probeFamily[stage]
	strip := func(problem LinearProblem) (ProblemSnapshot, []RowID) {
		out := LinearProblem{Variables: problem.Variables}
		var ids []RowID
		for _, row := range problem.Rows {
			if row.Family == family {
				ids = append(ids, row.ID)
				continue
			}
			out.Rows = append(out.Rows, row)
		}
		return SnapshotProblem(out), ids
	}
	sa, ia := strip(a)
	sb, ib := strip(b)
	ha, err := sa.SHA256()
	if err != nil {
		return err
	}
	hb, err := sb.SHA256()
	if err != nil {
		return err
	}
	if ha != hb || fmt.Sprint(ia) != fmt.Sprint(ib) {
		return fmt.Errorf("the two bracket ends do not share their common constraints")
	}
	return nil
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
