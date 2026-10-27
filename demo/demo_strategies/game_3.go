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

package demo_strategies

import (
	"github.com/zintix-labs/problab/sdk/strategy"
	"github.com/zintix-labs/problab/sdk/strategy/builtin"
)

// Game 3 is a choice game: besides the wager, each strategy picks a position at
// every choice point with Monotone's Choose.
var game3Strategies = []strategy.Registration{
	{Name: "first", Factory: builtin.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 5000, SatisfiedRatio: 3, Choose: builtin.ChooseFirst}},
	{Name: "last", Factory: builtin.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 5000, SatisfiedRatio: 3, Choose: builtin.ChooseLast}},
	{Name: "iter", Factory: builtin.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 5000, SatisfiedRatio: 3, Choose: builtin.ChooseIter}},
}
