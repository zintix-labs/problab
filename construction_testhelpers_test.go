// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import "github.com/zintix-labs/problab/spec"

// newProductionMachineForTest 只存在於測試，不屬於公開 API（PRD 0012 §3.2、
// techspec §5.5）。它經私有建構核心逐步重現舊 NewMachine(id, false) 的建立流程：
// 凍結檢查 → catalog 取設定 → resolveOptimal → generateSeed → production、保留
// Extend。用途只限於保留原有的 RNG 與 entropy 順序斷言。
func newProductionMachineForTest(p *Problab, id spec.GID) (*Machine, error) {
	b, err := p.resolveBuild(id, MachineConfig{}, "Machine")
	if err != nil {
		return nil, err
	}
	return newMachineWithSeed(b.gs, p.reg, p.cf, b.seed, false, b.optimal, rngProduction)
}
