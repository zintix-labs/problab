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
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEncodeFloat64RoundTripsEveryBit(t *testing.T) {
	special := []float64{0, math.Copysign(0, -1), 1, -1, math.MaxFloat64, -math.MaxFloat64,
		math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64, 0x1p-1022, 1e-308, 0.1, 1.0 / 3,
		math.Inf(1), math.Inf(-1)}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		value := math.Float64frombits(r.Uint64())
		if !math.IsNaN(value) {
			special = append(special, value)
		}
	}
	for _, value := range special {
		decoded, err := decodeFloat64(encodeFloat64(value))
		if err != nil {
			t.Fatalf("decode %v: %v", value, err)
		}
		if math.Float64bits(decoded) != math.Float64bits(value) {
			t.Fatalf("round trip changed bits: %v -> %v", value, decoded)
		}
	}
	if _, err := decodeFloat64("NaN"); err == nil {
		t.Fatal("NaN must not decode as a model value")
	}
}

func TestProblemSnapshotRoundTripsThroughJSON(t *testing.T) {
	problem := LinearProblem{
		Variables: []LinearVariable{{ID: "a", Lower: math.SmallestNonzeroFloat64, Upper: math.Inf(1)}, {ID: "b", Lower: -0.1, Upper: 1.0 / 3}},
		Rows: []LinearRow{{ID: "r", Family: "f", Origin: OriginDesignerHard, ClassID: "c", YAMLPath: "y",
			Sense: SenseGE, RHS: 1e308, Terms: []LinearTerm{{Variable: "a", Coeff: 1e-300}, {Variable: "b", Coeff: -7}}}},
	}
	objective := LinearObjective{Name: "o", Sense: Maximize, Origin: ObjectiveDiagnostic, Terms: []LinearTerm{{Variable: "b", Coeff: 0.3}}}
	raw, err := json.Marshal(struct {
		P ProblemSnapshot
		O ObjectiveSnapshot
	}{SnapshotProblem(problem), SnapshotObjective(objective)})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		P ProblemSnapshot
		O ObjectiveSnapshot
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	back, err := decoded.P.Problem()
	if err != nil {
		t.Fatal(err)
	}
	if back.Variables[0].Lower != problem.Variables[0].Lower || !math.IsInf(back.Variables[0].Upper, 1) ||
		back.Rows[0].RHS != problem.Rows[0].RHS || back.Rows[0].Terms[0].Coeff != 1e-300 || back.Rows[0].ClassID != "c" {
		t.Fatalf("problem did not round trip: %+v", back)
	}
	objectiveBack, err := decoded.O.Objective()
	if err != nil || objectiveBack.Terms[0].Coeff != 0.3 || objectiveBack.Sense != Maximize {
		t.Fatalf("objective did not round trip: %+v %v", objectiveBack, err)
	}
	first, _ := SnapshotProblem(problem).SHA256()
	second, _ := decoded.P.SHA256()
	if first == "" || first != second {
		t.Fatalf("snapshot hash changed across round trip: %s vs %s", first, second)
	}
}

func TestWriteFileAtomicReportsFailure(t *testing.T) {
	dir := t.TempDir()
	path, err := writeFileAtomic(dir, "x.json", []byte("{}"))
	if err != nil || filepath.Dir(path) != dir {
		t.Fatalf("write: %v %s", err, path)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".evidence-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeFileAtomic(filepath.Join(blocker, "sub"), "x.json", nil); err == nil {
		t.Fatal("expected failure when the directory cannot be created")
	}
	if _, err := writeFileAtomic("", "x.json", nil); err == nil {
		t.Fatal("expected failure without an evidence directory")
	}
}

// sleepySolver blocks for a fixed time and ignores ctx, like a Gonum solve.
type sleepySolver struct {
	delay time.Duration
	calls int
}

func (s *sleepySolver) Solve(_ context.Context, problem LinearProblem, _ LinearObjective, _ SolveOptions) (SolveResult, error) {
	s.calls++
	time.Sleep(s.delay)
	return SolveResult{Status: SolveOptimal, Values: make([]float64, len(problem.Variables))}, nil
}

type eventLog struct {
	mu     sync.Mutex
	events []OptimizationStageEvent
}

func (l *eventLog) observe(event OptimizationStageEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []OptimizationStageEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]OptimizationStageEvent(nil), l.events...)
}

func watchProblem() LinearProblem {
	return LinearProblem{
		Variables: []LinearVariable{{ID: "x", Lower: 0, Upper: math.Inf(1)}},
		Rows:      []LinearRow{{ID: "r", Sense: SenseLE, RHS: 1, Terms: []LinearTerm{{Variable: "x", Coeff: 1}}}},
	}
}

func newWatchedRun(solver Solver, dir string, log *eventLog) *engineRun {
	engine := &IntentEngine{solver: solver, evidenceDirectory: dir,
		slowSolveThreshold: 30 * time.Millisecond, slowSolveHeartbeat: 10 * time.Millisecond}
	run := engine.newRun(CompiledModel{}, log.observe)
	probe := 0.25
	run.at(solveContext{stage: StageMaximizeMainGroupInternalVisibility, source: "gap", class: 2, classCount: 5, probe: &probe})
	return run
}

// A10: slow hint, saved record, heartbeats, partial order, watcher cleanup.
func TestWatchedSolveSlowHintSavesAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	log := &eventLog{}
	run := newWatchedRun(&sleepySolver{delay: 120 * time.Millisecond}, dir, log)
	problem := watchProblem()
	objective := LinearObjective{Name: "probe", Sense: Minimize, Origin: ObjectiveDiagnostic, Terms: []LinearTerm{{Variable: "x", Coeff: -1}}}
	if _, err := run.Solve(context.Background(), problem, objective, SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9}); err != nil {
		t.Fatal(err)
	}
	returned := len(log.snapshot())
	time.Sleep(60 * time.Millisecond)
	events := log.snapshot()
	if len(events) != returned {
		t.Fatalf("watcher emitted %d events after the solve returned", len(events)-returned)
	}
	assertSolveEventOrder(t, events)
	var saved string
	running := 0
	for _, event := range events {
		switch event.State {
		case SolveEventSaved:
			saved = event.Path
		case SolveEventRunning:
			running++
		}
		if event.Class != 2 || event.ClassCount != 5 || event.Variables != 1 || event.Rows != 1 || event.SolveIndex != 1 {
			t.Fatalf("event lost its location: %+v", event)
		}
	}
	if saved == "" || running == 0 {
		t.Fatalf("expected a saved record and heartbeats, got %+v", events)
	}
	if last := events[len(events)-1]; last.Path != saved {
		t.Fatalf("finished event must carry the saved path, got %q", last.Path)
	}
	raw, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	var record slowSolveRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	back, err := record.Problem.Problem()
	if err != nil || !math.IsInf(back.Variables[0].Upper, 1) || back.Rows[0].RHS != 1 {
		t.Fatalf("saved problem did not round trip: %+v %v", back, err)
	}
	obj, err := record.Objective.Objective()
	if err != nil || obj.Terms[0].Coeff != -1 || record.Probe != "0.25" || record.Source != "gap" ||
		record.EngineVersion != engineImplementationVersion || record.Options["feasibility_tolerance"] != "1e-09" {
		t.Fatalf("saved record incomplete: %+v %v", record, err)
	}
	if run.slowSolves != 1 {
		t.Fatalf("slow solve count = %d", run.slowSolves)
	}
}

func TestWatchedSolveReportsSaveFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	log := &eventLog{}
	run := newWatchedRun(&sleepySolver{delay: 80 * time.Millisecond}, filepath.Join(blocker, "evidence"), log)
	if _, err := run.Solve(context.Background(), watchProblem(), LinearObjective{Name: "p", Sense: Minimize, Origin: ObjectiveDiagnostic}, SolveOptions{}); err != nil {
		t.Fatal(err)
	}
	events := log.snapshot()
	assertSolveEventOrder(t, events)
	found := false
	for _, event := range events {
		if event.State == SolveEventSaveFailed && event.Message != "" {
			found = true
		}
		if event.State == SolveEventSaved {
			t.Fatal("save must not be reported as successful")
		}
	}
	if !found {
		t.Fatalf("missing explicit save failure: %+v", events)
	}
}

func TestWatchedSolveFastSolveHasNoSlowEvents(t *testing.T) {
	log := &eventLog{}
	run := newWatchedRun(&sleepySolver{}, t.TempDir(), log)
	if _, err := run.Solve(context.Background(), watchProblem(), LinearObjective{Name: "p", Sense: Minimize, Origin: ObjectiveDiagnostic}, SolveOptions{}); err != nil {
		t.Fatal(err)
	}
	events := log.snapshot()
	if len(events) != 2 || events[0].State != SolveEventStarted || events[1].State != SolveEventFinished {
		t.Fatalf("fast solve events = %+v", events)
	}
}

// After cancellation no new backend solve may start.
func TestWatchedSolveStartsNoWorkAfterCancellation(t *testing.T) {
	solver := &sleepySolver{}
	log := &eventLog{}
	run := newWatchedRun(solver, t.TempDir(), log)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := run.Solve(ctx, watchProblem(), LinearObjective{Name: "p", Sense: Minimize, Origin: ObjectiveDiagnostic}, SolveOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if solver.calls != 0 || len(log.snapshot()) != 0 {
		t.Fatalf("cancelled run still called the solver (%d) or emitted events", solver.calls)
	}
}

// A solve already inside the backend is awaited; its caller then sees the
// cancellation instead of a usable result.
func TestWatchedSolveReturnsCancellationAfterBackendReturns(t *testing.T) {
	solver := &sleepySolver{delay: 40 * time.Millisecond}
	log := &eventLog{}
	run := newWatchedRun(solver, t.TempDir(), log)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	_, err := run.Solve(ctx, watchProblem(), LinearObjective{Name: "p", Sense: Minimize, Origin: ObjectiveDiagnostic}, SolveOptions{})
	if !errors.Is(err, context.Canceled) || solver.calls != 1 {
		t.Fatalf("err = %v calls = %d", err, solver.calls)
	}
}

// assertSolveEventOrder checks the documented partial order per LP.
func assertSolveEventOrder(t *testing.T, events []OptimizationStageEvent) {
	t.Helper()
	if len(events) < 2 || events[0].State != SolveEventStarted || events[len(events)-1].State != SolveEventFinished {
		t.Fatalf("started must be first and finished last: %+v", events)
	}
	slow := false
	for _, event := range events[1 : len(events)-1] {
		switch event.State {
		case SolveEventSlow:
			if slow {
				t.Fatal("slow emitted twice")
			}
			slow = true
		case SolveEventRunning, SolveEventSaved, SolveEventSaveFailed:
			if !slow {
				t.Fatalf("%s before slow", event.State)
			}
		default:
			t.Fatalf("unexpected state %q between started and finished", event.State)
		}
	}
}

// The SDK must not intercept signals or exit the process.
func TestSDKDoesNotHandleSignalsOrExit(t *testing.T) {
	for _, pattern := range []string{"*.go", "cli/*.go"} {
		files, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			for _, forbidden := range []string{"signal.Notify", "os.Exit("} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("%s contains %s", file, forbidden)
				}
			}
		}
	}
}
