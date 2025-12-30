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
	"testing"

	"github.com/zintix-labs/problab/spec"
)

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
