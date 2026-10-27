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

package v2

import (
	"testing"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

func fingerprintFixture() *buf.SpinResult {
	gmr := &buf.GameModeResult{
		TotalWin: 30, GameModeId: 1, Trigger: 1, ScreenSize: 15, BetType: spec.BetType(1),
		ActResults: []buf.ActResult{{ActType: "screen", ActWin: 30, ExtendResult: &struct{ N int }{2}}},
		Screens:    []int16{1, 2, 3}, HitsFlat: []int16{4},
		Details: []buf.CalcScreenDetail{{Win: 30}, {Win: 99}}, // the second entry is stale
		TmpAct:  &buf.TmpAct{CurrDetail: 1, Win: 5},
	}
	return &buf.SpinResult{
		TotalWin: 30, Bet: 40, BetMult: 1, GameModeCount: 1, GameModeList: []*buf.GameModeResult{gmr},
		Cycle: 1, ChooseNext: []int{7}, State: &buf.SpinState{StartCoreSnap: []byte{1}, Checkpoint: &struct{ S int }{3}},
	}
}

// The fingerprint keeps the outcome and ignores what the engine or the machine fills in.
func TestSegmentFingerprintComparesOutcomeOnly(t *testing.T) {
	base := fingerprintSegment(fingerprintFixture())
	for name, change := range map[string]func(*buf.SpinResult){
		"cycle":          func(sr *buf.SpinResult) { sr.Cycle = 2 },
		"rng snapshots":  func(sr *buf.SpinResult) { sr.State.StartCoreSnap, sr.State.AfterCoreSnap = []byte{9}, []byte{9} },
		"identity":       func(sr *buf.SpinResult) { sr.GameName, sr.GameID, sr.BetUnits = "x", 5, []int{1} },
		"tmp act":        func(sr *buf.SpinResult) { sr.GameModeList[0].TmpAct.Win = 8 },
		"stale details":  func(sr *buf.SpinResult) { sr.GameModeList[0].Details[1].Win = 1 },
		"equal pointers": func(sr *buf.SpinResult) { sr.State.Checkpoint = &struct{ S int }{3} },
	} {
		sr := fingerprintFixture()
		change(sr)
		if !fingerprintSegment(sr).equal(base) {
			t.Errorf("%s changed the fingerprint", name)
		}
	}
	for name, change := range map[string]func(*buf.SpinResult){
		"total win":   func(sr *buf.SpinResult) { sr.TotalWin++ },
		"choose next": func(sr *buf.SpinResult) { sr.ChooseNext = []int{3} },
		"game end":    func(sr *buf.SpinResult) { sr.IsGameEnd = true },
		"checkpoint":  func(sr *buf.SpinResult) { sr.State.Checkpoint = &struct{ S int }{4} },
		"mode id":     func(sr *buf.SpinResult) { sr.GameModeList[0].GameModeId = 2 },
		"bet type":    func(sr *buf.SpinResult) { sr.GameModeList[0].BetType = spec.BetType(2) },
		"screen size": func(sr *buf.SpinResult) { sr.GameModeList[0].ScreenSize = 20 },
		"act":         func(sr *buf.SpinResult) { sr.GameModeList[0].ActResults[0].ActWin = 1 },
		"extend":      func(sr *buf.SpinResult) { sr.GameModeList[0].ActResults[0].ExtendResult = &struct{ N int }{3} },
		"screens":     func(sr *buf.SpinResult) { sr.GameModeList[0].Screens[0] = 9 },
		"hits":        func(sr *buf.SpinResult) { sr.GameModeList[0].HitsFlat[0] = 9 },
		"details":     func(sr *buf.SpinResult) { sr.GameModeList[0].Details[0].Win = 1 },
		"valid range": func(sr *buf.SpinResult) { sr.GameModeList[0].TmpAct.CurrDetail = 2 },
	} {
		sr := fingerprintFixture()
		change(sr)
		if fingerprintSegment(sr).equal(base) {
			t.Errorf("%s did not change the fingerprint", name)
		}
	}
}

// The fingerprint is a copy: reusing the result buffers afterwards does not change it.
func TestSegmentFingerprintCopiesBuffers(t *testing.T) {
	sr := fingerprintFixture()
	f := fingerprintSegment(sr)
	gmr := sr.GameModeList[0]
	sr.ChooseNext[0], gmr.Screens[0], gmr.HitsFlat[0], gmr.Details[0].Win, gmr.ActResults[0].ActWin = 0, 0, 0, 0, 0
	if !f.equal(fingerprintSegment(fingerprintFixture())) {
		t.Fatal("the fingerprint shares the result buffers")
	}
}
