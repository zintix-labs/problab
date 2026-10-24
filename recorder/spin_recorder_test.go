// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package recorder

import (
	"testing"

	"github.com/zintix-labs/problab/sdk/buf"
)

// An assembled whole game is recorded once: the wager and the round count only
// once, the first mode of cycle 0 as the base win, the rest as the free win,
// and a trigger from the game's total mode count.
func TestRecordAssembledGame(t *testing.T) {
	seg := func(cycle int, end bool, wins ...int) *buf.SpinResult {
		sr := &buf.SpinResult{Bet: 40, BetMult: 1, Cycle: cycle, IsGameEnd: end}
		for _, w := range wins {
			sr.TotalWin += w
			sr.GameModeCount++
			sr.GameModeList = append(sr.GameModeList, &buf.GameModeResult{TotalWin: w, TmpAct: &buf.TmpAct{}})
		}
		return sr
	}
	a := buf.NewSpinAssembler()
	var game *buf.SpinResult
	var err error
	for _, sr := range []*buf.SpinResult{seg(0, false, 10), seg(1, false, 20), seg(2, true, 30, 5)} {
		if game, err = a.Add(sr); err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewSpinRecorder("g", 0, []int{40}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	r.Record(game)
	r.Record(seg(0, true, 7)) // 單段、只有 BG 的一局
	b := r.Basic
	if b.Rounds != 2 || b.TotalBet != 80 {
		t.Fatalf("rounds=%d total bet=%d, want 2 and 80", b.Rounds, b.TotalBet)
	}
	if b.TotalWin != 72 || b.BaseWin != 17 || b.FreeWin != 55 {
		t.Fatalf("total=%d base=%d free=%d, want 72, 17, 55", b.TotalWin, b.BaseWin, b.FreeWin)
	}
	if b.Trigger != 1 {
		t.Fatalf("trigger=%d, want 1 (only the assembled game has more than one mode)", b.Trigger)
	}
}
