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

package gen

import (
	"bytes"
	"fmt"
	"slices"
	"testing"

	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

func dampGenerator(t *testing.T, mode spec.GenReelType, damp int, trim []int) *ScreenGenerator {
	t.Helper()
	rng, err := core.Default().New(core.EncodeInt64Seed(77))
	if err != nil {
		t.Fatal(err)
	}
	gs := &spec.GenScreenSetting{GenReelType: mode}
	for set := range 2 {
		rs := spec.ReelSet{Weight: set + 1}
		for col := range 5 {
			base := int16(set*100 + col*10)
			rs.Reels = append(rs.Reels, spec.Reel{
				ReelSymbols: []int16{base + 1, base + 2, base + 3},
				ReelWeights: []int{1, 3, 2},
			})
		}
		gs.ReelSetGroup = append(gs.ReelSetGroup, rs)
	}
	g := &ScreenGenerator{core: core.New(rng), ScreenSetting: &spec.ScreenSetting{
		Columns: 5, Rows: 5, Damp: damp, Trim: slices.Clone(trim),
	}, GenScreenSetting: gs}
	if err := g.init(); err != nil {
		t.Fatal(err)
	}
	return g
}

func equalDampState(t *testing.T, a, b *ScreenGenerator) {
	t.Helper()
	x, err := a.core.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	y, err := b.core.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(x, y) {
		t.Fatal("RNG state differs")
	}
}

func TestGenScreenDampParityAndBuffers(t *testing.T) {
	for _, mode := range []spec.GenReelType{spec.GenReelByReelIdx, spec.GenReelBySymbolWeight} {
		for _, damp := range []int{0, 1, 7} {
			for _, trim := range [][]int{nil, {2, 1, 0, 1, 2}, {4, 4, 4, 4, 4}} {
				t.Run(fmt.Sprintf("mode%d/damp%d/trim%v", mode, damp, trim), func(t *testing.T) {
					a := dampGenerator(t, mode, damp, trim)
					b := dampGenerator(t, mode, damp, trim)
					sp, fp := &b.Screen[0], &b.ScreenWithDamp[0]
					for spin := range 20 {
						var normal, full, screen []int16
						if spin%2 == 0 {
							normal = a.GenScreen()
							full, screen = b.GenScreenWithDamp()
						} else {
							normal = a.GenScreenByReelSetIdx(1)
							full, screen = b.GenScreenWithDampByReelSetIdx(1)
						}
						if !slices.Equal(normal, screen) {
							t.Fatal("normal screens differ")
						}
						equalDampState(t, a, b)
						if &screen[0] != sp || &full[0] != fp || sp == fp {
							t.Fatal("buffers not independent and reused")
						}
						for col, top := range b.ScreenSetting.Trim {
							for row := range 5 + 2*damp {
								v := full[row*5+col]
								if (row < top && v != -1) || (row >= top && v <= 0) {
									t.Fatalf("invalid full cell row=%d col=%d value=%d", row, col, v)
								}
							}
							for row := range 5 {
								if row < top {
									if screen[row*5+col] != -1 {
										t.Fatal("trim not restored")
									}
								} else if screen[row*5+col] != full[(row+damp)*5+col] {
									t.Fatal("normal/full overlap differs")
								}
							}
						}
						// 模擬遊戲修改所有格子，下一次必須完整覆寫。
						for i := range screen {
							screen[i] = -99
						}
						for i := range full {
							full[i] = -88
						}
					}
					for _, v := range a.ScreenWithDamp {
						if v != 0 {
							t.Fatal("normal generation updated display buffer")
						}
					}
				})
			}
		}
	}
}

func TestGenScreenDampSamplingOrder(t *testing.T) {
	for _, mode := range []spec.GenReelType{spec.GenReelByReelIdx, spec.GenReelBySymbolWeight} {
		for _, damp := range []int{0, 1, 7} {
			t.Run(fmt.Sprintf("mode%d/damp%d", mode, damp), func(t *testing.T) {
				g := dampGenerator(t, mode, damp, []int{2, 1, 0, 1, 2})
				ref := dampGenerator(t, mode, damp, []int{2, 1, 0, 1, 2})
				for range 10 {
					set := ref.GenScreenSetting.ReelSetLUT.Pick(ref.core)
					reels := ref.ReelSetGroup[set].Reels
					want := make([]int16, 25)
					fullWant := make([]int16, (5+2*damp)*5)
					for i := range fullWant {
						fullWant[i] = -1
					}
					for col, top := range ref.ScreenSetting.Trim {
						reel := &reels[col]
						if mode == spec.GenReelByReelIdx {
							idx := reel.ReelLUT.Pick(ref.core)
							for row := top - damp; row < 5+damp; row++ {
								pos := ((idx+row)%reel.ReelLength + reel.ReelLength) % reel.ReelLength
								fullWant[(row+damp)*5+col] = reel.ReelSymbols[pos]
							}
							for row := top; row < 5; row++ {
								want[row*5+col] = fullWant[(row+damp)*5+col]
							}
						} else {
							for row := 0; row < 5; row++ {
								id := reel.ReelLUT.Pick(ref.core)
								if row >= top {
									want[row*5+col] = reel.ReelSymbols[id]
									fullWant[(row+damp)*5+col] = reel.ReelSymbols[id]
								}
							}
						}
						for row := 0; row < top; row++ {
							want[row*5+col] = -1
						}
					}
					if mode == spec.GenReelBySymbolWeight {
						for col, top := range ref.ScreenSetting.Trim {
							reel := &reels[col]
							for _, start := range []int{top, 5 + damp} {
								for i := 0; i < damp; i++ {
									fullWant[(start+i)*5+col] = reel.ReelSymbols[reel.ReelLUT.Pick(ref.core)]
								}
							}
						}
					}
					full, screen := g.GenScreenWithDamp()
					if !slices.Equal(screen, want) || !slices.Equal(full, fullWant) {
						t.Fatal("reel position or sampling order differs")
					}
					equalDampState(t, g, ref)
				}
			})
		}
	}
}

func TestGenScreenNoAllocation(t *testing.T) {
	for _, mode := range []spec.GenReelType{spec.GenReelByReelIdx, spec.GenReelBySymbolWeight} {
		g := dampGenerator(t, mode, 1, []int{2, 1, 0, 1, 2})
		if n := testing.AllocsPerRun(100, func() { g.GenScreen(); g.GenScreenWithDamp() }); n != 0 {
			t.Fatalf("mode=%d allocations=%v", mode, n)
		}
	}
}
