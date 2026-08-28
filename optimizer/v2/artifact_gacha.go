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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/sdk/sampler"
	"github.com/zintix-labs/problab/spec"
)

const legacyGachaPackageSchemaV1 = "problab.optimal-gacha/v1"

// GachaArtifactWriter publishes the legacy runtime pair for every mode:
// gacha_<mode>.json.zst plus seed_bank_<mode>.bin. Like Artifact v1, modes are
// durably staged across Runs and the visible game_<gid> package is replaced
// only after every bet mode passes a production-loader round trip.
type GachaArtifactWriter struct {
	Directory  string
	renamePath func(oldPath, newPath string) error
}

type legacyGachaPayload struct {
	Picker  *sampler.AliasTableF64 `json:"picker"`
	SeedLen int                    `json:"seed_len"`
}

type legacyGachaFileNames struct {
	gacha    string
	seedBank string
}

func legacyModeFileNames(mode int) legacyGachaFileNames {
	return legacyGachaFileNames{
		gacha:    fmt.Sprintf("gacha_%d.json.zst", mode),
		seedBank: fmt.Sprintf("seed_bank_%d.bin", mode),
	}
}

func (writer GachaArtifactWriter) PublishMode(
	ctx context.Context,
	gid spec.GID,
	snapshotFormat string,
	betUnits []int,
	mode MaterializedMode,
) (PublishedArtifact, error) {
	if ctx == nil {
		return PublishedArtifact{}, fmt.Errorf("publish legacy gacha mode: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish legacy gacha mode: %w", err)
	}
	if strings.TrimSpace(writer.Directory) == "" {
		return PublishedArtifact{}, fmt.Errorf("publish legacy gacha mode: output directory is required")
	}
	if strings.TrimSpace(snapshotFormat) == "" {
		return PublishedArtifact{}, fmt.Errorf("publish legacy gacha mode: snapshot format is required")
	}
	if len(betUnits) == 0 {
		return PublishedArtifact{}, fmt.Errorf("publish legacy gacha mode: runtime bet units must not be empty")
	}
	for i, betUnit := range betUnits {
		if betUnit <= 0 {
			return PublishedArtifact{}, fmt.Errorf("publish legacy gacha mode: runtime bet unit[%d] must be > 0", i)
		}
	}
	if mode.BetMode < 0 || mode.BetMode >= len(betUnits) {
		return PublishedArtifact{}, fmt.Errorf("publish legacy gacha mode: bet mode %d is outside runtime range [0,%d]", mode.BetMode, len(betUnits)-1)
	}
	if mode.BetUnit != betUnits[mode.BetMode] {
		return PublishedArtifact{}, fmt.Errorf("publish legacy gacha mode %d: bet unit mismatch: mode=%d runtime=%d", mode.BetMode, mode.BetUnit, betUnits[mode.BetMode])
	}
	if err := validateMaterializedMode(mode); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish legacy gacha mode %d: invalid materialization: %w", mode.BetMode, err)
	}

	if err := os.MkdirAll(writer.Directory, 0o755); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish legacy gacha mode: create output directory: %w", err)
	}
	releaseLock, err := acquireArtifactPublicationLock(writer.Directory, gid)
	if err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish legacy gacha mode: %w", err)
	}
	defer releaseLock()

	pending := modeStageDirectory(writer.Directory, gid)
	metadata := modeStageMetadata{
		SchemaVersion: modeStageSchemaV1, Game: gid,
		SnapshotFormat: snapshotFormat, BetUnits: append([]int(nil), betUnits...),
	}
	if err := ensureModeStageMetadata(pending, metadata); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish legacy gacha mode %d: %w", mode.BetMode, err)
	}
	if err := writer.replacePendingMode(ctx, pending, mode); err != nil {
		return PublishedArtifact{}, err
	}

	staged, missing, err := inspectLegacyPendingModes(pending, betUnits)
	if err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish legacy gacha mode %d: inspect pending modes: %w", mode.BetMode, err)
	}
	result := PublishedArtifact{
		Formats: []OutputFormat{OutputOptimalGacha}, StagingDirectory: pending,
		StagedModes: staged, MissingModes: missing,
		Paths: legacyPendingModePaths(pending, staged),
	}
	if len(missing) != 0 {
		return result, nil
	}
	return writer.publishPendingPackage(ctx, gid, metadata)
}

func (writer GachaArtifactWriter) replacePendingMode(ctx context.Context, pending string, mode MaterializedMode) error {
	temporary, err := os.MkdirTemp(pending, fmt.Sprintf(".mode_%d-", mode.BetMode))
	if err != nil {
		return fmt.Errorf("publish legacy gacha mode %d: create mode staging directory: %w", mode.BetMode, err)
	}
	temporaryOwned := true
	defer func() {
		if temporaryOwned {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish legacy gacha mode %d: %w", mode.BetMode, err)
	}

	gacha, err := encodeLegacyGacha(mode)
	if err != nil {
		return fmt.Errorf("publish legacy gacha mode %d: encode: %w", mode.BetMode, err)
	}
	names := legacyModeFileNames(mode.BetMode)
	if err := writeArtifactFile(filepath.Join(temporary, names.gacha), gacha); err != nil {
		return fmt.Errorf("publish legacy gacha mode %d: write gacha: %w", mode.BetMode, err)
	}
	if err := writeArtifactFile(filepath.Join(temporary, names.seedBank), mode.SeedBank); err != nil {
		return fmt.Errorf("publish legacy gacha mode %d: write seed bank: %w", mode.BetMode, err)
	}
	if _, err := verifyLegacyModeDirectory(temporary, mode.BetMode, true); err != nil {
		return fmt.Errorf("publish legacy gacha mode %d: verify staged files: %w", mode.BetMode, err)
	}

	renamePath := writer.renamePath
	if renamePath == nil {
		renamePath = os.Rename
	}
	target := filepath.Join(pending, fmt.Sprintf("mode_%d", mode.BetMode))
	if err := replaceArtifactDirectory(temporary, target, renamePath); err != nil {
		return fmt.Errorf("publish legacy gacha mode %d: replace pending mode: %w", mode.BetMode, err)
	}
	temporaryOwned = false
	return nil
}

func encodeLegacyGacha(mode MaterializedMode) ([]byte, error) {
	seedLen := len(mode.Samples[0].Snapshot)
	raw, err := json.Marshal(legacyGachaPayload{Picker: mode.Picker, SeedLen: seedLen})
	if err != nil {
		return nil, err
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer encoder.Close()
	return encoder.EncodeAll(raw, nil), nil
}

func inspectLegacyPendingModes(pending string, betUnits []int) ([]int, []int, error) {
	entries, err := os.ReadDir(pending)
	if err != nil {
		return nil, nil, err
	}
	allowed := map[string]struct{}{modeStageState: {}}
	for mode := range betUnits {
		allowed[fmt.Sprintf("mode_%d", mode)] = struct{}{}
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok {
			return nil, nil, fmt.Errorf("unexpected pending entry %q", entry.Name())
		}
	}
	staged := make([]int, 0, len(betUnits))
	missing := make([]int, 0, len(betUnits))
	for mode := range betUnits {
		directory := filepath.Join(pending, fmt.Sprintf("mode_%d", mode))
		info, err := os.Lstat(directory)
		if os.IsNotExist(err) {
			missing = append(missing, mode)
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("inspect mode %d directory: %w", mode, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("mode %d pending entry must be a real directory", mode)
		}
		if _, err := verifyLegacyModeDirectory(directory, mode, true); err != nil {
			return nil, nil, fmt.Errorf("mode %d: %w", mode, err)
		}
		staged = append(staged, mode)
	}
	return staged, missing, nil
}

func verifyLegacyModeDirectory(directory string, mode int, enforceExactFiles bool) (legacyGachaPayload, error) {
	names := legacyModeFileNames(mode)
	compressed, err := os.ReadFile(filepath.Join(directory, names.gacha))
	if err != nil {
		return legacyGachaPayload{}, fmt.Errorf("read gacha: %w", err)
	}
	reader, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return legacyGachaPayload{}, fmt.Errorf("open gacha zstd: %w", err)
	}
	raw, readErr := io.ReadAll(reader)
	reader.Close()
	if readErr != nil {
		return legacyGachaPayload{}, fmt.Errorf("decompress gacha: %w", readErr)
	}
	var gacha legacyGachaPayload
	if err := decodeStrictJSON(raw, &gacha); err != nil {
		return legacyGachaPayload{}, fmt.Errorf("parse gacha: %w", err)
	}
	if gacha.SeedLen <= 0 {
		return legacyGachaPayload{}, fmt.Errorf("gacha seed_len must be > 0")
	}
	if _, err := EffectiveAliasProbabilities(gacha.Picker); err != nil {
		return legacyGachaPayload{}, fmt.Errorf("gacha picker: %w", err)
	}
	bank, err := os.ReadFile(filepath.Join(directory, names.seedBank))
	if err != nil {
		return legacyGachaPayload{}, fmt.Errorf("read seed bank: %w", err)
	}
	if gacha.Picker.Size > int(^uint(0)>>1)/gacha.SeedLen {
		return legacyGachaPayload{}, fmt.Errorf("seed bank dimensions overflow")
	}
	if len(bank) != gacha.Picker.Size*gacha.SeedLen {
		return legacyGachaPayload{}, fmt.Errorf("seed bank size mismatch: got=%d want=%d", len(bank), gacha.Picker.Size*gacha.SeedLen)
	}
	if enforceExactFiles {
		expected := map[string]struct{}{names.gacha: {}, names.seedBank: {}}
		if err := verifyArtifactFileSet(directory, expected); err != nil {
			return legacyGachaPayload{}, err
		}
	}
	return gacha, nil
}

func legacyPendingModePaths(pending string, modes []int) []string {
	paths := make([]string, 0, len(modes)*2)
	for _, mode := range modes {
		directory := filepath.Join(pending, fmt.Sprintf("mode_%d", mode))
		names := legacyModeFileNames(mode)
		paths = append(paths, filepath.Join(directory, names.gacha), filepath.Join(directory, names.seedBank))
	}
	return paths
}

func (writer GachaArtifactWriter) publishPendingPackage(
	ctx context.Context,
	gid spec.GID,
	metadata modeStageMetadata,
) (PublishedArtifact, error) {
	pending := modeStageDirectory(writer.Directory, gid)
	staging, err := os.MkdirTemp(writer.Directory, fmt.Sprintf(".game_%d-gacha-", gid))
	if err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish complete legacy gacha: create staging directory: %w", err)
	}
	stagingOwned := true
	defer func() {
		if stagingOwned {
			_ = os.RemoveAll(staging)
		}
	}()

	paths := make([]string, 0, len(metadata.BetUnits)*2)
	for mode := range metadata.BetUnits {
		if err := ctx.Err(); err != nil {
			return PublishedArtifact{}, fmt.Errorf("publish complete legacy gacha: %w", err)
		}
		names := legacyModeFileNames(mode)
		source := filepath.Join(pending, fmt.Sprintf("mode_%d", mode))
		for _, name := range []string{names.gacha, names.seedBank} {
			data, err := os.ReadFile(filepath.Join(source, name))
			if err != nil {
				return PublishedArtifact{}, fmt.Errorf("publish complete legacy gacha: read mode %d %s: %w", mode, name, err)
			}
			if err := writeArtifactFile(filepath.Join(staging, name), data); err != nil {
				return PublishedArtifact{}, fmt.Errorf("publish complete legacy gacha: write mode %d %s: %w", mode, name, err)
			}
			paths = append(paths, name)
		}
	}
	if err := verifyLegacyPackageWithProductionLoader(staging, metadata); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish complete legacy gacha: verify package: %w", err)
	}
	artifactID, err := legacyPackageArtifactID(staging, metadata, paths)
	if err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish complete legacy gacha: identify package: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish complete legacy gacha: %w", err)
	}

	renamePath := writer.renamePath
	if renamePath == nil {
		renamePath = os.Rename
	}
	target := filepath.Join(writer.Directory, fmt.Sprintf("game_%d", gid))
	if err := replaceArtifactDirectory(staging, target, renamePath); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish complete legacy gacha: replace %s: %w", target, err)
	}
	stagingOwned = false
	for i, name := range paths {
		paths[i] = filepath.Join(target, name)
	}
	staged := make([]int, len(metadata.BetUnits))
	for mode := range staged {
		staged[mode] = mode
	}
	return PublishedArtifact{
		Complete: true, Formats: []OutputFormat{OutputOptimalGacha},
		Paths: paths, ArtifactID: artifactID, StagingDirectory: pending,
		StagedModes: staged,
	}, nil
}

func verifyLegacyPackageWithProductionLoader(directory string, metadata modeStageMetadata) (resultErr error) {
	seedLen := 0
	gachas := make([]string, len(metadata.BetUnits))
	seedBanks := make([]string, len(metadata.BetUnits))
	expectedFiles := make(map[string]struct{}, len(metadata.BetUnits)*2)
	for mode := range metadata.BetUnits {
		gacha, err := verifyLegacyModeDirectory(directory, mode, false)
		if err != nil {
			return fmt.Errorf("mode %d: %w", mode, err)
		}
		if seedLen == 0 {
			seedLen = gacha.SeedLen
		} else if seedLen != gacha.SeedLen {
			return fmt.Errorf("mode %d seed_len mismatch: got=%d want=%d", mode, gacha.SeedLen, seedLen)
		}
		names := legacyModeFileNames(mode)
		gachas[mode], seedBanks[mode] = names.gacha, names.seedBank
		expectedFiles[names.gacha] = struct{}{}
		expectedFiles[names.seedBank] = struct{}{}
	}
	if err := verifyArtifactFileSet(directory, expectedFiles); err != nil {
		return err
	}
	store, err := optimalrt.NewDirStore(directory, metadata.SnapshotFormat, seedLen)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil && resultErr == nil {
			resultErr = closeErr
		}
	}()
	setting := &spec.GameSetting{
		BetUnits: metadata.BetUnits,
		OptimalSetting: spec.OptimalSetting{
			UseOptimal: true, Gachas: gachas, SeedBank: seedBanks,
		},
	}
	artifact, err := store.Resolve(setting)
	if err != nil {
		return err
	}
	if artifact == nil || artifact.ModeCount() != len(metadata.BetUnits) || artifact.Backend() != "legacy-memory" {
		return fmt.Errorf("legacy runtime package mismatch")
	}
	return nil
}

func legacyPackageArtifactID(directory string, metadata modeStageMetadata, names []string) (string, error) {
	type payloadRef struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	refs := make([]payloadRef, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return "", err
		}
		ref := artifactFileReference(name, data)
		refs = append(refs, payloadRef{Path: ref.Path, Size: ref.Size, SHA256: ref.SHA256})
	}
	raw, err := json.Marshal(struct {
		Schema         string       `json:"schema"`
		SnapshotFormat string       `json:"snapshot_format"`
		BetUnits       []int        `json:"bet_units"`
		Payloads       []payloadRef `json:"payloads"`
	}{
		Schema: legacyGachaPackageSchemaV1, SnapshotFormat: metadata.SnapshotFormat,
		BetUnits: metadata.BetUnits, Payloads: refs,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
