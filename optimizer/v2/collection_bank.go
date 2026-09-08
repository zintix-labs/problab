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
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zintix-labs/problab"
)

func resolveRunPath(workingDirectory, configured string) (string, error) {
	if strings.TrimSpace(workingDirectory) == "" || strings.IndexByte(workingDirectory, 0) >= 0 {
		return "", fmt.Errorf("resolve optimizer v2 path: captured working directory is invalid")
	}
	if !filepath.IsAbs(workingDirectory) {
		return "", fmt.Errorf("resolve optimizer v2 path: captured working directory %q is not absolute", workingDirectory)
	}
	if strings.TrimSpace(configured) == "" {
		return "", fmt.Errorf("resolve optimizer v2 path: configured path must not be blank")
	}
	if strings.IndexByte(configured, 0) >= 0 {
		return "", fmt.Errorf("resolve optimizer v2 path: configured path must not contain NUL")
	}
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured), nil
	}
	return filepath.Clean(filepath.Join(workingDirectory, configured)), nil
}

type parsedCollectionStreamCursor struct {
	Cursor     uint64
	Recognized bool
	Distinct   bool
}

func parseCollectionBankStreamCursor(path string) parsedCollectionStreamCursor {
	base := filepath.Base(path)
	distinct := false
	switch {
	case strings.HasSuffix(base, ".distinct.bin"):
		distinct = true
		base = strings.TrimSuffix(base, ".distinct.bin")
	case strings.HasSuffix(base, ".bin"):
		base = strings.TrimSuffix(base, ".bin")
	default:
		return parsedCollectionStreamCursor{}
	}
	const prefix = "seed_bank_"
	if !strings.HasPrefix(base, prefix) {
		return parsedCollectionStreamCursor{}
	}
	payload := strings.TrimPrefix(base, prefix)
	marker := strings.LastIndex(payload, "_s")
	if marker <= 0 || marker+2 >= len(payload) {
		return parsedCollectionStreamCursor{}
	}
	timestamp, cursorText := payload[:marker], payload[marker+2:]
	if !decimalDigits(timestamp) || !decimalDigits(cursorText) {
		return parsedCollectionStreamCursor{}
	}
	cursor, err := strconv.ParseUint(cursorText, 10, 64)
	if err != nil {
		return parsedCollectionStreamCursor{}
	}
	return parsedCollectionStreamCursor{Cursor: cursor, Recognized: true, Distinct: distinct}
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range []byte(value) {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func parseConfiguredCollectionStreamCursors(paths []string) ([]parsedCollectionStreamCursor, uint64) {
	parsed := make([]parsedCollectionStreamCursor, len(paths))
	maximum := uint64(0)
	for i, path := range paths {
		parsed[i] = parseCollectionBankStreamCursor(path)
		if parsed[i].Recognized && parsed[i].Cursor > maximum {
			maximum = parsed[i].Cursor
		}
	}
	return parsed, maximum
}

func collectionBankPath(
	workingDirectory string,
	plan ResolvedPlan,
	betMode int,
	unixTimestamp int64,
	nextStreamOrdinal uint64,
	distinct bool,
) (string, error) {
	if betMode < 0 {
		return "", fmt.Errorf("collection bank path: bet mode must be non-negative")
	}
	if unixTimestamp < 0 {
		return "", fmt.Errorf("collection bank path: Unix timestamp must be non-negative")
	}
	outputRoot, err := resolveRunPath(workingDirectory, plan.Plan.Output.Directory)
	if err != nil {
		return "", fmt.Errorf("collection bank path: %w", err)
	}
	suffix := ".bin"
	if distinct {
		suffix = ".distinct.bin"
	}
	return filepath.Clean(filepath.Join(
		outputRoot,
		"collected",
		"game_"+strconv.FormatUint(uint64(plan.Plan.Target.Game), 10),
		"mode_"+strconv.Itoa(betMode),
		"seed_bank_"+strconv.FormatInt(unixTimestamp, 10)+"_s"+strconv.FormatUint(nextStreamOrdinal, 10)+suffix,
	)), nil
}

type replayIdentitySet map[string]struct{}

func (set replayIdentitySet) Has(snapshot []byte) bool {
	_, exists := set[string(snapshot)]
	return exists
}

// AddOwned uses the safe []byte-to-string conversion so the map key owns an
// immutable copy even though the record reader reuses its input buffer.
func (set replayIdentitySet) AddOwned(snapshot []byte) bool {
	identity := string(snapshot)
	if _, exists := set[identity]; exists {
		return false
	}
	set[identity] = struct{}{}
	return true
}

var collectionDuplicateOriginOrder = [...]CollectionDuplicateOrigin{
	CollectionDuplicateReplayFresh,
	CollectionDuplicateFreshFresh,
	CollectionDuplicateReplayReplay,
}

func auditCollectedReplayIdentities(
	ctx context.Context,
	collected CollectedProblem,
) (CollectionDuplicateAudit, CollectedProblem, error) {
	if ctx == nil {
		return CollectionDuplicateAudit{}, CollectedProblem{}, fmt.Errorf("audit collected replay identities: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return CollectionDuplicateAudit{}, CollectedProblem{}, err
	}

	recovery := collected
	recovery.Classes = make([]CollectedClass, len(collected.Classes))
	recovery.Evidence = cloneCollectionEvidence(collected.Evidence)
	audit := CollectionDuplicateAudit{}
	totalOrigins := make(map[CollectionDuplicateOrigin]uint64, len(collectionDuplicateOriginOrder))

	for classIndex, class := range collected.Classes {
		if err := ctx.Err(); err != nil {
			return CollectionDuplicateAudit{}, CollectedProblem{}, err
		}
		recoveryClass := CollectedClass{
			Intent:  cloneClassIntent(class.Intent),
			Samples: make([]CollectedSample, 0, len(class.Samples)),
		}
		classReport := CollectionClassDuplicateReport{
			Name:    class.Intent.Name,
			Records: uint64(len(class.Samples)),
		}
		classOrigins := make(map[CollectionDuplicateOrigin]uint64, len(collectionDuplicateOriginOrder))
		seen := make(map[string]uint64, len(class.Samples))
		for _, sample := range class.Samples {
			if err := ctx.Err(); err != nil {
				return CollectionDuplicateAudit{}, CollectedProblem{}, err
			}
			identity := string(sample.Snapshot)
			firstSequence, duplicate := seen[identity]
			if !duplicate {
				seen[identity] = sample.Sequence
				recoveryClass.Samples = append(recoveryClass.Samples, sample)
				continue
			}
			origin, err := collectionDuplicateOrigin(firstSequence, sample.Sequence, collected.Evidence.ReplayAccepted)
			if err != nil {
				return CollectionDuplicateAudit{}, CollectedProblem{}, fmt.Errorf("audit class %q: %w", class.Intent.Name, err)
			}
			classReport.Duplicates, err = checkedAddUint64(classReport.Duplicates, 1, "Class duplicate identities")
			if err != nil {
				return CollectionDuplicateAudit{}, CollectedProblem{}, err
			}
			classOrigins[origin], err = checkedAddUint64(classOrigins[origin], 1, "Class duplicate origin")
			if err != nil {
				return CollectionDuplicateAudit{}, CollectedProblem{}, err
			}
			totalOrigins[origin], err = checkedAddUint64(totalOrigins[origin], 1, "collection duplicate origin")
			if err != nil {
				return CollectionDuplicateAudit{}, CollectedProblem{}, err
			}
		}
		recovery.Classes[classIndex] = recoveryClass
		var err error
		audit.Records, err = checkedAddUint64(audit.Records, classReport.Records, "collection duplicate audit records")
		if err != nil {
			return CollectionDuplicateAudit{}, CollectedProblem{}, err
		}
		audit.Duplicates, err = checkedAddUint64(audit.Duplicates, classReport.Duplicates, "collection duplicate audit duplicates")
		if err != nil {
			return CollectionDuplicateAudit{}, CollectedProblem{}, err
		}
		classReport.UniqueRecords = classReport.Records - classReport.Duplicates
		classReport.DuplicateRate = duplicateRate(classReport.Duplicates, classReport.Records)
		if classReport.Duplicates > 0 {
			classReport.Origins = orderedDuplicateOriginReports(classOrigins)
			audit.Classes = append(audit.Classes, classReport)
		}
	}

	audit.UniqueRecords = audit.Records - audit.Duplicates
	audit.DuplicateRate = duplicateRate(audit.Duplicates, audit.Records)
	audit.Origins = orderedDuplicateOriginReports(totalOrigins)
	return audit, recovery, nil
}

func collectionDuplicateOrigin(firstSequence, laterSequence, replayAccepted uint64) (CollectionDuplicateOrigin, error) {
	switch {
	case firstSequence < replayAccepted && laterSequence >= replayAccepted:
		return CollectionDuplicateReplayFresh, nil
	case firstSequence >= replayAccepted && laterSequence >= replayAccepted:
		return CollectionDuplicateFreshFresh, nil
	case firstSequence < replayAccepted && laterSequence < replayAccepted:
		return CollectionDuplicateReplayReplay, nil
	default:
		return "", fmt.Errorf("duplicate sequence provenance is reversed: first=%d later=%d replay_accepted=%d", firstSequence, laterSequence, replayAccepted)
	}
}

func orderedDuplicateOriginReports(counts map[CollectionDuplicateOrigin]uint64) []CollectionDuplicateOriginReport {
	reports := make([]CollectionDuplicateOriginReport, 0, len(collectionDuplicateOriginOrder))
	for _, origin := range collectionDuplicateOriginOrder {
		if count := counts[origin]; count > 0 {
			reports = append(reports, CollectionDuplicateOriginReport{Origin: origin, Duplicates: count})
		}
	}
	return reports
}

func duplicateRate(duplicates, records uint64) float64 {
	if records == 0 {
		return 0
	}
	return float64(duplicates) / float64(records)
}

func cloneCollectionDuplicateAudit(audit CollectionDuplicateAudit) CollectionDuplicateAudit {
	cloned := audit
	cloned.Origins = append([]CollectionDuplicateOriginReport(nil), audit.Origins...)
	cloned.Classes = make([]CollectionClassDuplicateReport, len(audit.Classes))
	for i, class := range audit.Classes {
		cloned.Classes[i] = class
		cloned.Classes[i].Origins = append([]CollectionDuplicateOriginReport(nil), class.Origins...)
	}
	return cloned
}

func collectionDuplicateDiagnostic(plan ResolvedPlan, audit CollectionDuplicateAudit, recoveryPath string) Diagnostic {
	diagnostic := Diagnostic{
		Code:   DiagnosticDuplicateReplayIdentity,
		Status: StatusInfeasibleSupport,
		Message: fmt.Sprintf(
			"collection found %d duplicate replay identities among %d records; saved Class-local distinct recovery bank %q and stopped before Prepare without inline deduplication or quota refill",
			audit.Duplicates, audit.Records, recoveryPath,
		),
		Representation: RepresentationAtomicBuckets,
	}
	classIndexes := make(map[string]int, len(plan.Intent.Classes))
	for i, class := range plan.Intent.Classes {
		classIndexes[class.Name] = i
	}
	for _, class := range audit.Classes {
		originCounts := duplicateOriginCountMap(class.Origins)
		investigations := make([]string, 0, 3)
		if originCounts[CollectionDuplicateReplayFresh] > 0 {
			investigations = append(investigations, "inspect legacy or renamed bank cursor provenance, version compatibility, and third-party seed derivation")
		}
		if originCounts[CollectionDuplicateFreshFresh] > 0 {
			investigations = append(investigations, "inspect worker stream derivation, PRNG state collisions, and repeated states within a stream")
		}
		if originCounts[CollectionDuplicateReplayReplay] > 0 {
			investigations = append(investigations, "internal replay-dedup invariant violation; changing the root seed alone is not a sufficient remedy")
		}
		index := classIndexes[class.Name]
		sourcePath := fmt.Sprintf("intents.%s.classes[%d]", plan.Plan.Intent, index)
		diagnostic.SourcePaths = append(diagnostic.SourcePaths, sourcePath)
		diagnostic.Causes = append(diagnostic.Causes, Cause{
			Summary: fmt.Sprintf(
				"class %q has %d duplicate identities (%d unique of %d); provenance is investigative evidence only: %s",
				class.Name, class.Duplicates, class.UniqueRecords, class.Records, strings.Join(investigations, "; "),
			),
			SourcePaths: []string{sourcePath},
			Metrics: []NamedValue{
				{Name: "records", Value: float64(class.Records), Unit: "samples"},
				{Name: "unique_records", Value: float64(class.UniqueRecords), Unit: "samples"},
				{Name: "duplicates", Value: float64(class.Duplicates), Unit: "samples"},
				{Name: "duplicate_rate", Value: class.DuplicateRate},
				{Name: "replay_fresh", Value: float64(originCounts[CollectionDuplicateReplayFresh]), Unit: "samples"},
				{Name: "fresh_fresh", Value: float64(originCounts[CollectionDuplicateFreshFresh]), Unit: "samples"},
				{Name: "replay_replay", Value: float64(originCounts[CollectionDuplicateReplayReplay]), Unit: "samples"},
			},
		})
	}
	return diagnostic
}

func duplicateOriginCountMap(reports []CollectionDuplicateOriginReport) map[CollectionDuplicateOrigin]uint64 {
	counts := make(map[CollectionDuplicateOrigin]uint64, len(reports))
	for _, report := range reports {
		counts[report.Origin] = report.Duplicates
	}
	return counts
}

type replayRecordAction uint8

const (
	replayRecordContinue replayRecordAction = iota
	replayRecordStopQuotasFull
)

type replayRecordLoopEnd string

const (
	replayLoopExhausted  replayRecordLoopEnd = "EXHAUSTED"
	replayLoopQuotasFull replayRecordLoopEnd = "QUOTAS_FULL"
)

func replayCollectionRecords(
	ctx context.Context,
	reader io.Reader,
	totalRecords uint64,
	snapshotLen int,
	visit func(recordIndex uint64, snapshot []byte) (replayRecordAction, error),
) (uint64, replayRecordLoopEnd, error) {
	if ctx == nil {
		return 0, "", fmt.Errorf("replay collection records: context is nil")
	}
	if reader == nil || visit == nil || snapshotLen <= 0 {
		return 0, "", fmt.Errorf("replay collection records: invalid reader contract")
	}
	record := make([]byte, snapshotLen)
	for recordIndex := uint64(0); recordIndex < totalRecords; recordIndex++ {
		if err := ctx.Err(); err != nil {
			return recordIndex, "", err
		}
		if _, err := io.ReadFull(reader, record); err != nil {
			return recordIndex, "", err
		}
		recordsRead := recordIndex + 1
		action, err := visit(recordIndex, record)
		if err != nil {
			return recordsRead, "", err
		}
		switch action {
		case replayRecordContinue:
		case replayRecordStopQuotasFull:
			return recordsRead, replayLoopQuotasFull, nil
		default:
			return recordsRead, "", fmt.Errorf("replay collection records: visitor returned unknown action %d", action)
		}
	}
	return totalRecords, replayLoopExhausted, nil
}

type replaySourceIncompatibleError struct {
	record uint64
	cause  error
}

func (e *replaySourceIncompatibleError) Error() string {
	return fmt.Sprintf("record %d is incompatible with the current runtime: %v", e.record, e.cause)
}

func (e *replaySourceIncompatibleError) Unwrap() error { return e.cause }

type replayCollectionOperationalError struct{ cause error }

func (e *replayCollectionOperationalError) Error() string { return e.cause.Error() }
func (e *replayCollectionOperationalError) Unwrap() error { return e.cause }

func replayCollectionBanks(
	ctx context.Context,
	collector *Collector,
	plan ResolvedPlan,
	betMode int,
	replayMachine *problab.Machine,
	runtime collectionRuntime,
	collected *CollectedProblem,
	deficits collectionDeficits,
	seen replayIdentitySet,
	cursors []parsedCollectionStreamCursor,
) ([]CollectionReplaySourceReport, error) {
	if collector == nil || collector.Lab == nil || replayMachine == nil || collected == nil {
		return nil, fmt.Errorf("replay collection banks: incomplete runtime dependencies")
	}
	if ctx == nil || runtime.snapshotLen <= 0 || len(deficits) != len(collected.Classes) || len(runtime.predicates) != len(collected.Classes) {
		return nil, fmt.Errorf("replay collection banks: invalid collection state")
	}
	configured := plan.Plan.Collection.CollectedSeed
	if len(cursors) != len(configured) {
		return nil, fmt.Errorf("replay collection banks: %d cursor reports for %d configured sources", len(cursors), len(configured))
	}
	reports := make([]CollectionReplaySourceReport, len(configured))
	for i, configuredPath := range configured {
		resolved, err := resolveRunPath(collector.WorkingDirectory, configuredPath)
		if err != nil {
			return nil, fmt.Errorf("resolve collected_seed[%d]: %w", i, err)
		}
		reports[i] = CollectionReplaySourceReport{
			ConfiguredPath: configuredPath, ResolvedPath: resolved,
			StreamCursor: cursors[i].Cursor, StreamCursorRecognized: cursors[i].Recognized,
		}
		if !cursors[i].Recognized {
			reportReplayEvent(collector.Reporter, StageEvent{
				Stage: "collection-replay-cursor", State: "warning", BetMode: betMode,
				Path: resolved, SourceIndex: i + 1, SourceCount: len(configured),
				StreamCursor: 0, StreamCursorRecognized: false,
				Message: fmt.Sprintf("configured collected_seed %q has no recognized _s<cursor> filename; using logical stream cursor 0 as a best-effort fallback", configuredPath),
			})
		}
	}
	if len(reports) == 0 {
		return reports, nil
	}
	if deficitsFull(deficits) {
		markReplaySourcesNotOpened(reports, 0)
		reportReplaySourcesNotOpened(collector.Reporter, betMode, len(reports))
		return reports, nil
	}

	nextSequence, err := countCollectedSamples(*collected)
	if err != nil {
		return nil, err
	}
	currentMachine := replayMachine
	for sourceIndex := range reports {
		if deficitsFull(deficits) {
			remaining := len(reports) - sourceIndex
			markReplaySourcesNotOpened(reports, sourceIndex)
			reportReplaySourcesNotOpened(collector.Reporter, betMode, remaining)
			return reports, nil
		}
		report := &reports[sourceIndex]
		reportReplayEvent(collector.Reporter, StageEvent{
			Stage: "collection-replay", State: "started", BetMode: betMode,
			Path: report.ResolvedPath, SourceIndex: sourceIndex + 1, SourceCount: len(reports),
		})

		file, openErr := os.Open(report.ResolvedPath)
		if openErr != nil {
			completeSkippedReplaySource(collector.Reporter, betMode, sourceIndex, len(reports), report,
				fmt.Sprintf("open replay source: %v", openErr))
			continue
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			completeSkippedReplaySource(collector.Reporter, betMode, sourceIndex, len(reports), report,
				fmt.Sprintf("stat replay source: %v", statErr))
			continue
		}
		if info.Size() < 0 || info.Size()%int64(runtime.snapshotLen) != 0 {
			_ = file.Close()
			completeSkippedReplaySource(collector.Reporter, betMode, sourceIndex, len(reports), report,
				fmt.Sprintf("size %d is not divisible by snapshot length %d", info.Size(), runtime.snapshotLen))
			continue
		}
		report.TotalRecords = uint64(info.Size() / int64(runtime.snapshotLen))

		recordsRead, loopEnd, replayErr := replayCollectionRecords(
			ctx,
			bufio.NewReader(file),
			report.TotalRecords,
			runtime.snapshotLen,
			func(recordIndex uint64, snapshot []byte) (replayRecordAction, error) {
				report.Records = recordIndex + 1
				if !seen.AddOwned(snapshot) {
					var counterErr error
					report.Duplicates, counterErr = checkedAddUint64(report.Duplicates, 1, "replay duplicate records")
					if counterErr != nil {
						return replayRecordContinue, &replayCollectionOperationalError{cause: counterErr}
					}
					reportReplayProgress(collector.Reporter, plan, betMode, sourceIndex, report, *collected)
					return replayRecordContinue, nil
				}
				if restoreErr := currentMachine.RestoreCore(snapshot); restoreErr != nil {
					report.Rejected++
					reportReplayProgress(collector.Reporter, plan, betMode, sourceIndex, report, *collected)
					return replayRecordContinue, &replaySourceIncompatibleError{record: recordIndex, cause: fmt.Errorf("restore Core snapshot: %w", restoreErr)}
				}
				spin := currentMachine.SpinInternal(betMode)
				if spin == nil || spin.Bet <= 0 {
					report.Rejected++
					reportReplayProgress(collector.Reporter, plan, betMode, sourceIndex, report, *collected)
					return replayRecordContinue, &replaySourceIncompatibleError{record: recordIndex, cause: fmt.Errorf("invalid replay spin result")}
				}
				win := float64(spin.TotalWin) / float64(spin.Bet)
				tags := uint64(0)
				if runtime.tagger != nil {
					tags = runtime.tagger.Tagging(spin)
				}
				classIndex := firstAcceptingClass(plan.Intent.Classes, runtime.predicates, deficits, tags, win)
				if classIndex < 0 {
					report.Unmatched++
					reportReplayProgress(collector.Reporter, plan, betMode, sourceIndex, report, *collected)
					return replayRecordContinue, nil
				}
				if nextSequence == math.MaxUint64 {
					return replayRecordContinue, &replayCollectionOperationalError{cause: fmt.Errorf("replay collection Sequence overflow")}
				}
				collected.Classes[classIndex].Samples = append(collected.Classes[classIndex].Samples, CollectedSample{
					ClassID: plan.Intent.Classes[classIndex].Name,
					Win:     win, Snapshot: append([]byte(nil), snapshot...), Sequence: nextSequence,
				})
				nextSequence++
				deficits[classIndex]--
				report.Accepted++
				classEvidence := &collected.Evidence.Classes[classIndex]
				classEvidence.ReplayAccepted++
				classEvidence.Accepted++
				reportReplayProgress(collector.Reporter, plan, betMode, sourceIndex, report, *collected)
				if deficitsFull(deficits) {
					return replayRecordStopQuotasFull, nil
				}
				return replayRecordContinue, nil
			},
		)
		closeErr := file.Close()
		report.Records = recordsRead
		if err := assertReplaySourcePartition(*report); err != nil {
			return nil, fmt.Errorf("replay source %q: %w", report.ResolvedPath, err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		var incompatible *replaySourceIncompatibleError
		var operational *replayCollectionOperationalError
		switch {
		case errors.As(replayErr, &incompatible):
			completeIncompatibleReplaySource(collector.Reporter, betMode, sourceIndex, len(reports), report, incompatible.Error())
			if sourceIndex+1 < len(reports) {
				currentMachine, err = newReplayMachine(collector, plan)
				if err != nil {
					return nil, err
				}
			}
			continue
		case errors.As(replayErr, &operational):
			return nil, operational.cause
		case replayErr != nil:
			// Once open/stat/framing have succeeded, reader failures preserve the
			// valid prefix and mark only this source incompatible.
			completeIncompatibleReplaySource(
				collector.Reporter, betMode, sourceIndex, len(reports), report,
				fmt.Sprintf("record %d read failed: %v", recordsRead, replayErr),
			)
			continue
		case closeErr != nil:
			completeIncompatibleReplaySource(
				collector.Reporter, betMode, sourceIndex, len(reports), report,
				fmt.Sprintf("close replay source after %d records: %v", recordsRead, closeErr),
			)
			continue
		}

		report.State = CollectionReplayUsed
		switch loopEnd {
		case replayLoopExhausted:
			report.EndReason = CollectionReplayEndExhausted
		case replayLoopQuotasFull:
			report.EndReason = CollectionReplayEndQuotasFull
		default:
			return nil, fmt.Errorf("replay source %q ended without a typed reason", report.ResolvedPath)
		}
		reportReplayEvent(collector.Reporter, replayTerminalEvent(betMode, sourceIndex, len(reports), *report, "completed"))
		if loopEnd == replayLoopQuotasFull {
			remaining := len(reports) - sourceIndex - 1
			markReplaySourcesNotOpened(reports, sourceIndex+1)
			if remaining > 0 {
				reportReplaySourcesNotOpened(collector.Reporter, betMode, remaining)
			}
			return reports, nil
		}
	}
	return reports, nil
}

func newReplayMachine(collector *Collector, plan ResolvedPlan) (*problab.Machine, error) {
	machine, err := collector.Lab.NewUnoptimizedMachineWithSeedBytes(
		plan.Plan.Target.Game, plan.Plan.Seed.Bytes(), true,
	)
	if err != nil {
		return nil, fmt.Errorf("recreate raw optimizer replay machine: %w", err)
	}
	return machine, nil
}

func reportReplayProgress(reporter Reporter, plan ResolvedPlan, betMode, sourceIndex int, report *CollectionReplaySourceReport, collected CollectedProblem) {
	if reporter == nil || report.Records == 0 || report.Records%plan.Plan.Collection.BatchSize != 0 {
		return
	}
	reportReplayEvent(reporter, StageEvent{
		Stage: "collection-replay", State: "progress", BetMode: betMode,
		Path: report.ResolvedPath, SourceIndex: sourceIndex + 1, SourceCount: len(plan.Plan.Collection.CollectedSeed),
		Records: report.Records, TotalRecords: report.TotalRecords, Accepted: report.Accepted,
		Duplicates: report.Duplicates, Unmatched: report.Unmatched, Rejected: report.Rejected,
		Classes: currentClassProgress(collected),
	})
}

func completeSkippedReplaySource(reporter Reporter, betMode, sourceIndex, sourceCount int, report *CollectionReplaySourceReport, warning string) {
	report.State = CollectionReplaySkipped
	report.EndReason = CollectionReplayEndSourceSkipped
	report.Warning = warning
	reportReplayEvent(reporter, replayTerminalEvent(betMode, sourceIndex, sourceCount, *report, "warning"))
}

func completeIncompatibleReplaySource(reporter Reporter, betMode, sourceIndex, sourceCount int, report *CollectionReplaySourceReport, warning string) {
	report.State = CollectionReplayIncompatible
	report.EndReason = CollectionReplayEndIncompatible
	report.Warning = warning
	reportReplayEvent(reporter, replayTerminalEvent(betMode, sourceIndex, sourceCount, *report, "warning"))
}

func replayTerminalEvent(betMode, sourceIndex, sourceCount int, report CollectionReplaySourceReport, state string) StageEvent {
	return StageEvent{
		Stage: "collection-replay", State: state, BetMode: betMode,
		Path: report.ResolvedPath, SourceIndex: sourceIndex + 1, SourceCount: sourceCount,
		Records: report.Records, TotalRecords: report.TotalRecords, Accepted: report.Accepted,
		Duplicates: report.Duplicates, Unmatched: report.Unmatched, Rejected: report.Rejected,
		EndReason: report.EndReason, Message: report.Warning,
	}
}

func reportReplayEvent(reporter Reporter, event StageEvent) {
	if reporter != nil {
		reporter.Report(event)
	}
}

func markReplaySourcesNotOpened(reports []CollectionReplaySourceReport, start int) {
	for i := start; i < len(reports); i++ {
		reports[i].State = CollectionReplayNotOpened
		reports[i].EndReason = CollectionReplayEndQuotasFull
	}
}

func reportReplaySourcesNotOpened(reporter Reporter, betMode, remaining int) {
	if reporter == nil || remaining <= 0 {
		return
	}
	reporter.Report(StageEvent{
		Stage: "collection-replay", State: "info", BetMode: betMode,
		EndReason: CollectionReplayEndQuotasFull, RemainingSources: remaining,
		Message: fmt.Sprintf("remaining %d sources not opened: quotas full", remaining),
	})
}

func deficitsFull(deficits collectionDeficits) bool {
	for _, deficit := range deficits {
		if deficit != 0 {
			return false
		}
	}
	return true
}

func assertReplaySourcePartition(report CollectionReplaySourceReport) error {
	partition, err := checkedAddUint64(report.Accepted, report.Duplicates, "replay source result partition")
	if err != nil {
		return err
	}
	partition, err = checkedAddUint64(partition, report.Unmatched, "replay source result partition")
	if err != nil {
		return err
	}
	partition, err = checkedAddUint64(partition, report.Rejected, "replay source result partition")
	if err != nil {
		return err
	}
	if report.Records != partition {
		return fmt.Errorf("record partition mismatch: records=%d accepted=%d duplicates=%d unmatched=%d rejected=%d", report.Records, report.Accepted, report.Duplicates, report.Unmatched, report.Rejected)
	}
	return nil
}

type collectionBankWriter struct {
	renamePath func(oldPath, newPath string) error
}

func (writer collectionBankWriter) Write(ctx context.Context, path string, collected CollectedProblem, partial, distinct bool) (CollectionBankReport, error) {
	if ctx == nil {
		return CollectionBankReport{}, fmt.Errorf("write collection bank: context is nil")
	}
	if strings.TrimSpace(path) == "" || strings.IndexByte(path, 0) >= 0 {
		return CollectionBankReport{}, fmt.Errorf("write collection bank: path is invalid")
	}
	if collected.SnapshotLength <= 0 {
		return CollectionBankReport{}, fmt.Errorf("write collection bank %q: snapshot length must be positive", path)
	}
	if err := ctx.Err(); err != nil {
		return CollectionBankReport{}, err
	}
	seedCount, err := validateCanonicalCollection(ctx, collected)
	if err != nil {
		return CollectionBankReport{}, fmt.Errorf("write collection bank %q: %w", path, err)
	}
	if seedCount > uint64(math.MaxInt64)/uint64(collected.SnapshotLength) {
		return CollectionBankReport{}, fmt.Errorf("write collection bank %q: byte size overflows int64", path)
	}
	expectedBytes := int64(seedCount * uint64(collected.SnapshotLength))
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return CollectionBankReport{}, fmt.Errorf("create collection bank directory %q: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, ".seed_bank-*")
	if err != nil {
		return CollectionBankReport{}, fmt.Errorf("create temporary collection bank in %q: %w", directory, err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()

	digest := sha256.New()
	destination := io.MultiWriter(temporary, digest)
	written := int64(0)
	err = visitCanonicalCollectionSamples(collected, func(_ int, _ int, sample CollectedSample) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := destination.Write(sample.Snapshot)
		if err != nil {
			return err
		}
		if n != len(sample.Snapshot) {
			return io.ErrShortWrite
		}
		written += int64(n)
		return nil
	})
	if err != nil {
		return CollectionBankReport{}, fmt.Errorf("stream temporary collection bank: %w", err)
	}
	if written != expectedBytes {
		return CollectionBankReport{}, fmt.Errorf("stream temporary collection bank: wrote %d bytes, expected %d", written, expectedBytes)
	}
	if err := temporary.Sync(); err != nil {
		return CollectionBankReport{}, fmt.Errorf("sync temporary collection bank: %w", err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		return CollectionBankReport{}, fmt.Errorf("chmod temporary collection bank: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return CollectionBankReport{}, fmt.Errorf("sync temporary collection bank metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return CollectionBankReport{}, fmt.Errorf("close temporary collection bank: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return CollectionBankReport{}, err
	}
	renamePath := writer.renamePath
	if renamePath == nil {
		renamePath = os.Rename
	}
	if err := renamePath(temporaryPath, path); err != nil {
		return CollectionBankReport{}, fmt.Errorf("replace collection bank %q: %w", path, err)
	}
	committed = true
	if err := syncCollectionBankDirectory(directory); err != nil {
		return CollectionBankReport{}, fmt.Errorf("sync collection bank directory %q: %w", directory, err)
	}
	return CollectionBankReport{
		Path: path, SHA256: hex.EncodeToString(digest.Sum(nil)), SeedLength: collected.SnapshotLength,
		SeedCount: seedCount, Bytes: expectedBytes, Partial: partial, Distinct: distinct,
		NextStreamOrdinal: collected.NextStreamOrdinal,
	}, nil
}

func syncCollectionBankDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return err
	}
	return handle.Close()
}

func validateCanonicalCollection(ctx context.Context, collected CollectedProblem) (uint64, error) {
	if collected.SnapshotLength <= 0 {
		return 0, fmt.Errorf("snapshot length must be positive")
	}
	seedCount := uint64(0)
	err := visitCanonicalCollectionSamples(collected, func(classIndex, sampleIndex int, sample CollectedSample) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(sample.Snapshot) == 0 || len(sample.Snapshot) != collected.SnapshotLength {
			return fmt.Errorf("class[%d] sample[%d] snapshot length=%d want=%d", classIndex, sampleIndex, len(sample.Snapshot), collected.SnapshotLength)
		}
		var err error
		seedCount, err = checkedAddUint64(seedCount, 1, "collection bank seed count")
		return err
	})
	return seedCount, err
}

func visitCanonicalCollectionSamples(collected CollectedProblem, visit func(classIndex int, sampleIndex int, sample CollectedSample) error) error {
	if visit == nil {
		return fmt.Errorf("canonical collection visitor is nil")
	}
	for classIndex, class := range collected.Classes {
		var previous uint64
		for sampleIndex, sample := range class.Samples {
			if sample.ClassID != class.Intent.Name {
				return fmt.Errorf("class[%d] sample[%d] id %q does not match %q", classIndex, sampleIndex, sample.ClassID, class.Intent.Name)
			}
			if sampleIndex > 0 && sample.Sequence <= previous {
				return fmt.Errorf("class[%d] sample[%d] Sequence %d is not strictly greater than %d", classIndex, sampleIndex, sample.Sequence, previous)
			}
			previous = sample.Sequence
			if err := visit(classIndex, sampleIndex, sample); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeCollectionDescriptor is the reserved decoupling boundary between raw
// Problab collection and future external optimization platforms. It is
// intentionally a no-op: no placeholder or sidecar file is emitted. A future
// descriptor can expose exact payout and seed-bank indexes without changing
// the raw bank's Class/Sequence order or consulting the wall clock again.
func writeCollectionDescriptor(_ context.Context, _ ResolvedPlan, _ CollectedProblem, _ CollectionBankReport) (string, error) {
	return "", nil
}

func buildCollectionRunReport(
	collected CollectedProblem,
	audit CollectionDuplicateAudit,
	bank CollectionBankReport,
) (CollectionRunReport, error) {
	if err := validateCollectionEvidence(collected); err != nil {
		return CollectionRunReport{}, err
	}
	if !filepath.IsAbs(bank.Path) || filepath.Clean(bank.Path) != bank.Path {
		return CollectionRunReport{}, fmt.Errorf("collection bank report path must be cleaned and absolute: %q", bank.Path)
	}
	requested := uint64(0)
	accepted := uint64(0)
	for _, class := range collected.Evidence.Classes {
		var err error
		requested, err = checkedAddUint64(requested, class.Requested, "collection report requested samples")
		if err != nil {
			return CollectionRunReport{}, err
		}
		accepted, err = checkedAddUint64(accepted, class.Accepted, "collection report accepted samples")
		if err != nil {
			return CollectionRunReport{}, err
		}
	}
	if err := validateCollectionDuplicateAudit(collected, accepted, audit); err != nil {
		return CollectionRunReport{}, err
	}
	parsedPath := parseCollectionBankStreamCursor(bank.Path)
	if !parsedPath.Recognized || parsedPath.Cursor != collected.NextStreamOrdinal || parsedPath.Distinct != bank.Distinct {
		return CollectionRunReport{}, fmt.Errorf(
			"collection bank path cursor/variant does not match report: recognized=%t cursor=%d/%d distinct=%t/%t",
			parsedPath.Recognized, parsedPath.Cursor, collected.NextStreamOrdinal, parsedPath.Distinct, bank.Distinct,
		)
	}
	if bank.NextStreamOrdinal != collected.NextStreamOrdinal {
		return CollectionRunReport{}, fmt.Errorf("collection bank next stream ordinal=%d want=%d", bank.NextStreamOrdinal, collected.NextStreamOrdinal)
	}
	expectedSeedCount := accepted
	if bank.Distinct {
		if audit.Duplicates == 0 || !bank.Partial {
			return CollectionRunReport{}, fmt.Errorf("distinct collection bank requires duplicates and partial=true")
		}
		expectedSeedCount = audit.UniqueRecords
	} else if audit.Duplicates != 0 {
		return CollectionRunReport{}, fmt.Errorf("normal collection bank cannot report %d duplicates", audit.Duplicates)
	}
	if bank.SeedLength != collected.SnapshotLength || bank.SeedCount != expectedSeedCount {
		return CollectionRunReport{}, fmt.Errorf(
			"collection bank report does not match collected support: seed_length=%d/%d seed_count=%d/%d",
			bank.SeedLength, collected.SnapshotLength, bank.SeedCount, expectedSeedCount,
		)
	}
	if bank.SeedCount > uint64(math.MaxInt64)/uint64(bank.SeedLength) || bank.Bytes != int64(bank.SeedCount*uint64(bank.SeedLength)) {
		return CollectionRunReport{}, fmt.Errorf("collection bank report byte size does not match seed dimensions")
	}
	return CollectionRunReport{
		Requested: requested, Accepted: accepted,
		Evidence:       cloneCollectionEvidence(collected.Evidence),
		DuplicateAudit: cloneCollectionDuplicateAudit(audit), Bank: bank,
	}, nil
}

func validateCollectionDuplicateAudit(collected CollectedProblem, accepted uint64, audit CollectionDuplicateAudit) error {
	if audit.Records != accepted || audit.Duplicates > audit.Records || audit.UniqueRecords != audit.Records-audit.Duplicates {
		return fmt.Errorf(
			"collection duplicate audit totals are inconsistent: records=%d accepted=%d unique=%d duplicates=%d",
			audit.Records, accepted, audit.UniqueRecords, audit.Duplicates,
		)
	}
	if audit.DuplicateRate != duplicateRate(audit.Duplicates, audit.Records) {
		return fmt.Errorf("collection duplicate audit rate is inconsistent")
	}
	if !validOrderedOriginPrefix(audit.Origins) {
		return fmt.Errorf("collection duplicate audit origins are not in canonical order")
	}
	originTotal := uint64(0)
	for _, origin := range audit.Origins {
		var err error
		originTotal, err = checkedAddUint64(originTotal, origin.Duplicates, "collection duplicate audit origin total")
		if err != nil {
			return err
		}
	}
	if originTotal != audit.Duplicates {
		return fmt.Errorf("collection duplicate audit origin total=%d want=%d", originTotal, audit.Duplicates)
	}
	classTotal := uint64(0)
	classOriginTotals := make(map[CollectionDuplicateOrigin]uint64, len(collectionDuplicateOriginOrder))
	lastClassIndex := -1
	for _, class := range audit.Classes {
		classIndex := -1
		for i, collectedClass := range collected.Classes {
			if collectedClass.Intent.Name == class.Name {
				classIndex = i
				break
			}
		}
		if classIndex <= lastClassIndex || class.Duplicates == 0 || class.Duplicates > class.Records || class.UniqueRecords != class.Records-class.Duplicates || class.DuplicateRate != duplicateRate(class.Duplicates, class.Records) {
			return fmt.Errorf("collection duplicate audit class %q is inconsistent or out of order", class.Name)
		}
		if class.Records != uint64(len(collected.Classes[classIndex].Samples)) {
			return fmt.Errorf("collection duplicate audit class %q records=%d want=%d", class.Name, class.Records, len(collected.Classes[classIndex].Samples))
		}
		lastClassIndex = classIndex
		classOrigins := uint64(0)
		if !validOrderedOriginPrefix(class.Origins) {
			return fmt.Errorf("collection duplicate audit class %q origins are not in canonical order", class.Name)
		}
		for _, origin := range class.Origins {
			var err error
			classOrigins, err = checkedAddUint64(classOrigins, origin.Duplicates, "Class duplicate audit origin total")
			if err != nil {
				return err
			}
			classOriginTotals[origin.Origin], err = checkedAddUint64(classOriginTotals[origin.Origin], origin.Duplicates, "aggregate Class duplicate origin total")
			if err != nil {
				return err
			}
		}
		if classOrigins != class.Duplicates {
			return fmt.Errorf("collection duplicate audit class %q origin total=%d want=%d", class.Name, classOrigins, class.Duplicates)
		}
		var err error
		classTotal, err = checkedAddUint64(classTotal, class.Duplicates, "collection duplicate audit Class total")
		if err != nil {
			return err
		}
	}
	if classTotal != audit.Duplicates {
		return fmt.Errorf("collection duplicate audit Class total=%d want=%d", classTotal, audit.Duplicates)
	}
	auditOriginTotals := duplicateOriginCountMap(audit.Origins)
	for _, origin := range collectionDuplicateOriginOrder {
		if classOriginTotals[origin] != auditOriginTotals[origin] {
			return fmt.Errorf("collection duplicate audit origin %s Class total=%d want=%d", origin, classOriginTotals[origin], auditOriginTotals[origin])
		}
	}
	return nil
}

func validOrderedOriginPrefix(reports []CollectionDuplicateOriginReport) bool {
	orderIndex := -1
	for _, report := range reports {
		if report.Duplicates == 0 {
			return false
		}
		found := -1
		for i, origin := range collectionDuplicateOriginOrder {
			if report.Origin == origin {
				found = i
				break
			}
		}
		if found <= orderIndex {
			return false
		}
		orderIndex = found
	}
	return true
}

func cloneCollectionEvidence(evidence CollectionEvidence) CollectionEvidence {
	cloned := evidence
	cloned.ReplaySources = append([]CollectionReplaySourceReport(nil), evidence.ReplaySources...)
	cloned.Classes = append([]CollectionClassEvidence(nil), evidence.Classes...)
	return cloned
}

func summarizeReplayEvidence(collected *CollectedProblem) error {
	if collected == nil {
		return fmt.Errorf("summarize replay evidence: collection is nil")
	}
	collected.Evidence.ReplayRecords = 0
	collected.Evidence.ReplayDuplicates = 0
	collected.Evidence.ReplayAccepted = 0
	for _, report := range collected.Evidence.ReplaySources {
		if err := assertReplaySourcePartition(report); err != nil {
			return fmt.Errorf("summarize replay source %q: %w", report.ResolvedPath, err)
		}
		var err error
		collected.Evidence.ReplayRecords, err = checkedAddUint64(collected.Evidence.ReplayRecords, report.Records, "aggregate replay records")
		if err != nil {
			return err
		}
		collected.Evidence.ReplayDuplicates, err = checkedAddUint64(collected.Evidence.ReplayDuplicates, report.Duplicates, "aggregate replay duplicates")
		if err != nil {
			return err
		}
		collected.Evidence.ReplayAccepted, err = checkedAddUint64(collected.Evidence.ReplayAccepted, report.Accepted, "aggregate replay accepted samples")
		if err != nil {
			return err
		}
	}
	return nil
}

func validateCollectionEvidence(collected CollectedProblem) error {
	if collected.SnapshotLength <= 0 {
		return fmt.Errorf("collection evidence: snapshot length must be positive")
	}
	if collected.Spins != collected.Evidence.FreshSpins {
		return fmt.Errorf("collection evidence: Spins=%d FreshSpins=%d", collected.Spins, collected.Evidence.FreshSpins)
	}
	if len(collected.Classes) != len(collected.Evidence.Classes) {
		return fmt.Errorf("collection evidence: %d Classes but %d Class reports", len(collected.Classes), len(collected.Evidence.Classes))
	}
	sourceRecords := uint64(0)
	sourceDuplicates := uint64(0)
	sourceAccepted := uint64(0)
	for i, source := range collected.Evidence.ReplaySources {
		if err := assertReplaySourcePartition(source); err != nil {
			return fmt.Errorf("collection evidence: replay source[%d]: %w", i, err)
		}
		if err := validateReplaySourceState(source); err != nil {
			return fmt.Errorf("collection evidence: replay source[%d]: %w", i, err)
		}
		var err error
		sourceRecords, err = checkedAddUint64(sourceRecords, source.Records, "source replay records")
		if err != nil {
			return err
		}
		sourceDuplicates, err = checkedAddUint64(sourceDuplicates, source.Duplicates, "source replay duplicates")
		if err != nil {
			return err
		}
		sourceAccepted, err = checkedAddUint64(sourceAccepted, source.Accepted, "source replay accepted samples")
		if err != nil {
			return err
		}
	}
	if sourceRecords != collected.Evidence.ReplayRecords || sourceDuplicates != collected.Evidence.ReplayDuplicates || sourceAccepted != collected.Evidence.ReplayAccepted {
		return fmt.Errorf("collection evidence: aggregate replay source totals do not match")
	}
	replayAccepted := uint64(0)
	freshAccepted := uint64(0)
	accepted := uint64(0)
	for i, class := range collected.Classes {
		evidence := collected.Evidence.Classes[i]
		if evidence.Name != class.Intent.Name || evidence.Requested != class.Intent.Collect.Samples {
			return fmt.Errorf("collection evidence: class[%d] identity/request mismatch", i)
		}
		decomposed, err := checkedAddUint64(evidence.ReplayAccepted, evidence.FreshAccepted, "Class accepted decomposition")
		if err != nil {
			return err
		}
		if evidence.Accepted != decomposed || evidence.Accepted != uint64(len(class.Samples)) {
			return fmt.Errorf("collection evidence: class %q accepted=%d replay=%d fresh=%d samples=%d", evidence.Name, evidence.Accepted, evidence.ReplayAccepted, evidence.FreshAccepted, len(class.Samples))
		}
		if evidence.Accepted > evidence.Requested {
			return fmt.Errorf("collection evidence: class %q accepted %d exceeds requested %d", evidence.Name, evidence.Accepted, evidence.Requested)
		}
		var previousSequence uint64
		for sampleIndex, sample := range class.Samples {
			if sample.ClassID != class.Intent.Name {
				return fmt.Errorf("collection evidence: class[%d] sample[%d] id %q does not match %q", i, sampleIndex, sample.ClassID, class.Intent.Name)
			}
			if len(sample.Snapshot) != collected.SnapshotLength {
				return fmt.Errorf("collection evidence: class[%d] sample[%d] snapshot length=%d want=%d", i, sampleIndex, len(sample.Snapshot), collected.SnapshotLength)
			}
			if sampleIndex > 0 && sample.Sequence <= previousSequence {
				return fmt.Errorf("collection evidence: class[%d] sample[%d] Sequence %d is not strictly greater than %d", i, sampleIndex, sample.Sequence, previousSequence)
			}
			previousSequence = sample.Sequence
		}
		replayAccepted, err = checkedAddUint64(replayAccepted, evidence.ReplayAccepted, "per-Class replay accepted samples")
		if err != nil {
			return err
		}
		freshAccepted, err = checkedAddUint64(freshAccepted, evidence.FreshAccepted, "per-Class fresh accepted samples")
		if err != nil {
			return err
		}
		accepted, err = checkedAddUint64(accepted, evidence.Accepted, "per-Class accepted samples")
		if err != nil {
			return err
		}
	}
	if replayAccepted != collected.Evidence.ReplayAccepted || freshAccepted != collected.Evidence.FreshAccepted {
		return fmt.Errorf("collection evidence: aggregate origin totals do not match Classes")
	}
	totalByOrigin, err := checkedAddUint64(replayAccepted, freshAccepted, "aggregate accepted decomposition")
	if err != nil {
		return err
	}
	if accepted != totalByOrigin {
		return fmt.Errorf("collection evidence: accepted total %d does not match origin total %d", accepted, totalByOrigin)
	}
	return nil
}

func validateReplaySourceState(source CollectionReplaySourceReport) error {
	if source.Records > source.TotalRecords && source.State != CollectionReplaySkipped {
		return fmt.Errorf("records %d exceed total records %d", source.Records, source.TotalRecords)
	}
	switch source.State {
	case CollectionReplayUsed:
		if source.EndReason != CollectionReplayEndExhausted && source.EndReason != CollectionReplayEndQuotasFull {
			return fmt.Errorf("USED source has end reason %q", source.EndReason)
		}
		if source.EndReason == CollectionReplayEndExhausted && source.Records != source.TotalRecords {
			return fmt.Errorf("EXHAUSTED source read %d of %d records", source.Records, source.TotalRecords)
		}
		if source.Warning != "" {
			return fmt.Errorf("USED source has a warning")
		}
	case CollectionReplayNotOpened:
		if source.EndReason != CollectionReplayEndQuotasFull || source.TotalRecords != 0 || source.Records != 0 || source.Warning != "" {
			return fmt.Errorf("NOT_OPENED source has nonzero evidence or an invalid end reason")
		}
	case CollectionReplaySkipped:
		if source.EndReason != CollectionReplayEndSourceSkipped || source.TotalRecords != 0 || source.Records != 0 || source.Warning == "" {
			return fmt.Errorf("SKIPPED source has invalid evidence")
		}
	case CollectionReplayIncompatible:
		if source.EndReason != CollectionReplayEndIncompatible || source.Warning == "" {
			return fmt.Errorf("INCOMPATIBLE source has invalid evidence")
		}
	default:
		return fmt.Errorf("unknown source state %q", source.State)
	}
	return nil
}

func countCollectedSamples(collected CollectedProblem) (uint64, error) {
	total := uint64(0)
	for _, class := range collected.Classes {
		var err error
		total, err = checkedAddUint64(total, uint64(len(class.Samples)), "collected sample count")
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func sumUint64s(values []uint64, label string) (uint64, error) {
	total := uint64(0)
	for _, value := range values {
		var err error
		total, err = checkedAddUint64(total, value, label)
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func checkedAddUint64(left, right uint64, label string) (uint64, error) {
	if math.MaxUint64-left < right {
		return 0, fmt.Errorf("%s overflows uint64", label)
	}
	return left + right, nil
}
