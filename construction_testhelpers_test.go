// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"crypto/sha256"
	"fmt"

	"github.com/zintix-labs/problab/spec"
)

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

// detReader 是無限長、可重現的 reader：第 n 個 32 位元組區塊為
// SHA-256(label || n)。它同時計數被讀取的位元組數。
type detReader struct {
	label   string
	counter uint64
	pending []byte
	read    int64
}

func newDetReader(label string) *detReader { return &detReader{label: label} }

func (r *detReader) Read(dst []byte) (int, error) {
	n := 0
	for n < len(dst) {
		if len(r.pending) == 0 {
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s#%d", r.label, r.counter)))
			r.counter++
			r.pending = sum[:]
		}
		c := copy(dst[n:], r.pending)
		r.pending = r.pending[c:]
		n += c
	}
	r.read += int64(n)
	return n, nil
}
