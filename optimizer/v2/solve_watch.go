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
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Per-LP lifecycle states carried by OptimizationStageEvent.State. Only a
// partial order is promised: started first, finished last; slow precedes any
// running/saved/save-failed, which may interleave; nothing follows finished.
const (
	SolveEventStarted    = "solve-started"
	SolveEventSlow       = "solve-slow"
	SolveEventRunning    = "solve-running"
	SolveEventSaved      = "solve-saved"
	SolveEventSaveFailed = "solve-save-failed"
	SolveEventFinished   = "solve-finished"
)

const (
	// defaultSlowSolveThreshold is a display/evidence hint, never a failure
	// threshold: the solve keeps running after it fires.
	defaultSlowSolveThreshold = 5 * time.Second
	defaultSlowSolveHeartbeat = time.Second
	slowSolveRecordVersion    = "slow-solve-v1"
	evidenceSubdirectory      = "numerical-evidence"
)

// solveContext describes where the next solve happens for progress display and
// slow-solve records. It never influences the mathematics.
type solveContext struct {
	stage      OptimizationStageID
	source     string
	class      int // 1-based Class position; 0 for joint problems
	classCount int
	probe      *float64
}

// engineRun owns everything that is scoped to one IntentEngine.SolveObserved
// call: the serialized observer, the LP counter, slow-solve evidence, and the
// evidence counters. It implements Solver so every backend call, including
// diagnostics, passes through the same watcher.
type engineRun struct {
	engine      *IntentEngine
	compiled    CompiledModel
	observer    OptimizationStageObserver
	emitMu      sync.Mutex
	current     solveContext
	solveIndex  int
	evidenceDir string
	// runDir is this run's own subdirectory of evidenceDir, created on the
	// first write so runs sharing an output directory never overwrite each
	// other's evidence (slow-solve records and evidence files).
	runDirMu sync.Mutex
	runDir   string
	// locks are the fixed values of the stages completed so far; evidence
	// of later stage problems records them (ProbeBase).
	locks    ProbeLocks
	options  SolveOptions
	counters EvidenceCounters
	// stageStart and stagePeak scope counters to the running stage (stage
	// reports and per-stage evidence files); see beginStageCounters.
	stageStart EvidenceCounters
	stagePeak  exactPeak

	// The two sets of techspec §4.6: accept (internal) judges every
	// witness and is what probe and lock certificates prove empty;
	// hardProof (external, the production replay) is what a hard
	// INFEASIBLE_MODEL certificate proves empty.
	accept     acceptanceRule
	probeProof acceptanceRule
	hardProof  acceptanceRule
}

func (e *IntentEngine) newRun(compiled CompiledModel, observer OptimizationStageObserver) *engineRun {
	dir := e.evidenceDirectory
	if dir == "" && compiled.Prepared.Plan.Plan.Output.Directory != "" {
		dir = filepath.Join(compiled.Prepared.Plan.Plan.Output.Directory, evidenceSubdirectory)
	}
	run := &engineRun{engine: e, compiled: compiled, observer: observer, evidenceDir: dir,
		options: SolveOptions{
			FeasibilityTolerance: compiled.Prepared.Plan.EngineOptions.FeasibilityTolerance,
			OptimalityTolerance:  compiled.Prepared.Plan.EngineOptions.OptimalityTolerance,
		},
		counters: EvidenceCounters{WitnessAttempts: map[string]int{}, WitnessFound: map[string]int{}},
	}
	run.useTolerance(run.options.FeasibilityTolerance)
	return run
}

// useTolerance sets the replay tolerance τ and the two sets derived from it.
func (r *engineRun) useTolerance(tau float64) {
	r.options.FeasibilityTolerance = tau
	r.accept, r.probeProof, r.hardProof = internalRule(tau), internalRule(tau), productRule(tau)
}

// emit serializes every observer call (engine thread and watcher goroutines)
// so the existing synchronous, ordered observer contract still holds.
func (r *engineRun) emit(event OptimizationStageEvent) {
	if r == nil || r.observer == nil {
		return
	}
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	r.observer(event)
}

// at sets the solve context for the following backend calls.
func (r *engineRun) at(context solveContext) { r.current = context }

// slowSolveRecord is the self-contained input of one slow backend solve.
type slowSolveRecord struct {
	Version          string            `json:"version"`
	Encoding         string            `json:"encoding"`
	Stage            string            `json:"stage"`
	Source           string            `json:"source"`
	Class            int               `json:"class"`
	ClassCount       int               `json:"class_count"`
	Probe            string            `json:"probe,omitempty"`
	SolveIndex       int               `json:"solve_index"`
	EngineVersion    string            `json:"engine_version"`
	Solver           string            `json:"solver"`
	BackendVersion   string            `json:"backend_version"`
	TransformVersion string            `json:"transform_version"`
	Options          map[string]string `json:"options"`
	Problem          ProblemSnapshot   `json:"problem"`
	Objective        ObjectiveSnapshot `json:"objective"`
}

// Solve runs one backend solve under the watcher. After cancellation no new
// solve starts; a solve already inside the backend cannot be interrupted and
// is awaited, after which the caller sees the cancellation error.
func (r *engineRun) Solve(ctx context.Context, problem LinearProblem, objective LinearObjective, options SolveOptions) (SolveResult, error) {
	if err := ctx.Err(); err != nil {
		return SolveResult{}, err
	}
	r.solveIndex++
	r.counters.Solves++
	index := r.solveIndex
	meta := r.current
	// Own immutable input copy: the watcher reads only this.
	input := cloneLinearProblem(problem)
	inputObjective := objective
	inputObjective.Terms = append([]LinearTerm(nil), objective.Terms...)
	base := OptimizationStageEvent{
		Stage: meta.stage, SolveIndex: index, SolveSource: meta.source,
		Class: meta.class, ClassCount: meta.classCount,
		Variables: len(problem.Variables), Rows: len(problem.Rows),
	}
	started := time.Now()
	startEvent := base
	startEvent.State = SolveEventStarted
	r.emit(startEvent)

	watchCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	var savedPath string
	go func() {
		defer close(done)
		savedPath = r.watch(watchCtx, base, started, index, meta, input, inputObjective, options)
	}()
	result, err := r.engine.solver.Solve(ctx, problem, objective, options)
	stop()
	<-done

	finished := base
	finished.State = SolveEventFinished
	finished.Duration = time.Since(started)
	finished.Path = savedPath
	finished.Status = solveStatusName(result.Status)
	if finished.Duration >= r.engine.slowThreshold() {
		r.counters.SlowSolves++
	}
	r.emit(finished)
	if err == nil {
		err = ctx.Err()
	}
	return result, err
}

// watch fires the slow-solve hint, tries to save the input, then emits
// heartbeats until the solve returns. It returns the saved path, if any.
func (r *engineRun) watch(
	ctx context.Context, base OptimizationStageEvent, started time.Time, index int, meta solveContext,
	problem LinearProblem, objective LinearObjective, options SolveOptions,
) string {
	timer := time.NewTimer(r.engine.slowThreshold())
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ""
	case <-timer.C:
	}
	slow := base
	slow.State = SolveEventSlow
	slow.Duration = time.Since(started)
	r.emit(slow)

	path, err := r.saveSlowSolve(index, meta, problem, objective, options)
	saved := base
	saved.Duration = time.Since(started)
	if err != nil {
		saved.State = SolveEventSaveFailed
		saved.Message = err.Error()
	} else {
		saved.State = SolveEventSaved
		saved.Path = path
	}
	r.emit(saved)

	ticker := time.NewTicker(r.engine.slowHeartbeat())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return path
		case <-ticker.C:
			running := base
			running.State = SolveEventRunning
			running.Duration = time.Since(started)
			running.Path = path
			r.emit(running)
		}
	}
}

func (r *engineRun) saveSlowSolve(index int, meta solveContext, problem LinearProblem, objective LinearObjective, options SolveOptions) (string, error) {
	record := slowSolveRecord{
		Version: slowSolveRecordVersion, Encoding: numericEncodingVersion,
		Stage: string(meta.stage), Source: meta.source, Class: meta.class, ClassCount: meta.classCount,
		SolveIndex: index, EngineVersion: engineImplementationVersion,
		Solver: fmt.Sprintf("%T", r.engine.solver), BackendVersion: gonumBackendVersion, TransformVersion: gonumTransformVersion,
		Options: map[string]string{
			"feasibility_tolerance": encodeFloat64(options.FeasibilityTolerance),
			"optimality_tolerance":  encodeFloat64(options.OptimalityTolerance),
		},
		Problem: SnapshotProblem(problem), Objective: SnapshotObjective(objective),
	}
	if meta.probe != nil {
		record.Probe = encodeFloat64(*meta.probe)
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	dir, err := r.evidenceRunDir()
	if err != nil {
		return "", err
	}
	return writeFileAtomic(dir, fmt.Sprintf("slow-solve-%s-%06d.json", meta.stage, index), append(raw, '\n'))
}

// evidenceRunDir returns this run's evidence subdirectory, creating it with
// a unique name (UTC start time plus a random suffix) on first use.
func (r *engineRun) evidenceRunDir() (string, error) {
	r.runDirMu.Lock()
	defer r.runDirMu.Unlock()
	if r.runDir != "" {
		return r.runDir, nil
	}
	if err := os.MkdirAll(r.evidenceDir, 0o755); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(r.evidenceDir, "run-"+time.Now().UTC().Format("20060102T150405Z")+"-")
	if err != nil {
		return "", err
	}
	r.runDir = dir
	return dir, nil
}

func (e *IntentEngine) slowThreshold() time.Duration {
	if e.slowSolveThreshold > 0 {
		return e.slowSolveThreshold
	}
	return defaultSlowSolveThreshold
}

func (e *IntentEngine) slowHeartbeat() time.Duration {
	if e.slowSolveHeartbeat > 0 {
		return e.slowSolveHeartbeat
	}
	return defaultSlowSolveHeartbeat
}
