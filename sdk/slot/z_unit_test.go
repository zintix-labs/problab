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

package slot_test

import (
	"testing"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

type testLogic struct{}

type testExt struct{ v int }

func (t *testExt) Reset() {
	t.v = 0
}

func (t *testExt) Snapshot() any {
	return t.v
}

func (t *testLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	res := g.StartNewSpin(r)
	res.TotalWin = r.Bet
	return res
}

func testGameSetting() *spec.GameSetting {
	gms := spec.GameModeSetting{
		ScreenSetting: spec.ScreenSetting{
			Columns: 2,
			Rows:    1,
		},
		GenScreenSetting: spec.GenScreenSetting{
			GenReelTypeStr: "GenReelByReelIdx",
			ReelSetGroup: []spec.ReelSet{
				{Weight: 1, Reels: []spec.Reel{
					{ReelSymbols: []int16{1}},
					{ReelSymbols: []int16{1}},
				}},
			},
		},
		SymbolSetting: spec.SymbolSetting{
			SymbolUsedStr: []string{"H1"},
			PayTable:      [][]int{{0, 2}},
		},
		HitSetting: spec.HitSetting{
			BetTypeStr: "line_ltr",
			LineTable:  [][]int16{{0, 0}},
		},
	}

	return &spec.GameSetting{
		GameName:         "demo",
		GameID:           1,
		LogicKey:         "demo_logic",
		BetUnits:         []int{1},
		MaxWinLimit:      10,
		GameModeSettings: []spec.GameModeSetting{gms},
	}
}

func TestLogicRegistry(t *testing.T) {
	reg := slot.NewLogicRegistry()
	if err := reg.Register("demo", func(g *slot.Game) (slot.GameLogic, error) { return &testLogic{}, nil }); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if err := reg.Register("demo", func(g *slot.Game) (slot.GameLogic, error) { return &testLogic{}, nil }); err == nil {
		t.Fatalf("expected duplicate register error")
	}
	if _, err := reg.Build("demo", nil); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	reg2 := slot.NewLogicRegistry()
	_ = reg2.Register("demo", func(g *slot.Game) (slot.GameLogic, error) { return &testLogic{}, nil })
	if _, err := slot.MergeLogicRegistry(reg, reg2); err == nil {
		t.Fatalf("expected merge duplicate error")
	}
}
