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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/spec"
)

const (
	modeStageSchemaV1 = "problab.optimizer-mode-stage/v1"
	modeStageState    = "state.json"
)

// modeStageMetadata is the small cross-run compatibility contract shared by
// independently optimized modes. Designer intent, seed, solver tolerances, and
// config hashes are deliberately absent: different modes are allowed to use
// different optimization settings. Only runtime facts that must agree in one
// Manifest v1 bundle are persisted here.
type modeStageMetadata struct {
	SchemaVersion  string   `json:"schema_version"`
	Game           spec.GID `json:"game"`
	SnapshotFormat string   `json:"snapshot_format"`
	BetUnits       []int    `json:"bet_units"`
}

// PublishMode durably records exactly one already-materialized and replay-
// verified mode. A partial set lives only in game_<gid>.pending and therefore
// cannot be mistaken for a runtime artifact: no manifest is written there.
// When the last missing mode arrives, PublishMode copies the complete pending
// set into a fresh bundle, writes manifest.json last, verifies it through the
// production loader, and atomically replaces the visible game_<gid> directory.
//
// The pending directory is retained after publication. That is intentional: a
// later rerun of one mode can replace just that mode and immediately republish a
// complete bundle with the last verified versions of its siblings. The prior
// visible bundle remains untouched until the replacement is fully verified.
func (writer FileArtifactWriter) PublishMode(
	ctx context.Context,
	gid spec.GID,
	snapshotFormat string,
	betUnits []int,
	mode MaterializedMode,
) (PublishedArtifact, error) {
	if ctx == nil {
		return PublishedArtifact{}, fmt.Errorf("publish mode: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish mode: %w", err)
	}
	if strings.TrimSpace(writer.Directory) == "" {
		return PublishedArtifact{}, fmt.Errorf("publish mode: output directory is required")
	}
	if strings.TrimSpace(snapshotFormat) == "" {
		return PublishedArtifact{}, fmt.Errorf("publish mode: snapshot format is required")
	}
	if len(betUnits) == 0 {
		return PublishedArtifact{}, fmt.Errorf("publish mode: runtime bet units must not be empty")
	}
	for i, betUnit := range betUnits {
		if betUnit <= 0 {
			return PublishedArtifact{}, fmt.Errorf("publish mode: runtime bet unit[%d] must be > 0", i)
		}
	}
	if mode.BetMode < 0 || mode.BetMode >= len(betUnits) {
		return PublishedArtifact{}, fmt.Errorf(
			"publish mode: bet mode %d is outside runtime range [0,%d]",
			mode.BetMode,
			len(betUnits)-1,
		)
	}
	if mode.BetUnit != betUnits[mode.BetMode] {
		return PublishedArtifact{}, fmt.Errorf(
			"publish mode %d: bet unit mismatch: mode=%d runtime=%d",
			mode.BetMode,
			mode.BetUnit,
			betUnits[mode.BetMode],
		)
	}
	if err := validateMaterializedMode(mode); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish mode %d: invalid materialization: %w", mode.BetMode, err)
	}

	if err := os.MkdirAll(writer.Directory, 0o755); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish mode: create output directory: %w", err)
	}
	releaseLock, err := acquireArtifactPublicationLock(writer.Directory, gid)
	if err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish mode: %w", err)
	}
	defer releaseLock()

	pending := modeStageDirectory(writer.Directory, gid)
	metadata := modeStageMetadata{
		SchemaVersion:  modeStageSchemaV1,
		Game:           gid,
		SnapshotFormat: snapshotFormat,
		BetUnits:       slices.Clone(betUnits),
	}
	if err := ensureModeStageMetadata(pending, metadata); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish mode %d: %w", mode.BetMode, err)
	}
	if err := writer.replacePendingMode(ctx, pending, mode); err != nil {
		return PublishedArtifact{}, err
	}

	descriptors, stagedModes, missingModes, err := inspectPendingModes(pending, betUnits)
	if err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish mode %d: inspect pending modes: %w", mode.BetMode, err)
	}
	result := PublishedArtifact{
		Formats:          []OutputFormat{OutputOptimalArtifactV1},
		StagingDirectory: pending,
		StagedModes:      stagedModes,
		MissingModes:     missingModes,
		Paths:            pendingModePaths(pending, stagedModes),
	}
	if len(missingModes) != 0 {
		return result, nil
	}

	complete, err := writer.publishPendingBundle(ctx, gid, metadata, descriptors)
	if err != nil {
		return PublishedArtifact{}, err
	}
	complete.StagingDirectory = pending
	complete.StagedModes = stagedModes
	return complete, nil
}

// modeStageDirectory is deliberately separate from game_<gid>. Runtime loaders
// only receive the latter path, so a crash after one mode is staged cannot make
// an incomplete set visible or trigger a legacy-loader fallback.
func modeStageDirectory(directory string, gid spec.GID) string {
	return filepath.Join(directory, fmt.Sprintf("game_%d.pending", gid))
}

// ensureModeStageMetadata creates the persistent pending area on its first run
// and thereafter rejects runtime-incompatible reuse. It never auto-deletes a
// mismatched directory because that directory may contain expensive optimizer
// results that require explicit operator review.
func ensureModeStageMetadata(pending string, want modeStageMetadata) error {
	if err := os.MkdirAll(pending, 0o755); err != nil {
		return fmt.Errorf("create pending directory %q: %w", pending, err)
	}
	statePath := filepath.Join(pending, modeStageState)
	raw, err := os.ReadFile(statePath)
	if os.IsNotExist(err) {
		entries, readErr := os.ReadDir(pending)
		if readErr != nil {
			return fmt.Errorf("inspect new pending directory: %w", readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("pending directory %q has mode data but no %s", pending, modeStageState)
		}
		if err := writeAtomicJSON(pending, modeStageState, want); err != nil {
			return fmt.Errorf("initialize pending metadata: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read pending metadata: %w", err)
	}
	var got modeStageMetadata
	if err := decodeStrictJSON(raw, &got); err != nil {
		return fmt.Errorf("parse pending metadata: %w", err)
	}
	if got.SchemaVersion != want.SchemaVersion || got.Game != want.Game ||
		got.SnapshotFormat != want.SnapshotFormat || !slices.Equal(got.BetUnits, want.BetUnits) {
		return fmt.Errorf(
			"pending directory %q is incompatible: got schema=%q game=%d snapshot_format=%q bet_units=%v; want schema=%q game=%d snapshot_format=%q bet_units=%v",
			pending,
			got.SchemaVersion,
			got.Game,
			got.SnapshotFormat,
			got.BetUnits,
			want.SchemaVersion,
			want.Game,
			want.SnapshotFormat,
			want.BetUnits,
		)
	}
	return nil
}

// replacePendingMode serializes one mode into a private directory and swaps the
// directory as one rename. Four related files can therefore never be observed
// half-old and half-new by the finalizer, even when a mode is regenerated.
func (writer FileArtifactWriter) replacePendingMode(ctx context.Context, pending string, mode MaterializedMode) error {
	temporary, err := os.MkdirTemp(pending, fmt.Sprintf(".mode_%d-", mode.BetMode))
	if err != nil {
		return fmt.Errorf("publish mode %d: create mode staging directory: %w", mode.BetMode, err)
	}
	temporaryOwned := true
	defer func() {
		if temporaryOwned {
			_ = os.RemoveAll(temporary)
		}
	}()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish mode %d: %w", mode.BetMode, err)
	}
	descriptor, payloads, err := encodePendingMode(mode)
	if err != nil {
		return fmt.Errorf("publish mode %d: encode: %w", mode.BetMode, err)
	}
	names := modeFileNames(mode.BetMode)
	for _, file := range []struct {
		name string
		data []byte
	}{
		{name: names.prob, data: payloads.prob},
		{name: names.aliases, data: payloads.aliases},
		{name: names.seedBank, data: payloads.seedBank},
	} {
		if err := writeArtifactFile(filepath.Join(temporary, file.name), file.data); err != nil {
			return fmt.Errorf("publish mode %d: write %s: %w", mode.BetMode, file.name, err)
		}
	}
	descriptorBytes, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return fmt.Errorf("publish mode %d: marshal descriptor: %w", mode.BetMode, err)
	}
	descriptorBytes = append(descriptorBytes, '\n')
	if err := writeArtifactFile(filepath.Join(temporary, names.descriptor), descriptorBytes); err != nil {
		return fmt.Errorf("publish mode %d: write descriptor: %w", mode.BetMode, err)
	}
	if _, err := verifyPendingMode(temporary, mode.BetMode, mode.BetUnit); err != nil {
		return fmt.Errorf("publish mode %d: verify staged files: %w", mode.BetMode, err)
	}

	renamePath := writer.renamePath
	if renamePath == nil {
		renamePath = os.Rename
	}
	target := filepath.Join(pending, fmt.Sprintf("mode_%d", mode.BetMode))
	if err := replaceArtifactDirectory(temporary, target, renamePath); err != nil {
		return fmt.Errorf("publish mode %d: replace pending mode: %w", mode.BetMode, err)
	}
	temporaryOwned = false
	return nil
}

type pendingModePayloads struct {
	prob     []byte
	aliases  []byte
	seedBank []byte
}

type artifactModeFileNames struct {
	descriptor string
	prob       string
	aliases    string
	seedBank   string
}

// modeFileNames centralizes the preserved Artifact v1 filenames. Pending mode
// directories and final bundles use the same names, which makes final assembly
// a byte-for-byte copy instead of a second serialization step.
func modeFileNames(mode int) artifactModeFileNames {
	return artifactModeFileNames{
		descriptor: fmt.Sprintf("mode_%d.json", mode),
		prob:       fmt.Sprintf("prob_%d.bin", mode),
		aliases:    fmt.Sprintf("aliases_%d.bin", mode),
		seedBank:   fmt.Sprintf("seed_bank_%d.bin", mode),
	}
}

// encodePendingMode turns the in-memory alias table into the production binary
// layout and binds its descriptor to exact byte sizes and SHA-256 digests.
func encodePendingMode(mode MaterializedMode) (optimalrt.ManifestMode, pendingModePayloads, error) {
	prob, aliases, err := encodeAliasTable(mode.Picker)
	if err != nil {
		return optimalrt.ManifestMode{}, pendingModePayloads{}, err
	}
	names := modeFileNames(mode.BetMode)
	payloads := pendingModePayloads{
		prob:     prob,
		aliases:  aliases,
		seedBank: append([]byte(nil), mode.SeedBank...),
	}
	return optimalrt.ManifestMode{
		BetUnit:   mode.BetUnit,
		Size:      mode.Picker.Size,
		SeedLen:   len(mode.Samples[0].Snapshot),
		SeedCount: mode.Picker.Size,
		Prob:      artifactFileReference(names.prob, payloads.prob),
		Aliases:   artifactFileReference(names.aliases, payloads.aliases),
		SeedBank:  artifactFileReference(names.seedBank, payloads.seedBank),
	}, payloads, nil
}

// inspectPendingModes validates the exact pending directory topology and every
// completed mode. Missing mode directories are normal algorithm state; corrupt,
// unexpected, or runtime-incompatible files are operational publication errors.
func inspectPendingModes(pending string, betUnits []int) ([]optimalrt.ManifestMode, []int, []int, error) {
	entries, err := os.ReadDir(pending)
	if err != nil {
		return nil, nil, nil, err
	}
	allowed := map[string]struct{}{modeStageState: {}}
	for mode := range betUnits {
		allowed[fmt.Sprintf("mode_%d", mode)] = struct{}{}
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok {
			return nil, nil, nil, fmt.Errorf("unexpected pending entry %q", entry.Name())
		}
	}

	descriptors := make([]optimalrt.ManifestMode, len(betUnits))
	staged := make([]int, 0, len(betUnits))
	missing := make([]int, 0, len(betUnits))
	for mode, betUnit := range betUnits {
		directory := filepath.Join(pending, fmt.Sprintf("mode_%d", mode))
		info, err := os.Lstat(directory)
		if os.IsNotExist(err) {
			missing = append(missing, mode)
			continue
		}
		if err != nil {
			return nil, nil, nil, fmt.Errorf("inspect mode %d directory: %w", mode, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, nil, fmt.Errorf("mode %d pending entry must be a real directory", mode)
		}
		descriptor, err := verifyPendingMode(directory, mode, betUnit)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("mode %d: %w", mode, err)
		}
		descriptors[mode] = descriptor
		staged = append(staged, mode)
	}
	return descriptors, staged, missing, nil
}

// verifyPendingMode independently reopens a staged mode and proves that its
// descriptor names exactly the expected mode files and that every referenced
// byte sequence still matches its declared size and digest.
func verifyPendingMode(directory string, mode, betUnit int) (optimalrt.ManifestMode, error) {
	names := modeFileNames(mode)
	raw, err := os.ReadFile(filepath.Join(directory, names.descriptor))
	if err != nil {
		return optimalrt.ManifestMode{}, fmt.Errorf("read descriptor: %w", err)
	}
	var descriptor optimalrt.ManifestMode
	if err := decodeStrictJSON(raw, &descriptor); err != nil {
		return optimalrt.ManifestMode{}, fmt.Errorf("parse descriptor: %w", err)
	}
	if descriptor.BetUnit != betUnit {
		return optimalrt.ManifestMode{}, fmt.Errorf("bet unit mismatch: descriptor=%d runtime=%d", descriptor.BetUnit, betUnit)
	}
	probe := optimalrt.Manifest{
		SchemaVersion:  optimalrt.ManifestSchemaV1,
		ArtifactID:     "pending-mode-validation",
		SnapshotFormat: "pending-mode-validation",
		Modes:          []optimalrt.ManifestMode{descriptor},
	}
	if err := probe.Validate(); err != nil {
		return optimalrt.ManifestMode{}, err
	}
	if descriptor.Prob.Path != names.prob || descriptor.Aliases.Path != names.aliases || descriptor.SeedBank.Path != names.seedBank {
		return optimalrt.ManifestMode{}, fmt.Errorf("descriptor paths do not match mode %d filenames", mode)
	}

	expected := map[string]struct{}{names.descriptor: {}}
	for kind, ref := range map[string]optimalrt.FileRef{
		"prob": descriptor.Prob, "aliases": descriptor.Aliases, "seed_bank": descriptor.SeedBank,
	} {
		expected[ref.Path] = struct{}{}
		data, err := os.ReadFile(filepath.Join(directory, ref.Path))
		if err != nil {
			return optimalrt.ManifestMode{}, fmt.Errorf("read %s: %w", kind, err)
		}
		actual := artifactFileReference(ref.Path, data)
		if actual.Size != ref.Size || actual.SHA256 != ref.SHA256 {
			return optimalrt.ManifestMode{}, fmt.Errorf("%s size or sha256 mismatch", kind)
		}
	}
	if err := verifyArtifactFileSet(directory, expected); err != nil {
		return optimalrt.ManifestMode{}, err
	}
	return descriptor, nil
}

// pendingModePaths reports deterministic, directly inspectable paths for every
// staged mode. It never includes a manifest path because the pending area is not
// a runtime bundle and intentionally contains no manifest.
func pendingModePaths(pending string, modes []int) []string {
	paths := make([]string, 0, len(modes)*4)
	for _, mode := range modes {
		directory := filepath.Join(pending, fmt.Sprintf("mode_%d", mode))
		names := modeFileNames(mode)
		paths = append(paths,
			filepath.Join(directory, names.descriptor),
			filepath.Join(directory, names.prob),
			filepath.Join(directory, names.aliases),
			filepath.Join(directory, names.seedBank),
		)
	}
	return paths
}

// publishPendingBundle assembles immutable per-mode stage directories into the
// unchanged flat Artifact v1 layout. The visible target is replaced only after
// manifest validation, file-set verification, and a production-loader replay.
func (writer FileArtifactWriter) publishPendingBundle(
	ctx context.Context,
	gid spec.GID,
	metadata modeStageMetadata,
	descriptors []optimalrt.ManifestMode,
) (PublishedArtifact, error) {
	pending := modeStageDirectory(writer.Directory, gid)
	staging, err := os.MkdirTemp(writer.Directory, fmt.Sprintf(".game_%d-bundle-", gid))
	if err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish complete artifact: create bundle staging directory: %w", err)
	}
	stagingOwned := true
	defer func() {
		if stagingOwned {
			_ = os.RemoveAll(staging)
		}
	}()

	payloadNames := make([]string, 0, len(descriptors)*4)
	for mode := range descriptors {
		if err := ctx.Err(); err != nil {
			return PublishedArtifact{}, fmt.Errorf("publish complete artifact: %w", err)
		}
		names := modeFileNames(mode)
		sourceDirectory := filepath.Join(pending, fmt.Sprintf("mode_%d", mode))
		for _, name := range []string{names.descriptor, names.prob, names.aliases, names.seedBank} {
			data, err := os.ReadFile(filepath.Join(sourceDirectory, name))
			if err != nil {
				return PublishedArtifact{}, fmt.Errorf("publish complete artifact: read mode %d %s: %w", mode, name, err)
			}
			if err := writeArtifactFile(filepath.Join(staging, name), data); err != nil {
				return PublishedArtifact{}, fmt.Errorf("publish complete artifact: write mode %d %s: %w", mode, name, err)
			}
			payloadNames = append(payloadNames, name)
		}
	}

	manifest := optimalrt.Manifest{
		SchemaVersion:  optimalrt.ManifestSchemaV1,
		SnapshotFormat: metadata.SnapshotFormat,
		Modes:          descriptors,
	}
	manifest.ArtifactID = contentArtifactID(manifest.SnapshotFormat, manifest.Modes)
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish complete artifact: marshal manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := writeArtifactFile(filepath.Join(staging, "manifest.json"), manifestBytes); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish complete artifact: write manifest last: %w", err)
	}
	verified, err := verifyStagedArtifact(staging)
	if err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish complete artifact: verify bundle: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish complete artifact: %w", err)
	}

	renamePath := writer.renamePath
	if renamePath == nil {
		renamePath = os.Rename
	}
	target := filepath.Join(writer.Directory, fmt.Sprintf("game_%d", gid))
	if err := replaceArtifactDirectory(staging, target, renamePath); err != nil {
		return PublishedArtifact{}, fmt.Errorf("publish complete artifact: replace %s: %w", target, err)
	}
	stagingOwned = false

	paths := make([]string, len(payloadNames))
	for i, name := range payloadNames {
		paths[i] = filepath.Join(target, name)
	}
	return PublishedArtifact{
		Complete:     true,
		Formats:      []OutputFormat{OutputOptimalArtifactV1},
		ManifestPath: filepath.Join(target, "manifest.json"),
		Paths:        paths,
		ArtifactID:   verified.ArtifactID,
	}, nil
}

// writeAtomicJSON prevents a crash during pending-metadata initialization from
// leaving a valid-looking truncated state file. The temp file and destination
// share a directory, so the final rename is atomic on supported filesystems.
func writeAtomicJSON(directory, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(directory, ".state-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, filepath.Join(directory, name))
}

// decodeStrictJSON keeps persistent optimizer-owned state as strict as YAML
// input: unknown fields and trailing documents are rejected instead of being
// silently ignored during a future schema change.
func decodeStrictJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}
