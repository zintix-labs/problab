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

package slot

import (
	"fmt"

	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

// GameLogic is the slot game logic contract.
// Implementations should be fast and allocation-free on the hot path.
//
// GetResult must write/return the *buf.SpinResult for the given request.
// `g` provides access to the (already-initialized) runtime objects for this game instance.
//
// NOTE: GameSetting is treated as read-only after Init. If you intentionally mutate settings,
// you are responsible for correctness and concurrency safety.
type GameLogic interface {
	GetResult(r *buf.SpinRequest, g *Game) *buf.SpinResult
}

// LogicBuilder builds a GameLogic instance bound to a specific *Game (per-machine/per-game instance).
// It is invoked during game initialization.
type LogicBuilder func(g *Game) (GameLogic, error)

// GameRegister registers the logic builder for lkey
func GameRegister(lkey spec.LogicKey, builder LogicBuilder, reg *LogicRegistry) error {
	// register builder
	if err := reg.Register(lkey, builder); err != nil {
		return err
	}
	return nil
}

type LogicRegistry struct {
	builders map[spec.LogicKey]LogicBuilder
}

func NewLogicRegistry() *LogicRegistry {
	return &LogicRegistry{
		builders: make(map[spec.LogicKey]LogicBuilder, 64),
	}
}

func (r *LogicRegistry) Register(lkey spec.LogicKey, b LogicBuilder) error {
	if _, ok := r.builders[lkey]; ok {
		return errs.NewFatal("duplicate logic builder")
	}
	r.builders[lkey] = b
	return nil
}

func (r *LogicRegistry) Build(lkey spec.LogicKey, g *Game) (GameLogic, error) {
	b, ok := r.builders[lkey]
	if !ok {
		return nil, errs.NewFatal(fmt.Sprintf("logic is not exist: %s", lkey))
	}
	return b(g)
}

func (r *LogicRegistry) IsExist(lkey spec.LogicKey) bool {
	_, ok := r.builders[lkey]
	return ok
}

// MergeLogicRegistry merges multiple registries into a new one.
//
// Because function values are not comparable in Go (except to nil), duplicate keys are treated
// as an error unconditionally. This keeps behavior deterministic and avoids “last one wins” surprises.
func MergeLogicRegistry(regs ...*LogicRegistry) (*LogicRegistry, error) {
	lr := NewLogicRegistry()

	// Track where a key first came from to produce a useful error message.
	origin := make(map[spec.LogicKey]int, 64)

	for i, r := range regs {
		if r == nil {
			continue
		}
		for lkey, builder := range r.builders {
			if _, ok := lr.builders[lkey]; ok {
				prev := origin[lkey]
				return nil, errs.NewFatal(fmt.Sprintf("duplicate logic key %s (registry #%d and #%d)", lkey, prev, i))
			}
			lr.builders[lkey] = builder
			origin[lkey] = i
		}
	}

	return lr, nil
}
