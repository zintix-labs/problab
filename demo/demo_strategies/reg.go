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

// Package demo_strategies assembles explicitly selected factories for demo
// games. Reusable official implementations live in playerexp/strategies.
package demo_strategies

import "github.com/zintix-labs/problab/playerexp"

// Catalog is the application's explicit game-to-strategy injection catalog.
// Add a game's configured factory list in game_<gid>.go, then add one line here.
// There is no init-time registration or automatic cross-game fallback.
var Catalog = playerexp.StrategyCatalog{
	0: game0Strategies,
}
