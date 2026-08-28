//go:build !poc

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

package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
)

const (
	collectionBarWidth     = 24
	collectionLogMilestone = 10
	collectionRedrawPeriod = 100 * time.Millisecond
	cliPipelineStageCount  = 8
)

// cliProgressReporter translates optimizer/v2's typed operational events into
// human-readable stderr output. cmd/opt intentionally emits no success/failure
// JSON, so this reporter and reportV2Outcome form its complete operator-facing
// presentation without coupling terminal formatting to optimizer internals.
//
// The output has exactly two levels, and the shape of a line tells you which:
//
//	[NN/08] <stage> (mode N)          <- a top-level pipeline stage; permanent,
//	success (1m10s)                      never rewritten. Its outcome is the
//	                                     bare "success (<dur>)" / "failed: ..."
//	                                     line directly beneath it.
//	  step 1 <substage> ... success (48ms)   <- a sub-step of the stage above,
//	  <indented collection table>              or that stage's own detail. The
//	                                           "..." is replaced in place by
//	                                           "success" once the step finishes.
//
// So a top-level stage is never relabelled "[Success]"; only its short outcome
// line changes between runs. A redirected stderr gets the same text without the
// in-place rewrites: sub-steps print their final "... success (<dur>)" line
// directly and collection falls back to bounded ten-percent milestone logs.
type cliProgressReporter struct {
	output         io.Writer
	interactive    bool
	renderedLines  int
	lastRedraw     time.Time
	lastMilestones map[string]int
	// substepIndex numbers the optimization sub-steps within the solve stage; it
	// is reset when that stage starts. pendingBody / pendingInline carry a
	// sub-step's "  step N <label> ..." text so its completed/failed line can
	// rewrite the same physical line on an interactive terminal.
	substepIndex  int
	pendingBody   string
	pendingInline bool
}

// newCLIProgressReporter detects a character-device stderr without importing a
// terminal package. Failure to inspect the file is conservative: the reporter
// switches to append-only milestone logs, which never emit ANSI cursor codes.
func newCLIProgressReporter(output io.Writer) *cliProgressReporter {
	reporter := &cliProgressReporter{
		output:         output,
		lastMilestones: make(map[string]int),
	}
	if file, ok := output.(*os.File); ok {
		if info, err := file.Stat(); err == nil {
			reporter.interactive = info.Mode()&os.ModeCharDevice != 0
		}
	}
	return reporter
}

// Report implements optimizer/v2.Reporter synchronously. Stage boundary events
// always produce one entry. Collection snapshots are rate-limited only at the
// presentation layer; the Collector still emits its deterministic configured
// batch cadence and other Reporter implementations can observe every event.
func (r *cliProgressReporter) Report(event optimizerv2.StageEvent) {
	if r == nil || r.output == nil {
		return
	}
	if event.Substage != "" {
		r.reportOptimizationSubstage(event)
		return
	}
	if event.Stage == "collection-progress" {
		r.reportCollection(event)
		return
	}

	switch event.State {
	case "info":
		if event.Stage == "expected-rtp" {
			_, _ = fmt.Fprintf(r.output, "[Info] expected RTP: %.12g\n", event.ExpectedRTP)
		} else if event.Message != "" {
			_, _ = fmt.Fprintf(r.output, "[Info] %s\n", event.Message)
		}
	case "started":
		if strings.HasPrefix(event.Stage, "collect[") {
			r.renderedLines = 0
			r.lastRedraw = time.Time{}
			clear(r.lastMilestones)
		}
		if strings.HasPrefix(event.Stage, "solve[") {
			r.substepIndex = 0
		}
		header := cliStageLabel(event.Stage) + cliModeSuffix(event.BetMode)
		if event.Message != "" {
			header += ": " + event.Message
		}
		if step, ok := cliPipelineStep(event.Stage); ok {
			header = fmt.Sprintf("[%02d/%02d] %s", step, cliPipelineStageCount, header)
		}
		// The header is always its own permanent line. A stage's outcome and any
		// sub-steps are the lines beneath it; nothing ever rewrites the header.
		_, _ = fmt.Fprintf(r.output, "%s\n", header)
		r.clearPending()
	case "completed":
		r.closePendingSubstep()
		_, _ = fmt.Fprintf(r.output, "success (%s)\n", formatStageDuration(event.Duration))
		r.clearPending()
	case "failed":
		r.closePendingSubstep()
		if event.Message == "" {
			_, _ = fmt.Fprintf(r.output, "failed (%s)\n", formatStageDuration(event.Duration))
		} else {
			_, _ = fmt.Fprintf(r.output, "failed: %s\n", event.Message)
		}
		r.clearPending()
	}
}

// stageBody returns the in-flight sub-step text, falling back to a freshly built
// value when no start event was observed (defensive; start always precedes
// finish in a real run) so the terminal line still names its step.
func (r *cliProgressReporter) stageBody(fallback string) string {
	if r.pendingBody != "" {
		return r.pendingBody
	}
	return fallback
}

// closePendingSubstep terminates an interactive sub-step line that never
// received its own completed event, so a stage outcome never lands mid-line.
func (r *cliProgressReporter) closePendingSubstep() {
	if r.pendingInline {
		_, _ = fmt.Fprint(r.output, "\n")
		r.pendingInline = false
	}
}

func (r *cliProgressReporter) clearPending() {
	r.pendingBody = ""
	r.pendingInline = false
}

// reportOptimizationSubstage renders the semantic optimizer sub-steps of the
// solve stage as indented "  step N <label> ..." lines. Probe events are
// deliberately silent on every destination: carriage-return redraws are
// expanded into many physical lines by some otherwise-interactive terminals and
// log capture layers. On an interactive terminal the "..." is replaced in place
// by "success (<dur>)"; a redirected stderr prints that final line directly.
func (r *cliProgressReporter) reportOptimizationSubstage(event optimizerv2.StageEvent) {
	label := cliOptimizationStageLabel(event.Substage)
	mode := cliModeSuffix(event.BetMode)
	switch event.State {
	case "started":
		r.substepIndex++
		r.pendingBody = fmt.Sprintf("step %d %s%s", r.substepIndex, label, mode)
		r.pendingInline = false
		if r.interactive {
			_, _ = fmt.Fprintf(r.output, "  %s ... ", r.pendingBody)
			r.pendingInline = true
		}
	case "progress":
		return
	case "completed", "skipped":
		outcome := "success"
		if event.State == "skipped" {
			outcome = "skipped"
		}
		body := r.stageBody(fmt.Sprintf("step %d %s%s", r.substepIndex, label, mode))
		if r.pendingInline {
			_, _ = fmt.Fprint(r.output, "\r\x1b[2K")
		}
		_, _ = fmt.Fprintf(r.output, "  %s ... %s (%s)\n", body, outcome, formatStageDuration(event.Duration))
		r.clearPending()
	case "failed":
		body := r.stageBody(fmt.Sprintf("step %d %s%s", r.substepIndex, label, mode))
		if r.pendingInline {
			_, _ = fmt.Fprint(r.output, "\r\x1b[2K")
		}
		if event.Message == "" {
			_, _ = fmt.Fprintf(r.output, "  %s ... failed\n", body)
		} else {
			_, _ = fmt.Fprintf(r.output, "  %s ... failed: %s\n", body, event.Message)
		}
		r.clearPending()
	}
}

func cliOptimizationStageLabel(stage optimizerv2.OptimizationStageID) string {
	switch stage {
	case optimizerv2.StageProveHardFeasibility:
		return "Proving hard feasibility"
	case optimizerv2.StageMinimizeMainProfileDeviation:
		return "Minimizing Main Group relative profile deviation"
	case optimizerv2.StageMaximizeOtherBucketVisibility:
		return "Maximizing minimum relative visibility of Other buckets"
	case optimizerv2.StageMaximizeMainGroupInternalVisibility:
		return "Maximizing minimum relative visibility within Main Group buckets"
	case optimizerv2.StageSelectCanonicalBucketProbabilities:
		return "Selecting canonical bucket probabilities"
	default:
		return string(stage)
	}
}

// reportCollection chooses the safe rendering strategy for the destination.
// Completion is never throttled, ensuring every bar reaches 100% before the
// following dynamic-validation stage is announced.
func (r *cliProgressReporter) reportCollection(event optimizerv2.StageEvent) {
	if r.interactive {
		complete := event.Requested > 0 && event.Accepted >= event.Requested
		if !r.lastRedraw.IsZero() && !complete && time.Since(r.lastRedraw) < collectionRedrawPeriod {
			return
		}
		r.redrawCollection(event)
		r.lastRedraw = time.Now()
		return
	}
	r.logCollectionMilestones(event)
}

// redrawCollection paints the complete Class table in YAML declaration order.
// Cursor movement is limited to rows previously written by this reporter; it
// never clears unrelated terminal output above the collection table.
func (r *cliProgressReporter) redrawCollection(event optimizerv2.StageEvent) {
	if r.renderedLines > 0 {
		_, _ = fmt.Fprintf(r.output, "\x1b[%dA", r.renderedLines)
	}
	for _, class := range event.Classes {
		_, _ = fmt.Fprintf(
			r.output,
			"\r\x1b[2K  %-18s [%s] %6.2f%%  %d/%d\n",
			class.Name,
			collectionBar(class.Accepted, class.Requested),
			collectionPercent(class.Accepted, class.Requested),
			class.Accepted,
			class.Requested,
		)
	}
	_, _ = fmt.Fprintf(
		r.output,
		"\r\x1b[2K  spins=%d  total=%d/%d\n",
		event.Spins,
		event.Accepted,
		event.Requested,
	)
	r.renderedLines = len(event.Classes) + 1
}

// logCollectionMilestones emits the initial state, each crossed ten-percent
// boundary, and completion for every Class. The key includes the declaration
// index so duplicate names—although rejected by config validation—could not
// accidentally suppress one another if an internal test constructs bad input.
func (r *cliProgressReporter) logCollectionMilestones(event optimizerv2.StageEvent) {
	for index, class := range event.Classes {
		percent := int(collectionPercent(class.Accepted, class.Requested))
		milestone := percent / collectionLogMilestone * collectionLogMilestone
		key := fmt.Sprintf("%d:%s", index, class.Name)
		previous, seen := r.lastMilestones[key]
		if seen && milestone <= previous {
			continue
		}
		r.lastMilestones[key] = milestone
		_, _ = fmt.Fprintf(
			r.output,
			"[Progress] Collecting class %-18s [%s] %3d%%  %d/%d (spins=%d)\n",
			class.Name,
			collectionBar(class.Accepted, class.Requested),
			percent,
			class.Accepted,
			class.Requested,
			event.Spins,
		)
	}
}

// cliModeSuffix appends the bet-mode qualifier used on every mode-scoped entry.
// A negative BetMode marks a run-wide stage (config load, static validation).
func cliModeSuffix(betMode int) string {
	if betMode < 0 {
		return ""
	}
	return fmt.Sprintf(" (mode %d)", betMode)
}

// cliPipelineStep maps stable top-level stage IDs to the fixed operator-facing
// eight-step pipeline. Optimization substages are details within step 6 and do
// not consume additional top-level numbers.
func cliPipelineStep(stage string) (int, bool) {
	switch {
	case stage == "load-config":
		return 1, true
	case stage == "static-validation":
		return 2, true
	case strings.HasPrefix(stage, "collect["):
		return 3, true
	case strings.HasPrefix(stage, "prepare["):
		return 4, true
	case strings.HasPrefix(stage, "compile["):
		return 5, true
	case strings.HasPrefix(stage, "solve["):
		return 6, true
	case strings.HasPrefix(stage, "materialize-verify["):
		return 7, true
	case stage == "publish":
		return 8, true
	default:
		return 0, false
	}
}

// cliStageLabel gives implementation stage IDs stable user-facing meaning. The
// IDs remain in Run reports and failure JSON, while these labels explain the
// pipeline without requiring users to know optimizer package terminology.
func cliStageLabel(stage string) string {
	switch {
	case stage == "load-config":
		return "Loading config"
	case stage == "static-validation":
		return "Validating static constraints"
	case strings.HasPrefix(stage, "collect["):
		return "Collecting samples"
	case strings.HasPrefix(stage, "prepare["):
		return "Validating dynamic constraints"
	case strings.HasPrefix(stage, "compile["):
		return "Building LP model"
	case strings.HasPrefix(stage, "solve["):
		return "Running optimization"
	case strings.HasPrefix(stage, "materialize-verify["):
		return "Materializing and verifying mode"
	case stage == "publish":
		return "Publishing mode and checking manifest"
	default:
		return stage
	}
}

// collectionPercent and collectionBar deliberately handle a zero request even
// though valid config forbids it. Reporters should remain robust when fed a
// synthetic event from tests or a future operational adapter.
func collectionPercent(accepted, requested uint64) float64 {
	if requested == 0 {
		return 100
	}
	percent := 100 * float64(accepted) / float64(requested)
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func collectionBar(accepted, requested uint64) string {
	filled := int(collectionPercent(accepted, requested) * collectionBarWidth / 100)
	if filled > collectionBarWidth {
		filled = collectionBarWidth
	}
	return strings.Repeat("=", filled) + strings.Repeat("-", collectionBarWidth-filled)
}

// formatStageDuration keeps sub-second gates visible while avoiding long raw
// nanosecond strings in human output. Duration remains lossless in RunReport.
func formatStageDuration(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}
	if duration < time.Second {
		return duration.Round(time.Millisecond).String()
	}
	return duration.Round(100 * time.Millisecond).String()
}

// reportV2Outcome is the command's final presentation boundary. cmd/opt is an
// operator-facing optimization command, so it deliberately writes no JSON and
// keeps stdout empty. Programmatic callers still receive the complete typed
// RunResult from Tuner.Run; the CLI prints only the decisive failure explanation
// or the publication state of a successfully generated mode. The verified bucket
// distribution is written to a file by writeModeDistributionCSVs, not printed,
// so a successful run leaves a clean terminal.
func reportV2Outcome(output io.Writer, result optimizerv2.RunResult) {
	if output == nil {
		return
	}
	for _, advisory := range result.Report.Advisories {
		_, _ = fmt.Fprintf(output, "[Advisory/%s] %s\n", advisory.Code, advisory.Message)
		if len(advisory.SourcePaths) > 0 {
			_, _ = fmt.Fprintf(output, "  config location: %s\n", strings.Join(advisory.SourcePaths, ", "))
		}
	}
	if !result.Succeeded() {
		stopping := make([]optimizerv2.Diagnostic, 0, len(result.Diagnostics))
		for _, diagnostic := range result.Diagnostics {
			if diagnostic.StopsRun() {
				stopping = append(stopping, diagnostic)
			}
		}
		if len(stopping) == 0 {
			_, _ = fmt.Fprintf(output, "[Result] Failed (%s): no diagnostics available\n", result.Status)
			return
		}

		if len(stopping) == 1 {
			_, _ = fmt.Fprintf(output, "[Result] Failed (%s / %s): 1 localized problem found\n", result.Status, stopping[0].Code)
		} else {
			_, _ = fmt.Fprintf(output, "[Result] Failed (%s): %d localized problems found\n", result.Status, len(stopping))
		}
		for index, diagnostic := range stopping {
			_, _ = fmt.Fprintf(
				output,
				"  %d. [%s] %s\n",
				index+1,
				diagnostic.Code,
				diagnostic.Message,
			)
			if diagnostic.Requested != nil {
				_, _ = fmt.Fprintf(output, "     required range: %s\n", formatDiagnosticBound(*diagnostic.Requested))
			}
			if diagnostic.Achievable != nil {
				_, _ = fmt.Fprintf(output, "     achievable range: %s\n", formatDiagnosticBound(*diagnostic.Achievable))
			}
			if diagnostic.Deficit > 0 {
				_, _ = fmt.Fprintf(output, "     minimum required gap: %.12g\n", diagnostic.Deficit)
			}
			if len(diagnostic.SourcePaths) > 0 {
				_, _ = fmt.Fprintf(output, "     config location: %s\n", strings.Join(diagnostic.SourcePaths, ", "))
			}
		}
		return
	}

	publication := result.Report.Publication
	if publication == nil {
		_, _ = fmt.Fprintln(output, "[Result] Generation succeeded")
		return
	}
	if publication.State == optimizerv2.PublicationManifestPublished {
		if publication.ManifestPath == "" {
			_, _ = fmt.Fprintf(output, "[Result] mode %d generated; all-format output bundles produced\n", publication.BetMode)
			return
		}
		_, _ = fmt.Fprintf(
			output,
			"[Result] mode %d generated; all-format output bundles produced; Artifact v1 manifest: %s\n",
			publication.BetMode,
			publication.ManifestPath,
		)
		return
	}
	if publication.State == optimizerv2.PublicationOutputsPublished {
		_, _ = fmt.Fprintf(output, "[Result] mode %d generated; all-format output bundles produced\n", publication.BetMode)
		return
	}
	_, _ = fmt.Fprintf(
		output,
		"[Result] mode %d generated and staged; missing mode %v, output bundle incomplete\n",
		publication.BetMode,
		publication.MissingModes,
	)
}

// writeModeDistributionCSVs persists the verified alias marginals of each
// generated mode as one CSV beneath directory, keeping the terminal clean while
// still giving Designers the actual runtime distribution. Both conditional and
// unconditional Bucket probabilities are emitted so a within-Class shape is
// never confused with a whole-game hit rate, and the per-seed range is recorded
// as runtime truth because a verified alias approximation can make individual
// marginals differ by a tiny amount while preserving every semantic constraint.
// It returns the paths it wrote, in mode order.
func writeModeDistributionCSVs(directory string, modes []optimizerv2.ModeRunReport) ([]string, error) {
	written := make([]string, 0, len(modes))
	for _, mode := range modes {
		report := mode.Distribution
		if len(report.Classes) == 0 {
			continue
		}
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return written, fmt.Errorf("create distribution output directory %q: %w", directory, err)
		}
		path := filepath.Join(directory, fmt.Sprintf("distribution_mode_%d.csv", report.BetMode))
		if err := writeModeDistributionCSV(path, report); err != nil {
			return written, err
		}
		written = append(written, path)
	}
	return written, nil
}

func writeModeDistributionCSV(path string, report optimizerv2.BucketDistributionReport) (err error) {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create distribution file %q: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close distribution file %q: %w", path, closeErr)
		}
	}()

	writer := csv.NewWriter(file)
	_ = writer.Write([]string{
		"class",
		"class_global_probability",
		"bucket",
		"conditional_probability",
		"unconditional_probability",
		"seed_count",
		"seed_probability_min",
		"seed_probability_max",
		"collision_probability",
		"draws_at_collision_probability",
	})
	collision := strconv.FormatFloat(report.CollisionProbability, 'g', 6, 64)
	for _, class := range report.Classes {
		classProbability := strconv.FormatFloat(class.Probability, 'g', 9, 64)
		for _, bucket := range class.Buckets {
			_ = writer.Write([]string{
				class.Class,
				classProbability,
				formatBucketInterval(bucket),
				strconv.FormatFloat(bucket.ConditionalProbability, 'g', 9, 64),
				strconv.FormatFloat(bucket.UnconditionalProbability, 'g', 9, 64),
				strconv.Itoa(bucket.SeedCount),
				strconv.FormatFloat(bucket.SeedProbabilityMin, 'e', 9, 64),
				strconv.FormatFloat(bucket.SeedProbabilityMax, 'e', 9, 64),
				collision,
				formatCollisionDraws(bucket.DrawsAtCollisionProbability),
			})
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("write distribution file %q: %w", path, err)
	}
	return nil
}

// formatBucketInterval mirrors the configured classifier: every controlled
// Bucket is [lower, upper) except the final one, while empirical-uniform Classes
// use one inclusive interval.
func formatBucketInterval(bucket optimizerv2.BucketProbabilityReport) string {
	closing := ")"
	if bucket.UpperInclusive {
		closing = "]"
	}
	return fmt.Sprintf("[%.9g, %.9g%s", bucket.Lower, bucket.Upper, closing)
}

func formatCollisionDraws(draws float64) string {
	if draws <= 0 {
		return "never"
	}
	return fmt.Sprintf("%.0f", draws)
}

// formatDiagnosticBound keeps exact points compact while still distinguishing
// them from inclusive intervals in the operator-facing failure report.
func formatDiagnosticBound(bound optimizerv2.Bound) string {
	if bound.Min == bound.Max {
		return fmt.Sprintf("%.12g", bound.Min)
	}
	return fmt.Sprintf("[%.12g, %.12g]", bound.Min, bound.Max)
}
