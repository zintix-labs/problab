// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package optimalrt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/zintix-labs/problab/sdk/sampler"
	"github.com/zintix-labs/problab/spec"
)

func loadBinaryArtifact(source artifactSource, manifestPath, expectedSnapshotFormat string, expectedSnapshotSize int, gs *spec.GameSetting) (*Artifact, error) {
	if !validArtifactPath(manifestPath) {
		return nil, fmt.Errorf("invalid optimal manifest path: %q", manifestPath)
	}
	raw, err := source.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read optimal manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("parse optimal manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if len(manifest.Modes) != len(gs.BetUnits) {
		return nil, fmt.Errorf("manifest mode count (%d) must match bet_units count (%d)", len(manifest.Modes), len(gs.BetUnits))
	}
	if manifest.SnapshotFormat != expectedSnapshotFormat {
		return nil, fmt.Errorf("snapshot format mismatch: artifact=%q runtime=%q", manifest.SnapshotFormat, expectedSnapshotFormat)
	}

	modes := make([]mode, len(manifest.Modes))
	closeFns := make([]func() error, 0, len(manifest.Modes)*3)
	loaded := false
	defer func() {
		if !loaded {
			for i := len(closeFns) - 1; i >= 0; i-- {
				_ = closeFns[i]()
			}
		}
	}()
	backend := ""
	for i, descriptor := range manifest.Modes {
		if descriptor.SeedLen != expectedSnapshotSize {
			return nil, fmt.Errorf("manifest mode[%d] snapshot size mismatch: artifact=%d runtime=%d", i, descriptor.SeedLen, expectedSnapshotSize)
		}
		if descriptor.BetUnit != gs.BetUnits[i] {
			return nil, fmt.Errorf("manifest mode[%d] bet_unit mismatch: artifact=%d config=%d", i, descriptor.BetUnit, gs.BetUnits[i])
		}
		prob, closeProb, probBackend, err := readVerifiedFile(source, manifestPath, descriptor.Prob)
		if err != nil {
			return nil, fmt.Errorf("load mode[%d] prob: %w", i, err)
		}
		closeFns = append(closeFns, closeProb)
		backend = mergeBackend(backend, probBackend)
		aliases, closeAliases, aliasBackend, err := readVerifiedFile(source, manifestPath, descriptor.Aliases)
		if err != nil {
			return nil, fmt.Errorf("load mode[%d] aliases: %w", i, err)
		}
		closeFns = append(closeFns, closeAliases)
		backend = mergeBackend(backend, aliasBackend)
		bank, closeBank, bankBackend, err := readVerifiedFile(source, manifestPath, descriptor.SeedBank)
		if err != nil {
			return nil, fmt.Errorf("load mode[%d] seed_bank: %w", i, err)
		}
		closeFns = append(closeFns, closeBank)
		backend = mergeBackend(backend, bankBackend)
		table, err := sampler.NewAliasTableF64Binary(prob, aliases, descriptor.Size)
		if err != nil {
			return nil, fmt.Errorf("load mode[%d] alias table: %w", i, err)
		}
		modes[i] = mode{picker: table, seedLen: descriptor.SeedLen, bank: bank}
	}
	loaded = true
	return newArtifact("manifest:"+manifestPath+":"+manifest.ArtifactID, backend, modes, gs.BetUnits, closeFns...), nil
}

func readVerifiedFile(source artifactSource, manifestPath string, ref FileRef) ([]byte, func() error, string, error) {
	name, err := resolveManifestRef(manifestPath, ref.Path)
	if err != nil {
		return nil, nil, "", err
	}
	data, closeFn, backend, err := source.OpenBinary(name, ref.Size)
	if err != nil {
		return nil, nil, "", err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != ref.SHA256 {
		_ = closeFn()
		return nil, nil, "", fmt.Errorf("sha256 mismatch for %s", name)
	}
	return data, closeFn, backend, nil
}

func mergeBackend(current, next string) string {
	if current == "" || current == next {
		return next
	}
	return "mixed"
}
