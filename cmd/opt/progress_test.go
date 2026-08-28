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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
)

// TestCLIProgressReporterShowsStagesAndClassMilestones locks the non-terminal
// contract used by CI logs and redirected stderr: stage boundaries are explicit,
// Classes retain declaration order, and progress output never contains cursor
// movement that would corrupt an append-only log.
func TestCLIProgressReporterShowsStagesAndClassMilestones(t *testing.T) {
	var output bytes.Buffer
	reporter := newCLIProgressReporter(&output)
	reporter.Report(optimizerv2.StageEvent{Stage: "collect[mode=0]", BetMode: 0, State: "started"})
	reporter.Report(optimizerv2.StageEvent{
		Stage: "collection-progress", BetMode: 0, State: "progress", Spins: 100,
		Accepted: 50, Requested: 300,
		Classes: []optimizerv2.ClassCollectionProgress{
			{Name: "zero", Accepted: 20, Requested: 100},
			{Name: "bg_min", Accepted: 30, Requested: 200},
		},
	})
	completedSnapshot := optimizerv2.StageEvent{
		Stage: "collection-progress", BetMode: 0, State: "progress", Spins: 500,
		Accepted: 300, Requested: 300,
		Classes: []optimizerv2.ClassCollectionProgress{
			{Name: "zero", Accepted: 100, Requested: 100},
			{Name: "bg_min", Accepted: 200, Requested: 200},
		},
	}
	reporter.Report(completedSnapshot)
	// A completed Class remains in every later aggregate snapshot. Replaying the
	// same snapshot must not append another 100% line to redirected logs.
	reporter.Report(completedSnapshot)
	reporter.Report(optimizerv2.StageEvent{
		Stage: "collect[mode=0]", BetMode: 0, State: "completed", Duration: 1500 * time.Millisecond,
	})

	got := output.String()
	for _, want := range []string{
		"[03/08] Collecting samples (mode 0)",
		"Collecting class zero",
		"20/100",
		"Collecting class bg_min",
		"30/200",
		"success (1.5s)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress output = %q, want substring %q", got, want)
		}
	}
	if strings.Index(got, "class zero") > strings.Index(got, "class bg_min") {
		t.Fatalf("progress output changed Class declaration order: %q", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("non-interactive progress output contains ANSI cursor controls: %q", got)
	}
	if count := strings.Count(got, "100/100"); count != 1 {
		t.Fatalf("completed zero Class was logged %d times, want once: %q", count, got)
	}
	if count := strings.Count(got, "200/200"); count != 1 {
		t.Fatalf("completed bg_min Class was logged %d times, want once: %q", count, got)
	}
}

// TestCLIProgressReporterPrintsDerivedExpectedRTP locks the requested static
// information line without reintroducing overall.mean as authored config.
func TestCLIProgressReporterPrintsDerivedExpectedRTP(t *testing.T) {
	var output bytes.Buffer
	reporter := newCLIProgressReporter(&output)
	reporter.Report(optimizerv2.StageEvent{Stage: "expected-rtp", State: "info", BetMode: -1, ExpectedRTP: 0.947932})
	if got, want := output.String(), "[Info] expected RTP: 0.947932\n"; got != want {
		t.Fatalf("expected RTP output = %q, want %q", got, want)
	}
}

func TestCLIProgressReporterNumbersTopLevelStageAndKeepsHeader(t *testing.T) {
	var output bytes.Buffer
	reporter := newCLIProgressReporter(&output)
	reporter.interactive = true
	reporter.Report(optimizerv2.StageEvent{
		Stage: "load-config", State: "started", BetMode: -1, Message: `embedded "opt_cfg.yaml"`,
	})
	reporter.Report(optimizerv2.StageEvent{
		Stage: "load-config", State: "completed", BetMode: -1, Duration: 2 * time.Millisecond,
	})

	// The numbered header is its own permanent line; completion is only the bare
	// "success (<dur>)" line beneath it, never a relabelled "[Success] <stage>".
	if got, want := output.String(), "[01/08] Loading config: embedded \"opt_cfg.yaml\"\nsuccess (2ms)\n"; got != want {
		t.Fatalf("top-level stage output=%q, want %q", got, want)
	}
	if strings.Contains(output.String(), "[Success]") {
		t.Fatalf("top-level completion was relabelled: %q", output.String())
	}

	stages := []struct {
		stage string
		want  int
	}{
		{"load-config", 1},
		{"static-validation", 2},
		{"collect[mode=0]", 3},
		{"prepare[mode=0]", 4},
		{"compile[mode=0]", 5},
		{"solve[mode=0]", 6},
		{"materialize-verify[mode=0]", 7},
		{"publish", 8},
	}
	for _, test := range stages {
		if got, ok := cliPipelineStep(test.stage); !ok || got != test.want {
			t.Fatalf("cliPipelineStep(%q)=(%d, %t), want (%d, true)", test.stage, got, ok, test.want)
		}
	}
}

func TestCLIProgressReporterUsesSingleLineOptimizationCompletion(t *testing.T) {
	var output bytes.Buffer
	reporter := newCLIProgressReporter(&output)
	reporter.interactive = true
	reporter.Report(optimizerv2.StageEvent{
		Stage: "solve[mode=0]", Substage: optimizerv2.StageMaximizeMainGroupInternalVisibility,
		BetMode: 0, State: "started", Objective: string(optimizerv2.StageMaximizeMainGroupInternalVisibility),
		Metric: "main_group_internal_visibility_rho",
	})
	for probe := 1; probe <= 60; probe++ {
		lower, upper := 0.61, 0.63
		reporter.Report(optimizerv2.StageEvent{
			Stage: "solve[mode=0]", Substage: optimizerv2.StageMaximizeMainGroupInternalVisibility,
			BetMode: 0, State: "progress", Metric: "main_group_internal_visibility_rho",
			Probe: probe, Lower: &lower, Upper: &upper,
		})
	}
	lower, upper, fixed := 0.61, 0.63, 0.60999999
	reporter.Report(optimizerv2.StageEvent{
		Stage: "solve[mode=0]", Substage: optimizerv2.StageMaximizeMainGroupInternalVisibility,
		BetMode: 0, State: "completed", Metric: "main_group_internal_visibility_rho",
		Probe: 62, Lower: &lower, Upper: &upper, FixedValue: &fixed,
	})
	got := output.String()
	if want := "  step 1 Maximizing minimum relative visibility within Main Group buckets (mode 0) ... success (0s)\n"; !strings.Contains(got, want) {
		t.Fatalf("semantic progress=%q want sub-step completion line %q", got, want)
	}
	for _, unwanted := range []string{"metric:", "bracket:", "locked:", "probes:", "status:", "probe=", "[Progress]", "[Success]"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("single-line optimization completion contains %q: %q", unwanted, got)
		}
	}

	output.Reset()
	reporter.Report(optimizerv2.StageEvent{
		Stage: "solve[mode=0]", Substage: optimizerv2.StageMaximizeOtherBucketVisibility,
		BetMode: 0, State: "skipped", Metric: "other_bucket_visibility_rho",
		Message: "no-supported-other-buckets",
	})
	if got := output.String(); !strings.Contains(got, "Maximizing minimum relative visibility of Other buckets (mode 0) ... skipped") || strings.Contains(got, "failed") || strings.Contains(got, "status:") {
		t.Fatalf("skipped substage output=%q", got)
	}
}

// TestReportV2OutcomeKeepsSuccessTerminalClean verifies a successful run prints
// only the one-line result and never the verified distribution, which is a file
// artifact written by writeModeDistributionCSVs instead.
func TestReportV2OutcomeKeepsSuccessTerminalClean(t *testing.T) {
	var output bytes.Buffer
	reportV2Outcome(&output, optimizerv2.RunResult{
		Status: optimizerv2.StatusOptimal,
		Report: optimizerv2.RunReport{Modes: []optimizerv2.ModeRunReport{{
			Distribution: optimizerv2.BucketDistributionReport{
				BetMode: 0, CollisionProbability: 0.30,
				Classes: []optimizerv2.ClassDistributionReport{{
					Class: "bg_min", Probability: 0.1,
					Buckets: []optimizerv2.BucketProbabilityReport{{Lower: 0, Upper: 1, SeedCount: 2}},
				}},
			},
		}}},
	})
	if got := output.String(); got != "[Result] Generation succeeded\n" {
		t.Fatalf("success outcome = %q, want only the one-line result", got)
	}
}

func TestReportV2OutcomePrintsStructuredCollectionAdvisories(t *testing.T) {
	var output bytes.Buffer
	reportV2Outcome(&output, optimizerv2.RunResult{
		Status: optimizerv2.StatusOptimal,
		Report: optimizerv2.RunReport{Advisories: []optimizerv2.Advisory{{
			Code:        optimizerv2.AdvisoryClassCollectionOverlap,
			Message:     `classes "a" and "b" overlap under the same tag predicate`,
			SourcePaths: []string{"intents.demo.classes[0].collect", "intents.demo.classes[1].collect"},
		}}},
	})
	got := output.String()
	for _, want := range []string{
		"[Advisory/ClassCollectionOverlap]",
		`classes "a" and "b" overlap`,
		"config location: intents.demo.classes[0].collect, intents.demo.classes[1].collect",
		"[Result] Generation succeeded",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("advisory output=%q, want substring %q", got, want)
		}
	}
}

// TestWriteModeDistributionCSVsWritesVerifiedMarginals locks the file artifact:
// one CSV per generated mode beneath the output directory, carrying the actual
// runtime conditional and unconditional probabilities plus the collision data.
func TestWriteModeDistributionCSVsWritesVerifiedMarginals(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "optimizer")
	paths, err := writeModeDistributionCSVs(directory, []optimizerv2.ModeRunReport{{
		Distribution: optimizerv2.BucketDistributionReport{
			BetMode: 0, CollisionProbability: 0.30,
			Classes: []optimizerv2.ClassDistributionReport{{
				Class: "bg_min", Probability: 0.1,
				Buckets: []optimizerv2.BucketProbabilityReport{{
					Lower: 0, Upper: 1, SeedCount: 2,
					ConditionalProbability: 0.5, UnconditionalProbability: 0.05,
					SeedProbabilityMin: 0.02, SeedProbabilityMax: 0.03,
					DrawsAtCollisionProbability: 24,
				}},
			}},
		},
	}})
	if err != nil {
		t.Fatalf("writeModeDistributionCSVs: %v", err)
	}
	want := filepath.Join(directory, "distribution_mode_0.csv")
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("written paths = %v, want [%s]", paths, want)
	}
	raw, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read distribution csv: %v", err)
	}
	got := string(raw)
	for _, substring := range []string{
		"class,class_global_probability,bucket,",
		"draws_at_collision_probability",
		"bg_min,0.1,\"[0, 1)\",0.5,0.05,2,",
		"2.000000000e-02,3.000000000e-02,0.3,24",
	} {
		if !strings.Contains(got, substring) {
			t.Fatalf("distribution csv = %q, want substring %q", got, substring)
		}
	}
}

// TestCLIProgressReporterExplainsTypedStageFailure verifies that a mathematical
// diagnostic is not presented as a completed gate merely because its Go error
// channel is nil.
func TestCLIProgressReporterExplainsTypedStageFailure(t *testing.T) {
	var output bytes.Buffer
	reporter := newCLIProgressReporter(&output)
	reporter.Report(optimizerv2.StageEvent{Stage: "prepare[mode=0]", BetMode: 0, State: "started"})
	reporter.Report(optimizerv2.StageEvent{
		Stage: "prepare[mode=0]", BetMode: 0, State: "failed",
		Message: `class "bg_min" aggregate supported risk capacity is below 1`,
	})
	got := output.String()
	if !strings.Contains(got, "[04/08] Validating dynamic constraints (mode 0)") || !strings.Contains(got, "failed: ") || !strings.Contains(got, "bg_min") {
		t.Fatalf("failure output = %q, want stage header and actionable diagnostic", got)
	}
}

// TestReportV2OutcomePrintsOnlyFailureReason verifies the command-facing result
// boundary condenses a typed failure to one useful line rather than serializing
// the partial RunReport and all of its zero-value sections.
func TestReportV2OutcomePrintsOnlyFailureReason(t *testing.T) {
	var output bytes.Buffer
	reportV2Outcome(&output, optimizerv2.RunResult{
		Status: optimizerv2.StatusInfeasibleSupport,
		Diagnostics: optimizerv2.Diagnostics{{
			Code: optimizerv2.DiagnosticRiskCapacityInfeasible, Status: optimizerv2.StatusInfeasibleSupport,
			Message: `class "bg_min" risk capacity is below required mass`,
		}},
	})
	got := output.String()
	for _, want := range []string{"[Result] Failed", "INFEASIBLE_SUPPORT", "RiskCapacityInfeasible", "bg_min"} {
		if !strings.Contains(got, want) {
			t.Fatalf("failure summary = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "{") || strings.Contains(got, `"report"`) {
		t.Fatalf("failure summary contains JSON: %q", got)
	}
}

// TestReportV2OutcomePrintsEveryLocalizedProblem verifies the terminal report
// does not stop after the first Class and renders the structured adjustment
// bounds needed by Designers and Engineers without falling back to JSON.
func TestReportV2OutcomePrintsEveryLocalizedProblem(t *testing.T) {
	var output bytes.Buffer
	reportV2Outcome(&output, optimizerv2.RunResult{
		Status: optimizerv2.StatusInfeasibleModel,
		Diagnostics: optimizerv2.Diagnostics{
			{
				Code: optimizerv2.DiagnosticClassMeanInfeasible, Status: optimizerv2.StatusInfeasibleModel,
				Message:   `class "fg_02" exact mean 30 is below the minimum achievable 30.8`,
				Requested: &optimizerv2.Bound{Min: 30, Max: 30}, Achievable: &optimizerv2.Bound{Min: 30.8, Max: 40},
				Deficit: 0.8, SourcePaths: []string{"intents.demo.classes[5].design.exp"},
			},
			{
				Code: optimizerv2.DiagnosticMedianInfeasible, Status: optimizerv2.StatusInfeasibleModel,
				Message: `class "fg_03" median upper endpoint 60 requires P(X<=U) >= 0.5`,
				Deficit: 0.18, SourcePaths: []string{"intents.demo.classes[6].design.median[1]"},
			},
		},
	})
	got := output.String()
	for _, want := range []string{
		"2 localized problems found", "fg_02", "fg_03", "required range: 30", "achievable range: [30.8, 40]",
		"minimum required gap: 0.8", "config location: intents.demo.classes[5].design.exp",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("multi-diagnostic output = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "{") || strings.Contains(got, `"diagnostics"`) {
		t.Fatalf("multi-diagnostic output contains JSON: %q", got)
	}
}

// TestReportV2OutcomeDistinguishesStagedModeFromPublishedManifest protects the
// single-mode workflow: generating one mode is success even while publication
// correctly waits for missing siblings, and the last mode reports its manifest.
func TestReportV2OutcomeDistinguishesStagedModeFromPublishedManifest(t *testing.T) {
	tests := []struct {
		name        string
		publication optimizerv2.PublicationReport
		want        []string
	}{
		{
			name: "mode staged",
			publication: optimizerv2.PublicationReport{
				State: optimizerv2.PublicationModeStaged, BetMode: 0, MissingModes: []int{1, 2},
			},
			want: []string{"mode 0 generated and staged", "missing mode [1 2]", "output bundle incomplete"},
		},
		{
			name: "manifest published",
			publication: optimizerv2.PublicationReport{
				State: optimizerv2.PublicationManifestPublished, BetMode: 2, ManifestPath: "build/game/manifest.json",
			},
			want: []string{"mode 2 generated", "all-format output bundles produced", "Artifact v1 manifest", "build/game/manifest.json"},
		},
		{
			name: "gacha published",
			publication: optimizerv2.PublicationReport{
				State: optimizerv2.PublicationOutputsPublished, BetMode: 0,
				Formats: []optimizerv2.OutputFormat{optimizerv2.OutputOptimalGacha},
			},
			want: []string{"mode 0 generated", "all-format output bundles produced"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			reportV2Outcome(&output, optimizerv2.RunResult{
				Status: optimizerv2.StatusOptimal,
				Report: optimizerv2.RunReport{Publication: &test.publication},
			})
			for _, want := range test.want {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("success summary = %q, want substring %q", output.String(), want)
				}
			}
		})
	}
}
