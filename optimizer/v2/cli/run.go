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

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/dto"
	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
	"github.com/zintix-labs/problab/sdk/tag"
	"github.com/zintix-labs/problab/spec"
)

// Options supplies application-owned dependencies. Use keyed field literals.
type Options struct {
	// Config is YAML content; callers must not mutate it during Run.
	Config []byte
	// ConfigSource is a display label, never a file path to open.
	ConfigSource string
	// Lab is borrowed, never closed. Concurrent Run calls on the same Lab are unsupported.
	Lab *problab.Problab
	// Output receives text; nil disables text, not artifacts or CSV files.
	Output io.Writer
	// CollectionTags is game-scoped. No demo tags are supplied implicitly.
	CollectionTags map[spec.GID]map[string]tag.IsTag
	// ResultConverter is passed unchanged to Tuner. Nil uses IdentityConverter,
	// which includes complete replay state; custom output is the caller's responsibility.
	ResultConverter dto.ResultConverter
}

// Run executes configured plans in order. Exit classes are 1 (error), 2
// (expected non-success), 0 (success). Cancellation remains errors.Is-compatible.
// Run neither registers signals nor closes Lab nor exits the process.
func Run(ctx context.Context, options Options) (int, error) {
	if ctx == nil || options.Lab == nil {
		return 1, fmt.Errorf("optimizer cli: context and Lab must be non-nil")
	}
	if err := ctx.Err(); err != nil {
		return 1, err
	}
	if options.ConfigSource == "" {
		options.ConfigSource = "provided configuration"
	}
	output := options.Output
	if file, ok := output.(*os.File); ok && file == nil {
		output = nil
	}
	if output == nil {
		output = io.Discard
	}
	reporter := newCLIProgressReporter(output)
	configSource := options.ConfigSource
	reporter.Report(optimizerv2.StageEvent{Stage: "load-config", State: "started", BetMode: -1, Message: configSource})
	configStarted := time.Now()
	config, err := parseV2ConfigBytes(options.Config, configSource)
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
		// internally, but present only one human-readable result on output.
		reportV2Outcome(output, configInvalidRunResult(invalidConfig, failedStage, duration))
		return 2, nil
	}
	reporter.Report(optimizerv2.StageEvent{Stage: "load-config", State: "completed", BetMode: -1, Duration: time.Since(configStarted)})
	if err := validateTagInjection(config, options.CollectionTags); err != nil {
		reporter.Report(optimizerv2.StageEvent{Stage: "static-validation", State: "started", BetMode: -1})
		reporter.Report(optimizerv2.StageEvent{Stage: "static-validation", State: "failed", BetMode: -1, Message: err.Error()})
		reportV2Outcome(output, configInvalidRunResult(err, "static-validation", 0))
		return 2, nil
	}
	if err := ctx.Err(); err != nil {
		return 1, err
	}
	tuner, err := optimizerv2.NewTuner(config, options.Lab,
		optimizerv2.WithReporter(reporter),
		optimizerv2.WithCollectionTags(options.CollectionTags),
		optimizerv2.WithResultConverter(options.ResultConverter),
	)
	if err != nil {
		return 1, fmt.Errorf("construct optimizer/v2 tuner: %w", err)
	}
	return runPlans(ctx, config, output, tuner.Run)
}

func runPlans(ctx context.Context, config optimizerv2.Config, output io.Writer, runPlan func(context.Context, optimizerv2.RunRequest) (optimizerv2.RunResult, error)) (int, error) {
	exitCode := 0
	for _, plan := range config.Plans {
		if err := ctx.Err(); err != nil {
			return 1, err
		}
		result, err := runPlan(ctx, optimizerv2.RunRequest{PlanID: plan.ID})
		reportV2Outcome(output, result)
		if err != nil {
			return 1, err
		}
		if err := ctx.Err(); err != nil {
			return 1, err
		}
		if !result.Succeeded() {
			exitCode = 2
			continue
		}
		// The verified distribution is a file artifact, not terminal noise: write
		// it next to the published output and print only where it landed.
		paths, err := writeModeDistributionCSVs(plan.Output.Directory, result.Report.Modes)
		if err != nil {
			_, _ = fmt.Fprintf(output, "[warn] distribution report: %v\n", err)
		}
		for _, path := range paths {
			_, _ = fmt.Fprintf(output, "[Result] distribution written: %s\n", path)
		}
	}
	if err := ctx.Err(); err != nil {
		return 1, err
	}
	return exitCode, nil
}

func validateTagInjection(config optimizerv2.Config, tags map[spec.GID]map[string]tag.IsTag) error {
	for _, plan := range config.Plans {
		for _, class := range config.Intents[plan.Intent].Classes {
			if len(class.Collect.Tags.Matches)+len(class.Collect.Tags.Mismatches) > 0 && len(tags[plan.Target.Game]) == 0 {
				return fmt.Errorf("plan %q, game %v requires collection tags; please supply them through Options.CollectionTags", plan.ID, plan.Target.Game)
			}
		}
	}
	return nil
}
