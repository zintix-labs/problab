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
	"github.com/zintix-labs/problab/playerexp"
	"github.com/zintix-labs/problab/playerexp/strategies"
)

// Game 0 uses mode 0. Each factory creates a fresh strategy per player;
// BetUnits[0] determines actual cost; the strategy does not duplicate the ledger.
// Other games may reuse Monotone or supply private factories here.
var game0Strategies = []playerexp.StrategyRegistration{
	{Name: "monotone", Factory: strategies.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 5000, SatisfiedRatio: 3}},
	{Name: "higher_bet", Factory: strategies.Monotone{BetMode: 0, BetMult: 2, MaxSpins: 5000, SatisfiedRatio: 3}},
}
