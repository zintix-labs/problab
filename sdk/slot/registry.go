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
// Every non-nil result must be a legal segment result with at least one valid
// GameModeResult. The first GameModeResult of a whole game (the first one of cycle 0)
// is reported as the base game win; the first one of a later segment may be a free
// game. The engine overwrites the result's Cycle after GetResult returns.
//
// Segments: when the result is not the end of the game (IsGameEnd is false), ChooseNext
// must list the entries the player may choose next; when it is the end, ChooseNext must
// be empty. The Machine guarantees the request's structure before calling GetResult:
// Cycle >= 0; cycle 0 has no ChooseNext and Choice 0; a later cycle has a non-empty
// ChooseNext and 0 <= Choice < len(ChooseNext), so ChooseNext[Choice] is always a valid
// index. Whether that entry and the Checkpoint are acceptable at this point is up to the
// game.
//
// Returning nil means the game rejects the request (for example an entry or checkpoint it
// does not accept): Spin reports a request error and SpinUnsafe returns nil. Validate
// before drawing random numbers where possible. Internal failures must still panic, not
// return nil.
//
// The request is owned by the Machine and reset before every execution. Treat it as
// read-only and do not retain `r` or anything it references (ChooseNext, checkpoint)
// after GetResult returns; later executions overwrite them. A checkpoint written to the
// result must not be modified after it is returned, and must round-trip through the
// codec registered for the logic.
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
