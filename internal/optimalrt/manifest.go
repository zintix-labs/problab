// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package optimalrt

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
)

const ManifestSchemaV1 = "problab.optimal/v1"

// Manifest is the portable description of an Optimal Artifact bundle.
type Manifest struct {
	SchemaVersion  string         `json:"schema_version"`
	ArtifactID     string         `json:"artifact_id"`
	SnapshotFormat string         `json:"snapshot_format,omitempty"`
	Modes          []ManifestMode `json:"modes"`
}

type ManifestMode struct {
	BetUnit   int     `json:"bet_unit"`
	Size      int     `json:"size"`
	SeedLen   int     `json:"seed_len"`
	SeedCount int     `json:"seed_count"`
	Prob      FileRef `json:"prob"`
	Aliases   FileRef `json:"aliases"`
	SeedBank  FileRef `json:"seed_bank"`
}

type FileRef struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != ManifestSchemaV1 {
		return fmt.Errorf("unsupported optimal manifest schema: %q", m.SchemaVersion)
	}
	if strings.TrimSpace(m.ArtifactID) == "" {
		return fmt.Errorf("optimal manifest artifact_id is required")
	}
	if strings.TrimSpace(m.SnapshotFormat) == "" {
		return fmt.Errorf("optimal manifest snapshot_format is required")
	}
	if len(m.Modes) == 0 {
		return fmt.Errorf("optimal manifest modes must not be empty")
	}
	for i, mode := range m.Modes {
		if mode.BetUnit <= 0 || mode.Size <= 0 || mode.SeedLen <= 0 || mode.SeedCount <= 0 {
			return fmt.Errorf("optimal manifest mode[%d] has invalid dimensions", i)
		}
		const maxInt64 = int64(^uint64(0) >> 1)
		if int64(mode.Size) > maxInt64/8 || int64(mode.SeedCount) > maxInt64/int64(mode.SeedLen) {
			return fmt.Errorf("optimal manifest mode[%d] dimensions overflow", i)
		}
		if mode.Size != mode.SeedCount {
			return fmt.Errorf("optimal manifest mode[%d] size and seed_count mismatch", i)
		}
		for name, ref := range map[string]FileRef{
			"prob": mode.Prob, "aliases": mode.Aliases, "seed_bank": mode.SeedBank,
		} {
			if err := ref.validate(); err != nil {
				return fmt.Errorf("optimal manifest mode[%d] %s: %w", i, name, err)
			}
		}
		if mode.Prob.Size != int64(mode.Size)*8 {
			return fmt.Errorf("optimal manifest mode[%d] prob size mismatch", i)
		}
		if mode.Aliases.Size != int64(mode.Size)*4 {
			return fmt.Errorf("optimal manifest mode[%d] aliases size mismatch", i)
		}
		if mode.SeedBank.Size != int64(mode.SeedCount)*int64(mode.SeedLen) {
			return fmt.Errorf("optimal manifest mode[%d] seed_bank size mismatch", i)
		}
	}
	return nil
}

func (r FileRef) validate() error {
	if !validArtifactPath(r.Path) {
		return fmt.Errorf("invalid relative path: %q", r.Path)
	}
	if r.Size <= 0 {
		return fmt.Errorf("file size must be > 0")
	}
	if len(r.SHA256) != 64 {
		return fmt.Errorf("sha256 must contain 64 hexadecimal characters")
	}
	for _, c := range r.SHA256 {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return fmt.Errorf("sha256 must be lowercase hexadecimal")
		}
	}
	return nil
}

func validArtifactPath(name string) bool {
	return name != "" && name != "." && fs.ValidPath(name) &&
		!strings.HasPrefix(name, "/") && !strings.ContainsAny(name, `\:`)
}

func resolveManifestRef(manifestPath, ref string) (string, error) {
	if !validArtifactPath(manifestPath) || !validArtifactPath(ref) {
		return "", fmt.Errorf("invalid artifact path")
	}
	joined := path.Join(path.Dir(manifestPath), ref)
	if !validArtifactPath(joined) {
		return "", fmt.Errorf("invalid resolved artifact path: %q", joined)
	}
	return joined, nil
}
