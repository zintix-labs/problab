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
	"testing"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

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
	gmr := buf.NewGameModeResult(0, &gms, 4, 4)
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
	gmr := buf.NewGameModeResult(0, &gms, 4, 4)
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
	gmr := buf.NewGameModeResult(0, &gms, 4, 4)
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
	gmr := buf.NewGameModeResult(0, &gms, 4, 4)
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
