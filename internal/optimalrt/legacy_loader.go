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
	"sort"

	"github.com/klauspost/compress/zstd"
	"github.com/zintix-labs/problab/sdk/sampler"
	"github.com/zintix-labs/problab/spec"
)

type legacyGacha struct {
	Picker  *sampler.AliasTableF64 `json:"picker"`
	SeedLen int                    `json:"seed_len"`
}

func loadLegacyArtifact(source artifactSource, key string, expectedSnapshotSize int, checkSnapshot func([]byte) error, gs *spec.GameSetting) (*Artifact, error) {
	opt := gs.OptimalSetting
	if len(opt.Gachas) != len(gs.BetUnits) {
		return nil, fmt.Errorf("gachas count (%d) must match bet_units count (%d)", len(opt.Gachas), len(gs.BetUnits))
	}
	if len(opt.SeedBank) != len(gs.BetUnits) {
		return nil, fmt.Errorf("seed_bank count (%d) must match bet_units count (%d)", len(opt.SeedBank), len(gs.BetUnits))
	}

	modes := make([]modeSources, len(gs.BetUnits))
	closeFns := make([]func() error, 0, len(gs.BetUnits))
	loaded := false
	defer func() {
		if !loaded {
			for i := len(closeFns) - 1; i >= 0; i-- {
				_ = closeFns[i]()
			}
		}
	}()
	backend := ""
	for i := range gs.BetUnits {
		m, closeFn, modeBackend, err := loadGachaPair(source, opt.Gachas[i], opt.SeedBank[i], expectedSnapshotSize, checkSnapshot)
		if err != nil {
			return nil, fmt.Errorf("load bet mode %d: %w", i, err)
		}
		closeFns = append(closeFns, closeFn)
		backend = mergeBackend(backend, modeBackend)
		modes[i] = modeSources{start: seedSource{seeded: true, pair: m}}
	}
	loaded = true
	return newArtifact(key, backend, modes, gs.BetUnits, closeFns...), nil
}

// loadModesArtifact 載入 optimal_setting.modes：每個 bet mode 先載入 start，再依入口值遞增
// 載入 after，讓載入順序與錯誤訊息固定。抽庫的位置經由 loadGachaPair（mmap 與首筆檢查），
// 照常使用 RNG 的位置不開任何檔案。任一份失敗時，以相反順序釋放已載入的部分。同一組路徑
// 出現多次時各載入一次。
func loadModesArtifact(files artifactSource, key string, expectedSnapshotSize int, checkSnapshot func([]byte) error, gs *spec.GameSetting) (*Artifact, error) {
	settings := gs.OptimalSetting.Modes
	if len(settings) != len(gs.BetUnits) {
		return nil, fmt.Errorf("modes count (%d) must match bet_units count (%d)", len(settings), len(gs.BetUnits))
	}
	modes := make([]modeSources, len(settings))
	var closeFns []func() error
	loaded := false
	defer func() {
		if !loaded {
			for i := len(closeFns) - 1; i >= 0; i-- {
				_ = closeFns[i]()
			}
		}
	}()
	backend := ""
	load := func(slot *spec.GachaSlot, where string) (seedSource, error) {
		if slot == nil {
			return seedSource{}, fmt.Errorf("%s: gacha slot is missing", where)
		}
		if !slot.Seeded() {
			return seedSource{}, nil
		}
		m, closeFn, slotBackend, err := loadGachaPair(files, slot.Dist, slot.Bank, expectedSnapshotSize, checkSnapshot)
		if err != nil {
			return seedSource{}, fmt.Errorf("%s: %w", where, err)
		}
		closeFns = append(closeFns, closeFn)
		backend = mergeBackend(backend, slotBackend)
		return seedSource{seeded: true, pair: m}, nil
	}
	for i, setting := range settings {
		start, err := load(setting.Start, fmt.Sprintf("load bet mode %d start", i))
		if err != nil {
			return nil, err
		}
		modes[i].start = start
		if setting.After == nil {
			continue
		}
		modes[i].after = make(map[int]seedSource, len(setting.After))
		for _, entry := range sortedEntries(setting.After) {
			src, err := load(setting.After[entry], fmt.Sprintf("load bet mode %d after[%d]", i, entry))
			if err != nil {
				return nil, err
			}
			modes[i].after[entry] = src
		}
	}
	if backend == "" {
		backend = "none" // 所有位置都照常使用 RNG：沒有載入任何 seed bank
	}
	loaded = true
	return newArtifact(key, backend, modes, gs.BetUnits, closeFns...), nil
}

func sortedEntries(after map[int]*spec.GachaSlot) []int {
	entries := make([]int, 0, len(after))
	for entry := range after {
		entries = append(entries, entry)
	}
	sort.Ints(entries)
	return entries
}

// loadGachaPair 載入一對 gacha 與 seed bank。seed bank 經由 OpenBinary 取得：目錄來源在支援的平台上
// 是唯讀 mmap，其餘讀入記憶體；closeFn 釋放它。checkSnapshot 不為 nil 時，以它檢查第一筆快照。
func loadGachaPair(source artifactSource, gachaPath, bankPath string, snapshotSize int, checkSnapshot func([]byte) error) (mode, func() error, string, error) {
	gacha, err := loadLegacyGacha(source, gachaPath)
	if err != nil {
		return mode{}, nil, "", fmt.Errorf("load gacha (%s): %w", gachaPath, err)
	}
	if gacha.SeedLen != snapshotSize {
		return mode{}, nil, "", fmt.Errorf("gacha (%s) snapshot size mismatch: artifact=%d runtime=%d", gachaPath, gacha.SeedLen, snapshotSize)
	}
	if gacha.Picker.Size > math.MaxInt/gacha.SeedLen {
		return mode{}, nil, "", fmt.Errorf("gacha (%s) seed bank size overflows: size=%d seed_len=%d", gachaPath, gacha.Picker.Size, gacha.SeedLen)
	}
	bank, closeFn, backend, err := source.OpenBinary(bankPath, int64(gacha.Picker.Size*gacha.SeedLen))
	if err != nil {
		return mode{}, nil, "", fmt.Errorf("load seed_bank (%s): %w", bankPath, err)
	}
	if checkSnapshot != nil {
		if err := checkSnapshot(bank[:gacha.SeedLen]); err != nil {
			_ = closeFn()
			return mode{}, nil, "", fmt.Errorf("seed_bank (%s): the runtime PRNG rejects the first snapshot: %w", bankPath, err)
		}
	}
	return mode{picker: legacyPicker{table: gacha.Picker}, seedLen: gacha.SeedLen, bank: bank}, closeFn, backend, nil
}

func mergeBackend(current, next string) string {
	if current == "" || current == next {
		return next
	}
	return "mixed"
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
