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

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

const entryGame = spec.GID(3) // demo_entry

// violatingLogic wraps demo_entry and breaks the entry contract in one way.
type violatingLogic struct {
	inner   slot.GameLogic
	violate func(r *buf.SpinRequest, gh *slot.Game, sr *buf.SpinResult)
}

func (l *violatingLogic) GetResult(r *buf.SpinRequest, gh *slot.Game) *buf.SpinResult {
	sr := l.inner.GetResult(r, gh)
	if sr != nil && r.Cycle > 0 {
		l.violate(r, gh, sr)
	}
	return sr
}

// reusingFactory wraps the default factory. Its PRNGs return one reused buffer
// from Snapshot, which the Restorable contract allows, so a caller that keeps
// the returned slice instead of a copy sees it change under it.
type reusingFactory struct{ core.PRNGFactory }

func (f reusingFactory) New(seed []byte) (core.PRNG, error) {
	rng, err := f.PRNGFactory.New(seed)
	if err != nil {
		return nil, err
	}
	return &reusingPRNG{PRNG: rng}, nil
}

type reusingPRNG struct {
	core.PRNG
	buf []byte
}

func (p *reusingPRNG) SnapshotFormat() string {
	if f, ok := p.PRNG.(core.SnapshotFormatter); ok {
		return f.SnapshotFormat()
	}
	return ""
}

func (p *reusingPRNG) Snapshot() ([]byte, error) {
	snap, err := p.PRNG.Snapshot()
	if err != nil {
		return nil, err
	}
	p.buf = append(p.buf[:0], snap...)
	return p.buf, nil
}

// entryLab builds the demo catalog on PRNGs that reuse their snapshot buffer,
// with demo_entry optionally replaced by a contract violation.
func entryLab(t *testing.T, violate func(r *buf.SpinRequest, gh *slot.Game, sr *buf.SpinResult)) *problab.Problab {
	t.Helper()
	reg := slot.NewLogicRegistry()
	for _, key := range []string{"demo_normal", "demo_cascade", "demo_choice", "demo_entry"} {
		key := spec.LogicKey(key)
		if err := reg.Register(key, func(g *slot.Game) (slot.GameLogic, error) {
			inner, err := demo_logic.Logics.Build(key, g)
			if err != nil || violate == nil || key != "demo_entry" {
				return inner, err
			}
			return &violatingLogic{inner: inner, violate: violate}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	lab, err := problab.NewAuto(reusingFactory{core.Default()}, problab.Configs(demo_configs.FS), problab.Logics(reg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

// runEntryPipeline collects, optimizes, verifies and publishes the gacha of
// entry 7 (reached as position 1 of [3, 7]).
func runEntryPipeline(t *testing.T, lab *problab.Problab) RunResult {
	t.Helper()
	const samples uint64 = 128
	choice := 1
	target := Target{Game: entryGame, BetModes: []int{0}, Input: &TargetInput{ChooseNext: []int{3, 7}, Choice: &choice}}
	collection := CollectionOptions{Workers: 2, BatchSize: 64, MaxSpins: samples}
	fixture := collectionFixturePlan(0, collection.Workers, samples, collection.MaxSpins, collection.BatchSize)
	fixture.Plan.Target = target
	collected, diagnostics, err := NewCollector(lab).Collect(context.Background(), fixture, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("collect entry segments: %v %+v", err, diagnostics)
	}
	config := exactPipelineConfig(t, collected, samples, collection, Int64Seed(0), "entry-pipeline", target)
	tuner, err := NewTuner(config, lab)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tuner.Run(context.Background(), RunRequest{PlanID: "entry-pipeline"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func entryContractFailure(t *testing.T, result RunResult) string {
	t.Helper()
	if result.Succeeded() || result.Report.Publication != nil {
		t.Fatalf("a contract violation was published: status %s", result.Status)
	}
	for _, check := range result.Report.Verification.Checks {
		if strings.HasSuffix(check.Name, entryContractCheck) && !check.Pass {
			return check.Actual
		}
	}
	t.Fatalf("no failing %s check: %+v", entryContractCheck, result.Report.Verification.Checks)
	return ""
}

// demo_entry keeps the contract: the entry gacha is collected from entry
// segments, passes the contrasts and is published with its entry value.
func TestEntryPipelinePublishesEntryGacha(t *testing.T) {
	result := runEntryPipeline(t, entryLab(t, nil))
	if !result.Succeeded() || result.Report.Publication == nil {
		t.Fatalf("status %s diagnostics %+v", result.Status, result.Diagnostics)
	}
	modes := result.Report.Modes
	if len(modes) != 1 || modes[0].Entry == nil || *modes[0].Entry != 7 {
		t.Fatalf("mode reports %+v", modes)
	}
	passed := false
	for _, check := range result.Report.Verification.Checks {
		passed = passed || (strings.HasSuffix(check.Name, entryContractCheck) && check.Pass)
	}
	if !passed {
		t.Fatalf("no passing %s check: %+v", entryContractCheck, result.Report.Verification.Checks)
	}
	if result.Report.Plan.Plan.Target.Input == nil {
		t.Fatal("the report lost target.input")
	}
}

func TestEntryPipelineRejectsContractViolations(t *testing.T) {
	for name, c := range map[string]struct {
		violate func(r *buf.SpinRequest, gh *slot.Game, sr *buf.SpinResult)
		want    string
	}{
		"reads the cycle": {
			violate: func(r *buf.SpinRequest, _ *slot.Game, sr *buf.SpinResult) {
				if r.Cycle > 1 {
					sr.TotalWin++
				}
			},
			want: "outcome differs with cycle 2",
		},
		"reads the other options": {
			violate: func(r *buf.SpinRequest, _ *slot.Game, sr *buf.SpinResult) {
				if len(r.ChooseNext) == 1 {
					sr.TotalWin++
				}
			},
			want: "outcome differs with single option",
		},
		// Only the RNG differs. A check that kept the borrowed snapshot slice
		// instead of a copy would compare the buffer with itself and miss it.
		"consumes the RNG by cycle": {
			violate: func(r *buf.SpinRequest, gh *slot.Game, _ *buf.SpinResult) {
				if r.Cycle > 1 {
					gh.Core.Pick([]int{1, 2, 3})
				}
			},
			want: "consumes the RNG differently with cycle 2",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := entryContractFailure(t, runEntryPipeline(t, entryLab(t, c.violate))); !strings.Contains(got, c.want) {
				t.Fatalf("failure %q, want %q", got, c.want)
			}
		})
	}
}
