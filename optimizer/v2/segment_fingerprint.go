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
	"reflect"
	"slices"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

// segmentFingerprint is a deep copy of the outcome of one segment: everything a
// game decides, and nothing the engine or the machine fills in. Two runs of the
// same entry from the same seed must produce equal fingerprints when the game
// keeps the entry contract. It must be taken before the Machine runs again,
// because the result borrows Machine-owned buffers.
//
// Excluded on purpose: Cycle (written by the engine), the RNG snapshots in
// State (compared separately as the post-run Core), the machine identity
// (GameName, GameID, Logic, BetUnits), and per-mode bookkeeping that is not an
// outcome: TmpAct (an accumulation cursor whose CurrDetail is covered by
// GetDetails), Details beyond the valid range (stale entries kept across
// Reset), and the Extend policy of the machine.
type segmentFingerprint struct {
	totalWin, bet, betMode, betMult, modeCount int
	chooseNext                                 []int
	isGameEnd                                  bool
	checkpoint                                 any
	modes                                      []modeFingerprint
}

type modeFingerprint struct {
	totalWin, gameModeID, trigger, screenSize int
	isModeEnd                                 bool
	betType                                   spec.BetType
	acts                                      []buf.ActResult
	screens, hitsFlat                         []int16
	details                                   []buf.CalcScreenDetail
}

func fingerprintSegment(sr *buf.SpinResult) segmentFingerprint {
	f := segmentFingerprint{
		totalWin: sr.TotalWin, bet: sr.Bet, betMode: sr.BetMode, betMult: sr.BetMult,
		modeCount: sr.GameModeCount, chooseNext: slices.Clone(sr.ChooseNext), isGameEnd: sr.IsGameEnd,
		modes: make([]modeFingerprint, len(sr.GameModeList)),
	}
	if sr.State != nil {
		// A game hands over a new checkpoint value and never changes it afterwards.
		f.checkpoint = sr.State.Checkpoint
	}
	for i, gmr := range sr.GameModeList {
		f.modes[i] = modeFingerprint{
			totalWin: gmr.TotalWin, gameModeID: gmr.GameModeId, trigger: gmr.Trigger, screenSize: gmr.ScreenSize,
			isModeEnd: gmr.IsModeEnd, betType: gmr.BetType,
			acts: slices.Clone(gmr.ActResults), screens: slices.Clone(gmr.Screens),
			hitsFlat: slices.Clone(gmr.HitsFlat), details: slices.Clone(gmr.GetDetails()),
		}
	}
	return f
}

// equal compares the copies field by field; ExtendResult values and the
// checkpoint are compared with reflect.DeepEqual.
func (f segmentFingerprint) equal(g segmentFingerprint) bool {
	return reflect.DeepEqual(f, g)
}
