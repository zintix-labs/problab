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
	"slices"
	"testing"

	"github.com/zintix-labs/problab/demo"
)

// TestValidateRuntimeTargetAllowsEveryModeAsAnIndependentRun prevents a return
// of the old all-mode preflight gate. The runtime catalog still supplies the
// complete bet-unit layout to the publisher, but any one in-range mode must be
// allowed to proceed to collection by itself.
func TestValidateRuntimeTargetAllowsEveryModeAsAnIndependentRun(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatalf("construct demo Problab: %v", err)
	}
	defer func() { _ = lab.Close() }()

	summaries, err := lab.Summary()
	if err != nil {
		t.Fatalf("read demo catalog: %v", err)
	}
	var multiModeIndex int
	for i, summary := range summaries {
		if len(summary.BetUnits) >= 2 {
			multiModeIndex = i
			break
		}
	}
	summary := summaries[multiModeIndex]
	if len(summary.BetUnits) < 2 {
		t.Skip("demo catalog has no multi-mode game fixture")
	}

	for mode := range summary.BetUnits {
		plan := ResolvedPlan{Plan: RunPlan{Target: Target{
			Game: summary.GID, BetModes: []int{mode},
		}}}
		gotBetUnits, diagnostic, err := validateRuntimeTarget(lab, plan)
		if err != nil {
			t.Fatalf("mode %d runtime validation: %v", mode, err)
		}
		if diagnostic.StopsRun() {
			t.Fatalf("mode %d was incorrectly blocked: %+v", mode, diagnostic)
		}
		if !slices.Equal(gotBetUnits, summary.BetUnits) {
			t.Fatalf("mode %d bet units=%v want=%v", mode, gotBetUnits, summary.BetUnits)
		}
	}
}

// TestValidateRuntimeTargetRejectsMultipleModesInOneRun keeps multi-mode solve
// settings from leaking back into Tuner even if a caller bypasses Config.Validate
// and constructs a ResolvedPlan manually.
func TestValidateRuntimeTargetRejectsMultipleModesInOneRun(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatalf("construct demo Problab: %v", err)
	}
	defer func() { _ = lab.Close() }()

	summaries, err := lab.Summary()
	if err != nil {
		t.Fatalf("read demo catalog: %v", err)
	}
	plan := ResolvedPlan{Plan: RunPlan{Target: Target{
		Game: summaries[0].GID, BetModes: []int{0, 1},
	}}}
	_, diagnostic, err := validateRuntimeTarget(lab, plan)
	if err != nil {
		t.Fatalf("runtime validation returned operational error: %v", err)
	}
	if !diagnostic.StopsRun() || diagnostic.Code != DiagnosticConfigInvalid || diagnostic.Status != StatusInfeasibleConfig {
		t.Fatalf("multi-mode plan diagnostic=%+v", diagnostic)
	}
}
