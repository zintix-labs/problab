// Copyright 2026 Zintix Labs
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

package ops

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/zintix-labs/problab/sdk/layer"
	"github.com/zintix-labs/problab/spec"
)

func TestOptionalLayersEmptyAndNoAllocation(t *testing.T) {
	s := []int16{1, 0, -1, 2}
	x := slices.Clone(s)
	f, g := make([]int, 2), make([]int, 2)
	Gravity(s, 2, 2, f)
	Gravity(x, 2, 2, g)
	if !slices.Equal(s, x) || !slices.Equal(f, g) {
		t.Fatal("empty layers gravity")
	}
	Clear(s, []int16{0})
	Clear(x, []int16{0})
	r := &spec.ReelSet{Reels: []spec.Reel{{ReelSymbols: []int16{7}}, {ReelSymbols: []int16{8}}}}
	p, q := make([]int, 2), make([]int, 2)
	FillScreenByHole(s, r, p, 2, 2)
	FillScreenByHole(x, r, q, 2, 2)
	if !slices.Equal(s, x) || !slices.Equal(p, q) {
		t.Fatal("empty layers fill")
	}
	f[0], g[0] = 0, 0
	FillScreen(s, r, f, p, 2)
	FillScreen(x, r, g, q, 2)
	if !slices.Equal(s, x) || !slices.Equal(p, q) {
		t.Fatal("empty layers stacked fill")
	}
	a := layer.New(4, 0)
	b := layer.New(4, false)
	layers := []layer.SyncOps{a, b}
	if n := testing.AllocsPerRun(100, func() {
		s[0], s[1], s[2], s[3] = 1, 0, -1, 2
		Clear(s, []int16{0})
		Gravity(s, 2, 2, f)
		FillScreen(s, r, f, p, 2)
		s[1] = 0
		FillScreenByHole(s, r, p, 2, 2)
	}); n != 0 {
		t.Fatalf("without layers allocations=%v", n)
	}
	if n := testing.AllocsPerRun(100, func() {
		s[0], s[1], s[2], s[3] = 1, 0, -1, 2
		Clear(s, []int16{0}, layers...)
		Gravity(s, 2, 2, f, layers...)
		FillScreen(s, r, f, p, 2, layers...)
		s[1] = 0
		FillScreenByHole(s, r, p, 2, 2, layers...)
	}); n != 0 {
		t.Fatalf("allocations=%v", n)
	}
}

func TestGravityLayersReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(17, 29))
	for trial := 0; trial < 200; trial++ {
		const cols, rows = 4, 6
		s := make([]int16, cols*rows)
		a := layer.New(len(s), -99)
		b := layer.New(len(s), true)
		for i := range s {
			s[i] = int16(rng.IntN(6) - 2)
			a.State[i] = i
			b.State[i] = i%2 == 0
		}
		want, wa, wb := slices.Clone(s), slices.Clone(a.State), slices.Clone(b.State)
		for c := 0; c < cols; c++ {
			var cells, sources []int
			for r := 0; r < rows; r++ {
				i := r*cols + c
				if s[i] >= 0 {
					cells = append(cells, i)
				}
				if s[i] > 0 {
					sources = append(sources, i)
				}
			}
			for j, i := range cells {
				k := j - (len(cells) - len(sources))
				if k < 0 {
					want[i] = 0
					wa[i] = -99
					wb[i] = true
				} else {
					src := sources[k]
					want[i] = s[src]
					wa[i] = a.State[src]
					wb[i] = b.State[src]
				}
			}
		}
		plain := slices.Clone(s)
		f, g := make([]int, cols), make([]int, cols)
		Gravity(plain, cols, rows, f)
		Gravity(s, cols, rows, g, a, b)
		if !slices.Equal(s, want) || !slices.Equal(a.State, wa) || !slices.Equal(b.State, wb) || !slices.Equal(s, plain) || !slices.Equal(f, g) {
			t.Fatal("gravity mismatch", trial)
		}
	}
}

func TestClearAndFillLayers(t *testing.T) {
	s := []int16{-1, 1, 0, -2, 2, 0}
	a := layer.New(6, -9)
	b := layer.New(6, true)
	for i := range s {
		a.State[i] = i
		b.State[i] = false
	}
	plain := slices.Clone(s)
	hits := []int16{-1, 0, 1, 1, 9}
	Clear(plain, hits)
	Clear(s, hits, a, b)
	if !slices.Equal(s, plain) || a.State[0] != 0 || a.State[1] != -9 || !b.State[1] {
		t.Fatal("clear")
	}
	Gravity(s, 2, 3, nil, a, b)
	for _, hole := range []bool{false, true} {
		x, y := slices.Clone(s), slices.Clone(s)
		la, lb := a.Snapshot(), b.Snapshot()
		beforeA, beforeB := slices.Clone(la.State), slices.Clone(lb.State)
		p, q := []int{0, 0}, []int{0, 0}
		reels := &spec.ReelSet{Reels: []spec.Reel{{ReelSymbols: []int16{7, 8, 9}}, {ReelSymbols: []int16{10, 11}}}}
		if hole {
			FillScreenByHole(x, reels, p, 2, 3)
			FillScreenByHole(y, reels, q, 2, 3, la, lb)
		} else {
			f := make([]int, 2)
			Gravity(x, 2, 3, f)
			FillScreen(x, reels, f, p, 2)
			FillScreen(y, reels, f, q, 2, la, lb)
		}
		if !slices.Equal(x, y) || !slices.Equal(p, q) {
			t.Fatal("fill parity")
		}
		for i, v := range s {
			if v == 0 {
				if la.State[i] != -9 || !lb.State[i] {
					t.Fatal("new symbol state")
				}
			} else if la.State[i] != beforeA[i] || lb.State[i] != beforeB[i] {
				t.Fatal("existing state changed")
			}
		}
	}
}

func TestLayersValidateBeforeWrites(t *testing.T) {
	for _, op := range []func([]int16, []int, []int, []layer.SyncOps){
		func(s []int16, f, p []int, l []layer.SyncOps) { Clear(s, []int16{0}, l...) },
		func(s []int16, f, p []int, l []layer.SyncOps) { Gravity(s, 1, 2, f, l...) },
		func(s []int16, f, p []int, l []layer.SyncOps) { FillScreen(s, nil, f, p, 1, l...) },
		func(s []int16, f, p []int, l []layer.SyncOps) { FillScreenByHole(s, nil, p, 1, 2, l...) },
	} {
		var typedNil *layer.Layer[int]
		for _, bad := range []layer.SyncOps{nil, typedNil, layer.New(1, 0), layer.New(3, 0)} {
			s := []int16{1, 0}
			f, p := []int{17}, []int{19}
			good := layer.New(2, 7)
			func() {
				defer func() {
					if recover() == nil {
						t.Error("expected panic")
					}
				}()
				op(s, f, p, []layer.SyncOps{good, bad})
			}()
			if !slices.Equal(s, []int16{1, 0}) || f[0] != 17 || p[0] != 19 || !slices.Equal(good.State, []int{7, 7}) {
				t.Fatal("partial write")
			}
		}
	}
}
