// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"fmt"

	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

// 本檔把每個 B0 案例鍵對應到「現在」的建構寫法。
//
// 施工步驟 1、2：舊入口仍存在，直接呼叫舊入口。
// 施工步驟 3 起：舊入口刪除，本檔改寫為 PRD 0012 §3.6 的新寫法；案例鍵與 B0
// 不變，比對基準由 baselineCase.target() 決定。

// baselineBuildersMigrated 標示本檔是否已改寫為新寫法。false 時比對 C1（舊入口對
// 自己的 B0 案例）；true 時比對 C2（依案例映射表，被移除模式比對參考案例）。
const baselineBuildersMigrated = false

// baselineEngineOmitsSnapshot 標示引擎是否已在省略模式下跳過 Snapshot()（施工步驟 2）。
// false 時 fixture 遊戲在 Simulator 中也必須與 B0 完全相同；true 時只比對非 Extend
// 結果，且 Snapshot() 呼叫次數必須為 0（techspec C3）。
const baselineEngineOmitsSnapshot = false

func baselineMachineOmitExtend(m *Machine) bool { return m.gh.IsSim }

func buildBaselineMachine(lab *Problab, c baselineCase) (*Machine, error) {
	s := c.Seed
	switch c.Entry {
	case "NewMachine":
		return lab.NewMachine(c.Game, c.IsSim)
	case "NewMachineWithSeed":
		return lab.NewMachineWithSeed(c.Game, s.Int, c.IsSim)
	case "NewMachineWithSeedBytes":
		return lab.NewMachineWithSeedBytes(c.Game, s.Bytes, c.IsSim)
	case "NewMachineWithSeedString":
		return lab.NewMachineWithSeedString(c.Game, s.Str, c.IsSim)
	case "NewUnoptimizedMachineWithSeed":
		return lab.NewUnoptimizedMachineWithSeed(c.Game, s.Int, c.IsSim)
	case "NewUnoptimizedMachineWithSeedBytes":
		return lab.NewUnoptimizedMachineWithSeedBytes(c.Game, s.Bytes, c.IsSim)
	case "NewMachineByJSON":
		return lab.NewMachineByJSON(c.Raw, s.Int)
	case "NewMachineByJSONWithSeedBytes":
		return lab.NewMachineByJSONWithSeedBytes(c.Raw, s.Bytes)
	case "NewMachineByYAML":
		return lab.NewMachineByYAML(c.Raw, s.Int)
	case "NewMachineByYAMLWithSeedBytes":
		return lab.NewMachineByYAMLWithSeedBytes(c.Raw, s.Bytes)
	}
	return nil, fmt.Errorf("unknown machine entry %q", c.Entry)
}

func buildBaselineSimulator(lab *Problab, c baselineCase) (*Simulator, error) {
	s := c.Seed
	switch c.Entry {
	case "NewSimulator":
		return lab.NewSimulator(c.Game)
	case "NewSimulatorWithSeed":
		return lab.NewSimulatorWithSeed(c.Game, s.Int)
	case "NewSimulatorWithSeedBytes":
		return lab.NewSimulatorWithSeedBytes(c.Game, s.Bytes)
	case "NewSimulatorWithSeedString":
		return lab.NewSimulatorWithSeedString(c.Game, s.Str)
	case "NewSimulatorByJSON":
		return lab.NewSimulatorByJSON(c.Raw, s.Int)
	case "NewSimulatorByJSONWithSeedBytes":
		return lab.NewSimulatorByJSONWithSeedBytes(c.Raw, s.Bytes)
	case "NewSimulatorByYAML":
		return lab.NewSimulatorByYAML(c.Raw, s.Int)
	case "NewSimulatorByYAMLWithSeedBytes":
		return lab.NewSimulatorByYAMLWithSeedBytes(c.Raw, s.Bytes)
	}
	return nil, fmt.Errorf("unknown simulator entry %q", c.Entry)
}

// buildBaselineReference 建立被移除模式（省略 Extend 的 Machine）的參考案例：
// 相同設定、相同 seed、相同 optimal、deterministic、保留 Extend
// （techspec §6.1）。只在錄製 B0 時使用。
func buildBaselineReference(lab *Problab, c baselineCase) (*Machine, error) {
	var (
		gs  *spec.GameSetting
		err error
	)
	if kind, ok := machineByRawEntries[c.Entry]; ok {
		if kind == "json" {
			gs, err = spec.GetGameSettingByJSON(c.Raw)
		} else {
			gs, err = spec.GetGameSettingByYAML(c.Raw)
		}
		if err != nil {
			return nil, err
		}
		if err := lab.validCfg(gs); err != nil {
			return nil, err
		}
	} else {
		gs, err = lab.cat.GameSettingById(c.Game)
		if err != nil {
			return nil, err
		}
	}
	unoptimized := c.Entry == "NewUnoptimizedMachineWithSeed" || c.Entry == "NewUnoptimizedMachineWithSeedBytes"
	if unoptimized {
		raw := *gs
		raw.OptimalSetting = spec.OptimalSetting{}
		gs = &raw
	}
	art, err := lab.resolveOptimal(gs)
	if err != nil {
		return nil, err
	}
	var seed []byte
	switch c.Entry {
	case "NewMachine":
		seed, err = lab.generateSeed()
		if err != nil {
			return nil, err
		}
	case "NewMachineWithSeed", "NewUnoptimizedMachineWithSeed", "NewMachineByJSON", "NewMachineByYAML":
		seed = core.EncodeInt64Seed(c.Seed.Int)
	case "NewMachineWithSeedString":
		seed = []byte(c.Seed.Str)
	default:
		seed = c.Seed.Bytes
	}
	return newMachineWithSeed(gs, lab.reg, lab.cf, seed, false, art, rngDeterministic)
}
