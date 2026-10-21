// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// probeOmitLogic 在 build 當下記錄 gh.OmitExtend()，用來證明旗標在遊戲邏輯
// 建出之前就已設定（techspec P2、U1）。
type probeOmitLogic struct{}

func (probeOmitLogic) GetResult(r *buf.SpinRequest, gh *slot.Game) *buf.SpinResult {
	sr := gh.StartNewSpin(r)
	sr.End()
	return sr
}

func probeOmitSetting(t testing.TB) *spec.GameSetting {
	t.Helper()
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "logic_key: demo_normal", "logic_key: probe_omit", 1)
	gs, err := spec.GetGameSettingByYAML([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return gs
}

func probeOmitRegistry(t testing.TB, seen *[]bool) *slot.LogicRegistry {
	t.Helper()
	reg := slot.NewLogicRegistry()
	if err := slot.GameRegister("probe_omit", func(gh *slot.Game) (slot.GameLogic, error) {
		*seen = append(*seen, gh.OmitExtend())
		return probeOmitLogic{}, nil
	}, reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

// U1：gh.OmitExtend() 在 build 時即可讀到，值等於建構政策；涵蓋 slot.NewGame 與
// Machine 的私有建構核心。
func TestOmitExtendVisibleAtBuild(t *testing.T) {
	gs := probeOmitSetting(t)
	for _, omit := range []bool{true, false} {
		var seen []bool
		reg := probeOmitRegistry(t, &seen)
		rng, err := core.Default().New(core.EncodeInt64Seed(1))
		if err != nil {
			t.Fatal(err)
		}
		gh, err := slot.NewGame(gs, reg, core.New(rng), omit)
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != omit || gh.OmitExtend() != omit {
			t.Fatalf("slot.NewGame(omitExtend=%v): seen at build=%v, after=%v", omit, seen, gh.OmitExtend())
		}

		seen = nil
		m, err := newMachineWithSeed(gs, reg, core.Default(), core.EncodeInt64Seed(1), omit, nil, rngDeterministic)
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != omit || m.OmitExtend() != omit {
			t.Fatalf("Machine(omitExtend=%v): seen at build=%v, after=%v", omit, seen, m.OmitExtend())
		}
	}
}

// 私有建構核心拒絕 production 搭配省略 Extend（techspec §5.1、C7）。
func TestProductionMachineMustKeepExtend(t *testing.T) {
	gs := probeOmitSetting(t)
	var seen []bool
	_, err := newMachineWithSeed(gs, probeOmitRegistry(t, &seen), core.Default(), core.EncodeInt64Seed(1), true, nil, rngProduction)
	if err == nil {
		t.Fatal("production machine with omitExtend=true was accepted")
	}
	if len(seen) != 0 {
		t.Fatal("rejected machine still built its game logic")
	}
}

// U2：fixture 遊戲每轉 4 次 YieldResult，觸發 GMR pool 擴容；擴容後新建的 GMR
// 也必須遵守省略政策，且跨轉 Reset 後不變。
func TestOmitExtendAppliesToGrownGameModeResults(t *testing.T) {
	lab, err := newFixtureLab(t)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	gs, err := lab.cat.GameSettingById(fixtureGameID)
	if err != nil {
		t.Fatal(err)
	}
	for _, omit := range []bool{true, false} {
		m, err := newMachineWithSeed(gs, lab.reg, lab.cf, core.EncodeInt64Seed(7), omit, nil, rngDeterministic)
		if err != nil {
			t.Fatal(err)
		}
		before := fixtureSnapshotCalls.Load()
		for spin := range 20 {
			sr := m.SpinInternal(0)
			if len(sr.GameModeList) < fixtureBaseYield {
				t.Fatalf("spin %d: only %d mode results; pool growth not exercised", spin, len(sr.GameModeList))
			}
			for gi, gmr := range sr.GameModeList {
				for ai, act := range gmr.ActResults {
					if act.ActType != "screen" {
						continue
					}
					if (act.ExtendResult == nil) != omit {
						t.Fatalf("omitExtend=%v spin %d gmr %d act %d: ExtendResult=%v", omit, spin, gi, ai, act.ExtendResult)
					}
				}
			}
		}
		calls := fixtureSnapshotCalls.Load() - before
		if omit && calls != 0 {
			t.Fatalf("omitExtend=true: Snapshot called %d times", calls)
		}
		if !omit && calls == 0 {
			t.Fatal("omitExtend=false: Snapshot was never called")
		}
	}
}
