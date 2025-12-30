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

package gen

import (
	"testing"

	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

func testScreenSetting(cols, rows int) *spec.ScreenSetting {
	return &spec.ScreenSetting{
		Columns: cols,
		Rows:    rows,
	}
}

func testGenScreenSetting(cols int) *spec.GenScreenSetting {
	reels := make([]spec.Reel, cols)
	for i := 0; i < cols; i++ {
		reels[i] = spec.Reel{ReelSymbols: []int16{1, 2, 3}}
	}
	return &spec.GenScreenSetting{
		GenReelTypeStr: "GenReelByReelIdx",
		ReelSetGroup: []spec.ReelSet{
			{Weight: 1, Reels: reels},
		},
	}
}

func TestGenScreenByReelSetIdx(t *testing.T) {
	ss := testScreenSetting(2, 2)
	gs := &spec.GenScreenSetting{
		GenReelTypeStr: "GenReelByReelIdx",
		ReelSetGroup: []spec.ReelSet{
			{Weight: 1, Reels: []spec.Reel{
				{ReelSymbols: []int16{1}},
				{ReelSymbols: []int16{1}},
			}},
			{Weight: 1, Reels: []spec.Reel{
				{ReelSymbols: []int16{9}},
				{ReelSymbols: []int16{9}},
			}},
		},
	}
	c := core.New(core.Default().New(1))
	g := NewScreenGenerator(c, ss, gs)
	screen := g.GenScreenByReelSetIdx(1)
	for _, v := range screen {
		if v != 9 {
			t.Fatalf("expected reelset idx 1 symbols, got %v", screen)
		}
	}
}
