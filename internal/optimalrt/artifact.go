// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package optimalrt owns the immutable runtime representation of optimizer
// artifacts. It is internal so the application-facing API remains Problab's
// constructor options rather than raw artifact objects.
package optimalrt

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/sampler"
)

type picker interface {
	Pick(*core.Core) int
	Len() int
}

type legacyPicker struct {
	table *sampler.AliasTableF64
}

func (p legacyPicker) Pick(c *core.Core) int { return p.table.Pick(c) }
func (p legacyPicker) Len() int              { return p.table.Size }

type mode struct {
	picker  picker
	seedLen int
	bank    []byte
}

// Artifact is a fully validated, immutable set of optimal sampling data.
// Its fields are deliberately unexported: Machines may sample it, but cannot
// replace or resize the shared tables or seed banks.
type Artifact struct {
	id       string
	backend  string
	modes    []modeSources
	betUnits []int

	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
	closeFns  []func() error
}

func newArtifact(id, backend string, modes []modeSources, betUnits []int, closeFns ...func() error) *Artifact {
	return &Artifact{
		id: id, backend: backend, modes: modes,
		betUnits: append([]int(nil), betUnits...), closeFns: closeFns,
	}
}

func (a *Artifact) ValidateBetUnits(betUnits []int) error {
	if a == nil {
		return fmt.Errorf("optimal artifact is nil")
	}
	if len(a.betUnits) != len(betUnits) {
		return fmt.Errorf("artifact bet-unit count mismatch: artifact=%d config=%d", len(a.betUnits), len(betUnits))
	}
	for i := range betUnits {
		if a.betUnits[i] != betUnits[i] {
			return fmt.Errorf("artifact bet_unit[%d] mismatch: artifact=%d config=%d", i, a.betUnits[i], betUnits[i])
		}
	}
	return nil
}

// ID is the canonical identity used by the owning Store.
func (a *Artifact) ID() string {
	if a == nil {
		return ""
	}
	return a.id
}

// Backend reports how the seed banks are backed: memory, mmap, or mixed.
func (a *Artifact) Backend() string {
	if a == nil {
		return ""
	}
	return a.backend
}

// ModeCount returns the number of bet modes represented by the artifact.
func (a *Artifact) ModeCount() int {
	if a == nil {
		return 0
	}
	return len(a.modes)
}

// seedSource 是一段的起點來源：seeded 為 true 時從 pair 抽 seed，否則照常使用機台的 RNG。
type seedSource struct {
	seeded bool
	pair   mode
}

// modeSources 是一個 bet mode 的起點來源：start 是初始段，after 以入口值為鍵。after 為 nil
// 表示沒有配置任何入口。
type modeSources struct {
	start seedSource
	after map[int]seedSource
}

// StartSeeded 回報 betMode 的初始段是否抽庫。betMode 超出範圍時回傳 false。
func (a *Artifact) StartSeeded(betMode int) bool {
	if a == nil || betMode < 0 || betMode >= len(a.modes) {
		return false
	}
	return a.modes[betMode].start.seeded
}

// Entry 回報入口 entry 是否配置在 betMode 的 after 中（configured），以及是否抽庫（seeded）。
func (a *Artifact) Entry(betMode, entry int) (seeded, configured bool) {
	if a == nil || betMode < 0 || betMode >= len(a.modes) {
		return false, false
	}
	src, ok := a.modes[betMode].after[entry]
	return src.seeded, ok
}

// Offers 回報 next 是否非空，且每個入口都配置在 betMode 的 after 中。這是交付未結束的段
// 之前的檢查：沒有配置的入口在下一段無法接續。
func (a *Artifact) Offers(betMode int, next []int) bool {
	if a == nil || betMode < 0 || betMode >= len(a.modes) || len(next) == 0 {
		return false
	}
	after := a.modes[betMode].after
	for _, entry := range next {
		if _, ok := after[entry]; !ok {
			return false
		}
	}
	return true
}

// PickStart 從 betMode 的 start 庫抽 seed，回傳唯讀的視圖：呼叫端不得修改，對外公開前
// 必須複製。
func (a *Artifact) PickStart(betMode int, c *core.Core) ([]byte, error) {
	m, err := a.modeSourcesOf(betMode)
	if err != nil {
		return nil, err
	}
	return m.start.pick(c, "start")
}

// PickEntry 從 betMode 的 after[entry] 庫抽 seed，回傳唯讀的視圖。
func (a *Artifact) PickEntry(betMode, entry int, c *core.Core) ([]byte, error) {
	m, err := a.modeSourcesOf(betMode)
	if err != nil {
		return nil, err
	}
	src, ok := m.after[entry]
	if !ok {
		return nil, fmt.Errorf("entry %d is not configured for bet_mode %d", entry, betMode)
	}
	return src.pick(c, fmt.Sprintf("after[%d]", entry))
}

func (a *Artifact) modeSourcesOf(betMode int) (*modeSources, error) {
	if a == nil {
		return nil, fmt.Errorf("optimal artifact is nil")
	}
	if a.closed.Load() {
		return nil, fmt.Errorf("optimal artifact is closed")
	}
	if betMode < 0 || betMode >= len(a.modes) {
		return nil, fmt.Errorf("bet_mode %d out of range for optimal (max: %d)", betMode, len(a.modes)-1)
	}
	return &a.modes[betMode], nil
}

func (s *seedSource) pick(c *core.Core, what string) ([]byte, error) {
	if !s.seeded {
		return nil, fmt.Errorf("%s has no gacha: it uses the machine RNG", what)
	}
	m := &s.pair
	idx := m.picker.Pick(c)
	start := idx * m.seedLen
	end := start + m.seedLen
	if idx < 0 || start < 0 || end > len(m.bank) || start >= end {
		return nil, fmt.Errorf("invalid gacha pick range: start=%d, end=%d, bank_len=%d", start, end, len(m.bank))
	}
	return m.bank[start:end], nil
}

// Close releases backend resources. The owner must ensure no Machine is still
// sampling this artifact before calling Close.
func (a *Artifact) Close() error {
	if a == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		a.closed.Store(true)
		for i := len(a.closeFns) - 1; i >= 0; i-- {
			if err := a.closeFns[i](); err != nil && a.closeErr == nil {
				a.closeErr = err
			}
		}
	})
	return a.closeErr
}

func (a *Artifact) Closed() bool {
	return a == nil || a.closed.Load()
}
