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
	"strings"

	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/spec"
)

// GachaArtifactWriter publishes each verified mode as one gacha pair —
// gacha.json.zst plus seed_bank.bin — in an immutable directory beneath
// Directory/game_<gid>/mode_<mode>/. Every mode is published on its own; it does
// not wait for sibling modes.
type GachaArtifactWriter struct {
	Directory string
}

// PublishMode publishes one mode. Cancellation is honored only before the
// pair is written: once writing starts it runs to completion, and a successful
// write is reported as success. A cancelled Run may therefore leave a complete
// publication directory, which a later Run with the same content reuses.
func (writer GachaArtifactWriter) PublishMode(
	ctx context.Context,
	gid spec.GID,
	snapshotFormat string,
	betUnits []int,
	mode MaterializedMode,
) (PublishedArtifact, error) {
	if ctx == nil {
		return PublishedArtifact{}, fmt.Errorf("publish gacha mode: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish gacha mode: %w", err)
	}
	if strings.TrimSpace(writer.Directory) == "" {
		return PublishedArtifact{}, fmt.Errorf("publish gacha mode: output directory is required")
	}
	if strings.TrimSpace(snapshotFormat) == "" {
		return PublishedArtifact{}, fmt.Errorf("publish gacha mode: snapshot format is required")
	}
	if len(betUnits) == 0 {
		return PublishedArtifact{}, fmt.Errorf("publish gacha mode: runtime bet units must not be empty")
	}
	for i, betUnit := range betUnits {
		if betUnit <= 0 {
			return PublishedArtifact{}, fmt.Errorf("publish gacha mode: runtime bet unit[%d] must be > 0", i)
		}
	}
	if mode.BetMode < 0 || mode.BetMode >= len(betUnits) {
		return PublishedArtifact{}, fmt.Errorf("publish gacha mode: bet mode %d is outside runtime range [0,%d]", mode.BetMode, len(betUnits)-1)
	}
	if mode.BetUnit != betUnits[mode.BetMode] {
		return PublishedArtifact{}, fmt.Errorf("publish gacha mode %d: bet unit mismatch: mode=%d runtime=%d", mode.BetMode, mode.BetUnit, betUnits[mode.BetMode])
	}
	if err := validateMaterializedMode(mode); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish gacha mode %d: invalid materialization: %w", mode.BetMode, err)
	}

	parent := filepath.Join(writer.Directory, fmt.Sprintf("game_%d", gid), fmt.Sprintf("mode_%d", mode.BetMode))
	pair, err := optimalrt.PublishGachaPair(parent, optimalrt.GachaPair{
		Picker: mode.Picker, SeedLen: len(mode.Samples[0].Snapshot), SeedBank: mode.SeedBank,
	})
	if err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish gacha mode %d: %w", mode.BetMode, err)
	}
	return PublishedArtifact{
		Formats: []OutputFormat{OutputOptimalGacha}, Directory: pair.Dir,
		Paths: []string{pair.Gacha, pair.SeedBank}, ArtifactID: pair.ID, Reused: pair.Reused,
	}, nil
}
