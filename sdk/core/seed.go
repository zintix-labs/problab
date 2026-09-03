// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package core

import (
	"encoding/binary"
	"fmt"
)

const int64SeedSize = 8

// EncodeInt64Seed returns the canonical byte representation used by Problab's
// compatibility int64 entrypoints. The representation is the big-endian
// two's-complement bit pattern of seed.
func EncodeInt64Seed(seed int64) []byte {
	encoded := make([]byte, int64SeedSize)
	binary.BigEndian.PutUint64(encoded, uint64(seed))
	return encoded
}

// DecodeInt64Seed decodes the canonical representation produced by
// EncodeInt64Seed. It rejects every other length so legacy PCG seeds cannot be
// interpreted ambiguously.
func DecodeInt64Seed(seed []byte) (int64, error) {
	if len(seed) != int64SeedSize {
		return 0, fmt.Errorf("core: int64 seed must be exactly %d bytes, got %d", int64SeedSize, len(seed))
	}
	return int64(binary.BigEndian.Uint64(seed)), nil
}
