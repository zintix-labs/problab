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
	"errors"
	"fmt"

	"github.com/zintix-labs/problab"
	legacyoptimizer "github.com/zintix-labs/problab/optimizer"
	"github.com/zintix-labs/problab/spec"
)

// CollectedSample is one immutable replay atom accepted by the declaration-
// ordered Class classifier. Win is normalized exactly once as TotalWin / Bet;
// Snapshot is the Core state captured immediately before the raw game spin.
type CollectedSample struct {
	ClassID  string
	Win      float64
	Snapshot []byte
	Sequence uint64
}

// CollectedClass retains the source ClassIntent next to its accepted replay
// atoms. Samples stay in deterministic worker-index/acceptance order; equal
// payouts are never reordered because their snapshots are distinct artifact
// identities.
type CollectedClass struct {
	Intent  ClassIntent
	Samples []CollectedSample
}

// CollectedProblem is the complete read-only collection result for one game
// and bet mode. Spins records produced spins, not accepted outcomes.
type CollectedProblem struct {
	Game    spec.GID
	BetMode int
	BetUnit int
	Spins   uint64
	Classes []CollectedClass
}

type classPredicate struct {
	matchMask    uint64
	mismatchMask uint64
}

// Collector creates one independent raw Machine/PRNG stream per configured
// worker. Keeping Problab as an explicit dependency makes collection testable
// without placing simulation or command-line concerns inside the math engine.
type Collector struct {
	Lab      *problab.Problab
	Reporter Reporter
	GameTags map[spec.GID]map[string]legacyoptimizer.IsTag
}

// NewCollector creates the production collector. A nil Lab is rejected by
// Collect as a broken dependency instead of being reported as designer
// infeasibility.
func NewCollector(lab *problab.Problab) *Collector {
	return &Collector{Lab: lab}
}

// Collect executes raw game logic until every statically partitioned Class
// quota is full or every worker's statically partitioned MaxSpins budget is
// exhausted. Each worker owns an independent Machine and scans Classes in YAML
// declaration order, skips its full local Classes, assigns the outcome to the
// first matching Class, appends once, and immediately breaks.
//
// Static partitioning is intentional: no worker can race for a shared final
// quota or borrow another worker's unused spin budget. The same seed and worker
// count therefore always produce the same accepted set regardless of goroutine
// scheduling. Changing the worker count changes the random-stream partition and
// is expected to change the collected set.
//
// A collection shortfall is an expected support outcome and is returned as a
// typed diagnostic with nil error. Snapshot, game, and registry failures are
// operational dependency errors and therefore use the Go error channel.
func (c *Collector) Collect(
	ctx context.Context,
	plan ResolvedPlan,
	betMode int,
) (CollectedProblem, Diagnostics, error) {
	if c == nil || c.Lab == nil {
		return CollectedProblem{}, nil, fmt.Errorf("v2 collector requires a Problab dependency")
	}
	if err := ctx.Err(); err != nil {
		return CollectedProblem{}, nil, err
	}
	if plan.Plan.Collection.Workers < 1 {
		return CollectedProblem{}, Diagnostics{configDiagnostic("collection.workers must be greater than zero")}, nil
	}
	if plan.Plan.Collection.BatchSize == 0 {
		return CollectedProblem{}, Diagnostics{configDiagnostic("collection.batch_size must be greater than zero")}, nil
	}
	betUnit, err := optimizerBetUnit(c.Lab, plan.Plan.Target.Game, betMode)
	if err != nil {
		return CollectedProblem{}, nil, err
	}
	if err := legacyoptimizer.NewRegisterTags(c.GameTags[plan.Plan.Target.Game]); err != nil {
		return CollectedProblem{}, nil, fmt.Errorf("register collection tags: %w", err)
	}
	tagger, predicates, err := compileTagPredicates(plan.Intent.Classes)
	if err != nil {
		return CollectedProblem{}, Diagnostics{configDiagnostic(err.Error())}, nil
	}
	workers := plan.Plan.Collection.Workers
	machines := make([]*problab.Machine, workers)
	seedMaker := problab.NewSeedMaker(plan.Plan.Seed)
	for worker := range workers {
		// Match Simulator.SimMP: worker zero retains the original single-stream
		// seed, while every additional worker receives the next deterministic
		// sub-seed. This preserves workers=1 collection output.
		seed := plan.Plan.Seed
		if worker > 0 {
			seed = seedMaker.Next()
		}
		machines[worker], err = c.Lab.NewUnoptimizedMachineWithSeed(plan.Plan.Target.Game, seed, true)
		if err != nil {
			return CollectedProblem{}, nil, fmt.Errorf("create raw optimizer machine for worker %d: %w", worker, err)
		}
	}

	collected := CollectedProblem{
		Game: plan.Plan.Target.Game, BetMode: betMode, BetUnit: betUnit,
		Classes: make([]CollectedClass, len(plan.Intent.Classes)),
	}
	remaining := uint64(0)
	for i, class := range plan.Intent.Classes {
		collected.Classes[i] = CollectedClass{Intent: cloneClassIntent(class)}
		remaining += class.Collect.Samples
	}
	requested := remaining
	if c.Reporter != nil {
		c.Reporter.Report(collectionProgressEvent(collected, requested, remaining))
	}

	workerQuotas := partitionClassQuotas(plan.Intent.Classes, workers)

	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	// Each worker sends at most one update per BatchSize local spins. Across
	// evenly partitioned workers this keeps fan-in traffic proportional to
	// aggregate spins / BatchSize instead of multiplying it by Workers.
	progressEvery := plan.Plan.Collection.BatchSize
	progresses := make(chan collectionWorkerProgress, workers)
	results := make(chan collectionWorkerResult, workers)
	for worker := range workers {
		worker := worker
		go func() {
			results <- collectWorker(
				workerCtx,
				worker,
				machines[worker],
				betMode,
				plan.Intent.Classes,
				predicates,
				tagger,
				workerQuotas[worker],
				partitionShare(plan.Plan.Collection.MaxSpins, workers, worker),
				progressEvery,
				progresses,
			)
		}()
	}

	workerSpins := make([]uint64, workers)
	workerAccepted := make([][]uint64, workers)
	workerResults := make([]collectionWorkerResult, workers)
	for worker := range workers {
		workerAccepted[worker] = make([]uint64, len(plan.Intent.Classes))
	}
	nextReport := plan.Plan.Collection.BatchSize
	lastReportedSpins := uint64(0)
	lastReportedAccepted := uint64(0)
	completed := 0
	for completed < workers {
		select {
		case progress := <-progresses:
			mergeWorkerProgress(workerSpins, workerAccepted, progress)
		case result := <-results:
			workerResults[result.worker] = result
			mergeWorkerProgress(workerSpins, workerAccepted, collectionWorkerProgress{
				worker: result.worker, spins: result.spins, accepted: result.classAccepted,
			})
			completed++
			if result.err != nil && !errors.Is(result.err, context.Canceled) {
				cancelWorkers()
			}
		}
		spins, acceptedByClass, accepted := aggregateWorkerProgress(workerSpins, workerAccepted)
		if c.Reporter != nil && spins >= nextReport {
			c.Reporter.Report(collectionProgressCountsEvent(betMode, spins, plan.Intent.Classes, acceptedByClass, requested))
			lastReportedSpins, lastReportedAccepted = spins, accepted
			for nextReport <= spins {
				if ^uint64(0)-nextReport < plan.Plan.Collection.BatchSize {
					nextReport = ^uint64(0)
					break
				}
				nextReport += plan.Plan.Collection.BatchSize
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return CollectedProblem{}, nil, err
	}
	for worker := range workerResults {
		if workerResults[worker].err != nil && !errors.Is(workerResults[worker].err, context.Canceled) {
			return CollectedProblem{}, nil, workerResults[worker].err
		}
	}

	sequence := uint64(0)
	for worker := range workerResults {
		result := workerResults[worker]
		collected.Spins += result.spins
		for _, accepted := range result.samples {
			accepted.sample.Sequence = sequence
			sequence++
			collected.Classes[accepted.classIndex].Samples = append(
				collected.Classes[accepted.classIndex].Samples,
				accepted.sample,
			)
		}
	}
	remaining = requested - sequence
	if c.Reporter != nil && (collected.Spins != lastReportedSpins || sequence != lastReportedAccepted) {
		c.Reporter.Report(collectionProgressEvent(collected, requested, remaining))
	}

	if remaining == 0 {
		return collected, nil, nil
	}
	diagnostic := Diagnostic{
		Code:           DiagnosticCollectionInsufficient,
		Status:         StatusInfeasibleSupport,
		Message:        fmt.Sprintf("collection stopped after %d spins with %d requested samples still missing", collected.Spins, remaining),
		Representation: RepresentationAtomicBuckets,
	}
	for i, class := range collected.Classes {
		missing := class.Intent.Collect.Samples - uint64(len(class.Samples))
		if missing == 0 {
			continue
		}
		diagnostic.Causes = append(diagnostic.Causes, Cause{
			Summary:     fmt.Sprintf("class %q is missing %d samples", class.Intent.Name, missing),
			SourcePaths: []string{fmt.Sprintf("intents.%s.classes[%d].collect.samples", plan.Plan.Intent, i)},
			Metrics: []NamedValue{
				{Name: "requested", Value: float64(class.Intent.Collect.Samples), Unit: "samples"},
				{Name: "collected", Value: float64(len(class.Samples)), Unit: "samples"},
			},
		})
	}
	return collected, Diagnostics{diagnostic}, nil
}

type acceptedWorkerSample struct {
	classIndex int
	sample     CollectedSample
}

type collectionWorkerResult struct {
	worker        int
	spins         uint64
	samples       []acceptedWorkerSample
	classAccepted []uint64
	err           error
}

type collectionWorkerProgress struct {
	worker   int
	spins    uint64
	accepted []uint64
}

// collectWorker owns every mutable value it touches: one Machine, one set of
// local quotas, and one accepted-sample log. progress contains copied counters
// only; the coordinator is the sole Reporter caller.
func collectWorker(
	ctx context.Context,
	worker int,
	machine *problab.Machine,
	betMode int,
	classes []ClassIntent,
	predicates []classPredicate,
	tagger *legacyoptimizer.Tagger,
	quotas []uint64,
	maxSpins uint64,
	progressEvery uint64,
	progresses chan<- collectionWorkerProgress,
) collectionWorkerResult {
	result := collectionWorkerResult{
		worker:        worker,
		classAccepted: make([]uint64, len(classes)),
	}
	remaining := uint64(0)
	for _, quota := range quotas {
		remaining += quota
	}
	report := func() {
		progress := collectionWorkerProgress{
			worker: worker, spins: result.spins,
			accepted: append([]uint64(nil), result.classAccepted...),
		}
		select {
		case progresses <- progress:
		case <-ctx.Done():
		}
	}

	for result.spins < maxSpins && remaining > 0 {
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
		snapshot, err := machine.SnapshotCore()
		if err != nil {
			result.err = fmt.Errorf("snapshot raw machine for worker %d before local spin %d: %w", worker, result.spins, err)
			return result
		}
		spin := machine.SpinInternal(betMode)
		if spin == nil || spin.Bet <= 0 {
			result.err = fmt.Errorf("raw machine for worker %d returned an invalid spin result at local sequence %d", worker, result.spins)
			return result
		}
		win := float64(spin.TotalWin) / float64(spin.Bet)
		tags := uint64(0)
		if tagger != nil {
			tags = tagger.Tagging(spin)
		}
		result.spins++
		for classIndex, class := range classes {
			if result.classAccepted[classIndex] >= quotas[classIndex] {
				continue
			}
			if !classAccepts(class, predicates[classIndex], tags, win) {
				continue
			}
			result.samples = append(result.samples, acceptedWorkerSample{
				classIndex: classIndex,
				sample: CollectedSample{
					ClassID: class.Name, Win: win,
					Snapshot: append([]byte(nil), snapshot...),
				},
			})
			result.classAccepted[classIndex]++
			remaining--
			break
		}
		if result.spins%progressEvery == 0 || remaining == 0 {
			report()
		}
	}
	if result.spins%progressEvery != 0 && remaining > 0 {
		report()
	}
	return result
}

// partitionShare deterministically assigns both quota and spin-budget
// remainders to lower worker indexes. Shares always sum to total.
func partitionShare(total uint64, workers, worker int) uint64 {
	count := uint64(workers)
	share := total / count
	if uint64(worker) < total%count {
		share++
	}
	return share
}

// partitionClassQuotas gives every worker either floor or ceil of each Class
// quota. Remainders rotate across Classes instead of always accumulating on
// worker zero. Consequently each worker's total requested load is exactly the
// same partitionShare of the aggregate request, aligned with MaxSpins shares.
func partitionClassQuotas(classes []ClassIntent, workers int) [][]uint64 {
	quotas := make([][]uint64, workers)
	for worker := range workers {
		quotas[worker] = make([]uint64, len(classes))
	}
	remainderCursor := 0
	for classIndex, class := range classes {
		base := class.Collect.Samples / uint64(workers)
		for worker := range workers {
			quotas[worker][classIndex] = base
		}
		remainder := int(class.Collect.Samples % uint64(workers))
		for offset := range remainder {
			worker := (remainderCursor + offset) % workers
			quotas[worker][classIndex]++
		}
		remainderCursor = (remainderCursor + remainder) % workers
	}
	return quotas
}

func mergeWorkerProgress(spins []uint64, accepted [][]uint64, progress collectionWorkerProgress) {
	if progress.spins > spins[progress.worker] {
		spins[progress.worker] = progress.spins
	}
	for classIndex, count := range progress.accepted {
		if count > accepted[progress.worker][classIndex] {
			accepted[progress.worker][classIndex] = count
		}
	}
}

func aggregateWorkerProgress(spins []uint64, accepted [][]uint64) (uint64, []uint64, uint64) {
	totalSpins := uint64(0)
	acceptedByClass := make([]uint64, len(accepted[0]))
	totalAccepted := uint64(0)
	for worker := range spins {
		totalSpins += spins[worker]
		for classIndex, count := range accepted[worker] {
			acceptedByClass[classIndex] += count
			totalAccepted += count
		}
	}
	return totalSpins, acceptedByClass, totalAccepted
}

// collectionProgressEvent copies the complete declaration-ordered Class quota
// state into one event. Emitting one aggregate snapshot per reporting cadence
// avoids multiplying Reporter calls by the number of Classes, while still
// giving a UI enough information to maintain one independent progress bar per
// Class. No slice in the event aliases Collector-owned sample storage.
func collectionProgressEvent(collected CollectedProblem, requested, _ uint64) StageEvent {
	accepted := make([]uint64, len(collected.Classes))
	for i, class := range collected.Classes {
		accepted[i] = uint64(len(class.Samples))
	}
	return collectionProgressCountsEvent(collected.BetMode, collected.Spins, classIntents(collected.Classes), accepted, requested)
}

func collectionProgressCountsEvent(betMode int, spins uint64, intents []ClassIntent, accepted []uint64, requested uint64) StageEvent {
	classes := make([]ClassCollectionProgress, len(intents))
	totalAccepted := uint64(0)
	for i, class := range intents {
		classes[i] = ClassCollectionProgress{Name: class.Name, Accepted: accepted[i], Requested: class.Collect.Samples}
		totalAccepted += accepted[i]
	}
	return StageEvent{
		Stage: "collection-progress", BetMode: betMode, State: "progress",
		Spins: spins, Accepted: totalAccepted, Requested: requested,
		Classes: classes,
	}
}

func classIntents(classes []CollectedClass) []ClassIntent {
	intents := make([]ClassIntent, len(classes))
	for i, class := range classes {
		intents[i] = class.Intent
	}
	return intents
}

// optimizerBetUnit resolves a zero-based mode against the frozen catalog. It
// deliberately obtains the unit from Problab rather than accepting a second
// caller-owned copy that could normalize payouts with the wrong denominator.
func optimizerBetUnit(lab *problab.Problab, gid spec.GID, betMode int) (int, error) {
	summaries, err := lab.Summary()
	if err != nil {
		return 0, fmt.Errorf("read Problab catalog summary: %w", err)
	}
	for _, summary := range summaries {
		if summary.GID != gid {
			continue
		}
		if betMode < 0 || betMode >= len(summary.BetUnits) {
			return 0, fmt.Errorf("bet mode %d is out of range for game %d", betMode, gid)
		}
		return summary.BetUnits[betMode], nil
	}
	return 0, fmt.Errorf("game %d is not present in the Problab catalog", gid)
}

// compileTagPredicates constructs one shared tag evaluation order and a pair
// of masks per Class. The tag registry is reused from the legacy optimizer so
// custom RegisterTag integrations keep working during the additive migration.
func compileTagPredicates(classes []ClassIntent) (*legacyoptimizer.Tagger, []classPredicate, error) {
	tags := make([]string, 0)
	seen := make(map[string]struct{})
	for _, class := range classes {
		for _, tag := range append(append([]string(nil), class.Collect.Tags.Matches...), class.Collect.Tags.Mismatches...) {
			if _, exists := seen[tag]; exists {
				continue
			}
			seen[tag] = struct{}{}
			tags = append(tags, tag)
		}
	}
	predicates := make([]classPredicate, len(classes))
	if len(tags) == 0 {
		return nil, predicates, nil
	}
	tagger, err := legacyoptimizer.GetTagger(tags...)
	if err != nil {
		return nil, nil, fmt.Errorf("compile class tags: %w", err)
	}
	for i, class := range classes {
		predicates[i] = classPredicate{
			matchMask:    tagger.Mask(class.Collect.Tags.Matches...),
			mismatchMask: tagger.Mask(class.Collect.Tags.Mismatches...),
		}
	}
	return tagger, predicates, nil
}

// classAccepts evaluates the closed payout range, positive tag conjunction,
// and negative tag conjunction without changing first-match ownership.
func classAccepts(class ClassIntent, predicate classPredicate, tags uint64, win float64) bool {
	if win < class.Collect.WinRange.Lower() || win > class.Collect.WinRange.Upper() {
		return false
	}
	if tags&predicate.matchMask != predicate.matchMask {
		return false
	}
	return tags&predicate.mismatchMask == 0
}

// configDiagnostic turns an execution-policy or predicate validation failure
// into the public expected-result contract used by stage gates.
func configDiagnostic(message string) Diagnostic {
	return Diagnostic{
		Code:           DiagnosticConfigInvalid,
		Status:         StatusInfeasibleConfig,
		Message:        message,
		Representation: RepresentationAtomicBuckets,
	}
}
