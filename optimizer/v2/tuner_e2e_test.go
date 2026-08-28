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
	"math"
	"testing"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/spec"
)

// TestTunerRunExecutesTheCompleteProductionPipeline is the lightweight seam
// test intentionally missing from the focused stage suite. It derives an exact
// feasible intent from one deterministic raw collection, then requires a fresh
// Tuner.Run with the same stream to collect, prepare, compile, solve every
// semantic substage, materialize, runtime-replay, and publish Artifact v1.
func TestTunerRunExecutesTheCompleteProductionPipeline(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatalf("construct demo Problab: %v", err)
	}
	defer func() { _ = lab.Close() }()

	const (
		seed    int64  = 4127483647
		samples uint64 = 256
	)
	collection := CollectionOptions{Workers: 2, BatchSize: 64, MaxSpins: samples}
	fixture := collectionFixturePlan(seed, collection.Workers, samples, collection.MaxSpins, collection.BatchSize)
	collected, diagnostics, err := NewCollector(lab).Collect(context.Background(), fixture, 0)
	if err != nil {
		t.Fatalf("derive deterministic fixture support: %v", err)
	}
	if diagnostics.StopsRun() || len(collected.Classes) != 1 || len(collected.Classes[0].Samples) != int(samples) {
		t.Fatalf("fixture collection=%+v diagnostics=%+v", collected, diagnostics)
	}

	minimum, maximum := math.Inf(1), math.Inf(-1)
	mean, secondMoment := 0.0, 0.0
	for _, sample := range collected.Classes[0].Samples {
		minimum = math.Min(minimum, sample.Win)
		maximum = math.Max(maximum, sample.Win)
		mean += sample.Win
		secondMoment += sample.Win * sample.Win
	}
	mean /= float64(samples)
	secondMoment /= float64(samples)
	if !(minimum < maximum) || !(mean > 0) {
		t.Fatalf("demo fixture needs nondegenerate positive support, got min=%g max=%g mean=%g", minimum, maximum, mean)
	}
	midpoint := minimum + (maximum-minimum)/2
	variance := math.Max(0, secondMoment-mean*mean)
	empiricalCV := math.Sqrt(variance) / mean
	enabled := true
	intent := MathIntent{
		Overall: OverallIntent{CV: NumericRange{Min: 0, Max: empiricalCV + 1}},
		Classes: []ClassIntent{{
			Name: "all_outcomes", Weight: ClassWeightBase,
			Collect: CollectIntent{Samples: samples, WinRange: ClosedInterval{minimum, maximum}},
			Design: ClassDesign{
				Exp: mean, Median: ClosedInterval{minimum, maximum},
				Subjective: SubjectiveIntent{
					Intent:  &enabled,
					Buckets: []float64{minimum, midpoint, maximum},
					MainExperience: &MainExperience{
						Groups:      []ClosedInterval{{minimum, maximum}},
						Probability: NumericRange{Min: 1, Max: 1},
						Prefer:      []float64{1},
					},
				},
			},
		}},
	}
	options := DefaultEngineOptions()
	options.ProfileBisectionIterations = 12
	options.OtherVisibilityBisectionIterations = 12
	options.MainGroupInternalVisibilityBisectionIterations = 12
	config := Config{
		Version: ConfigVersion,
		Plans: []RunPlan{{
			ID: "complete-pipeline", Target: Target{Game: spec.GID(1), BetModes: []int{0}},
			Engine: EngineIntentLPV2, Intent: "complete-pipeline", Seed: seed,
			Collection:         collection,
			CandidateSelection: CandidateSelectionOptions{Evaluator: "none", MaxCandidates: 1},
			Output: OutputOptions{
				Format: []OutputFormat{OutputOptimalArtifactV1}, Directory: t.TempDir(),
			},
		}},
		Intents:       map[string]MathIntent{"complete-pipeline": intent},
		EngineOptions: options,
	}

	tuner, err := NewTuner(config, lab)
	if err != nil {
		t.Fatalf("construct Tuner: %v", err)
	}
	result, err := tuner.Run(context.Background(), RunRequest{PlanID: "complete-pipeline"})
	if err != nil {
		t.Fatalf("Tuner.Run: %v", err)
	}
	if !result.Succeeded() || result.Status != StatusOptimal {
		t.Fatalf("complete run status=%s diagnostics=%+v", result.Status, result.Diagnostics)
	}
	if len(result.Report.Modes) != 1 || !result.Report.Modes[0].Verification.Pass || !result.Report.Verification.Pass {
		t.Fatalf("complete run verification=%+v modes=%+v", result.Report.Verification, result.Report.Modes)
	}
	if result.Report.Publication == nil || result.Report.Publication.State != PublicationManifestPublished {
		t.Fatalf("complete run publication=%+v", result.Report.Publication)
	}
	if len(result.ArtifactPaths) == 0 || result.Report.ModelHash == "" || result.Report.SolutionHash == "" || result.Report.ArtifactHash == "" {
		t.Fatalf("complete run did not preserve artifact/hash evidence: paths=%v report=%+v", result.ArtifactPaths, result.Report)
	}
	wantStages := []OptimizationStageID{
		StageProveHardFeasibility,
		StageMinimizeMainProfileDeviation,
		StageMaximizeOtherBucketVisibility,
		StageMaximizeMainGroupInternalVisibility,
		StageSelectCanonicalBucketProbabilities,
	}
	if len(result.Report.OptimizationStages) != len(wantStages) {
		t.Fatalf("optimization stages=%+v", result.Report.OptimizationStages)
	}
	for index, want := range wantStages {
		if got := result.Report.OptimizationStages[index].Stage; got != want {
			t.Fatalf("optimization stage[%d]=%q want=%q", index, got, want)
		}
	}
}
