// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package optimalrt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/zintix-labs/problab/spec"
)

type cacheEntry struct {
	ready    chan struct{}
	artifact *Artifact
	err      error
}

// Store owns all optimal artifacts for exactly one Problab instance.
type Store struct {
	source         artifactSource
	snapshotFormat string
	snapshotSize   int

	mu        sync.Mutex
	entries   map[string]*cacheEntry
	closed    bool
	loading   sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
	loads     atomic.Uint64
}

func NewStore(source fs.FS, snapshotFormat string, snapshotSize int) *Store {
	return &Store{source: fsSource{fsys: source}, snapshotFormat: snapshotFormat, snapshotSize: snapshotSize, entries: make(map[string]*cacheEntry)}
}

func NewDirStore(root, snapshotFormat string, snapshotSize int) (*Store, error) {
	source, err := newDirSource(root)
	if err != nil {
		return nil, err
	}
	return &Store{source: source, snapshotFormat: snapshotFormat, snapshotSize: snapshotSize, entries: make(map[string]*cacheEntry)}, nil
}

// Resolve returns the single immutable Artifact associated with the setting.
// Concurrent callers for the same key wait for the first load to finish.
func (s *Store) Resolve(gs *spec.GameSetting) (*Artifact, error) {
	if gs == nil {
		return nil, fmt.Errorf("game setting is nil")
	}
	if !gs.OptimalSetting.UseOptimal {
		return nil, nil
	}
	key := artifactKey(gs)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("optimal store is closed")
	}
	if existing, ok := s.entries[key]; ok {
		s.mu.Unlock()
		<-existing.ready
		if existing.err != nil {
			return nil, existing.err
		}
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return nil, fmt.Errorf("optimal store is closed")
		}
		if err := existing.artifact.ValidateBetUnits(gs.BetUnits); err != nil {
			return nil, err
		}
		return existing.artifact, nil
	}
	entry := &cacheEntry{ready: make(chan struct{})}
	s.entries[key] = entry
	s.loading.Add(1)
	s.mu.Unlock()
	defer s.loading.Done()

	var artifact *Artifact
	var err error
	if gs.OptimalSetting.Artifact != "" {
		artifact, err = loadBinaryArtifact(s.source, gs.OptimalSetting.Artifact, s.snapshotFormat, s.snapshotSize, gs)
	} else {
		artifact, err = loadLegacyArtifact(s.source, key, s.snapshotSize, gs)
	}
	s.loads.Add(1)

	s.mu.Lock()
	entry.artifact = artifact
	entry.err = err
	close(entry.ready)
	s.mu.Unlock()
	return artifact, err
}

// Close prevents new resolves, waits for any in-progress load, then releases
// every unique artifact exactly once.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.loading.Wait()

		s.mu.Lock()
		artifacts := make([]*Artifact, 0, len(s.entries))
		for _, entry := range s.entries {
			if entry.artifact != nil {
				artifacts = append(artifacts, entry.artifact)
			}
		}
		s.mu.Unlock()
		for _, artifact := range artifacts {
			if err := artifact.Close(); err != nil && s.closeErr == nil {
				s.closeErr = err
			}
		}
	})
	return s.closeErr
}

func artifactKey(gs *spec.GameSetting) string {
	opt := gs.OptimalSetting
	if opt.Artifact != "" {
		return "manifest:" + opt.Artifact
	}
	return legacyKey(opt, gs.BetUnits)
}

func legacyKey(opt spec.OptimalSetting, betUnits []int) string {
	var b strings.Builder
	b.WriteString("legacy-v0\x00")
	for _, path := range opt.Gachas {
		b.WriteString(path)
		b.WriteByte(0)
	}
	b.WriteByte(1)
	for _, path := range opt.SeedBank {
		b.WriteString(path)
		b.WriteByte(0)
	}
	b.WriteByte(2)
	for _, betUnit := range betUnits {
		fmt.Fprintf(&b, "%d\x00", betUnit)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "legacy:" + hex.EncodeToString(sum[:])
}

// LoadCount is exposed for diagnostics and invariants tests.
func (s *Store) LoadCount() uint64 {
	if s == nil {
		return 0
	}
	return s.loads.Load()
}
