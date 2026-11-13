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

package calc

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

func TestCalculatorsExcludeNegativeCells(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		screen []int16
		want   int
	}{
		{"count", []int16{0, 0, 0, -1, -32768}, 7},
		{"line_ltr", []int16{0, 0, 0, -1, 0}, 7},
		{"line_ltr", []int16{-2, 0, 0, 0, 0}, 0},
		{"line_rtl", []int16{0, -2, 0, 0, 0}, 7},
		{"way_ltr", []int16{0, 0, 0, -1, 0}, 7},
		{"way_rtl", []int16{0, -2, 0, 0, 0}, 7},
		{"cluster", []int16{0, 0, 0, -1, 0}, 7},
		{"cluster", []int16{0, 0, -2, 0, 0}, 0},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			gms := buildGameModeSetting(5, 1, tc.mode, [][]int16{{0, 0, 0, 0, 0}}, []string{"H1"}, [][]int{{0, 0, 7, 11, 19}})
			sc := NewScreenCalculator(&gms)
			gmr := buf.NewGameModeResult(0, &gms, 4, 4, false)
			before := slices.Clone(tc.screen)
			sc.CalcScreen(1, tc.screen, gmr)
			if gmr.GetTmpWin() != tc.want {
				t.Fatalf("win=%d want=%d", gmr.GetTmpWin(), tc.want)
			}
			if !slices.Equal(before, tc.screen) {
				t.Fatal("calculator changed screen")
			}
			for _, i := range gmr.HitMapTmp() {
				if tc.screen[i] < 0 {
					t.Fatal("negative cell in hitmap")
				}
			}
		})
	}
}

func buildGameModeSetting(cols, rows int, betType string, lineTable [][]int16, symbolUsed []string, payTable [][]int) spec.GameModeSetting {
	return spec.GameModeSetting{
		ScreenSetting: spec.ScreenSetting{
			Columns: cols,
			Rows:    rows,
		},
		HitSetting: spec.HitSetting{
			BetTypeStr: betType,
			LineTable:  lineTable,
		},
		SymbolSetting: spec.SymbolSetting{
			SymbolUsedStr: symbolUsed,
			PayTable:      payTable,
		},
	}
}

func TestCalcByLine(t *testing.T) {
	gms := buildGameModeSetting(5, 3, "line_ltr", [][]int16{{1, 1, 1, 1, 1}}, []string{"H1", "W1"}, [][]int{{0, 0, 0, 0, 9}, {0, 0, 0, 0, 0}})
	sc := NewScreenCalculator(&gms)
	gmr := buf.NewGameModeResult(0, &gms, 4, 4, false)
	maxIdx := int16(-1)
	for _, v := range sc.LineTableFlat {
		if v > maxIdx {
			maxIdx = v
		}
	}
	if maxIdx >= int16(sc.ScreenSize) {
		t.Fatalf("line table out of range: max=%d screen=%d table=%v", maxIdx, sc.ScreenSize, sc.LineTableFlat)
	}
	screen := make([]int16, sc.ScreenSize)

	sc.CalcScreen(1, screen, gmr)

	if gmr.GetTmpWin() != 9 {
		t.Fatalf("expected tmp win 9, got %d", gmr.GetTmpWin())
	}
	details := gmr.GetDetails()
	if len(details) != 1 || details[0].Win != 9 {
		t.Fatalf("unexpected details: %+v", details)
	}
	if hit := gmr.HitMapTmp(); len(hit) != 5 {
		t.Fatalf("expected hitmap length 5, got %v", hit)
	}
}

func TestCalcByWay(t *testing.T) {
	gms := buildGameModeSetting(3, 1, "way_ltr", nil, []string{"H1"}, [][]int{{0, 3, 6}})
	sc := NewScreenCalculator(&gms)
	gmr := buf.NewGameModeResult(0, &gms, 4, 4, false)
	screen := []int16{0, 0, 0}

	sc.CalcScreen(1, screen, gmr)

	if gmr.GetTmpWin() != 6 {
		t.Fatalf("expected tmp win 6, got %d", gmr.GetTmpWin())
	}
	details := gmr.GetDetails()
	if len(details) != 1 || details[0].Win != 6 {
		t.Fatalf("unexpected details: %+v", details)
	}
}

func TestCalcByCount(t *testing.T) {
	gms := buildGameModeSetting(3, 1, "count", nil, []string{"H1", "W1"}, [][]int{{0, 0, 4}, {0, 0, 0}})
	sc := NewScreenCalculator(&gms)
	gmr := buf.NewGameModeResult(0, &gms, 4, 4, false)
	screen := []int16{0, 0, 1}

	sc.CalcScreen(1, screen, gmr)

	if gmr.GetTmpWin() != 4 {
		t.Fatalf("expected tmp win 4, got %d", gmr.GetTmpWin())
	}
	details := gmr.GetDetails()
	if len(details) != 1 || details[0].Win != 4 {
		t.Fatalf("unexpected details: %+v", details)
	}
}

func TestCalcByCluster(t *testing.T) {
	gms := buildGameModeSetting(2, 2, "cluster", nil, []string{"H1"}, [][]int{{0, 0, 5, 10}})
	sc := NewScreenCalculator(&gms)
	gmr := buf.NewGameModeResult(0, &gms, 4, 4, false)
	screen := []int16{0, 0, 0, 0}

	sc.CalcScreen(1, screen, gmr)

	if gmr.GetTmpWin() != 10 {
		t.Fatalf("expected tmp win 10, got %d", gmr.GetTmpWin())
	}
	details := gmr.GetDetails()
	if len(details) != 1 || details[0].Win != 10 {
		t.Fatalf("unexpected details: %+v", details)
	}
	if hit := gmr.HitMapTmp(); len(hit) != 4 {
		t.Fatalf("expected hitmap length 4, got %v", hit)
	}
}

// 用上下左右填 neighbor 時，cluster_by_neighbor 必須和 cluster 的結果與 hits 順序完全相同。
func TestCalcByClusterByNeighborMatchesCluster(t *testing.T) {
	cols, rows := 6, 5
	symbols := []string{"H1", "H2", "H3", "W1"}
	pay := [][]int{}
	for range symbols {
		p := make([]int, cols*rows)
		for k := 3; k < len(p); k++ {
			p[k] = k + 1
		}
		pay = append(pay, p)
	}
	pay[3] = make([]int, cols*rows)
	neighbor := map[int][]int16{}
	for i := range cols * rows {
		r, c := i/cols, i%cols
		if c > 0 {
			neighbor[i] = append(neighbor[i], int16(i-1))
		}
		if c+1 < cols {
			neighbor[i] = append(neighbor[i], int16(i+1))
		}
		if r > 0 {
			neighbor[i] = append(neighbor[i], int16(i-cols))
		}
		if r+1 < rows {
			neighbor[i] = append(neighbor[i], int16(i+cols))
		}
	}
	grid := buildGameModeSetting(cols, rows, "cluster", nil, symbols, pay)
	custom := buildGameModeSetting(cols, rows, "cluster_by_neighbor", nil, symbols, pay)
	custom.HitSetting.Neighbor = neighbor
	scGrid, scCustom := NewScreenCalculator(&grid), NewScreenCalculator(&custom)
	gGrid := buf.NewGameModeResult(0, &grid, 4, 4, false)
	gCustom := buf.NewGameModeResult(0, &custom, 4, 4, false)

	rng := rand.New(rand.NewPCG(1, 2))
	screen := make([]int16, cols*rows)
	for range 2000 {
		for i := range screen {
			screen[i] = int16(rng.IntN(len(symbols)+1)) - 1 // 含 -1 的裁切格
		}
		gGrid.Reset()
		gCustom.Reset()
		scGrid.CalcScreen(2, screen, gGrid)
		scCustom.CalcScreen(2, screen, gCustom)
		if !slices.Equal(gGrid.GetDetails(), gCustom.GetDetails()) || !slices.Equal(gGrid.HitMapTmp(), gCustom.HitMapTmp()) {
			t.Fatalf("screen=%v\ncluster=%+v %v\ncluster_by_neighbor=%+v %v", screen,
				gGrid.GetDetails(), gGrid.HitMapTmp(), gCustom.GetDetails(), gCustom.HitMapTmp())
		}
	}
}

// 每格鄰居數不限於 4：3x3 八方向，中心格有 8 個鄰居，0、4、8 只靠斜角相連。
func TestCalcByClusterByNeighborEightDirections(t *testing.T) {
	neighbor := map[int][]int16{}
	for i := range 9 {
		r, c := i/3, i%3
		for dr := -1; dr <= 1; dr++ {
			for dc := -1; dc <= 1; dc++ {
				rr, cc := r+dr, c+dc
				if (dr != 0 || dc != 0) && rr >= 0 && rr < 3 && cc >= 0 && cc < 3 {
					neighbor[i] = append(neighbor[i], int16(rr*3+cc))
				}
			}
		}
	}
	gms := buildGameModeSetting(3, 3, "cluster_by_neighbor", nil, []string{"H1", "H2"}, [][]int{{0, 0, 7, 0, 0, 0, 0, 0, 0}, {0, 0, 0, 0, 0, 0, 0, 0, 0}})
	gms.HitSetting.Neighbor = neighbor
	sc := NewScreenCalculator(&gms)
	gmr := buf.NewGameModeResult(0, &gms, 4, 4, false)
	sc.CalcScreen(1, []int16{0, 1, 1, 1, 0, 1, 1, 1, 0}, gmr)
	if gmr.GetTmpWin() != 7 || !slices.Equal(gmr.HitMapTmp(), []int16{0, 4, 8}) {
		t.Fatalf("win=%d hits=%v, want win=7 hits=[0 4 8]", gmr.GetTmpWin(), gmr.HitMapTmp())
	}
}

func TestCalcByClusterByNeighborUsesOnlyListedCells(t *testing.T) {
	// 0 和 2 在格子上不相鄰但被指定相鄰；0 和 1 相鄰但未列出
	gms := buildGameModeSetting(3, 1, "cluster_by_neighbor", nil, []string{"H1", "H2"}, [][]int{{0, 5, 9}, {0, 0, 0}})
	gms.HitSetting.Neighbor = map[int][]int16{0: {2}, 2: {0}}
	sc := NewScreenCalculator(&gms)
	gmr := buf.NewGameModeResult(0, &gms, 4, 4, false)
	for _, tc := range []struct {
		screen []int16
		want   int
		hits   []int16
	}{
		{[]int16{0, 1, 0}, 5, []int16{0, 2}},
		{[]int16{0, 0, 1}, 0, nil},
		{[]int16{0, 0, 0}, 5, []int16{0, 2}}, // 1 只有自己一顆，不足 2 顆
	} {
		gmr.Reset()
		sc.CalcScreen(1, tc.screen, gmr)
		if gmr.GetTmpWin() != tc.want || !slices.Equal(gmr.HitMapTmp(), tc.hits) {
			t.Fatalf("screen=%v win=%d hits=%v, want win=%d hits=%v", tc.screen, gmr.GetTmpWin(), gmr.HitMapTmp(), tc.want, tc.hits)
		}
	}
}
