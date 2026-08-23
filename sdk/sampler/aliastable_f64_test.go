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

package sampler

import (
	"math"
	"testing"

	"github.com/zintix-labs/problab/sdk/core"
)

type scriptedPRNG struct {
	words    []uint64
	word     int
	intValue int
}

func (r *scriptedPRNG) Uint64() uint64 {
	if r.word >= len(r.words) {
		panic("scriptedPRNG: no Uint64 value left")
	}
	v := r.words[r.word]
	r.word++
	return v
}

func (r *scriptedPRNG) Float64() float64 {
	panic("scriptedPRNG: unexpected Float64 call")
}

func (r *scriptedPRNG) UintN(max uint) uint {
	if max == 0 {
		return 0
	}
	return uint(r.intValue) % max
}

func (r *scriptedPRNG) IntN(max int) int {
	if max <= 0 {
		return -1
	}
	return r.intValue % max
}

func (r *scriptedPRNG) Snapshot() ([]byte, error) { return nil, nil }
func (r *scriptedPRNG) Restore([]byte) error      { return nil }

func TestBernoulliF64Boundaries(t *testing.T) {
	const half = uint64(1) << 63

	t.Run("below cutoff succeeds", func(t *testing.T) {
		c := core.New(&scriptedPRNG{words: []uint64{half - 1}})
		if !bernoulliF64(c, 0.5) {
			t.Fatal("expected success immediately below the 0.5 cutoff")
		}
	})

	t.Run("cutoff itself fails", func(t *testing.T) {
		c := core.New(&scriptedPRNG{words: []uint64{half}})
		if bernoulliF64(c, 0.5) {
			t.Fatal("expected failure at the strict 0.5 cutoff")
		}
	})
}

func TestBernoulliF64BelowFloat64Resolution(t *testing.T) {
	// 2^-200 requires three all-zero 64-bit prefixes before the fourth word
	// can decide the result. It is far below the 2^-53 resolution of Float64.
	p := math.Ldexp(1, -200)

	t.Run("success path", func(t *testing.T) {
		c := core.New(&scriptedPRNG{words: []uint64{0, 0, 0, 0}})
		if !bernoulliF64(c, p) {
			t.Fatal("expected the zero-prefix path to sample the tiny probability")
		}
	})

	t.Run("failure path", func(t *testing.T) {
		c := core.New(&scriptedPRNG{words: []uint64{1}})
		if bernoulliF64(c, p) {
			t.Fatal("expected a non-zero first word to reject the tiny probability")
		}
	})
}

func TestAliasTableF64PickTinyWeight(t *testing.T) {
	at := BuildAliasTableF64([]float64{1e-70, 1})
	if at.Prob[0] == 0 {
		t.Fatal("tiny positive weight was lost while building the alias table")
	}
	if at.Aliases[0] != 1 {
		t.Fatalf("unexpected alias for tiny weight: got %d want 1", at.Aliases[0])
	}

	t.Run("select tiny item", func(t *testing.T) {
		c := core.New(&scriptedPRNG{
			intValue: 0,
			words:    []uint64{0, 0, 0, 0},
		})
		if got := at.Pick(c); got != 0 {
			t.Fatalf("Pick() = %d, want tiny item 0", got)
		}
	})

	t.Run("select alias", func(t *testing.T) {
		c := core.New(&scriptedPRNG{
			intValue: 0,
			words:    []uint64{1},
		})
		if got := at.Pick(c); got != 1 {
			t.Fatalf("Pick() = %d, want alias item 1", got)
		}
	})
}
