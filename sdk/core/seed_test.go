// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package core

import (
	"math"
	"testing"
)

func TestInt64SeedCodecRoundTrip(t *testing.T) {
	for _, value := range []int64{0, 1, -1, math.MaxInt64, math.MinInt64} {
		encoded := EncodeInt64Seed(value)
		if len(encoded) != 8 {
			t.Fatalf("EncodeInt64Seed(%d) length=%d", value, len(encoded))
		}
		decoded, err := DecodeInt64Seed(encoded)
		if err != nil {
			t.Fatalf("DecodeInt64Seed(%d): %v", value, err)
		}
		if decoded != value {
			t.Fatalf("round trip=%d, want %d", decoded, value)
		}
		encoded[0] ^= 0xff
		fresh := EncodeInt64Seed(value)
		if decodedAgain, err := DecodeInt64Seed(fresh); err != nil || decodedAgain != value {
			t.Fatalf("encoder returned shared mutable storage: decoded=%d err=%v", decodedAgain, err)
		}
	}
}

func TestDecodeInt64SeedRejectsNonCanonicalLengths(t *testing.T) {
	for _, size := range []int{0, 1, 7, 9, 32} {
		if _, err := DecodeInt64Seed(make([]byte, size)); err == nil {
			t.Fatalf("DecodeInt64Seed accepted length %d", size)
		}
	}
}
