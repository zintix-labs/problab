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
	"errors"
	"fmt"
	"time"

	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
)

// invalidV2ConfigError marks only successfully read configuration bytes whose
// YAML, schema, or semantic content is invalid. Keeping this distinction local
// to the CLI lets Run map bad user input to a typed RunResult while real
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
// structured inspection while Run relies only on this wrapper's provenance.
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
