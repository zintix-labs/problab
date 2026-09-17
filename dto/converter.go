// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package dto

import (
	"encoding/json"

	"github.com/zintix-labs/problab/spec"
)

// ResultConverter encodes one replayed result. The input is borrowed until the
// call returns; retain it only after copying mutable data. Implementations own
// their platform schema, including payout semantics and removal of PRNG state.
type ResultConverter func(SpinResult) (json.RawMessage, error)

// IdentityConverter is the default RGS converter. It includes the DTO's start
// and after PRNG snapshots and checkpoint: do not disclose it to parties that
// should not receive the underlying replay material.
func IdentityConverter(r SpinResult) (json.RawMessage, error) {
	return json.Marshal(r)
}

// SpinResultWithoutSnapshot is the payload handed to an RGS or platform: the
// same round data as dto.SpinResult, with the engine's Core snapshots removed.
//
// The field list is deliberately explicit rather than embedding dto.SpinResult.
// A field added to SpinResult upstream must not start flowing into an exported
// bank on its own; with this shape the default is to leave it out.
//
// This removes only what problab put in State. A game's own ExtendResult
// payload (gamemodes[].acts[].ext) is authored by the game and is passed
// through verbatim — auditing that stays with the game author.
type SpinResultWithoutSnapshot struct {
	GameName   string              `json:"game"`
	GameID     spec.GID            `json:"gameid"`
	TotalWin   int                 `json:"win"`
	Bet        int                 `json:"bet"`
	BetMode    int                 `json:"betmode"`
	BetMult    int                 `json:"betmult"`
	GameModes  []GameModeResultDTO `json:"gamemodes,omitempty"`
	IsGameEnd  bool                `json:"isend"`
	Checkpoint json.RawMessage     `json:"cp,omitempty"` // 完整局結束的遊戲可整欄拿掉
}

// WithoutSnapshotConverter is a dto.ResultConverter. The DTO is borrowed for the duration of the
// call; every field below is copied or re-referenced, nothing is mutated.
func WithoutSnapshotConverter(r SpinResult) (json.RawMessage, error) {
	return json.Marshal(SpinResultWithoutSnapshot{
		GameName:   r.GameName,
		GameID:     r.GameID,
		TotalWin:   r.TotalWin,
		Bet:        r.Bet,
		BetMode:    r.BetMode,
		BetMult:    r.BetMult,
		GameModes:  r.GameModes,
		IsGameEnd:  r.IsGameEnd,
		Checkpoint: r.State.Checkpoint,
	})
}
