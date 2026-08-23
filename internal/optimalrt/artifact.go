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
	modes    []mode
	betUnits []int

	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
	closeFns  []func() error
}

func newArtifact(id, backend string, modes []mode, betUnits []int, closeFns ...func() error) *Artifact {
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

// Backend reports how the artifact is backed (legacy-memory, memory, mmap).
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

// PickSeed returns a read-only view of the selected seed. Callers must not
// mutate the returned bytes. Public result paths must copy the view before
// exposing it outside the runtime.
func (a *Artifact) PickSeed(betMode int, c *core.Core) ([]byte, error) {
	if a == nil {
		return nil, fmt.Errorf("optimal artifact is nil")
	}
	if a.closed.Load() {
		return nil, fmt.Errorf("optimal artifact is closed")
	}
	if betMode < 0 || betMode >= len(a.modes) {
		return nil, fmt.Errorf("bet_mode %d out of range for optimal (max: %d)", betMode, len(a.modes)-1)
	}
	m := &a.modes[betMode]
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
