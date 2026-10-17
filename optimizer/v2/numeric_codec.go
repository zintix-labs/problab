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
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

// numericEncodingVersion identifies the lossless float encoding used by every
// evidence and slow-solve file. Plain JSON numbers cannot carry ±Inf and do not
// promise bit-exact float64 round trips, so values are strings instead.
const numericEncodingVersion = "float64-shortest-roundtrip-v1"

// encodeFloat64 returns the shortest decimal string that parses back to the
// identical float64, or "+Inf"/"-Inf". NaN is never part of a valid model.
func encodeFloat64(value float64) string {
	switch {
	case math.IsInf(value, 1):
		return "+Inf"
	case math.IsInf(value, -1):
		return "-Inf"
	case math.IsNaN(value):
		return "NaN"
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// decodeFloat64 is the exact inverse of encodeFloat64.
func decodeFloat64(text string) (float64, error) {
	switch text {
	case "+Inf":
		return math.Inf(1), nil
	case "-Inf":
		return math.Inf(-1), nil
	case "NaN":
		return 0, fmt.Errorf("NaN is not a valid encoded model value")
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("decode float64 %q: %w", text, err)
	}
	return value, nil
}

// ProblemSnapshot is the self-contained, losslessly encoded form of one
// semantic LP. It keeps every field the evidence verifiers and Class split need
// (ClassID, Family, Origin, YAMLPath) so no in-process object is required to
// rebuild the problem.
type ProblemSnapshot struct {
	Encoding  string             `json:"encoding"`
	Variables []VariableSnapshot `json:"variables"`
	Rows      []RowSnapshot      `json:"rows"`
}

// VariableSnapshot is one encoded LinearVariable.
type VariableSnapshot struct {
	ID    VariableID `json:"id"`
	Lower string     `json:"lower"`
	Upper string     `json:"upper"`
}

// RowSnapshot is one encoded LinearRow.
type RowSnapshot struct {
	ID       RowID          `json:"id"`
	Family   string         `json:"family,omitempty"`
	Origin   RuleOrigin     `json:"origin,omitempty"`
	ClassID  string         `json:"class_id,omitempty"`
	YAMLPath string         `json:"yaml_path,omitempty"`
	Sense    Sense          `json:"sense"`
	RHS      string         `json:"rhs"`
	Terms    []TermSnapshot `json:"terms"`
}

// TermSnapshot is one encoded LinearTerm.
type TermSnapshot struct {
	Variable VariableID `json:"variable"`
	Coeff    string     `json:"coeff"`
}

// ObjectiveSnapshot is one encoded LinearObjective.
type ObjectiveSnapshot struct {
	Name   OptimizationStageID `json:"name"`
	Sense  ObjectiveSense      `json:"sense"`
	Origin ObjectiveOrigin     `json:"origin"`
	Terms  []TermSnapshot      `json:"terms"`
}

func encodeTerms(terms []LinearTerm) []TermSnapshot {
	out := make([]TermSnapshot, len(terms))
	for i, term := range terms {
		out[i] = TermSnapshot{Variable: term.Variable, Coeff: encodeFloat64(term.Coeff)}
	}
	return out
}

func decodeTerms(terms []TermSnapshot) ([]LinearTerm, error) {
	out := make([]LinearTerm, len(terms))
	for i, term := range terms {
		coeff, err := decodeFloat64(term.Coeff)
		if err != nil {
			return nil, err
		}
		out[i] = LinearTerm{Variable: term.Variable, Coeff: coeff}
	}
	return out, nil
}

// SnapshotProblem encodes a semantic LP. The result shares no memory with the
// input, so later mutation of the source cannot change the snapshot.
func SnapshotProblem(problem LinearProblem) ProblemSnapshot {
	snapshot := ProblemSnapshot{
		Encoding:  numericEncodingVersion,
		Variables: make([]VariableSnapshot, len(problem.Variables)),
		Rows:      make([]RowSnapshot, len(problem.Rows)),
	}
	for i, variable := range problem.Variables {
		snapshot.Variables[i] = VariableSnapshot{ID: variable.ID, Lower: encodeFloat64(variable.Lower), Upper: encodeFloat64(variable.Upper)}
	}
	for i, row := range problem.Rows {
		snapshot.Rows[i] = RowSnapshot{
			ID: row.ID, Family: row.Family, Origin: row.Origin, ClassID: row.ClassID, YAMLPath: row.YAMLPath,
			Sense: row.Sense, RHS: encodeFloat64(row.RHS), Terms: encodeTerms(row.Terms),
		}
	}
	return snapshot
}

// Problem decodes the snapshot back into a semantic LP with identical float64
// bits. Bound provenance and descriptions are not part of the numerical model.
func (snapshot ProblemSnapshot) Problem() (LinearProblem, error) {
	if snapshot.Encoding != numericEncodingVersion {
		return LinearProblem{}, fmt.Errorf("unsupported numeric encoding %q", snapshot.Encoding)
	}
	problem := LinearProblem{
		Variables: make([]LinearVariable, len(snapshot.Variables)),
		Rows:      make([]LinearRow, len(snapshot.Rows)),
	}
	for i, variable := range snapshot.Variables {
		lower, err := decodeFloat64(variable.Lower)
		if err != nil {
			return LinearProblem{}, err
		}
		upper, err := decodeFloat64(variable.Upper)
		if err != nil {
			return LinearProblem{}, err
		}
		problem.Variables[i] = LinearVariable{ID: variable.ID, Lower: lower, Upper: upper}
	}
	for i, row := range snapshot.Rows {
		rhs, err := decodeFloat64(row.RHS)
		if err != nil {
			return LinearProblem{}, err
		}
		terms, err := decodeTerms(row.Terms)
		if err != nil {
			return LinearProblem{}, err
		}
		problem.Rows[i] = LinearRow{
			ID: row.ID, Family: row.Family, Origin: row.Origin, ClassID: row.ClassID, YAMLPath: row.YAMLPath,
			Sense: row.Sense, RHS: rhs, Terms: terms,
		}
	}
	return problem, nil
}

// SHA256 is the hex digest of the snapshot's canonical JSON encoding.
func (snapshot ProblemSnapshot) SHA256() (string, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// SnapshotObjective encodes a LinearObjective.
func SnapshotObjective(objective LinearObjective) ObjectiveSnapshot {
	return ObjectiveSnapshot{Name: objective.Name, Sense: objective.Sense, Origin: objective.Origin, Terms: encodeTerms(objective.Terms)}
}

// Objective decodes the snapshot back into a LinearObjective.
func (snapshot ObjectiveSnapshot) Objective() (LinearObjective, error) {
	terms, err := decodeTerms(snapshot.Terms)
	if err != nil {
		return LinearObjective{}, err
	}
	return LinearObjective{Name: snapshot.Name, Sense: snapshot.Sense, Origin: snapshot.Origin, Terms: terms}, nil
}

// encodeValues and decodeValues carry witness vectors losslessly.
func encodeValues(values []float64) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = encodeFloat64(value)
	}
	return out
}

func decodeValues(values []string) ([]float64, error) {
	out := make([]float64, len(values))
	for i, value := range values {
		decoded, err := decodeFloat64(value)
		if err != nil {
			return nil, err
		}
		out[i] = decoded
	}
	return out, nil
}

// writeFileAtomic writes data under directory/name through a same-directory
// temporary file that is fsynced before the rename, so a reader never sees a
// truncated evidence file. The directory is created when missing.
func writeFileAtomic(directory, name string, data []byte) (string, error) {
	if directory == "" {
		return "", fmt.Errorf("evidence directory is not configured")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(directory, ".evidence-")
	if err != nil {
		return "", err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	target := filepath.Join(directory, name)
	if err := os.Rename(temporaryName, target); err != nil {
		return "", err
	}
	return target, nil
}
