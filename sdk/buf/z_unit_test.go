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
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestDecodeSpinRequestGET(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/spin?uid=u1&game=demo&gid=7&bet=10&bet_mode=1&bet_mult=2&session=3&choice=4", nil)
	req, err := DecodeSpinRequest(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.UID != "u1" || req.GameName != "demo" || req.GameId != 7 {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.Bet != 10 || req.BetMode != 1 || req.BetMult != 2 || req.Session != 3 {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.Choice == nil || *req.Choice != 4 {
		t.Fatalf("unexpected choice: %+v", req.Choice)
	}
}

func TestDecodeSpinRequestPOST(t *testing.T) {
	payload := map[string]any{
		"uid":      "u2",
		"game":     "demo",
		"gid":      9,
		"bet":      5,
		"bet_mode": 0,
		"bet_mult": 1,
		"session":  2,
	}
	data, _ := json.Marshal(payload)
	r := httptest.NewRequest(http.MethodPost, "/spin", bytes.NewReader(data))
	req, err := DecodeSpinRequest(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.GameId != 9 || req.Bet != 5 || req.BetMode != 0 {
		t.Fatalf("unexpected request: %+v", req)
	}
}

func TestDecodeSpinRequestRejectsUnknownFields(t *testing.T) {
	data := []byte(`{"gid":1,"game":"demo","bet":1,"bet_mode":0,"bet_mult":1,"unknown":true}`)
	r := httptest.NewRequest(http.MethodPost, "/spin", bytes.NewReader(data))
	if _, err := DecodeSpinRequest(r); err == nil {
		t.Fatalf("expected error for unknown field")
	}
}
