// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import "fmt"

// 本檔把每個 B0 案例鍵對應到「現在」的建構寫法。
//
// 施工步驟 1、2：舊入口仍存在，直接呼叫舊入口（見 git 歷史）。
// 施工步驟 3 起（本版本）：舊入口已刪除，本檔就是 PRD 0012 §3.6 遷移對照表的
// 可執行版本；案例鍵與 B0 不變，比對基準由 baselineCase.target() 決定。

// baselineBuildersMigrated 標示本檔是否已改寫為新寫法。false 時比對 C1（舊入口對
// 自己的 B0 案例）；true 時比對 C2（依案例映射表，被移除模式比對參考案例）。
const baselineBuildersMigrated = true

// baselineEngineOmitsSnapshot 標示引擎是否已在省略模式下跳過 Snapshot()（施工步驟 2）。
// false 時 fixture 遊戲在 Simulator 中也必須與 B0 完全相同；true 時只比對非 Extend
// 結果，且 Snapshot() 呼叫次數必須為 0（techspec C3）。
const baselineEngineOmitsSnapshot = true

func baselineMachineOmitExtend(m *Machine) bool { return m.OmitExtend() }

func (s seedArg) seed(entry string) Seed {
	switch entry {
	case "NewMachineWithSeedBytes", "NewUnoptimizedMachineWithSeedBytes",
		"NewMachineByJSONWithSeedBytes", "NewMachineByYAMLWithSeedBytes",
		"NewSimulatorWithSeedBytes", "NewSimulatorByJSONWithSeedBytes", "NewSimulatorByYAMLWithSeedBytes":
		return SeedBytes(s.Bytes)
	case "NewMachineWithSeedString", "NewSimulatorWithSeedString":
		return SeedString(s.Str)
	case "NewMachine", "NewSimulator":
		return Seed{}
	}
	return SeedInt64(s.Int)
}

func rawSetting(entry string, raw []byte) SettingSource {
	kind, ok := machineByRawEntries[entry]
	if !ok {
		kind, ok = simulatorByRawEntries[entry]
	}
	switch {
	case !ok:
		return SettingSource{}
	case kind == "json":
		return SettingFromJSON(raw)
	default:
		return SettingFromYAML(raw)
	}
}

func buildBaselineMachine(lab *Problab, c baselineCase) (*Machine, error) {
	switch c.Entry {
	case "NewMachine":
		if !c.IsSim {
			// 舊的單台 production Machine：公開入口已移除，以測試輔助函式重現原流程（R2）。
			return newProductionMachineForTest(lab, c.Game)
		}
		return lab.NewMachine(c.Game, MachineConfig{})
	case "NewMachineWithSeed", "NewMachineWithSeedBytes", "NewMachineWithSeedString":
		return lab.NewMachine(c.Game, MachineConfig{Seed: c.Seed.seed(c.Entry)})
	case "NewUnoptimizedMachineWithSeed", "NewUnoptimizedMachineWithSeedBytes":
		return lab.NewMachine(c.Game, MachineConfig{Seed: c.Seed.seed(c.Entry), Unoptimized: true})
	case "NewMachineByJSON", "NewMachineByJSONWithSeedBytes", "NewMachineByYAML", "NewMachineByYAMLWithSeedBytes":
		return lab.NewMachine(c.Game, MachineConfig{Seed: c.Seed.seed(c.Entry), Setting: rawSetting(c.Entry, c.Raw)})
	}
	return nil, fmt.Errorf("unknown machine entry %q", c.Entry)
}

func buildBaselineSimulator(lab *Problab, c baselineCase) (*Simulator, error) {
	switch c.Entry {
	case "NewSimulator", "NewSimulatorWithSeed", "NewSimulatorWithSeedBytes", "NewSimulatorWithSeedString":
		return lab.NewSimulator(c.Game, MachineConfig{Seed: c.Seed.seed(c.Entry)})
	case "NewSimulatorByJSON", "NewSimulatorByJSONWithSeedBytes", "NewSimulatorByYAML", "NewSimulatorByYAMLWithSeedBytes":
		return lab.NewSimulator(c.Game, MachineConfig{Seed: c.Seed.seed(c.Entry), Setting: rawSetting(c.Entry, c.Raw)})
	}
	return nil, fmt.Errorf("unknown simulator entry %q", c.Entry)
}

// buildBaselineReference 只用於錄製 B0。舊入口刪除後不再能錄製 B0。
func buildBaselineReference(*Problab, baselineCase) (*Machine, error) {
	return nil, errBaselineRecordClosed
}
