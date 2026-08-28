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
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/zintix-labs/problab"
	legacyoptimizer "github.com/zintix-labs/problab/optimizer"
	"github.com/zintix-labs/problab/spec"
)

const (
	engineImplementationVersion = "intent-lp-v2.3.1"
	stableOrderingVersion       = "class-declaration/worker-index/sample-acceptance-v2"
)

// StageEvent is the typed observation emitted at a pipeline boundary. It is
// intentionally operational: timestamps and messages never enter model,
// solution, or artifact hashes.
type StageEvent struct {
	Stage       string
	Substage    OptimizationStageID
	BetMode     int
	State       string
	Message     string
	Objective   string
	Metric      string
	Probe       int
	Lower       *float64
	Upper       *float64
	FixedValue  *float64
	Status      string
	Spins       uint64
	Accepted    uint64
	Requested   uint64
	Classes     []ClassCollectionProgress
	ExpectedRTP float64
	Duration    time.Duration
}

// ClassCollectionProgress is one immutable snapshot of a Class quota. Reporter
// implementations receive the complete declaration-ordered slice in every
// collection-progress event, which lets a terminal redraw one stable row per
// Class without inspecting Collector internals or changing collection order.
type ClassCollectionProgress struct {
	Name      string
	Accepted  uint64
	Requested uint64
}

// Reporter receives ordered pipeline events without coupling the optimizer to
// terminal output, JSON logging, or tests. Implementations must return quickly;
// reporting is serialized even while collection workers run concurrently.
type Reporter interface {
	Report(StageEvent)
}

// ArtifactPublisher is the cross-run transaction boundary for one verified
// mode. It stages that mode independently and exposes a runtime bundle only
// when all game modes are present. The default implementation is
// FileArtifactWriter.
type ArtifactPublisher interface {
	PublishMode(context.Context, spec.GID, string, []int, MaterializedMode) (PublishedArtifact, error)
}

// ArtifactWriterFactory is the existing publication replacement seam. Custom
// callers receive the configured base directory and own all format handling;
// the production factory separately consumes the complete OutputOptions.
type ArtifactWriterFactory func(directory string) ArtifactPublisher

type outputWriterFactory func(OutputOptions) ArtifactPublisher

// TunerOption replaces one long-lived dependency at construction time. Options
// are intended for backend selection, reporting, and isolated tests—not for
// changing Designer constraints during a Run.
type TunerOption func(*Tuner) error

// Tuner owns only long-lived dependencies and immutable configuration. Samples,
// compiled rows, solver witnesses, and candidate data are local to each Run so
// repeated calls cannot leak state into one another.
type Tuner struct {
	config        Config
	lab           *problab.Problab
	collector     *Collector
	engine        *IntentEngine
	reporter      Reporter
	writerFactory outputWriterFactory
}

// NewTuner constructs the v2 application facade used by cmd/opt. Configuration
// is validated once, while every Run still resolves a deep self-contained plan
// and records explicit overrides in its report.
func NewTuner(config Config, lab *problab.Problab, options ...TunerOption) (*Tuner, error) {
	if lab == nil {
		return nil, fmt.Errorf("optimizer v2 tuner requires a Problab dependency")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	tuner := &Tuner{
		config:        cloneConfig(config),
		lab:           lab,
		collector:     NewCollector(lab),
		engine:        NewIntentEngine(NewGonumSolver()),
		writerFactory: newOutputPublisher,
	}
	for i, option := range options {
		if option == nil {
			return nil, fmt.Errorf("optimizer v2 tuner option[%d] is nil", i)
		}
		if err := option(tuner); err != nil {
			return nil, fmt.Errorf("optimizer v2 tuner option[%d]: %w", i, err)
		}
	}
	if tuner.collector == nil || tuner.engine == nil || tuner.writerFactory == nil {
		return nil, fmt.Errorf("optimizer v2 tuner has incomplete dependencies")
	}
	return tuner, nil
}

// WithSolver replaces the backend while retaining the production Engine's
// stage mapping and bisection policy. It is primarily useful for contract tests
// and future registered LP adapters.
func WithSolver(solver Solver) TunerOption {
	return func(tuner *Tuner) error {
		if solver == nil {
			return fmt.Errorf("solver is nil")
		}
		tuner.engine = NewIntentEngine(solver)
		return nil
	}
}

// WithReporter installs an operational observer. A nil Reporter is rejected so
// a mistaken option does not silently hide requested progress output.
func WithReporter(reporter Reporter) TunerOption {
	return func(tuner *Tuner) error {
		if reporter == nil {
			return fmt.Errorf("reporter is nil")
		}
		tuner.reporter = reporter
		tuner.collector.Reporter = reporter
		return nil
	}
}

// WithCollectionTags binds each game's custom collection tag predicates by
// GID. It stores the map on the already-constructed Collector and performs
// no registration or validation itself — resolution and registration into
// the legacy tag registry happen per-plan inside Collector.Collect, scoped
// to that plan's target game, via legacyoptimizer.NewRegisterTags. A game
// absent from gameTags is valid and collects using only the built-in bg/fg
// tags.
func WithCollectionTags(gameTags map[spec.GID]map[string]legacyoptimizer.IsTag) TunerOption {
	return func(tuner *Tuner) error {
		tuner.collector.GameTags = gameTags
		return nil
	}
}

// WithArtifactWriterFactory replaces publication without changing materialized
// probabilities or verification. The factory receives the resolved RunPlan
// base directory and replaces production multi-format routing completely.
func WithArtifactWriterFactory(factory ArtifactWriterFactory) TunerOption {
	return func(tuner *Tuner) error {
		if factory == nil {
			return fmt.Errorf("artifact writer factory is nil")
		}
		tuner.writerFactory = func(output OutputOptions) ArtifactPublisher {
			return factory(output.Directory)
		}
		return nil
	}
}

// Run executes one resolved tuning plan through explicit deterministic stage
// gates. Expected config/support/model/representation/artifact outcomes are
// returned in RunResult with nil error. Go error is reserved for cancellation,
// I/O/dependency failure, and broken application contracts.
func (t *Tuner) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	if t == nil {
		return RunResult{}, fmt.Errorf("optimizer v2 tuner is nil")
	}
	if ctx == nil {
		return RunResult{}, fmt.Errorf("optimizer v2 Run context is nil")
	}
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}
	// Static validation includes plan resolution, explicit override application,
	// canonical hashing, and checking that the selected game/mode exists. It is
	// deliberately visible as its own stage because every one of these checks can
	// fail before the expensive collection begins.
	staticStage := "static-validation"
	staticStarted := t.startStage(staticStage, -1)
	report := RunReport{Overrides: request.Overrides}
	resolved, err := t.config.ResolvePlan(request.PlanID)
	if err != nil {
		diagnostics := Diagnostics{configDiagnostic(err.Error())}
		t.finishStage(&report, staticStage, -1, staticStarted, nil, diagnostics)
		return resultFromDiagnostics(report, diagnostics), nil
	}
	resolved, err = resolved.WithOverrides(request.Overrides)
	if err != nil {
		report.Plan = resolved
		diagnostics := Diagnostics{configDiagnostic(err.Error())}
		t.finishStage(&report, staticStage, -1, staticStarted, nil, diagnostics)
		return resultFromDiagnostics(report, diagnostics), nil
	}
	report = newRunReport(resolved, request.Overrides)
	report.ConfigHash, err = hashCanonicalJSON(resolved)
	if err != nil {
		t.finishStage(&report, staticStage, -1, staticStarted, err, nil)
		return RunResult{}, fmt.Errorf("hash resolved optimizer v2 plan: %w", err)
	}
	betUnits, diagnostic, err := validateRuntimeTarget(t.lab, resolved)
	if err != nil {
		t.finishStage(&report, staticStage, -1, staticStarted, err, nil)
		return RunResult{}, err
	}
	if diagnostic.StopsRun() {
		diagnostics := Diagnostics{diagnostic}
		t.finishStage(&report, staticStage, -1, staticStarted, nil, diagnostics)
		return resultFromDiagnostics(report, diagnostics), nil
	}
	t.finishStage(&report, staticStage, -1, staticStarted, nil, nil)
	if t.reporter != nil {
		t.reporter.Report(StageEvent{
			Stage: "expected-rtp", State: "info", BetMode: -1,
			ExpectedRTP: resolved.Intent.ExpectedRTP(),
		})
	}

	// Config validation and validateRuntimeTarget both enforce the single-mode
	// Run contract. Keeping the selected value scalar below makes it impossible
	// for one invocation to accidentally share collection or solver settings
	// across modes that were intended to be optimized independently.
	betMode := resolved.Plan.Target.BetModes[0]
	report.Verification.Pass = true
	collected, diagnostics, err := t.collectStage(ctx, resolved, betMode, &report)
	if err != nil || diagnostics.StopsRun() {
		return resultFromDiagnostics(report, diagnostics), err
	}
	prepared, diagnostics, err := t.prepareStage(resolved, collected, &report)
	if err != nil || diagnostics.StopsRun() {
		return resultFromDiagnostics(report, diagnostics), err
	}
	compiled, diagnostics, err := t.compileStage(prepared, &report)
	if err != nil || diagnostics.StopsRun() {
		return resultFromDiagnostics(report, diagnostics), err
	}
	modelHash, err := hashCanonicalJSON(compiled.Hard)
	if err != nil {
		return RunResult{}, fmt.Errorf("hash optimizer v2 semantic model for mode %d: %w", betMode, err)
	}
	solution, err := t.solveStage(ctx, compiled, betMode, &report)
	if err != nil {
		return RunResult{}, err
	}
	report.Solver = solverProvenance(solution.Evidence)
	if solution.Status != StatusOptimal {
		return resultFromDiagnostics(report, solution.Diagnostics), nil
	}
	mode, verification, err := t.materializeAndVerifyStage(ctx, compiled, solution, betMode, betUnits[betMode], &report)
	if err != nil {
		return RunResult{}, err
	}
	if !verification.Pass {
		diagnostic := materializationViolationDiagnostic()
		report.Verification = verification
		return resultFromDiagnostics(report, Diagnostics{diagnostic}), nil
	}
	modeHash := hashModeSolution(mode)
	intentReport := BuildIntentQualityReport(compiled, solution)
	distribution, err := BuildBucketDistributionReport(compiled, mode)
	if err != nil {
		return RunResult{}, fmt.Errorf("build optimizer v2 bucket distribution report for mode %d: %w", betMode, err)
	}
	modeReport := ModeRunReport{
		BetMode: betMode, BetUnit: betUnits[betMode], Spins: collected.Spins,
		Solver: solverProvenance(solution.Evidence), Intent: intentReport,
		Verification: verification, Distribution: distribution, ModelHash: modelHash, SolutionHash: modeHash,
	}
	report.Modes = append(report.Modes, modeReport)
	report.Intent = intentReport
	report.Solver = modeReport.Solver
	report.ModelHash = modelHash
	report.SolutionHash = modeHash
	appendVerification(&report.Verification, betMode, verification)
	// evaluator:none yields one canonical candidate for this mode. Sibling modes
	// are separate Runs and may intentionally use different plans or intents.
	report.Candidates.Generated = 1
	published, err := t.publishStage(ctx, resolved, betUnits, mode, &report)
	if err != nil {
		return RunResult{}, err
	}
	publicationState := PublicationModeStaged
	if published.Complete {
		publicationState = PublicationOutputsPublished
		if published.ManifestPath != "" {
			publicationState = PublicationManifestPublished
		}
		report.ArtifactHash = published.ArtifactID
	}
	report.Publication = &PublicationReport{
		State:            publicationState,
		Formats:          append([]OutputFormat(nil), published.Formats...),
		BetMode:          betMode,
		ExpectedModes:    len(betUnits),
		StagedModes:      append([]int(nil), published.StagedModes...),
		MissingModes:     append([]int(nil), published.MissingModes...),
		StagingDirectory: published.StagingDirectory,
		ManifestPath:     published.ManifestPath,
	}
	paths := make([]string, 0, len(published.Paths)+1)
	if published.ManifestPath != "" {
		paths = append(paths, published.ManifestPath)
	}
	paths = append(paths, published.Paths...)
	return RunResult{Status: StatusOptimal, Report: report, ArtifactPaths: paths}, nil
}

// collectStage invokes the single-owner Collector and records only operational
// duration/event metadata around its immutable output.
func (t *Tuner) collectStage(ctx context.Context, plan ResolvedPlan, betMode int, report *RunReport) (CollectedProblem, Diagnostics, error) {
	stage := fmt.Sprintf("collect[mode=%d]", betMode)
	started := t.startStage(stage, betMode)
	collected, diagnostics, err := t.collector.Collect(ctx, plan, betMode)
	t.finishStage(report, stage, betMode, started, err, diagnostics)
	return collected, diagnostics, err
}

// prepareStage derives empirical bucket statistics and prechecks without
// mutating collection order or retrying an infeasible support set.
func (t *Tuner) prepareStage(plan ResolvedPlan, collected CollectedProblem, report *RunReport) (PreparedProblem, Diagnostics, error) {
	stage := fmt.Sprintf("prepare[mode=%d]", collected.BetMode)
	started := t.startStage(stage, collected.BetMode)
	prepared, diagnostics, err := PrepareProblem(plan, collected)
	t.finishStage(report, stage, collected.BetMode, started, err, diagnostics)
	return prepared, diagnostics, err
}

// compileStage builds the named immutable hard model; it never calls a solver
// or changes the collected support after seeing a conflict.
func (t *Tuner) compileStage(prepared PreparedProblem, report *RunReport) (CompiledModel, Diagnostics, error) {
	stage := fmt.Sprintf("compile[mode=%d]", prepared.BetMode)
	started := t.startStage(stage, prepared.BetMode)
	compiled, diagnostics, err := CompileHardModel(prepared)
	t.finishStage(report, stage, prepared.BetMode, started, err, diagnostics)
	return compiled, diagnostics, err
}

// solveStage delegates the semantic optimization sequence to IntentEngine,
// bridges its substages to the application Reporter, and retains terminal
// substage provenance separately from the existing parent solve duration.
func (t *Tuner) solveStage(ctx context.Context, compiled CompiledModel, betMode int, report *RunReport) (EngineSolution, error) {
	stage := fmt.Sprintf("solve[mode=%d]", betMode)
	started := t.startStage(stage, betMode)
	observer := func(event OptimizationStageEvent) {
		if t.reporter != nil {
			t.reporter.Report(StageEvent{
				Stage: stage, Substage: event.Stage, BetMode: betMode,
				State: event.State, Message: event.Message,
				Objective: event.Objective, Metric: event.Metric, Probe: event.Probe,
				Lower: cloneFloatPointer(event.Lower), Upper: cloneFloatPointer(event.Upper),
				FixedValue: cloneFloatPointer(event.FixedValue), Status: event.Status,
				Duration: event.Duration,
			})
		}
		switch event.State {
		case "completed", "skipped", "failed":
			report.OptimizationStages = append(report.OptimizationStages, OptimizationStageReport{
				Parent: stage, Stage: event.Stage, BetMode: betMode, State: event.State,
				Objective: event.Objective, Metric: event.Metric, Probes: event.Probe,
				Lower: cloneFloatPointer(event.Lower), Upper: cloneFloatPointer(event.Upper),
				FixedValue: cloneFloatPointer(event.FixedValue), Duration: event.Duration,
			})
		}
	}
	solution, err := t.engine.SolveObserved(ctx, compiled, observer)
	t.finishStage(report, stage, betMode, started, err, solution.Diagnostics)
	return solution, err
}

func cloneFloatPointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// materializeAndVerifyStage expands c*p/n outcome weights, builds the runtime
// alias representation, restores every seed-bank entry through the raw game,
// and replays the resulting payouts plus effective alias marginals against the
// hard model before allowing the mode into the publication transaction.
func (t *Tuner) materializeAndVerifyStage(ctx context.Context, compiled CompiledModel, solution EngineSolution, betMode, betUnit int, report *RunReport) (MaterializedMode, VerificationReport, error) {
	stage := fmt.Sprintf("materialize-verify[mode=%d]", betMode)
	started := t.startStage(stage, betMode)
	samples, err := ExpandSolution(compiled, solution, betMode, betUnit)
	if err != nil {
		t.finishStage(report, stage, betMode, started, err, nil)
		return MaterializedMode{}, VerificationReport{}, fmt.Errorf("expand optimizer v2 solution for mode %d: %w", betMode, err)
	}
	// The solver witness has already passed original-row replay at the configured
	// feasibility tolerance. Use that same proved allowance for the expanded
	// near-one sum and the alias table's bounded approximation. Materialization
	// then records the reconstructed runtime marginals as artifact truth, and the
	// verifier replays those marginals through every immutable hard constraint.
	mode, err := materializeModeWithNormalizationTolerance(
		betMode,
		betUnit,
		samples,
		compiled.Prepared.Plan.EngineOptions.FeasibilityTolerance,
	)
	if err != nil {
		t.finishStage(report, stage, betMode, started, err, nil)
		return MaterializedMode{}, VerificationReport{}, fmt.Errorf("materialize optimizer v2 mode %d: %w", betMode, err)
	}
	verification, err := VerifyRuntimeMaterialized(ctx, t.lab, compiled, solution, mode)
	if err != nil {
		t.finishStage(report, stage, betMode, started, err, nil)
		return MaterializedMode{}, VerificationReport{}, fmt.Errorf("runtime-replay optimizer v2 mode %d: %w", betMode, err)
	}
	if !verification.Pass {
		t.finishStage(report, stage, betMode, started, nil, Diagnostics{materializationViolationDiagnostic()})
	} else {
		t.finishStage(report, stage, betMode, started, nil, nil)
	}
	return mode, verification, nil
}

// publishStage persists one verified mode. The writer may return a successful
// pending result when siblings are missing; only a complete set creates a
// manifest and replaces the runtime-visible game directory.
func (t *Tuner) publishStage(ctx context.Context, plan ResolvedPlan, betUnits []int, mode MaterializedMode, report *RunReport) (PublishedArtifact, error) {
	stage := "publish"
	started := t.startStage(stage, mode.BetMode)
	writer := t.writerFactory(plan.Plan.Output)
	if writer == nil {
		err := fmt.Errorf("artifact writer factory returned nil")
		t.finishStage(report, stage, mode.BetMode, started, err, nil)
		return PublishedArtifact{}, err
	}
	published, err := writer.PublishMode(ctx, plan.Plan.Target.Game, t.lab.SnapshotFormat(), betUnits, mode)
	t.finishStage(report, stage, mode.BetMode, started, err, nil)
	return published, err
}

// startStage emits a typed start event and returns the operational timestamp
// used only for RunReport.StageDuration.
func (t *Tuner) startStage(stage string, betMode int) time.Time {
	if t.reporter != nil {
		t.reporter.Report(StageEvent{Stage: stage, BetMode: betMode, State: "started"})
	}
	return time.Now()
}

// finishStage appends elapsed operational metadata and emits the matching
// terminal event without changing the stage value or its deterministic hashes.
func (t *Tuner) finishStage(report *RunReport, stage string, betMode int, started time.Time, stageErr error, diagnostics Diagnostics) {
	duration := time.Since(started)
	report.Stages = append(report.Stages, StageDuration{Stage: stage, Duration: duration})
	if t.reporter == nil {
		return
	}
	message := ""
	state := "completed"
	if stageErr != nil {
		state, message = "failed", stageErr.Error()
	} else {
		stoppingCount := 0
		for _, diagnostic := range diagnostics {
			if diagnostic.StopsRun() {
				stoppingCount++
				if message == "" {
					message = diagnostic.Message
				}
			}
		}
		if stoppingCount > 0 {
			state = "failed"
		}
		if stoppingCount > 1 {
			message = fmt.Sprintf("detected %d independently localized hard-model conflicts; see the result list for required and achievable bounds", stoppingCount)
		}
	}
	t.reporter.Report(StageEvent{Stage: stage, BetMode: betMode, State: state, Message: message, Duration: duration})
}

// materializationViolationDiagnostic keeps the stage event and final RunResult
// on the same typed explanation when semantic replay rejects a materialized
// alias table. Nothing is published after this diagnostic is constructed.
func materializationViolationDiagnostic() Diagnostic {
	return Diagnostic{
		Code: DiagnosticArtifactMaterializationViolation, Status: StatusArtifactInvalid,
		Message:        "materialized alias distribution failed semantic replay; nothing was staged",
		Representation: RepresentationAtomicBuckets,
	}
}

// newRunReport initializes product claims before any collection begins. With
// evaluator none, the canonical LP point is explicitly a fallback representative
// and CandidateReport never implies player-experience optimality.
func newRunReport(plan ResolvedPlan, overrides RunOverrides) RunReport {
	return RunReport{
		Plan: plan, ExpectedRTP: plan.Intent.ExpectedRTP(), Overrides: overrides,
		Engine: EngineProvenance{
			Name: EngineIntentLPV2, Version: engineImplementationVersion,
			SemanticAxioms: []string{MainSemanticAxiomVersion},
		},
		StableOrderingVersion: stableOrderingVersion,
		Representation:        RepresentationAtomicBuckets,
		Advisories:            AnalyzeCollectionTopology(plan.Plan.Intent, plan.Intent),
		Candidates: CandidateReport{
			Budget:                     plan.Plan.CandidateSelection.MaxCandidates,
			CanonicalOnly:              true,
			PlayerExperienceOptimality: "NOT_CLAIMED",
		},
	}
}

// validateRuntimeTarget proves that one Run selects exactly one existing mode
// while retaining the complete runtime bet-unit list for cross-run publication.
// Manifest v1 still requires all modes, but completeness is now enforced by the
// persistent publisher after each mode has been optimized independently.
func validateRuntimeTarget(lab *problab.Problab, plan ResolvedPlan) ([]int, Diagnostic, error) {
	summaries, err := lab.Summary()
	if err != nil {
		return nil, Diagnostic{}, fmt.Errorf("resolve optimizer v2 runtime target: %w", err)
	}
	var betUnits []int
	for _, summary := range summaries {
		if summary.GID == plan.Plan.Target.Game {
			betUnits = append([]int(nil), summary.BetUnits...)
			break
		}
	}
	if len(betUnits) == 0 {
		return nil, configDiagnostic(fmt.Sprintf("game %d is not present in the Problab catalog", plan.Plan.Target.Game)), nil
	}
	modes := plan.Plan.Target.BetModes
	if len(modes) != 1 {
		return nil, configDiagnostic(fmt.Sprintf("optimizer v2 requires exactly one target bet mode per Run; plan requests %d", len(modes))), nil
	}
	if modes[0] < 0 || modes[0] >= len(betUnits) {
		return nil, configDiagnostic(fmt.Sprintf("game %d has bet modes [0..%d]; plan requests mode %d", plan.Plan.Target.Game, len(betUnits)-1, modes[0])), nil
	}
	return betUnits, Diagnostic{}, nil
}

// resultFromDiagnostics selects the first stopping diagnostic's status while
// preserving every typed explanation in deterministic contributor order.
func resultFromDiagnostics(report RunReport, diagnostics Diagnostics) RunResult {
	status := StatusInternalError
	for _, diagnostic := range diagnostics {
		if diagnostic.StopsRun() {
			status = diagnostic.Status
			break
		}
	}
	return RunResult{Status: status, Diagnostics: diagnostics, Report: report}
}

// solverProvenance copies backend evidence into the stable public report shape.
// The two structs share an identical field layout, so a direct conversion keeps
// them provably in sync: adding a field to one without the other stops compiling.
func solverProvenance(evidence SolverEvidence) SolverProvenance {
	return SolverProvenance(evidence)
}

// appendVerification copies the selected mode's checks into the Run-level view
// while retaining the mode index in every check name. This remains useful when
// reports from independently optimized sibling modes are compared later.
func appendVerification(target *VerificationReport, betMode int, source VerificationReport) {
	target.Pass = target.Pass && source.Pass
	for _, check := range source.Checks {
		check.Name = fmt.Sprintf("mode[%d].%s", betMode, check.Name)
		target.Checks = append(target.Checks, check)
	}
}

// hashCanonicalJSON hashes self-contained resolved values whose structs and
// slices have stable field/order semantics. It never hashes durations.
func hashCanonicalJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// hashModeSolution hashes stable sample identity, payout, and actual runtime
// marginal probability rather than backend auxiliary variables.
func hashModeSolution(mode MaterializedMode) string {
	hash := sha256.New()
	var scratch [8]byte
	binary.LittleEndian.PutUint64(scratch[:], uint64(mode.BetMode))
	_, _ = hash.Write(scratch[:])
	for i, sample := range mode.Samples {
		binary.LittleEndian.PutUint64(scratch[:], math.Float64bits(sample.Win))
		_, _ = hash.Write(scratch[:])
		binary.LittleEndian.PutUint64(scratch[:], math.Float64bits(mode.EffectiveProbabilities[i]))
		_, _ = hash.Write(scratch[:])
		_, _ = hash.Write(sample.Snapshot)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
