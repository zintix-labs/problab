// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package core

import (
	"math"
	"slices"
	"testing"
)

func TestCoreDeterminism(t *testing.T) {
	c1 := New(Default().New(7))
	c2 := New(Default().New(7))
	for i := 0; i < 5; i++ {
		if c1.Uint64() != c2.Uint64() {
			t.Fatalf("Uint64 mismatch at %d", i)
		}
	}
	if c1.IntN(10) != c2.IntN(10) {
		t.Fatalf("IntN mismatch")
	}
	if c1.UintN(10) != c2.UintN(10) {
		t.Fatalf("UintN mismatch")
	}
}

func TestCorePickAndShuffle(t *testing.T) {
	c := New(Default().New(9))
	if got := c.Pick(nil); got != -1 {
		t.Fatalf("expected -1 for empty pick, got %d", got)
	}

	src := []int{1, 2, 3, 4}
	c.ShuffleInts(src)
	if len(src) != 4 {
		t.Fatalf("unexpected length after shuffle")
	}
	want := []int{1, 2, 3, 4}
	got := slices.Clone(src)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Fatalf("shuffle changed elements: %v", src)
	}
}

func TestExpFloat64Deterministic(t *testing.T) {
	c1 := New(Default().New(11))
	c2 := New(Default().New(11))
	v1 := c1.ExpFloat64()
	v2 := c2.ExpFloat64()
	if v1 != v2 {
		t.Fatalf("expected deterministic ExpFloat64")
	}
	if v1 <= 0 || math.IsNaN(v1) || math.IsInf(v1, 0) {
		t.Fatalf("unexpected ExpFloat64 value: %v", v1)
	}
}

func TestCoreUintN_EdgeCases(t *testing.T) {
	c := New(Default().New(42))
	// UintN(0) should return 0
	if got := c.UintN(0); got != 0 {
		t.Fatalf("UintN(0) expected 0, got %d", got)
	}
	// UintN(1) should return 0
	if got := c.UintN(1); got != 0 {
		t.Fatalf("UintN(1) expected 0, got %d", got)
	}
}

func TestCoreIntN_EdgeCases(t *testing.T) {
	c := New(Default().New(42))
	// IntN(0) should return -1
	if got := c.IntN(0); got != -1 {
		t.Fatalf("IntN(0) expected -1, got %d", got)
	}
	// IntN(-1) should return -1
	if got := c.IntN(-1); got != -1 {
		t.Fatalf("IntN(-1) expected -1, got %d", got)
	}
	// IntN(1) should return 0
	if got := c.IntN(1); got != 0 {
		t.Fatalf("IntN(1) expected 0, got %d", got)
	}
}

func TestCorePick_EdgeCases(t *testing.T) {
	c := New(Default().New(42))
	// Pick from empty slice
	if got := c.Pick(nil); got != -1 {
		t.Fatalf("Pick(nil) expected -1, got %d", got)
	}
	if got := c.Pick([]int{}); got != -1 {
		t.Fatalf("Pick([]int{}) expected -1, got %d", got)
	}
	// Pick from single element
	single := []int{42}
	got := c.Pick(single)
	if got != 42 {
		t.Fatalf("Pick([42]) expected 42 (element value), got %d", got)
	}
	// Pick from multiple elements (should be in range)
	multi := []int{0, 1, 2, 3, 4}
	for i := 0; i < 100; i++ {
		idx := c.Pick(multi)
		if idx < 0 || idx >= len(multi) {
			t.Fatalf("Pick out of range: %d (len=%d)", idx, len(multi))
		}
	}
}

func TestCoreShuffleInts_EdgeCases(t *testing.T) {
	c := New(Default().New(42))
	// Shuffle empty slice
	empty := []int{}
	c.ShuffleInts(empty)
	if len(empty) != 0 {
		t.Fatalf("shuffle empty slice should remain empty")
	}
	// Shuffle nil slice
	var nilSlice []int
	c.ShuffleInts(nilSlice)
	// Shuffle single element
	single := []int{42}
	original := []int{42}
	c.ShuffleInts(single)
	if len(single) != 1 || single[0] != 42 {
		t.Fatalf("shuffle single element should not change: %v", single)
	}
	if !slices.Equal(single, original) {
		t.Fatalf("single element shuffle changed value")
	}
}

func TestCoreShuffleInts_PreservesElements(t *testing.T) {
	c := New(Default().New(42))
	src := []int{1, 2, 3, 4, 5}
	original := slices.Clone(src)
	c.ShuffleInts(src)
	// Check length
	if len(src) != len(original) {
		t.Fatalf("shuffle changed length: %d -> %d", len(original), len(src))
	}
	// Check elements are preserved (sorted should be equal)
	sortedSrc := slices.Clone(src)
	sortedOrig := slices.Clone(original)
	slices.Sort(sortedSrc)
	slices.Sort(sortedOrig)
	if !slices.Equal(sortedSrc, sortedOrig) {
		t.Fatalf("shuffle changed elements: original=%v, shuffled=%v", original, src)
	}
}

func TestCoreFloat64_Range(t *testing.T) {
	c := New(Default().New(42))
	for i := 0; i < 1000; i++ {
		v := c.Float64()
		if v < 0 || v >= 1.0 {
			t.Fatalf("Float64 out of range [0,1): %v", v)
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("Float64 invalid: %v", v)
		}
	}
}

func TestCoreDifferentSeeds_DifferentValues(t *testing.T) {
	c1 := New(Default().New(1))
	c2 := New(Default().New(2))
	// Different seeds should produce different sequences (with high probability)
	different := false
	for i := 0; i < 10; i++ {
		if c1.Uint64() != c2.Uint64() {
			different = true
			break
		}
	}
	if !different {
		t.Fatalf("different seeds produced identical sequences (unlikely but possible)")
	}
}
