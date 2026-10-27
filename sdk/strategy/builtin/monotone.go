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

// Package builtin provides optional strategy factories. Nothing is registered
// automatically; applications choose factories per game and inject them.
package builtin

import (
	"fmt"
	"math"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/strategy"
)

// Monotone repeatedly requests the same wager. Reaching initial balance times
// SatisfiedRatio ends Satisfied; reaching MaxSpins first ends Dissatisfied.
// The executor alone determines Bust when the next legal wager is unaffordable.
type Monotone struct {
	BetMode        int
	BetMult        int
	MaxSpins       int
	SatisfiedRatio int
}

// NewStrategy creates independent player state from an already converted,
// actual initial balance (not YAML init_bet). Invalid settings or target
// overflow panic: the Factory interface cannot return an error, so the executor
// captures this as a factory error, never as a player exit. The game's mode
// upper bound, the actual wager cost and the executor's own spin limit remain
// executor checks: a MaxSpins above that limit is cut short by the executor.
func (m Monotone) NewStrategy(initialBalance int) strategy.Strategy {
	if m.BetMode < 0 || m.BetMult <= 0 || m.MaxSpins < 1 || m.SatisfiedRatio <= 1 {
		panic(fmt.Errorf("Monotone: invalid configuration (mode=%d mult=%d max_spins=%d satisfied_ratio=%d)", m.BetMode, m.BetMult, m.MaxSpins, m.SatisfiedRatio))
	}
	if initialBalance <= 0 || initialBalance > math.MaxInt/m.SatisfiedRatio {
		panic(fmt.Errorf("Monotone: invalid or overflowing target balance (initial=%d ratio=%d)", initialBalance, m.SatisfiedRatio))
	}
	return &monotone{
		BetMode: m.BetMode, BetMult: m.BetMult, MaxSpins: m.MaxSpins,
		Satisfied: initialBalance * m.SatisfiedRatio,
	}
}

type monotone struct {
	BetMode   int
	BetMult   int
	MaxSpins  int
	Satisfied int // Actual target balance, not a ratio.
}

func (m *monotone) FirstSpin(state strategy.PlayerState) strategy.Decision {
	return m.decide(state)
}
func (m *monotone) Next(state strategy.PlayerState, _ *buf.SpinResult) strategy.Decision {
	return m.decide(state)
}
func (m *monotone) decide(state strategy.PlayerState) strategy.Decision {
	keep := true
	bet := strategy.Bet{BetMode: m.BetMode, BetMult: m.BetMult}
	end := strategy.Other
	if state.Balance >= m.Satisfied {
		keep = false
		end = strategy.Satisfied
	} else if state.Spins >= m.MaxSpins {
		keep = false
		end = strategy.Dissatisfied
	}
	return strategy.Decision{KeepPlay: keep, Bet: bet, EndType: end}
}
