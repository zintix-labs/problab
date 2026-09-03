// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package internal

const (
	pcgSeedMask63 = uint64(1<<63) - 1
	pcgSeedMult   = uint64(6364136223846793005)
	pcgSeedInc    = uint64(1442695040888963407)
)

// DerivePCG64Seed returns the legacy SeedMaker output at zero-based index.
// The affine LCG advance is O(log index), and arithmetic is reduced modulo
// 2^63 to preserve the exact historical mask63 behavior.
func DerivePCG64Seed(parent int64, index uint64) int64 {
	state := uint64(parent) & pcgSeedMask63
	steps := (index + 1) & pcgSeedMask63
	state = advancePCGSeedLCG(state, steps)
	return int64(mixPCGSeed63(state))
}

func advancePCGSeedLCG(state, steps uint64) uint64 {
	accMult := uint64(1)
	accPlus := uint64(0)
	curMult := pcgSeedMult
	curPlus := pcgSeedInc
	for steps > 0 {
		if steps&1 != 0 {
			accMult = (accMult * curMult) & pcgSeedMask63
			accPlus = (accPlus*curMult + curPlus) & pcgSeedMask63
		}
		curPlus = ((curMult + 1) * curPlus) & pcgSeedMask63
		curMult = (curMult * curMult) & pcgSeedMask63
		steps >>= 1
	}
	return (accMult*state + accPlus) & pcgSeedMask63
}

func mixPCGSeed63(x uint64) uint64 {
	x &= pcgSeedMask63
	x ^= x >> 30
	x = (x * 0xBF58476D1CE4E5B9) & pcgSeedMask63
	x ^= x >> 27
	x = (x * 0x94D049BB133111EB) & pcgSeedMask63
	x ^= x >> 31
	return x & pcgSeedMask63
}
