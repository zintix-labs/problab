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
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/demo/demo_logic/optimizer_tags"
	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
)

const embeddedConfigName = "opt_cfg.yaml"

var gameTags = optimizer_tags.GameTags

// main is deliberately a thin composition root: it loads the command-owned
// embedded config, constructs Problab plus optimizer/v2, and runs every declared
// plan in order. Collection, LP rows, bisection, materialization, and status
// mapping remain in optimizer/v2 and are shared by CLI and programmatic callers.
func main() {
	exitCode, err := runV2(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cmd/opt: %v\n", err)
		exitCode = 1
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runV2 owns command lifecycle and returns a stable process classification:
// zero for a verified OPTIMAL mode that was durably staged (and may also have
// completed the all-mode manifest), two for an expected typed non-success Run
// status, and an error for operational failures.
func runV2(arguments []string, _ io.Writer, stderr io.Writer) (int, error) {
	if len(arguments) != 0 {
		return 1, fmt.Errorf(
			"cmd/opt does not accept command-line parameters; edit embedded %q instead (got %s)",
			embeddedConfigName,
			strings.Join(arguments, " "),
		)
	}

	reporter := newCLIProgressReporter(stderr)
	configSource := fmt.Sprintf("embedded %q", embeddedConfigName)
	reporter.Report(optimizerv2.StageEvent{Stage: "load-config", State: "started", BetMode: -1, Message: configSource})
	configStarted := time.Now()
	config, err := loadV2Config()
	if err != nil {
		duration := time.Since(configStarted)
		var invalidConfig *invalidV2ConfigError
		if !errors.As(err, &invalidConfig) {
			reporter.Report(optimizerv2.StageEvent{Stage: "load-config", State: "failed", BetMode: -1, Message: err.Error(), Duration: duration})
			return 1, err
		}
		failedStage := invalidV2ConfigStage(invalidConfig)
		if failedStage == "static-validation" {
			reporter.Report(optimizerv2.StageEvent{Stage: "load-config", State: "completed", BetMode: -1, Duration: duration})
			reporter.Report(optimizerv2.StageEvent{Stage: failedStage, State: "started", BetMode: -1})
			reporter.Report(optimizerv2.StageEvent{Stage: failedStage, State: "failed", BetMode: -1, Message: err.Error()})
		} else {
			reporter.Report(optimizerv2.StageEvent{Stage: failedStage, State: "failed", BetMode: -1, Message: err.Error(), Duration: duration})
		}
		// A document that was read successfully but cannot be decoded or
		// validated is an expected, actionable Run outcome. Keep its typed value
		// internally, but present only one human-readable result on stderr.
		reportV2Outcome(stderr, configInvalidRunResult(invalidConfig, failedStage, duration))
		return 2, nil
	}
	reporter.Report(optimizerv2.StageEvent{Stage: "load-config", State: "completed", BetMode: -1, Duration: time.Since(configStarted)})
	lab, err := demo.NewProbLab()
	if err != nil {
		return 1, fmt.Errorf("construct demo Problab: %w", err)
	}
	defer func() { _ = lab.Close() }()
	tuner, err := optimizerv2.NewTuner(config, lab,
		optimizerv2.WithReporter(reporter),
		optimizerv2.WithCollectionTags(gameTags),
	)
	if err != nil {
		return 1, fmt.Errorf("construct optimizer/v2 tuner: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	exitCode := 0
	for _, plan := range config.Plans {
		result, err := tuner.Run(ctx, optimizerv2.RunRequest{PlanID: plan.ID})
		if err != nil {
			return 1, err
		}
		reportV2Outcome(stderr, result)
		if !result.Succeeded() {
			exitCode = 2
			continue
		}
		// The verified distribution is a file artifact, not terminal noise: write
		// it next to the published output and print only where it landed.
		paths, err := writeModeDistributionCSVs(plan.Output.Directory, result.Report.Modes)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "[warn] distribution report: %v\n", err)
		}
		for _, path := range paths {
			_, _ = fmt.Fprintf(stderr, "[Result] distribution written: %s\n", path)
		}
	}
	return exitCode, nil
}

// invalidV2ConfigError marks only successfully read configuration bytes whose
// YAML, schema, or semantic content is invalid. Keeping this distinction local
// to the command lets runV2 map bad user input to a typed RunResult while real
// filesystem failures continue through the operational Go-error/exit-one path.
type invalidV2ConfigError struct {
	source string
	cause  error
}

// Error includes the embedded source so a ConfigInvalid diagnostic identifies
// the command-owned document. Unwrap preserves yaml.v3 and optimizer/v2
// ConfigError inspection.
func (e *invalidV2ConfigError) Error() string {
	return fmt.Sprintf("load v2 config %s: %v", e.source, e.cause)
}

// Unwrap preserves the parser or ConfigError cause for callers that need
// structured inspection while runV2 relies only on this wrapper's provenance.
func (e *invalidV2ConfigError) Unwrap() error { return e.cause }

// parseV2ConfigBytes is the strict content boundary for embedded YAML.
// ParseConfig enforces KnownFields, one document, and all v2 semantic
// invariants; every failure after bytes have been obtained is therefore safe to
// classify as INFEASIBLE_CONFIG rather than an operational command failure.
func parseV2ConfigBytes(raw []byte, source string) (optimizerv2.Config, error) {
	config, err := optimizerv2.ParseConfig(raw)
	if err != nil {
		return optimizerv2.Config{}, &invalidV2ConfigError{source: source, cause: err}
	}
	return config, nil
}

// loadV2Config reads the command-owned document. cmd/opt intentionally has no
// external config or field-level override path: embeddedConfigName is the one
// auditable source of every RunPlan value executed by this binary.
func loadV2Config() (optimizerv2.Config, error) {
	raw, err := embeddedV2Config.ReadFile(embeddedConfigName)
	if err != nil {
		return optimizerv2.Config{}, fmt.Errorf("read embedded v2 config %q: %w", embeddedConfigName, err)
	}
	return parseV2ConfigBytes(raw, fmt.Sprintf("embedded %q", embeddedConfigName))
}

// configInvalidRunResult converts a strict configuration parse/validation
// failure into the public status contract used by Tuner.Run. No partially
// decoded Config is copied into the report because rejected input has no valid
// resolved plan or trustworthy effective values.
func configInvalidRunResult(err error, failedStage string, duration time.Duration) optimizerv2.RunResult {
	return optimizerv2.RunResult{
		Status: optimizerv2.StatusInfeasibleConfig,
		Diagnostics: optimizerv2.Diagnostics{{
			Code:        optimizerv2.DiagnosticConfigInvalid,
			Status:      optimizerv2.StatusInfeasibleConfig,
			Message:     err.Error(),
			SourcePaths: []string{"config"},
		}},
		Report: optimizerv2.RunReport{Stages: []optimizerv2.StageDuration{{Stage: failedStage, Duration: duration}}},
	}
}

// invalidV2ConfigStage distinguishes semantic Config validation from YAML/schema
// loading. This preserves the user's mental pipeline even though ParseConfig is
// intentionally one strict API that performs decoding and validation together.
func invalidV2ConfigStage(err error) string {
	var configError *optimizerv2.ConfigError
	if errors.As(err, &configError) {
		return "static-validation"
	}
	return "load-config"
}
