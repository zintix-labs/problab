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
	"strings"
	"testing"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/spec"
)

// TestReplayMaterializedSnapshotsUsesRawGameOutcome proves publication checks
// do not trust the Win annotation captured during collection. The exact same
// pre-spin Core snapshot must reproduce the same normalized payout through the
// real raw Machine; changing only the annotation must fail verification.
func TestReplayMaterializedSnapshotsUsesRawGameOutcome(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatalf("construct demo Problab: %v", err)
	}
	defer func() { _ = lab.Close() }()

	const seed int64 = 4127483647
	machine, err := lab.NewUnoptimizedMachineWithSeed(spec.GID(1), seed, true)
	if err != nil {
		t.Fatalf("construct raw fixture machine: %v", err)
	}
	snapshot, err := machine.SnapshotCore()
	if err != nil {
		t.Fatalf("snapshot raw fixture machine: %v", err)
	}
	result := machine.SpinInternal(0)
	if result == nil || result.Bet <= 0 {
		t.Fatalf("fixture spin is invalid: %+v", result)
	}
	win := float64(result.TotalWin) / float64(result.Bet)

	compiled := runtimeReplayCompiledFixture(seed, result.Bet)
	mode, err := MaterializeMode(0, result.Bet, []MaterializedSample{{
		ClassID: "all_outcomes", BucketIndex: 0, Win: win,
		Snapshot: snapshot, Probability: 1,
	}})
	if err != nil {
		t.Fatalf("materialize fixture: %v", err)
	}
	replayed, check, err := replayMaterializedSnapshots(context.Background(), lab, compiled, mode)
	if err != nil {
		t.Fatalf("replay valid snapshot: %v", err)
	}
	if !check.Pass || len(replayed.Samples) != 1 || replayed.Samples[0].Win != win {
		t.Fatalf("valid runtime replay = %+v, samples=%+v", check, replayed.Samples)
	}

	drifted := cloneMaterializedMode(mode)
	drifted.Samples[0].Win++
	_, check, err = replayMaterializedSnapshots(context.Background(), lab, compiled, drifted)
	if err != nil {
		t.Fatalf("replay drifted annotation: %v", err)
	}
	if check.Pass || !strings.Contains(check.Actual, "differs from modeled payout") {
		t.Fatalf("drift verification = %+v, want modeled-payout failure", check)
	}
}

// runtimeReplayCompiledFixture supplies only the immutable ownership metadata
// needed by raw snapshot replay. It deliberately avoids LP rows so this test
// isolates the collection-to-runtime identity contract from solver behavior.
func runtimeReplayCompiledFixture(seed int64, betUnit int) CompiledModel {
	disabled := false
	class := ClassIntent{
		Name: "all_outcomes",
		Collect: CollectIntent{
			WinRange: ClosedInterval{0, 20000},
		},
		Design: ClassDesign{Subjective: SubjectiveIntent{Intent: &disabled}},
	}
	plan := ResolvedPlan{
		Plan: RunPlan{
			Target: Target{Game: spec.GID(1), BetModes: []int{0}},
			Seed:   seed,
		},
		Intent: MathIntent{Classes: []ClassIntent{class}},
	}
	return CompiledModel{Prepared: PreparedProblem{
		Plan: plan, Game: spec.GID(1), BetMode: 0, BetUnit: betUnit,
		Classes: []PreparedClass{{
			ID: "all_outcomes", Index: 0, Probability: 1, Intent: false,
			Buckets: []PreparedBucket{{Index: 0}},
		}},
	}}
}
