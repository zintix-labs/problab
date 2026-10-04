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

package strategies_test

import (
	"math"
	"testing"

	"github.com/zintix-labs/problab/playerexp"
	"github.com/zintix-labs/problab/playerexp/strategies"
)

func TestMonotoneDecisions(t *testing.T) {
	f := strategies.Monotone{BetMode: 2, BetMult: 3, MaxSpins: 10, SatisfiedRatio: 3}
	s := f.NewStrategy(500)
	for _, tc := range []struct {
		name           string
		balance, spins int
		keep           bool
		end            playerexp.EndType
	}{
		{"initial", 500, 0, true, playerexp.Other},
		{"below target", 1499, 9, true, playerexp.Other},
		{"target", 1500, 9, false, playerexp.Satisfied},
		{"above target", 1501, 9, false, playerexp.Satisfied},
		{"limit", 500, 10, false, playerexp.Dissatisfied},
		{"target precedes limit", 1500, 10, false, playerexp.Satisfied},
		{"affordability belongs to executor", 0, 9, true, playerexp.Other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := playerexp.PlayerState{Balance: tc.balance, Spins: tc.spins}
			want := playerexp.Decision{KeepPlay: tc.keep, Bet: playerexp.Bet{BetMode: 2, BetMult: 3}, EndType: tc.end}
			if got := s.FirstSpin(state); got != want {
				t.Fatalf("FirstSpin=%+v want %+v", got, want)
			}
			if got := s.Next(state, nil); got != want {
				t.Fatalf("Next=%+v want %+v", got, want)
			}
		})
	}
}

func TestMonotoneIndependentPlayers(t *testing.T) {
	f := strategies.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 100, SatisfiedRatio: 2}
	a, b := f.NewStrategy(500), f.NewStrategy(1000)
	f.SatisfiedRatio = 10
	f.BetMult = 99
	state := playerexp.PlayerState{Balance: 1000}
	if got := a.Next(state, nil); got.KeepPlay || got.EndType != playerexp.Satisfied || got.BetMult != 1 {
		t.Fatal(got)
	}
	if got := b.Next(state, nil); !got.KeepPlay || got.BetMult != 1 {
		t.Fatal(got)
	}
}

func TestMonotoneRejectsInvalidSettings(t *testing.T) {
	valid := strategies.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 100, SatisfiedRatio: 2}
	for _, tc := range []struct {
		name    string
		change  func(*strategies.Monotone)
		initial int
	}{
		{"negative mode", func(m *strategies.Monotone) { m.BetMode = -1 }, 100},
		{"zero mult", func(m *strategies.Monotone) { m.BetMult = 0 }, 100},
		{"negative mult", func(m *strategies.Monotone) { m.BetMult = -1 }, 100},
		{"zero spins", func(m *strategies.Monotone) { m.MaxSpins = 0 }, 100},
		{"exceeds limit", func(m *strategies.Monotone) { m.MaxSpins = playerexp.MaxSpinsPerPlayer + 1 }, 100},
		{"zero ratio", func(m *strategies.Monotone) { m.SatisfiedRatio = 0 }, 100},
		{"unit ratio", func(m *strategies.Monotone) { m.SatisfiedRatio = 1 }, 100},
		{"zero balance", func(*strategies.Monotone) {}, 0},
		{"negative balance", func(*strategies.Monotone) {}, -1},
		{"overflow", func(*strategies.Monotone) {}, math.MaxInt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected configuration panic")
				}
			}()
			f := valid
			tc.change(&f)
			f.NewStrategy(tc.initial)
		})
	}
	valid.MaxSpins = playerexp.MaxSpinsPerPlayer
	s := valid.NewStrategy(math.MaxInt / 2)
	if s.FirstSpin(playerexp.PlayerState{Balance: math.MaxInt / 2}).KeepPlay != true {
		t.Fatal("valid boundary rejected")
	}
}
