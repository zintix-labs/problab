// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package optimalrt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"

	"github.com/klauspost/compress/zstd"
	"github.com/zintix-labs/problab/sdk/sampler"
	"github.com/zintix-labs/problab/spec"
)

type legacyGacha struct {
	Picker  *sampler.AliasTableF64 `json:"picker"`
	SeedLen int                    `json:"seed_len"`
}

func loadLegacyArtifact(source artifactSource, key string, expectedSnapshotSize int, gs *spec.GameSetting) (*Artifact, error) {
	opt := gs.OptimalSetting
	if len(opt.Gachas) != len(gs.BetUnits) {
		return nil, fmt.Errorf("gachas count (%d) must match bet_units count (%d)", len(opt.Gachas), len(gs.BetUnits))
	}
	if len(opt.SeedBank) != len(gs.BetUnits) {
		return nil, fmt.Errorf("seed_bank count (%d) must match bet_units count (%d)", len(opt.SeedBank), len(gs.BetUnits))
	}

	modes := make([]mode, len(gs.BetUnits))
	for i := range gs.BetUnits {
		gacha, err := loadLegacyGacha(source, opt.Gachas[i])
		if err != nil {
			return nil, fmt.Errorf("load gacha[%d] (%s): %w", i, opt.Gachas[i], err)
		}
		if gacha.SeedLen != expectedSnapshotSize {
			return nil, fmt.Errorf("gacha[%d] snapshot size mismatch: artifact=%d runtime=%d", i, gacha.SeedLen, expectedSnapshotSize)
		}
		bank, err := source.ReadFile(opt.SeedBank[i])
		if err != nil {
			return nil, fmt.Errorf("load seed_bank[%d] (%s): %w", i, opt.SeedBank[i], err)
		}
		if len(bank) != gacha.Picker.Size*gacha.SeedLen {
			return nil, fmt.Errorf("seed_bank[%d] size mismatch: got=%d want=%d", i, len(bank), gacha.Picker.Size*gacha.SeedLen)
		}
		modes[i] = mode{picker: legacyPicker{table: gacha.Picker}, seedLen: gacha.SeedLen, bank: bank}
	}
	return newArtifact(key, "legacy-memory", modes, gs.BetUnits), nil
}

func loadLegacyGacha(source artifactSource, path string) (*legacyGacha, error) {
	if path == "" {
		return nil, fmt.Errorf("gacha path is empty")
	}
	compressed, err := source.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read gacha file: %w", err)
	}
	zr, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("create zstd reader: %w", err)
	}
	defer zr.Close()
	jsonBytes, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("read decompressed data: %w", err)
	}

	var g legacyGacha
	if err := json.Unmarshal(jsonBytes, &g); err != nil {
		return nil, fmt.Errorf("unmarshal gacha json: %w", err)
	}
	if err := validateLegacyGacha(&g); err != nil {
		return nil, err
	}
	return &g, nil
}

func validateLegacyGacha(g *legacyGacha) error {
	if g.Picker == nil {
		return fmt.Errorf("gacha picker is required")
	}
	if g.SeedLen <= 0 {
		return fmt.Errorf("gacha seed_len must be > 0")
	}
	p := g.Picker
	if p.Size <= 0 || len(p.Prob) != p.Size || len(p.Aliases) != p.Size {
		return fmt.Errorf("invalid alias table dimensions: size=%d prob=%d aliases=%d", p.Size, len(p.Prob), len(p.Aliases))
	}
	for i := 0; i < p.Size; i++ {
		if math.IsNaN(p.Prob[i]) || math.IsInf(p.Prob[i], 0) || p.Prob[i] < 0 || p.Prob[i] > 1 {
			return fmt.Errorf("invalid alias probability at %d", i)
		}
		if p.Aliases[i] < 0 || p.Aliases[i] >= p.Size {
			return fmt.Errorf("invalid alias index at %d: %d", i, p.Aliases[i])
		}
	}
	return nil
}
