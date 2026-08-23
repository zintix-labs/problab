// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package spec

import "testing"

func TestOptimalSettingRequiresExactBetUnitCoverage(t *testing.T) {
	gs := &GameSetting{
		GameName:    "test",
		BetUnits:    []int{10, 20},
		MaxWinLimit: 100,
		OptimalSetting: OptimalSetting{
			UseOptimal: true,
			Gachas:     []string{"gacha_0.json.zst"},
			SeedBank:   []string{"seed_bank_0.bin"},
		},
	}

	if err := gs.valid(); err == nil {
		t.Fatal("expected exact bet-unit coverage validation error")
	}
}

func TestOptimalSettingAcceptsExactBetUnitCoverage(t *testing.T) {
	s := OptimalSetting{
		UseOptimal: true,
		Gachas:     []string{"gacha_0.json.zst", "gacha_1.json.zst"},
		SeedBank:   []string{"seed_bank_0.bin", "seed_bank_1.bin"},
	}
	if err := s.valid(); err != nil {
		t.Fatalf("valid optimal setting rejected: %v", err)
	}
}

func TestOptimalSettingAcceptsManifestAndRejectsMixedFormats(t *testing.T) {
	manifest := OptimalSetting{UseOptimal: true, Artifact: "game_0/manifest.json"}
	if err := manifest.valid(); err != nil {
		t.Fatalf("valid manifest setting rejected: %v", err)
	}
	manifest.Gachas = []string{"gacha.json.zst"}
	if err := manifest.valid(); err == nil {
		t.Fatal("manifest and legacy settings were accepted together")
	}
}
