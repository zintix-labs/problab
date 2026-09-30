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

package ops

import (
	"slices"
	"testing"

	"github.com/zintix-labs/problab/spec"
)

func TestNegativeCellsGravityAndFill(t *testing.T) {
	for _, tc := range []struct {
		name        string
		input, want []int16
		fill        int
	}{
		{"top trim", []int16{-1, 1, 0, 2}, []int16{-1, 0, 1, 2}, 1},
		{"cross holes", []int16{1, -2, 0, -1, 2, 0}, []int16{0, -2, 0, -1, 1, 2}, 2},
		{"bottom hole", []int16{1, 0, -3}, []int16{0, 1, -3}, 0},
		{"all blocked", []int16{-1, -32768}, []int16{-1, -32768}, -1},
		{"full", []int16{-1, 1, -2, 2}, []int16{-1, 1, -2, 2}, -1},
		{"empty", []int16{-1, 0, -2, 0}, []int16{-1, 0, -2, 0}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := slices.Clone(tc.input)
			fill := []int{999}
			Gravity(s, 1, len(s), fill)
			if !slices.Equal(s, tc.want) || fill[0] != tc.fill {
				t.Fatalf("screen=%v fill=%v", s, fill)
			}
			n := slices.Clone(tc.input)
			Gravity(n, 1, len(n), nil)
			if !slices.Equal(n, s) {
				t.Fatal("nil fill buffer changes result")
			}
			reels := &spec.ReelSet{Reels: []spec.Reel{{ReelSymbols: []int16{7, 8, 9}}}}
			p, q := []int{0}, []int{0}
			FillScreen(s, reels, fill, p, 1)
			FillScreenByHole(n, reels, q, 1, len(n))
			if !slices.Equal(s, n) || !slices.Equal(p, q) {
				t.Fatal("fill methods differ")
			}
			count := 0
			for i, v := range tc.want {
				if v < 0 && s[i] != v {
					t.Fatal("negative cell changed")
				}
				if v > 0 && s[i] != v {
					t.Fatal("existing symbol changed")
				}
				if v == 0 {
					count++
					if s[i] <= 0 {
						t.Fatal("hole not filled")
					}
				}
			}
			if p[0] != (3-count%3)%3 {
				t.Fatalf("wrong reel consumption: %v", p)
			}
		})
	}
}

func TestNegativeCellsMultiColumnPipeline(t *testing.T) {
	s := []int16{-1, 1, 2, -2, 0, 3, 4, 0}
	Clear(s, []int16{0, 3, 4, 5, -1, 99})
	fill := make([]int, 2)
	Gravity(s, 2, 4, fill)
	if !slices.Equal(s, []int16{-1, 0, 0, -2, 2, 0, 4, 1}) || !slices.Equal(fill, []int{2, 5}) {
		t.Fatalf("screen=%v fill=%v", s, fill)
	}
	reels := &spec.ReelSet{Reels: []spec.Reel{{ReelSymbols: []int16{7, 8, 9}}, {ReelSymbols: []int16{10, 11, 12}}}}
	p := []int{0, 0}
	FillScreen(s, reels, fill, p, 2)
	if !slices.Equal(s, []int16{-1, 11, 9, -2, 2, 12, 4, 1}) || !slices.Equal(p, []int{2, 1}) {
		t.Fatalf("screen=%v positions=%v", s, p)
	}
}

func TestClearNegativeCellsAndIndices(t *testing.T) {
	s := []int16{-1, -32768, 5, 0}
	Clear(s, []int16{-32768, -1, 0, 1, 2, 2, 3, 4, 32767})
	if !slices.Equal(s, []int16{-1, -32768, 0, 0}) {
		t.Fatal(s)
	}
	Clear(nil, []int16{-1, 0, 1})
	large := make([]int16, 32768)
	large[32767] = 9
	Clear(large, []int16{32767})
	if large[32767] != 0 {
		t.Fatal("screen length narrowed to int16")
	}
}

func TestClear(t *testing.T) {
	screen := []int16{1, 2, 3}
	Clear(screen, []int16{0, 2, 10})
	if screen[0] != 0 || screen[1] != 2 || screen[2] != 0 {
		t.Fatalf("unexpected clear result: %v", screen)
	}
}

func TestGravityAndFillScreen(t *testing.T) {
	cols, rows := 3, 3
	screen := []int16{
		1, 0, 2,
		0, 3, 0,
		4, 0, 5,
	}
	fillIdx := make([]int, cols)
	Gravity(screen, cols, rows, fillIdx)

	for c := 0; c < cols; c++ {
		if fillIdx[c] < 0 || fillIdx[c] >= cols*rows {
			t.Fatalf("unexpected fill idx: %v", fillIdx)
		}
	}

	reels := &spec.ReelSet{Reels: []spec.Reel{
		{ReelSymbols: []int16{7, 8, 9}},
		{ReelSymbols: []int16{7, 8, 9}},
		{ReelSymbols: []int16{7, 8, 9}},
	}}
	reelPos := []int{0, 0, 0}
	FillScreen(screen, reels, fillIdx, reelPos, cols)

	for _, v := range screen {
		if v == 0 {
			t.Fatalf("expected filled screen, got %v", screen)
		}
	}
}

func TestFillScreenByHole(t *testing.T) {
	cols, rows := 2, 2
	screen := []int16{1, 0, 0, 2}
	reels := &spec.ReelSet{Reels: []spec.Reel{
		{ReelSymbols: []int16{5}},
		{ReelSymbols: []int16{6}},
	}}
	reelPos := []int{0, 0}
	FillScreenByHole(screen, reels, reelPos, cols, rows)

	for _, v := range screen {
		if v == 0 {
			t.Fatalf("expected no holes, got %v", screen)
		}
	}
}
