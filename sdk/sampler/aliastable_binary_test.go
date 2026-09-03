// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package sampler

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestAliasTableF64BinaryMatchesSliceTable(t *testing.T) {
	regular := BuildAliasTableF64([]float64{1e-40, 0.1, 0.3, 0.6})
	prob := make([]byte, regular.Size*8)
	aliases := make([]byte, regular.Size*4)
	for i := 0; i < regular.Size; i++ {
		binary.LittleEndian.PutUint64(prob[i*8:], math.Float64bits(regular.Prob[i]))
		binary.LittleEndian.PutUint32(aliases[i*4:], uint32(regular.Aliases[i]))
	}
	binaryTable, err := NewAliasTableF64Binary(prob, aliases, regular.Size)
	if err != nil {
		t.Fatalf("NewAliasTableF64Binary: %v", err)
	}

	c1 := newTestCore(t, 991)
	c2 := newTestCore(t, 991)
	for i := 0; i < 100_000; i++ {
		if got, want := binaryTable.Pick(c1), regular.Pick(c2); got != want {
			t.Fatalf("pick %d mismatch: binary=%d regular=%d", i, got, want)
		}
	}
}
