// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package spec

import (
	"strings"
	"testing"
)

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

// 舊設定的 artifact 鍵已不支援；use_optimal 而沒有 gachas 時，錯誤訊息指出要改列 gachas 與 seed_bank。
func TestOptimalSettingWithoutGachasPointsAtArtifactRemoval(t *testing.T) {
	err := OptimalSetting{UseOptimal: true}.valid()
	if err == nil || !strings.Contains(err.Error(), "artifact is no longer supported") {
		t.Fatalf("error %v", err)
	}
}
