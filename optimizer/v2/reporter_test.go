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
	"reflect"
	"testing"
	"time"
)

type recordingReporter struct {
	events []StageEvent
}

func TestSolveStageBridgesOrderedSemanticSubstagesAndKeepsParentDuration(t *testing.T) {
	solver := &scriptedSolver{t: t, steps: []solverStep{
		{origin: ObjectiveHardFeasibility, status: SolveOptimal},
		{origin: ObjectiveMainProfileProbe, status: SolveOptimal},
		{origin: ObjectiveMainProfileProbe, status: SolveOptimal},
		{origin: ObjectiveIntentRefinement, status: SolveOptimal},
		{origin: ObjectiveCanonicalBucketProbability, status: SolveOptimal},
	}}
	recorder := &recordingReporter{}
	tuner := &Tuner{engine: NewIntentEngine(solver), reporter: recorder}
	report := RunReport{}
	solution, err := tuner.solveStage(context.Background(), engineTestModel(false, 0, 0), 3, &report)
	if err != nil {
		t.Fatalf("solveStage: %v", err)
	}
	if solution.Status != StatusOptimal {
		t.Fatalf("solution=%+v", solution)
	}
	solver.assertConsumed()
	if len(report.Stages) != 1 || report.Stages[0].Stage != "solve[mode=3]" {
		t.Fatalf("parent stages=%+v", report.Stages)
	}
	if len(report.OptimizationStages) != 5 {
		t.Fatalf("optimization stages=%+v", report.OptimizationStages)
	}
	want := []OptimizationStageID{
		StageProveHardFeasibility,
		StageMinimizeMainProfileDeviation,
		StageMaximizeOtherBucketVisibility,
		StageMaximizeMainGroupInternalVisibility,
		StageSelectCanonicalBucketProbabilities,
	}
	got := make([]OptimizationStageID, len(report.OptimizationStages))
	for i, stage := range report.OptimizationStages {
		got[i] = stage.Stage
		if stage.Parent != "solve[mode=3]" || stage.BetMode != 3 {
			t.Fatalf("optimization stage[%d]=%+v", i, stage)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("optimization stage order=%v want=%v", got, want)
	}
	if report.OptimizationStages[2].State != "skipped" || report.OptimizationStages[3].State != "skipped" {
		t.Fatalf("inapplicable visibility states=%+v", report.OptimizationStages)
	}
	if len(recorder.events) < 2 || recorder.events[0].Substage != "" || recorder.events[0].State != "started" || recorder.events[len(recorder.events)-1].Substage != "" || recorder.events[len(recorder.events)-1].State != "completed" {
		t.Fatalf("parent solve events were not preserved: %+v", recorder.events)
	}
}

func TestRunReportUsesMainGroupVisibilityEngineVersion(t *testing.T) {
	report := newRunReport(ResolvedPlan{}, RunOverrides{})
	if report.Engine.Version != "intent-lp-v2.3.1" {
		t.Fatalf("engine version=%q", report.Engine.Version)
	}
	if !reflect.DeepEqual(report.Engine.SemanticAxioms, []string{MainSemanticAxiomVersion}) {
		t.Fatalf("semantic axioms=%v", report.Engine.SemanticAxioms)
	}
	if report.StableOrderingVersion != "class-declaration/worker-index/sample-acceptance-v2" {
		t.Fatalf("stable ordering version=%q", report.StableOrderingVersion)
	}
	provenance := solverProvenance(SolverEvidence{Objective: string(StageSelectCanonicalBucketProbabilities)})
	if provenance.Objective != string(StageSelectCanonicalBucketProbabilities) {
		t.Fatalf("solver provenance objective=%q", provenance.Objective)
	}
}

func (r *recordingReporter) Report(event StageEvent) {
	r.events = append(r.events, event)
}

// TestFinishStageTreatsStoppingDiagnosticAsFailure guards the distinction
// between expected typed infeasibility and Go errors. Both stop the pipeline,
// and both must be visible as failed operational stages to a Reporter.
func TestFinishStageTreatsStoppingDiagnosticAsFailure(t *testing.T) {
	recorder := &recordingReporter{}
	tuner := &Tuner{reporter: recorder}
	report := RunReport{}
	diagnostic := supportDiagnostic(
		DiagnosticRiskCapacityInfeasible,
		`class "bg_min" risk capacity is below required mass`,
		"intents.test.classes[1].design.risk",
	)

	tuner.finishStage(&report, "prepare[mode=0]", 0, time.Now().Add(-time.Millisecond), nil, Diagnostics{diagnostic})

	if len(recorder.events) != 1 {
		t.Fatalf("reported events = %d, want 1", len(recorder.events))
	}
	event := recorder.events[0]
	if event.State != "failed" || event.Stage != "prepare[mode=0]" || event.Message != diagnostic.Message {
		t.Fatalf("stage event = %+v, want typed diagnostic failure", event)
	}
	if len(report.Stages) != 1 || report.Stages[0].Stage != event.Stage || event.Duration <= 0 {
		t.Fatalf("stage duration/report mismatch: event=%+v report=%+v", event, report.Stages)
	}
}

// TestCollectionProgressEventCopiesDeclarationOrderedQuotas verifies the
// Collector-to-UI boundary does not expose sample slices and does not sort Class
// names by completion percentage or alphabetically.
func TestCollectionProgressEventCopiesDeclarationOrderedQuotas(t *testing.T) {
	collected := CollectedProblem{
		BetMode: 2,
		Spins:   17,
		Classes: []CollectedClass{
			{Intent: ClassIntent{Name: "second-name", Collect: CollectIntent{Samples: 2}}, Samples: []CollectedSample{{}}},
			{Intent: ClassIntent{Name: "first-name", Collect: CollectIntent{Samples: 3}}, Samples: []CollectedSample{{}, {}}},
		},
	}
	event := collectionProgressEvent(collected, 5, 2)

	if event.BetMode != 2 || event.Spins != 17 || event.Accepted != 3 || event.Requested != 5 {
		t.Fatalf("aggregate progress event = %+v", event)
	}
	if len(event.Classes) != 2 || event.Classes[0].Name != "second-name" || event.Classes[1].Name != "first-name" {
		t.Fatalf("Class progress order = %+v, want declaration order", event.Classes)
	}
	if event.Classes[0].Accepted != 1 || event.Classes[0].Requested != 2 ||
		event.Classes[1].Accepted != 2 || event.Classes[1].Requested != 3 {
		t.Fatalf("Class quota progress = %+v", event.Classes)
	}
}
