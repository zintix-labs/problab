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
	"fmt"
	"path/filepath"
	"slices"

	"github.com/zintix-labs/problab/spec"
)

type formatPublisher struct {
	format    OutputFormat
	publisher ArtifactPublisher
}

type multiFormatPublisher struct {
	outputs []formatPublisher
}

// newOutputPublisher is the only format-routing boundary. Every publisher
// consumes the same replay-verified MaterializedMode, so adding compatibility
// packaging cannot change collection, LP solving, alias construction, or
// semantic verification.
func newOutputPublisher(output OutputOptions) ArtifactPublisher {
	publishers := make([]formatPublisher, 0, len(output.Format))
	for _, format := range output.Format {
		switch format {
		case OutputOptimalArtifactV1:
			publishers = append(publishers, formatPublisher{
				format: format,
				publisher: FileArtifactWriter{
					Directory: filepath.Join(output.Directory, "artifact_v1"),
				},
			})
		case OutputOptimalGacha:
			publishers = append(publishers, formatPublisher{
				format: format,
				publisher: GachaArtifactWriter{
					Directory: filepath.Join(output.Directory, "gacha"),
				},
			})
		}
	}
	return multiFormatPublisher{outputs: publishers}
}

func (publisher multiFormatPublisher) PublishMode(
	ctx context.Context,
	gid spec.GID,
	snapshotFormat string,
	betUnits []int,
	mode MaterializedMode,
) (PublishedArtifact, error) {
	if len(publisher.outputs) == 0 {
		return PublishedArtifact{}, fmt.Errorf("publish mode: no output formats configured")
	}
	results := make([]PublishedArtifact, 0, len(publisher.outputs))
	formats := make([]OutputFormat, 0, len(publisher.outputs))
	for _, output := range publisher.outputs {
		if output.publisher == nil {
			return PublishedArtifact{}, fmt.Errorf("publish mode %d format %q: nil publisher", mode.BetMode, output.format)
		}
		published, err := output.publisher.PublishMode(ctx, gid, snapshotFormat, betUnits, mode)
		if err != nil {
			return PublishedArtifact{}, fmt.Errorf("publish mode %d format %q: %w", mode.BetMode, output.format, err)
		}
		published.Formats = []OutputFormat{output.format}
		results = append(results, published)
		formats = append(formats, output.format)
	}
	return combinePublishedFormats(len(betUnits), formats, results), nil
}

func combinePublishedFormats(expectedModes int, formats []OutputFormat, results []PublishedArtifact) PublishedArtifact {
	combined := PublishedArtifact{Complete: true, Formats: slices.Clone(formats)}
	missing := make([]bool, expectedModes)
	for _, result := range results {
		combined.Complete = combined.Complete && result.Complete
		combined.Paths = append(combined.Paths, result.Paths...)
		for _, mode := range result.MissingModes {
			if mode >= 0 && mode < expectedModes {
				missing[mode] = true
			}
		}
		// Artifact v1 is the canonical report identity when present. For a
		// gacha-only output, the first result supplies the equivalent fields.
		if combined.StagingDirectory == "" || slices.Contains(result.Formats, OutputOptimalArtifactV1) {
			combined.StagingDirectory = result.StagingDirectory
			combined.ManifestPath = result.ManifestPath
			combined.ArtifactID = result.ArtifactID
		}
	}
	for mode, isMissing := range missing {
		if isMissing {
			combined.MissingModes = append(combined.MissingModes, mode)
		} else {
			combined.StagedModes = append(combined.StagedModes, mode)
		}
	}
	if !combined.Complete {
		combined.ManifestPath = ""
		combined.ArtifactID = ""
	}
	return combined
}
