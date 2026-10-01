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
	"fmt"
	"io"
	"strings"

	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
)

func reportV2Outcome(output io.Writer, result optimizerv2.RunResult) {
	if output == nil {
		return
	}
	for _, export := range result.Report.RGSExports {
		_, _ = fmt.Fprintf(output, "[RGS/%s] %s records=%d/%d\n", export.Family, export.State, export.Records, export.Total)
		for _, file := range export.Files {
			_, _ = fmt.Fprintf(output, "  %s (%d bytes)\n", file.Path, file.Bytes)
		}
		if export.Error != "" {
			_, _ = fmt.Fprintf(output, "  %s\n", export.Error)
		}
	}
	if result.Status == optimizerv2.StatusExported {
		_, _ = fmt.Fprintln(output, "[Result] Exported; LP and distribution verification were not requested.")
		return
	}

	for _, mode := range result.Report.Modes {
		if mode.Distribution.Source == optimizerv2.DistributionSourcePointProbabilities {
			_, _ = fmt.Fprintf(output, "[Distribution] mode %d: optimized point probabilities (not alias marginals)\n", mode.BetMode)
		}
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

// formatDiagnosticBound keeps exact points compact while still distinguishing
// them from inclusive intervals in the operator-facing failure report.
func formatDiagnosticBound(bound optimizerv2.Bound) string {
	if bound.Min == bound.Max {
		return fmt.Sprintf("%.12g", bound.Min)
	}
	return fmt.Sprintf("[%.12g, %.12g]", bound.Min, bound.Max)
}
