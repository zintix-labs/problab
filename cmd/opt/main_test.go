//go:build !poc

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

package main

import (
	"bytes"
	"strings"
	"testing"

	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
)

// TestLoadV2ConfigUsesOnlyEmbeddedIntentPlans proves the command-owned YAML is
// the complete execution source. Every declared plan is directly resolvable;
// runV2 therefore needs neither a plan selector nor field-level overrides.
func TestLoadV2ConfigUsesOnlyEmbeddedIntentPlans(t *testing.T) {
	t.Parallel()

	config, err := loadV2Config()
	if err != nil {
		t.Fatalf("loadV2Config: %v", err)
	}
	if len(config.Plans) == 0 {
		t.Fatal("embedded config contains no plans")
	}
	for _, plan := range config.Plans {
		resolved, err := config.ResolvePlan(plan.ID)
		if err != nil {
			t.Fatalf("ResolvePlan(%q): %v", plan.ID, err)
		}
		if resolved.Plan.Engine != optimizerv2.EngineIntentLPV2 {
			t.Fatalf("plan %q engine=%q, want %q", plan.ID, resolved.Plan.Engine, optimizerv2.EngineIntentLPV2)
		}
		if resolved.Plan.ID != plan.ID || resolved.Plan.Target.Game != plan.Target.Game ||
			resolved.Plan.Target.BetModes[0] != plan.Target.BetModes[0] || resolved.Plan.Seed != plan.Seed {
			t.Fatalf("plan %q was not used directly from embedded config: resolved=%+v source=%+v", plan.ID, resolved.Plan, plan)
		}
	}
}

// TestParseEmbeddedConfigContentFailuresAreTyped retains the command boundary's
// distinction between strict-schema decoding and semantic validation without
// reintroducing an external-config runtime path.
func TestParseEmbeddedConfigContentFailuresAreTyped(t *testing.T) {
	canonical, err := embeddedV2Config.ReadFile(embeddedConfigName)
	if err != nil {
		t.Fatalf("read embedded %s: %v", embeddedConfigName, err)
	}

	tests := []struct {
		name      string
		content   []byte
		want      string
		wantStage string
	}{
		{
			name:      "unknown field",
			content:   append(append([]byte(nil), canonical...), []byte("\nunexpected_embedded_field: true\n")...),
			want:      "unexpected_embedded_field",
			wantStage: "load-config",
		},
		{
			name:      "semantic invalid",
			content:   []byte(strings.Replace(string(canonical), "version: 2", "version: 999", 1)),
			want:      "version",
			wantStage: "static-validation",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseV2ConfigBytes(test.content, `embedded "opt_cfg.yaml"`)
			if err == nil {
				t.Fatal("parseV2ConfigBytes succeeded")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%q, want %q", err, test.want)
			}
			if got := invalidV2ConfigStage(err); got != test.wantStage {
				t.Fatalf("invalid stage=%q, want %q", got, test.wantStage)
			}
			result := configInvalidRunResult(err, test.wantStage, 0)
			if result.Status != optimizerv2.StatusInfeasibleConfig ||
				len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != optimizerv2.DiagnosticConfigInvalid {
				t.Fatalf("typed result=%+v", result)
			}
		})
	}
}

// TestRunV2RejectsAllCommandLineParameters locks embeddedConfigName as the one
// runtime source. Reintroducing even a valid-looking old flag must fail before
// config loading, Problab construction, or collection.
func TestRunV2RejectsAllCommandLineParameters(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"-plan", "demo_0"},
		{"-config", "another.yaml"},
		{"-game", "0"},
		{"-mode", "0"},
		{"-seed", "0"},
		{"unexpected"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		exitCode, err := runV2(arguments, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "does not accept command-line parameters") {
			t.Fatalf("runV2(%v) error=%v", arguments, err)
		}
		if exitCode != 1 || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("runV2(%v): exit=%d stdout=%q stderr=%q", arguments, exitCode, stdout.String(), stderr.String())
		}
	}
}
