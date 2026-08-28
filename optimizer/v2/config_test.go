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
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab/spec"
)

const validConfigYAML = `
version: 2

plans:
  - id: demo-high-win-v2
    target:
      game: 0
      bet_modes: [0]
    engine: intent_lp_v2
    intent: high-win-v2
    seed: 4127483647
    collection:
      workers: 1
      batch_size: 10000
      max_spins: 1000000000
    candidate_selection:
      evaluator: none
      max_candidates: 1
    output:
      format: [optimal_artifact_v1]
      directory: build/optimizer

intents:
  high-win-v2:
    overall:
      cv: {min: 18.0, max: 21.0}
    classes:
      - name: zero
        weight: 997000
        collect:
          samples: 100000
          win_range: [0.0, 0.0]
          tags:
            matches: [bg]
            mismatches: []
        design:
          exp: 0.0
          median: [0.0, 0.0]
          subjective:
            intent: false

      - name: high_win
        weight: 3000
        collect:
          samples: 50000
          win_range: [200.0, 500.0]
          tags:
            matches: [fg]
            mismatches: [bg]
        design:
          exp: 320.0
          median: [220.0, 250.0]
          subjective:
            intent: true
            buckets: [200, 230, 260, 290, 320, 350, 380, 410, 440, 470, 500]
            main_experience:
              groups:
                - [230, 260]
                - [290, 320]
                - [380, 440]
              probability: {min: 0.60, max: 0.95}
              prefer: [7, 2, 1]
          risk:
            rounds: 100000
            collision:
              max: 0.30

engine_options:
  feasibility_tolerance: 1.0e-9
  optimality_tolerance: 1.0e-9
  quantile_epsilon: 1.0e-9
  profile_tolerance: 1.0e-8
  visibility_tolerance: 1.0e-8
  profile_bisection_iterations: 60
  other_visibility_bisection_iterations: 60
  main_group_internal_visibility_bisection_iterations: 60
`

// TestLoadConfigStrictAndResolve verifies the happy-path public loader, stable
// routing types, and the no-alias guarantee of a resolved plan.
func TestLoadConfigStrictAndResolve(t *testing.T) {
	config, err := ParseConfig([]byte(validConfigYAML))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	if config.Version != ConfigVersion {
		t.Fatalf("Version = %d, want %d", config.Version, ConfigVersion)
	}
	if config.Plans[0].Engine != EngineIntentLPV2 {
		t.Fatalf("Engine = %q, want %q", config.Plans[0].Engine, EngineIntentLPV2)
	}
	if !slices.Equal(config.Plans[0].Output.Format, []OutputFormat{OutputOptimalArtifactV1}) {
		t.Fatalf("output formats=%v", config.Plans[0].Output.Format)
	}
	if got := config.EngineOptions.MainGroupInternalVisibilityBisectionIterations; got != 60 {
		t.Fatalf("MainGroupInternalVisibilityBisectionIterations=%d want=60", got)
	}

	resolved, err := config.ResolvePlan("demo-high-win-v2")
	if err != nil {
		t.Fatalf("ResolvePlan() error = %v", err)
	}
	if got := resolved.Intent.Classes[1].Design.Subjective.Enabled(); !got {
		t.Fatal("resolved high_win Subjective.Enabled() = false, want true")
	}
	if got := resolved.Intent.ExpectedRTP(); got != 0.96 {
		t.Fatalf("resolved expected RTP = %.17g, want 0.96", got)
	}

	// Mutate every reference-like source shape that ResolvePlan must detach.
	config.Plans[0].Target.BetModes[0] = 99
	config.Plans[0].Output.Format[0] = OutputOptimalGacha
	intent := config.Intents["high-win-v2"]
	intent.Classes[1].Collect.Tags.Matches[0] = "changed"
	intent.Classes[1].Design.Subjective.Buckets[0] = -1
	*intent.Classes[1].Design.Subjective.Intent = false
	intent.Classes[1].Design.Subjective.MainExperience.Groups[0][0] = -1
	intent.Classes[1].Design.Subjective.MainExperience.Prefer[0] = -1
	intent.Classes[1].Design.Risk.Rounds = 2
	config.Intents["high-win-v2"] = intent
	config.EngineOptions.MainGroupInternalVisibilityBisectionIterations = 1

	if resolved.Plan.Target.BetModes[0] != 0 {
		t.Fatalf("resolved bet mode aliased Config: got %d", resolved.Plan.Target.BetModes[0])
	}
	if !slices.Equal(resolved.Plan.Output.Format, []OutputFormat{OutputOptimalArtifactV1}) {
		t.Fatalf("resolved output formats aliased Config: %v", resolved.Plan.Output.Format)
	}
	if resolved.EngineOptions.MainGroupInternalVisibilityBisectionIterations != 60 {
		t.Fatalf("resolved Main Group visibility iterations aliased Config: %d", resolved.EngineOptions.MainGroupInternalVisibilityBisectionIterations)
	}
	high := resolved.Intent.Classes[1]
	if high.Collect.Tags.Matches[0] != "fg" || high.Design.Subjective.Buckets[0] != 200 ||
		!high.Design.Subjective.Enabled() || high.Design.Subjective.MainExperience.Groups[0][0] != 230 ||
		high.Design.Subjective.MainExperience.Prefer[0] != 7 || high.Design.Risk.Rounds != 100000 {
		t.Fatalf("resolved intent aliases source Config: %+v", high)
	}
}

func TestLoadConfigAcceptsParallelCollectionWorkers(t *testing.T) {
	raw := strings.Replace(validConfigYAML, "workers: 1", "workers: 4", 1)
	config, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	if config.Plans[0].Collection.Workers != 4 {
		t.Fatalf("collection workers=%d, want 4", config.Plans[0].Collection.Workers)
	}
}

func TestLoadConfigAcceptsBothOutputFormatsInDeclarationOrder(t *testing.T) {
	raw := strings.Replace(
		validConfigYAML,
		"format: [optimal_artifact_v1]",
		"format: [optimal_gacha, optimal_artifact_v1]",
		1,
	)
	config, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	want := []OutputFormat{OutputOptimalGacha, OutputOptimalArtifactV1}
	if !slices.Equal(config.Plans[0].Output.Format, want) {
		t.Fatalf("output formats=%v, want %v", config.Plans[0].Output.Format, want)
	}
}

func TestLoadConfigRejectsScalarOutputFormat(t *testing.T) {
	raw := strings.Replace(validConfigYAML, "format: [optimal_artifact_v1]", "format: optimal_artifact_v1", 1)
	if _, err := ParseConfig([]byte(raw)); err == nil || !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Fatalf("scalar output format error=%v", err)
	}
}

func TestMainGroupInternalVisibilityBisectionConfigurationIsRequiredAndHashed(t *testing.T) {
	missing := strings.Replace(validConfigYAML, "  main_group_internal_visibility_bisection_iterations: 60\n", "", 1)
	if _, err := ParseConfig([]byte(missing)); err == nil || !strings.Contains(err.Error(), "engine_options.main_group_internal_visibility_bisection_iterations") {
		t.Fatalf("missing option error=%v", err)
	}
	for _, value := range []string{"0", "-1"} {
		raw := strings.Replace(validConfigYAML, "main_group_internal_visibility_bisection_iterations: 60", "main_group_internal_visibility_bisection_iterations: "+value, 1)
		if _, err := ParseConfig([]byte(raw)); err == nil || !strings.Contains(err.Error(), "must be greater than zero") {
			t.Fatalf("value %s error=%v", value, err)
		}
	}
	defaults := DefaultEngineOptions()
	if defaults.MainGroupInternalVisibilityBisectionIterations != DefaultMainGroupInternalVisibilityBisectionIterations {
		t.Fatalf("default Main Group visibility iterations=%d", defaults.MainGroupInternalVisibilityBisectionIterations)
	}
	config, err := ParseConfig([]byte(validConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	base, err := config.ResolvePlan("demo-high-win-v2")
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.EngineOptions.MainGroupInternalVisibilityBisectionIterations++
	baseHash, err := hashCanonicalJSON(base)
	if err != nil {
		t.Fatal(err)
	}
	changedHash, err := hashCanonicalJSON(changed)
	if err != nil {
		t.Fatal(err)
	}
	if baseHash == changedHash {
		t.Fatal("Main Group internal visibility iteration count did not affect canonical config hash")
	}
}

// TestLoadConfigFS verifies the embedded-filesystem entry point used by cmd/opt.
func TestLoadConfigFS(t *testing.T) {
	config, err := LoadConfigFS(fstest.MapFS{
		"opt_cfg.yaml": &fstest.MapFile{Data: []byte(validConfigYAML)},
	}, "opt_cfg.yaml")
	if err != nil {
		t.Fatalf("LoadConfigFS() error = %v", err)
	}
	if len(config.Plans) != 1 {
		t.Fatalf("len(Plans) = %d, want 1", len(config.Plans))
	}
}

// TestLoadConfigRejectsUnknownFields proves KnownFields is effective inside a
// nested structure, where a misspelling would otherwise become workers == 0.
func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	raw := strings.Replace(validConfigYAML, "workers: 1", "workerz: 1", 1)
	_, err := ParseConfig([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "field workerz not found") {
		t.Fatalf("ParseConfig() error = %v, want unknown-field error", err)
	}
}

// TestLoadConfigRejectsRemovedOverallMean proves RTP has one source of truth.
// Strict decoding must not silently accept the retired duplicate field.
func TestLoadConfigRejectsRemovedOverallMean(t *testing.T) {
	raw := strings.Replace(validConfigYAML, "overall:\n      cv:", "overall:\n      mean: 0.96\n      cv:", 1)
	_, err := ParseConfig([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "field mean not found") {
		t.Fatalf("ParseConfig() error = %v, want removed overall.mean rejection", err)
	}
}

// TestLoadConfigRejectsMultipleDocuments ensures a valid first document cannot
// hide a second configuration or patch after a YAML document separator.
func TestLoadConfigRejectsMultipleDocuments(t *testing.T) {
	_, err := ParseConfig([]byte(validConfigYAML + "\n---\nversion: 2\n"))
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("ParseConfig() error = %v, want multiple-document error", err)
	}
}

// TestConfigValidateSemanticInvariants covers the pre-collection contradictions
// that must never reach the collector or LP backend.
func TestConfigValidateSemanticInvariants(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name:   "version",
			mutate: func(config *Config) { config.Version = 1 },
			want:   "version: must be 2",
		},
		{
			name:   "unsupported engine",
			mutate: func(config *Config) { config.Plans[0].Engine = "intent_lp" },
			want:   "engine: must be \"intent_lp_v2\"",
		},
		{
			name:   "unknown intent",
			mutate: func(config *Config) { config.Plans[0].Intent = "missing" },
			want:   "references unknown intent",
		},
		{
			name:   "collection workers",
			mutate: func(config *Config) { config.Plans[0].Collection.Workers = 0 },
			want:   "collection.workers: must be greater than zero",
		},
		{
			name:   "collection budget",
			mutate: func(config *Config) { config.Plans[0].Collection.MaxSpins = 0 },
			want:   "collection.max_spins: must be greater than zero",
		},
		{
			name:   "multiple bet modes",
			mutate: func(config *Config) { config.Plans[0].Target.BetModes = []int{0, 1} },
			want:   "must contain exactly one bet-mode index",
		},
		{
			name:   "blank output",
			mutate: func(config *Config) { config.Plans[0].Output.Directory = "  " },
			want:   "output.directory: must not be blank",
		},
		{
			name:   "missing output format",
			mutate: func(config *Config) { config.Plans[0].Output.Format = nil },
			want:   "output.format: must contain at least one output format",
		},
		{
			name:   "unknown output format",
			mutate: func(config *Config) { config.Plans[0].Output.Format = []OutputFormat{"unknown"} },
			want:   "output.format[0]: must be",
		},
		{
			name: "duplicate output format",
			mutate: func(config *Config) {
				config.Plans[0].Output.Format = []OutputFormat{OutputOptimalGacha, OutputOptimalGacha}
			},
			want: "duplicates output format",
		},
		{
			name:   "invalid tolerance",
			mutate: func(config *Config) { config.EngineOptions.FeasibilityTolerance = 0 },
			want:   "feasibility_tolerance: must be finite and greater than zero",
		},
		{
			name:   "unbounded bisection",
			mutate: func(config *Config) { config.EngineOptions.ProfileBisectionIterations = 0 },
			want:   "profile_bisection_iterations: must be greater than zero",
		},
		{
			name: "weight sum",
			mutate: func(config *Config) {
				updateClass(config, 1, func(class *ClassIntent) { class.Weight = 2999 })
			},
			want: "weights must sum to 1000000",
		},
		{
			name: "tag contradiction",
			mutate: func(config *Config) {
				updateClass(config, 1, func(class *ClassIntent) { class.Collect.Tags.Mismatches = []string{"fg"} })
			},
			want: "also appears in matches",
		},
		{
			name: "median outside collection range",
			mutate: func(config *Config) {
				updateClass(config, 1, func(class *ClassIntent) {
					class.Design.Median = ClosedInterval{199, 250}
				})
			},
			want: "design.median: must lie inside collect.win_range",
		},
		{
			name: "missing intent switch",
			mutate: func(config *Config) {
				updateClass(config, 1, func(class *ClassIntent) { class.Design.Subjective.Intent = nil })
			},
			want: "subjective.intent: is required",
		},
		{
			name: "uniform class with buckets",
			mutate: func(config *Config) {
				updateClass(config, 0, func(class *ClassIntent) { class.Design.Subjective.Buckets = []float64{0, 1, 2} })
			},
			want: "buckets: must be omitted when intent is false",
		},
		{
			name: "descending boundaries",
			mutate: func(config *Config) {
				updateClass(config, 1, func(class *ClassIntent) { class.Design.Subjective.Buckets[2] = 220 })
			},
			want: "must be strictly greater than the previous boundary",
		},
		{
			name: "unaligned group",
			mutate: func(config *Config) {
				updateClass(config, 1, func(class *ClassIntent) {
					class.Design.Subjective.MainExperience.Groups[0] = ClosedInterval{231, 260}
				})
			},
			want: "endpoints must exactly match atomic bucket boundaries",
		},
		{
			name: "overlapping groups",
			mutate: func(config *Config) {
				updateClass(config, 1, func(class *ClassIntent) {
					class.Design.Subjective.MainExperience.Groups[1] = ClosedInterval{230, 320}
				})
			},
			want: "overlaps another Main group",
		},
		{
			name: "preference arity",
			mutate: func(config *Config) {
				updateClass(config, 1, func(class *ClassIntent) {
					class.Design.Subjective.MainExperience.Prefer = []float64{1}
				})
			},
			want: "must equal groups length",
		},
		{
			name: "undefined other visibility",
			mutate: func(config *Config) {
				updateClass(config, 1, func(class *ClassIntent) {
					class.Design.Subjective.MainExperience.Probability.Max = 1
				})
			},
			want: "must be less than 1 when any atomic bucket lies outside Main groups",
		},
		{
			name: "invalid risk",
			mutate: func(config *Config) {
				updateClass(config, 1, func(class *ClassIntent) { class.Design.Risk.Collision.Max = 1 })
			},
			want: "collision.max: must be finite and strictly between 0 and 1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, err := ParseConfig([]byte(validConfigYAML))
			if err != nil {
				t.Fatalf("load baseline: %v", err)
			}
			test.mutate(&config)
			err = config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.want)
			}
			var configError *ConfigError
			if !errors.As(err, &configError) {
				t.Fatalf("Validate() error type = %T, want *ConfigError", err)
			}
		})
	}
}

// TestAnalyzeCollectionTopologySeparatesNonFatalFactsFromHardValidation locks
// the advisory boundary: same-predicate overlaps and gaps are explainable
// collection risks, while ranges under a different predicate are not compared
// because config alone cannot prove the game tags are jointly reachable.
func TestAnalyzeCollectionTopologySeparatesNonFatalFactsFromHardValidation(t *testing.T) {
	classes := []ClassIntent{
		{Name: "first", Collect: CollectIntent{WinRange: ClosedInterval{0, 10}, Tags: TagFilters{Matches: []string{"bg"}}}},
		{Name: "second", Collect: CollectIntent{WinRange: ClosedInterval{10, 20}, Tags: TagFilters{Matches: []string{"bg"}}}},
		{Name: "third", Collect: CollectIntent{WinRange: ClosedInterval{25, 30}, Tags: TagFilters{Matches: []string{"bg"}}}},
		{Name: "different-tag", Collect: CollectIntent{WinRange: ClosedInterval{5, 27}, Tags: TagFilters{Matches: []string{"fg"}}}},
	}
	intent := MathIntent{Classes: classes}
	got := AnalyzeCollectionTopology("topology", intent)
	if len(got) != 2 {
		t.Fatalf("advisories=%+v, want one overlap and one gap", got)
	}
	if got[0].Code != AdvisoryClassCollectionOverlap ||
		!strings.Contains(got[0].Message, `"first" and "second"`) ||
		!reflect.DeepEqual(got[0].SourcePaths, []string{
			"intents.topology.classes[0].collect",
			"intents.topology.classes[1].collect",
		}) {
		t.Fatalf("overlap advisory=%+v", got[0])
	}
	if got[1].Code != AdvisoryClassCollectionGap ||
		!strings.Contains(got[1].Message, "(20, 25)") ||
		!reflect.DeepEqual(got[1].SourcePaths, []string{
			"intents.topology.classes[1].collect.win_range",
			"intents.topology.classes[2].collect.win_range",
		}) {
		t.Fatalf("gap advisory=%+v", got[1])
	}
	if repeated := AnalyzeCollectionTopology("topology", intent); !reflect.DeepEqual(repeated, got) {
		t.Fatalf("topology analysis is nondeterministic: first=%+v repeated=%+v", got, repeated)
	}
}

// TestResolvedPlanWithOverrides verifies invocation-scoped routing changes and
// confirms that applying them does not mutate the reusable resolved plan.
func TestResolvedPlanWithOverrides(t *testing.T) {
	config, err := ParseConfig([]byte(validConfigYAML))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	base, err := config.ResolvePlan("demo-high-win-v2")
	if err != nil {
		t.Fatalf("ResolvePlan() error = %v", err)
	}
	game := spec.GID(7)
	mode := 3
	seed := int64(-42)
	overridden, err := base.WithOverrides(RunOverrides{Game: &game, BetMode: &mode, Seed: &seed})
	if err != nil {
		t.Fatalf("WithOverrides() error = %v", err)
	}
	if overridden.Plan.Target.Game != game || len(overridden.Plan.Target.BetModes) != 1 ||
		overridden.Plan.Target.BetModes[0] != mode || overridden.Plan.Seed != seed {
		t.Fatalf("WithOverrides() = %+v", overridden.Plan)
	}
	if base.Plan.Target.Game != 0 || base.Plan.Target.BetModes[0] != 0 || base.Plan.Seed != 4127483647 {
		t.Fatalf("WithOverrides() mutated base plan: %+v", base.Plan)
	}

	negative := -1
	if _, err := base.WithOverrides(RunOverrides{BetMode: &negative}); err == nil {
		t.Fatal("WithOverrides(negative mode) error = nil, want validation error")
	}
}

// TestStatusAndDiagnosticsHelpers locks the value/error boundary used by future
// Tuner stage gates and command exit-code mapping.
func TestStatusAndDiagnosticsHelpers(t *testing.T) {
	if !StatusOptimal.Valid() || !StatusOptimal.Success() || Status("").Valid() {
		t.Fatal("Status validity/success helpers violate the public status contract")
	}
	diagnostics := Diagnostics{{
		Code:   DiagnosticRiskCapacityInfeasible,
		Status: StatusInfeasibleSupport,
	}}
	if !diagnostics.StopsRun() {
		t.Fatal("Diagnostics.StopsRun() = false, want true")
	}
	if (RunResult{Status: StatusInfeasibleSupport}).Succeeded() {
		t.Fatal("infeasible RunResult.Succeeded() = true, want false")
	}
}

// updateClass safely updates a class inside the map-valued fixture intent and
// writes the value back, avoiding accidental tests against a discarded map copy.
func updateClass(config *Config, index int, update func(*ClassIntent)) {
	intent := config.Intents["high-win-v2"]
	update(&intent.Classes[index])
	config.Intents["high-win-v2"] = intent
}
