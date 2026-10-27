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

// Package strategy defines the contract between player strategies and the
// executor that runs them (playerexp). A strategy author depends only on this
// package and sdk/buf; implementations are injected explicitly through a
// Catalog or Registry, never registered automatically.
package strategy

import "github.com/zintix-labs/problab/sdk/buf"

// PlayerState is a value snapshot of the executor's authoritative ledger. The
// wager is paid when a game starts and the game's win is settled when it ends:
// at a choice point Balance and TotalBet already include the wager, while Spins
// and TotalWin do not yet include the game.
type PlayerState struct{ Balance, Spins, TotalBet, TotalWin int }

// Bet selects a game-specific mode and a strictly positive multiplier.
type Bet struct{ BetMode, BetMult int }

// EndType is a mutually exclusive player outcome. Bust is executor-owned.
type EndType uint8

const (
	Other EndType = iota
	Bust
	Satisfied
	Dissatisfied
)

// Decision is read according to when it is returned.
//
// From FirstSpin and from Next after a finished game, it decides the next game:
// Bet is used only while KeepPlay, otherwise only EndType; Choice must be nil.
// Its zero value leaves as Other without making a wager.
//
// From Next at a choice point, it only chooses: Choice must be set to a position
// in result.ChooseNext and every other field must be zero. KeepPlay == false is
// not a request to leave there; a player cannot leave in the middle of a game.
type Decision struct {
	KeepPlay bool
	Bet
	EndType

	// Choice is the position in result.ChooseNext to enter at a choice point;
	// nil means no choice was made. It is a pointer so that a strategy that does
	// not handle choice points is caught instead of silently taking position 0.
	Choice *int
}

// ChoiceAt returns the decision for a choice point that enters position i of
// result.ChooseNext.
func ChoiceAt(i int) Decision { return Decision{Choice: &i} }

// Factory creates independent player memory. Configure factories before
// registration; they and their closure dependencies must not change during Run.
type Factory interface {
	NewStrategy(initialBalance int) Strategy
}

// Strategy reads authoritative state and borrowed, read-only results. Copy only
// needed values for history; dto.NewSpinResultDTO is an optional full-result
// copy (Extend independence still depends on the game's Snapshot contract).
// A blocked callback cannot be preempted by context cancellation.
//
// Next is called whenever there is a result. When result.IsGameEnd is true it
// is the whole game, settled into state, and the decision is about the next
// game. When it is false the game stopped at a choice point: result is only
// that segment, the decision must be a choice, and Next is called again with
// the next segment or the whole game. Do not add up TotalWin across calls; the
// whole game already includes every segment. A strategy that needs randomness
// must use its own RNG, never the game's.
type Strategy interface {
	FirstSpin(PlayerState) Decision
	Next(PlayerState, *buf.SpinResult) Decision
}
