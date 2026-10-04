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

// Package strategies provides optional playerexp strategy factories. Nothing is
// registered automatically; applications choose factories per game.
package strategies

import (
	"fmt"
	"math"

	"github.com/zintix-labs/problab/playerexp"
	"github.com/zintix-labs/problab/sdk/buf"
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
// overflow panic: the current StrategyFactory interface cannot return an error;
// playerexp.Run captures this as a factory error, never as a player exit.
// The game's mode upper bound and actual wager cost remain executor checks.
func (m Monotone) NewStrategy(initialBalance int) playerexp.Strategy {
	if m.BetMode < 0 || m.BetMult <= 0 || m.MaxSpins < 1 || m.MaxSpins > playerexp.MaxSpinsPerPlayer || m.SatisfiedRatio <= 1 {
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

func (m *monotone) FirstSpin(state playerexp.PlayerState) playerexp.Decision {
	return m.decide(state)
}
func (m *monotone) Next(state playerexp.PlayerState, _ *buf.SpinResult) playerexp.Decision {
	return m.decide(state)
}
func (m *monotone) decide(state playerexp.PlayerState) playerexp.Decision {
	keep := true
	bet := playerexp.Bet{BetMode: m.BetMode, BetMult: m.BetMult}
	end := playerexp.Other
	if state.Balance >= m.Satisfied {
		keep = false
		end = playerexp.Satisfied
	} else if state.Spins >= m.MaxSpins {
		keep = false
		end = playerexp.Dissatisfied
	}
	return playerexp.Decision{KeepPlay: keep, Bet: bet, EndType: end}
}
