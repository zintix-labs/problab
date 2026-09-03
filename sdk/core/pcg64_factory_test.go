// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package core

import (
	"bytes"
	"encoding/hex"
	"math"
	"testing"
)

func TestPCG64CompatibilityGolden(t *testing.T) {
	tests := []struct {
		seed     int64
		snapshot string
		outputs  []uint64
	}{
		{
			seed: 0, snapshot: "7063673a6e789e6aa1b965f48333db8c3066051c",
			outputs: []uint64{
				0x4579b1ed6fb523aa, 0x3929e4836cb85419, 0x695d1292d1af2dda, 0x358b6d967bdd4987,
				0x64e87bc7ed207b8f, 0x054e53e10ab3dbd4, 0xf592c03bcc305473, 0x26c9cca7fe08b24f,
				0x83dc9e394d21a9cd, 0x2e90b7e1099a48c5, 0x465b01fda3d0ce29, 0x9af5eb366ceb5ad7,
				0x093c5499a63b8f42, 0x40ae4d605d77154a, 0x72ee014a97235beb, 0x1811442e4b5509fd,
			},
		},
		{
			seed: 1, snapshot: "7063673ae99ff867dbf682c91394ca1695ac4a12",
			outputs: []uint64{
				0xa9cbc97683f011f1, 0xd66ada12f9e860ad, 0x1815248ace19a281, 0x349ff86e10cb1ea0,
				0xfa5478b099ff3c68, 0x6df7fe2885d489d9, 0x698d728a89c7398a, 0x26c0ec7b0017029b,
				0xebc28b960fb08f77, 0xe6753eaf4e04cc70, 0x81cf4db21cf73232, 0xdc3735c3b970c702,
				0xf9a66b46a1ff54e7, 0x62b81fb804931b40, 0xdd2a09a209b84895, 0xde61fd8b7dc14581,
			},
		},
		{seed: -1, snapshot: "7063673ab4d055fcf2cbbd7be82a6cb8f1b79d73"},
		{seed: math.MaxInt64, snapshot: "7063673a5a682afe7965debd6b4dc2beb7f6fcbd"},
		{seed: math.MinInt64, snapshot: "7063673ac46fa638a63090121f5122a4a31cc80f"},
	}
	factory := PCG64()
	for _, test := range tests {
		rng, err := factory.New(EncodeInt64Seed(test.seed))
		if err != nil {
			t.Fatalf("New(%d): %v", test.seed, err)
		}
		snapshot, err := rng.Snapshot()
		if err != nil {
			t.Fatalf("Snapshot(%d): %v", test.seed, err)
		}
		if got := hex.EncodeToString(snapshot); got != test.snapshot {
			t.Fatalf("Snapshot(%d)=%s, want %s", test.seed, got, test.snapshot)
		}
		for i, want := range test.outputs {
			if got := rng.Uint64(); got != want {
				t.Fatalf("seed=%d output[%d]=%016x, want %016x", test.seed, i, got, want)
			}
		}
	}
}

func TestPCG64DeriveSeedMatchesLegacySequence(t *testing.T) {
	const mask63 = uint64(1<<63) - 1
	const multiplier = uint64(6364136223846793005)
	const increment = uint64(1442695040888963407)
	mix := func(x uint64) uint64 {
		x &= mask63
		x ^= x >> 30
		x = (x * 0xBF58476D1CE4E5B9) & mask63
		x ^= x >> 27
		x = (x * 0x94D049BB133111EB) & mask63
		x ^= x >> 31
		return x & mask63
	}

	factory := PCG64()
	for _, parent := range []int64{0, 1, -1, math.MinInt64, math.MaxInt64} {
		state := uint64(parent) & mask63
		encoded := EncodeInt64Seed(parent)
		for index := uint64(0); index < 10_000; index++ {
			state = (state*multiplier + increment) & mask63
			child, err := factory.DeriveSeed(encoded, StreamID{Domain: "ignored", Index: index})
			if err != nil {
				t.Fatalf("parent=%d index=%d: %v", parent, index, err)
			}
			got, err := DecodeInt64Seed(child)
			if err != nil {
				t.Fatal(err)
			}
			if want := int64(mix(state)); got != want {
				t.Fatalf("parent=%d index=%d child=%d, want %d", parent, index, got, want)
			}
		}
	}
}

func TestPCG64LegacyDerivationSemantics(t *testing.T) {
	factory := PCG64()
	zero, err := factory.DeriveSeed(EncodeInt64Seed(0), StreamID{Domain: "a", Index: 77})
	if err != nil {
		t.Fatal(err)
	}
	min, err := factory.DeriveSeed(EncodeInt64Seed(math.MinInt64), StreamID{Domain: "b", Index: 77})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(zero, min) {
		t.Fatal("PCG64 legacy mask63 collision changed")
	}
	a, _ := factory.DeriveSeed(EncodeInt64Seed(123), StreamID{Domain: "a", Index: 4})
	b, _ := factory.DeriveSeed(EncodeInt64Seed(123), StreamID{Domain: "b", Index: 4})
	if !bytes.Equal(a, b) {
		t.Fatal("PCG64 compatibility Factory unexpectedly separates domains")
	}
	if _, err := factory.DeriveSeed(EncodeInt64Seed(1), StreamID{Index: 0}); err == nil {
		t.Fatal("PCG64Factory accepted an empty stream domain")
	}
}

func TestPCG64ReseedIsDocumentedNoOp(t *testing.T) {
	rng, err := PCG64().New(EncodeInt64Seed(99))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := rng.Snapshot()
	control, _ := PCG64().New(EncodeInt64Seed(0))
	if err := control.Restore(before); err != nil {
		t.Fatal(err)
	}
	if err := rng.Reseed(); err != nil {
		t.Fatal(err)
	}
	after, _ := rng.Snapshot()
	if !bytes.Equal(before, after) {
		t.Fatal("PCG64 compatibility Reseed changed state")
	}
	if got, want := rng.Uint64(), control.Uint64(); got != want {
		t.Fatalf("PCG64 Reseed changed next output: got=%x want=%x", got, want)
	}
}
