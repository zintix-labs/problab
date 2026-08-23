// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package sampler

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/zintix-labs/problab/sdk/core"
)

// AliasTableF64Binary is a read-only AliasTable view over little-endian binary
// data. The backing bytes may be regular heap memory or a read-only mmap.
// The owner must keep the bytes alive and immutable for the table's lifetime.
type AliasTableF64Binary struct {
	prob    []byte
	aliases []byte
	size    int
}

func NewAliasTableF64Binary(prob, aliases []byte, size int) (*AliasTableF64Binary, error) {
	if size <= 0 {
		return nil, fmt.Errorf("alias table size must be > 0")
	}
	if size > int(^uint(0)>>1)/8 {
		return nil, fmt.Errorf("alias table size overflows byte dimensions")
	}
	if len(prob) != size*8 {
		return nil, fmt.Errorf("prob byte length mismatch: got=%d want=%d", len(prob), size*8)
	}
	if len(aliases) != size*4 {
		return nil, fmt.Errorf("alias byte length mismatch: got=%d want=%d", len(aliases), size*4)
	}
	at := &AliasTableF64Binary{prob: prob, aliases: aliases, size: size}
	for i := 0; i < size; i++ {
		p := at.probability(i)
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return nil, fmt.Errorf("invalid alias probability at %d", i)
		}
		if alias := at.alias(i); alias < 0 || alias >= size {
			return nil, fmt.Errorf("invalid alias index at %d: %d", i, alias)
		}
	}
	return at, nil
}

func (at *AliasTableF64Binary) Len() int {
	if at == nil {
		return 0
	}
	return at.size
}

func (at *AliasTableF64Binary) Pick(c *core.Core) int {
	if at == nil || at.size == 0 {
		return -1
	}
	idx := c.IntN(at.size)
	if bernoulliF64(c, at.probability(idx)) {
		return idx
	}
	return at.alias(idx)
}

func (at *AliasTableF64Binary) probability(idx int) float64 {
	off := idx * 8
	return math.Float64frombits(binary.LittleEndian.Uint64(at.prob[off : off+8]))
}

func (at *AliasTableF64Binary) alias(idx int) int {
	off := idx * 4
	return int(binary.LittleEndian.Uint32(at.aliases[off : off+4]))
}
