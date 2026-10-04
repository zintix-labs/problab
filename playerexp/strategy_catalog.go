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

package playerexp

import (
	"fmt"
	"slices"

	"github.com/zintix-labs/problab/spec"
)

// StrategyRegistration binds a configuration name to a factory, not a shared
// player Strategy. The factory must create independent memory for each player.
type StrategyRegistration struct {
	Name    string
	Factory StrategyFactory
}

// StrategyCatalog is application-owned declarative registration data.
// Configure it before BuildRegistry; neither it nor factories may be mutated
// concurrently with building. Factories remain borrowed during Run.
type StrategyCatalog map[spec.GID][]StrategyRegistration

// BuildRegistry validates entries using Register and returns a fresh registry.
// GIDs are sorted and each game's slice order is preserved, making validation
// errors deterministic. On failure no partial registry is returned.
// Map/slice entries are copied into the registry; factories are not deep-copied.
func (c StrategyCatalog) BuildRegistry() (*StrategyRegistry, error) {
	gids := make([]spec.GID, 0, len(c))
	for gid := range c {
		gids = append(gids, gid)
	}
	slices.Sort(gids)
	r := NewStrategyRegistry()
	for _, gid := range gids {
		for i, entry := range c[gid] {
			if err := r.Register(gid, entry.Name, entry.Factory); err != nil {
				return nil, fmt.Errorf("game %d strategy[%d] %q: %w", gid, i, entry.Name, err)
			}
		}
	}
	return r, nil
}
