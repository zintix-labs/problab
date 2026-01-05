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

package buf

import (
	"testing"

	"github.com/zintix-labs/problab/spec"
)

func testGameSetting() *spec.GameSetting {
	return &spec.GameSetting{
		GameName:    "demo",
		GameID:      7,
		LogicKey:    "demo_logic",
		BetUnits:    []int{1, 2},
		MaxWinLimit: 100,
	}
}

func testGameModeSetting() *spec.GameModeSetting {
	return &spec.GameModeSetting{
		ScreenSetting: spec.ScreenSetting{
			Columns: 3,
			Rows:    1,
		},
		HitSetting: spec.HitSetting{
			BetTypeStr: "line_ltr",
			LineTable:  [][]int16{{0, 0, 0}},
		},
		SymbolSetting: spec.SymbolSetting{
			SymbolUsedStr: []string{"H1"},
			PayTable:      [][]int{{0, 2, 5}},
		},
	}
}

func TestSpinResultAppendReset(t *testing.T) {
	gs := testGameSetting()
	sr := NewSpinResult(gs)
	if sr.GameName != gs.GameName || sr.GameID != spec.GID(gs.GameID) || sr.Logic != gs.LogicKey {
		t.Fatalf("unexpected spin result metadata: %+v", sr)
	}

	gmr1 := &GameModeResult{TotalWin: 10}
	gmr2 := &GameModeResult{TotalWin: 7}
	sr.AppendModeResult(gmr1)
	sr.AppendModeResult(gmr2)

	if sr.TotalWin != 17 {
		t.Fatalf("expected total win 17, got %d", sr.TotalWin)
	}
	if sr.GameModeCount != 2 || len(sr.GameModeList) != 2 {
		t.Fatalf("expected 2 game modes, got %d (%d)", sr.GameModeCount, len(sr.GameModeList))
	}

	sr.End()
	if !sr.IsGameEnd {
		t.Fatalf("expected game end flag")
	}

	sr.Reset()
	if sr.TotalWin != 0 || sr.GameModeCount != 0 || len(sr.GameModeList) != 0 || sr.IsGameEnd {
		t.Fatalf("spin result not reset: %+v", sr)
	}
}

func TestSpinResultAppendAfterEndPanics(t *testing.T) {
	gs := testGameSetting()
	sr := NewSpinResult(gs)
	sr.End()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic when appending after End")
		}
	}()
	sr.AppendModeResult(&GameModeResult{TotalWin: 1})
}

func TestGameModeResultRecordAndHitMap(t *testing.T) {
	gms := testGameModeSetting()
	if err := gms.ScreenSetting.Init(); err != nil {
		t.Fatalf("screen init error: %v", err)
	}
	if err := gms.HitSetting.Init(); err != nil {
		t.Fatalf("hit setting init error: %v", err)
	}
	gmr := NewGameModeResult(0, gms, 4, 4)
	screen := []int16{1, 1, 1}
	gmr.RecordDetail(10, 1, 0, 3, 0, 0, []int16{0, 1, 2})
	if gmr.GetTmpWin() != 10 {
		t.Fatalf("expected tmp win 10, got %d", gmr.GetTmpWin())
	}

	gmr.AddAct(FinishRound, "base", screen, nil)

	if len(gmr.ActResults) != 1 {
		t.Fatalf("expected 1 act result, got %d", len(gmr.ActResults))
	}
	if gmr.ActResults[0].IsRoundEnd != true {
		t.Fatalf("expected round end flag")
	}
	if got := gmr.HitMapLastAct(); len(got) != 3 {
		t.Fatalf("expected hitmap length 3, got %v", got)
	}
	if view := gmr.View(); len(view) != 3 {
		t.Fatalf("expected view length 3, got %v", view)
	}
	if gmr.GetTmpWin() != 0 {
		t.Fatalf("expected tmp win reset after AddAct")
	}
}

func TestGameModeResultDiscard(t *testing.T) {
	gms := testGameModeSetting()
	if err := gms.ScreenSetting.Init(); err != nil {
		t.Fatalf("screen init error: %v", err)
	}
	if err := gms.HitSetting.Init(); err != nil {
		t.Fatalf("hit setting init error: %v", err)
	}
	gmr := NewGameModeResult(0, gms, 4, 4)
	initialTotal := gmr.TotalWin

	gmr.RecordDetail(10, 1, 0, 3, 0, 0, []int16{0, 1, 2})
	gmr.RecordDetail(5, 1, 0, 2, 0, 0, []int16{0, 1})
	tmpWinBefore := gmr.GetTmpWin()
	if tmpWinBefore != 15 {
		t.Fatalf("expected tmp win 15, got %d", tmpWinBefore)
	}

	// Discard should rollback
	gmr.Discard()
	if gmr.GetTmpWin() != 0 {
		t.Fatalf("expected tmp win 0 after discard, got %d", gmr.GetTmpWin())
	}
	if gmr.TotalWin != initialTotal {
		t.Fatalf("expected total win unchanged after discard")
	}
	// HitsFlat should be rolled back
	if len(gmr.HitsFlat) != 0 {
		t.Fatalf("expected hitsflat rolled back, got len %d", len(gmr.HitsFlat))
	}
}

func TestGameModeResultRecordDetailSegments(t *testing.T) {
	gms := testGameModeSetting()
	if err := gms.ScreenSetting.Init(); err != nil {
		t.Fatalf("screen init error: %v", err)
	}
	if err := gms.HitSetting.Init(); err != nil {
		t.Fatalf("hit setting init error: %v", err)
	}
	gmr := NewGameModeResult(0, gms, 4, 4)

	seg1 := []int16{0, 1}
	seg2 := []int16{2, 3}
	gmr.RecordDetailSegments(20, 1, 0, 4, 2, 0, seg1, seg2)

	if gmr.GetTmpWin() != 20 {
		t.Fatalf("expected tmp win 20, got %d", gmr.GetTmpWin())
	}
	details := gmr.GetDetails()
	if len(details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(details))
	}
	if details[0].HitsFlatLen != 4 {
		t.Fatalf("expected hitsflat len 4, got %d", details[0].HitsFlatLen)
	}
	if len(gmr.HitsFlat) != 4 {
		t.Fatalf("expected hitsflat length 4, got %d", len(gmr.HitsFlat))
	}
}

func TestGameModeResultMultipleActs(t *testing.T) {
	gms := testGameModeSetting()
	if err := gms.ScreenSetting.Init(); err != nil {
		t.Fatalf("screen init error: %v", err)
	}
	if err := gms.HitSetting.Init(); err != nil {
		t.Fatalf("hit setting init error: %v", err)
	}
	gmr := NewGameModeResult(0, gms, 4, 4)
	screen := []int16{1, 1, 1}

	// First act
	gmr.RecordDetail(10, 1, 0, 3, 0, 0, []int16{0, 1, 2})
	gmr.AddAct(FinishAct, "act1", screen, nil)

	// Second act
	gmr.RecordDetail(5, 1, 0, 2, 0, 0, []int16{0, 1})
	gmr.AddAct(FinishStep, "act2", nil, nil)

	if len(gmr.ActResults) != 2 {
		t.Fatalf("expected 2 acts, got %d", len(gmr.ActResults))
	}
	if gmr.ActResults[0].IsStepEnd {
		t.Fatalf("first act should not be step end")
	}
	if !gmr.ActResults[1].IsStepEnd {
		t.Fatalf("second act should be step end")
	}
	if gmr.TotalWin != 15 {
		t.Fatalf("expected total win 15, got %d", gmr.TotalWin)
	}
}

func TestGameModeResultFinishStepAndRound(t *testing.T) {
	gms := testGameModeSetting()
	if err := gms.ScreenSetting.Init(); err != nil {
		t.Fatalf("screen init error: %v", err)
	}
	if err := gms.HitSetting.Init(); err != nil {
		t.Fatalf("hit setting init error: %v", err)
	}
	gmr := NewGameModeResult(0, gms, 4, 4)
	screen := []int16{1, 1, 1}

	gmr.RecordDetail(10, 1, 0, 3, 0, 0, []int16{0, 1, 2})
	gmr.AddAct(FinishAct, "act", screen, nil)

	// Manual finish step
	gmr.FinishStep()
	if !gmr.ActResults[0].IsStepEnd {
		t.Fatalf("expected step end after FinishStep")
	}

	// Manual finish round
	gmr.FinishRound()
	if !gmr.ActResults[0].IsRoundEnd {
		t.Fatalf("expected round end after FinishRound")
	}
}

func TestGameModeResultView_EdgeCases(t *testing.T) {
	gms := testGameModeSetting()
	if err := gms.ScreenSetting.Init(); err != nil {
		t.Fatalf("screen init error: %v", err)
	}
	if err := gms.HitSetting.Init(); err != nil {
		t.Fatalf("hit setting init error: %v", err)
	}
	gmr := NewGameModeResult(0, gms, 4, 4)

	// View before any screen is added
	view := gmr.View()
	if view != nil {
		t.Fatalf("expected nil view before screen added, got %v", view)
	}

	// View after screen is added
	screen := []int16{1, 1, 1}
	gmr.AddAct(FinishAct, "act", screen, nil)
	view = gmr.View()
	if len(view) != 3 {
		t.Fatalf("expected view length 3, got %d", len(view))
	}
}

func TestGameModeResultHitMapTmp(t *testing.T) {
	gms := testGameModeSetting()
	if err := gms.ScreenSetting.Init(); err != nil {
		t.Fatalf("screen init error: %v", err)
	}
	if err := gms.HitSetting.Init(); err != nil {
		t.Fatalf("hit setting init error: %v", err)
	}
	gmr := NewGameModeResult(0, gms, 4, 4)

	// HitMapTmp before any detail
	tmp := gmr.HitMapTmp()
	if len(tmp) != 0 {
		t.Fatalf("expected empty tmp hitmap, got %v", tmp)
	}

	// Record detail but not AddAct
	gmr.RecordDetail(10, 1, 0, 3, 0, 0, []int16{0, 1, 2})
	tmp = gmr.HitMapTmp()
	if len(tmp) != 3 {
		t.Fatalf("expected tmp hitmap length 3, got %d", len(tmp))
	}

	// After AddAct, tmp should be cleared
	gmr.AddAct(FinishAct, "act", nil, nil)
	tmp = gmr.HitMapTmp()
	if len(tmp) != 0 {
		t.Fatalf("expected empty tmp hitmap after AddAct, got %v", tmp)
	}
}

func TestGameModeResultReset(t *testing.T) {
	gms := testGameModeSetting()
	if err := gms.ScreenSetting.Init(); err != nil {
		t.Fatalf("screen init error: %v", err)
	}
	if err := gms.HitSetting.Init(); err != nil {
		t.Fatalf("hit setting init error: %v", err)
	}
	gmr := NewGameModeResult(0, gms, 4, 4)
	modeId := gmr.GameModeId

	// Add some data
	gmr.RecordDetail(10, 1, 0, 3, 0, 0, []int16{0, 1, 2})
	gmr.AddAct(FinishRound, "act", []int16{1, 1, 1}, nil)
	gmr.TotalWin = 10
	gmr.Trigger = 1

	// Reset
	gmr.Reset()

	if gmr.TotalWin != 0 {
		t.Fatalf("expected total win 0 after reset, got %d", gmr.TotalWin)
	}
	if gmr.GameModeId != modeId {
		t.Fatalf("expected mode id unchanged after reset")
	}
	if gmr.IsModeEnd {
		t.Fatalf("expected mode end false after reset")
	}
	if gmr.Trigger != 0 {
		t.Fatalf("expected trigger 0 after reset, got %d", gmr.Trigger)
	}
	if len(gmr.ActResults) != 0 {
		t.Fatalf("expected empty act results after reset")
	}
	if len(gmr.Screens) != 0 {
		t.Fatalf("expected empty screens after reset")
	}
	if len(gmr.HitsFlat) != 0 {
		t.Fatalf("expected empty hitsflat after reset")
	}
}

func TestGameModeResultUpdateTmpWin(t *testing.T) {
	gms := testGameModeSetting()
	if err := gms.ScreenSetting.Init(); err != nil {
		t.Fatalf("screen init error: %v", err)
	}
	if err := gms.HitSetting.Init(); err != nil {
		t.Fatalf("hit setting init error: %v", err)
	}
	gmr := NewGameModeResult(0, gms, 4, 4)

	// Record detail adds to tmp win
	gmr.RecordDetail(10, 1, 0, 3, 0, 0, []int16{0, 1, 2})
	if gmr.GetTmpWin() != 10 {
		t.Fatalf("expected tmp win 10, got %d", gmr.GetTmpWin())
	}

	// UpdateTmpWin directly sets value
	gmr.UpdateTmpWin(20)
	if gmr.GetTmpWin() != 20 {
		t.Fatalf("expected tmp win 20 after UpdateTmpWin, got %d", gmr.GetTmpWin())
	}

	// Should accumulate in total
	gmr.AddAct(FinishAct, "act", nil, nil)
	if gmr.TotalWin != 20 {
		t.Fatalf("expected total win 20, got %d", gmr.TotalWin)
	}
}

func TestGameModeResultAddAct_ScreenSizePanic(t *testing.T) {
	gms := testGameModeSetting()
	if err := gms.ScreenSetting.Init(); err != nil {
		t.Fatalf("screen init error: %v", err)
	}
	if err := gms.HitSetting.Init(); err != nil {
		t.Fatalf("hit setting init error: %v", err)
	}
	gmr := NewGameModeResult(0, gms, 4, 4)

	// Wrong screen size should panic
	wrongScreen := []int16{1, 2} // size 2, expected 3
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for wrong screen size")
		}
	}()
	gmr.AddAct(FinishAct, "act", wrongScreen, nil)
}

func TestSpinResultReset_PreservesCapacity(t *testing.T) {
	gs := testGameSetting()
	sr := NewSpinResult(gs)

	// Add multiple modes
	for i := 0; i < 5; i++ {
		sr.AppendModeResult(&GameModeResult{TotalWin: i * 10})
	}
	initialCap := cap(sr.GameModeList)

	// Reset
	sr.Reset()

	// Capacity should be preserved
	if cap(sr.GameModeList) != initialCap {
		t.Fatalf("expected capacity preserved, got %d (initial %d)", cap(sr.GameModeList), initialCap)
	}
	// Length should be 0
	if len(sr.GameModeList) != 0 {
		t.Fatalf("expected length 0 after reset, got %d", len(sr.GameModeList))
	}
}
