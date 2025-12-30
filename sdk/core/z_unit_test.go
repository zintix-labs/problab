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
