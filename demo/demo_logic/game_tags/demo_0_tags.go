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

package game_tags

import (
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/tag"
)

var Demo_0_Tags = map[string]tag.IsTag{
	"bg":           Demo_0_IsOnlyBG,
	"fg":           Demo_0_IsEntryFree,
	"demo_0_tag_1": Demo_0_IsMatchClass01,
}

// Demo_0_IsOnlyBG selects results containing only the base game.
func Demo_0_IsOnlyBG(sr *buf.SpinResult) bool {
	return sr.GameModeCount == 1
}

// Demo_0_IsEntryFree preserves this game's collection policy: free-game payout
// divided by bet using integer division must exceed five (not merely enter FG).
func Demo_0_IsEntryFree(sr *buf.SpinResult) bool {
	return sr.GameModeCount > 1 && ((sr.TotalWin-sr.GameModeList[0].TotalWin)/sr.Bet) > 5
}

// Demo_0_IsMatchClass01
func Demo_0_IsMatchClass01(sr *buf.SpinResult) bool {
	return true
}
