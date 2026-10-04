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
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/zintix-labs/problab"
	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
)

func TestRunV2UsesInjectedLabFactory(t *testing.T) {
	// Do not parallelize: this test temporarily replaces the composition point.
	original := pLab
	t.Cleanup(func() { pLab = original })
	sentinel := errors.New("private runtime construction failed")
	calls := 0
	pLab = func() (*problab.Problab, error) {
		calls++
		return nil, sentinel
	}
	var stdout, stderr bytes.Buffer
	code, err := runV2(nil, &stdout, &stderr)
	if code != 1 || !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("code=%d err=%v factory calls=%d", code, err, calls)
	}
	if !strings.HasPrefix(err.Error(), "construct Problab: ") {
		t.Fatalf("missing project-neutral construction context: %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestMainRejectsArgumentsSubprocess(t *testing.T) {
	if os.Getenv("PROBLAB_TEST_OPT_MAIN") == "1" {
		os.Args = []string{"opt", "unsupported"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainRejectsArgumentsSubprocess$")
	cmd.Env = append(os.Environ(), "PROBLAB_TEST_OPT_MAIN=1")
	out, err := cmd.CombinedOutput()
	if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 1 {
		t.Fatalf("err=%v output=%s", err, out)
	}
	if !strings.Contains(string(out), "does not accept command-line parameters") {
		t.Fatalf("output=%s", out)
	}
}

// TestLoadV2ConfigUsesOnlyEmbeddedIntentPlans proves the command-owned YAML is
// the complete execution source. Every declared plan is directly resolvable;
// runV2 therefore needs neither a plan selector nor field-level overrides.
func TestLoadV2ConfigUsesOnlyEmbeddedIntentPlans(t *testing.T) {
	t.Parallel()

	raw, err := optConfig.ReadFile(embeddedConfigName)
	if err != nil {
		t.Fatal(err)
	}
	config, err := optimizerv2.ParseConfig(raw)
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
			resolved.Plan.Target.BetModes[0] != plan.Target.BetModes[0] ||
			resolved.Plan.Seed.Kind() != plan.Seed.Kind() || !bytes.Equal(resolved.Plan.Seed.Bytes(), plan.Seed.Bytes()) {
			t.Fatalf("plan %q was not used directly from embedded config: resolved=%+v source=%+v", plan.ID, resolved.Plan, plan)
		}
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
