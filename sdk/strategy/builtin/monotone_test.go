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

package builtin_test

import (
	"math"
	"testing"

	"github.com/zintix-labs/problab/sdk/buf"

	"github.com/zintix-labs/problab/sdk/strategy"
	"github.com/zintix-labs/problab/sdk/strategy/builtin"
)

func TestMonotoneDecisions(t *testing.T) {
	f := builtin.Monotone{BetMode: 2, BetMult: 3, MaxSpins: 10, SatisfiedRatio: 3}
	s := f.NewStrategy(500)
	for _, tc := range []struct {
		name           string
		balance, spins int
		keep           bool
		end            strategy.EndType
	}{
		{"initial", 500, 0, true, strategy.Other},
		{"below target", 1499, 9, true, strategy.Other},
		{"target", 1500, 9, false, strategy.Satisfied},
		{"above target", 1501, 9, false, strategy.Satisfied},
		{"limit", 500, 10, false, strategy.Dissatisfied},
		{"target precedes limit", 1500, 10, false, strategy.Satisfied},
		{"affordability belongs to executor", 0, 9, true, strategy.Other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := strategy.PlayerState{Balance: tc.balance, Spins: tc.spins}
			want := strategy.Decision{KeepPlay: tc.keep, Bet: strategy.Bet{BetMode: 2, BetMult: 3}, EndType: tc.end}
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
	f := builtin.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 100, SatisfiedRatio: 2}
	a, b := f.NewStrategy(500), f.NewStrategy(1000)
	f.SatisfiedRatio = 10
	f.BetMult = 99
	state := strategy.PlayerState{Balance: 1000}
	if got := a.Next(state, nil); got.KeepPlay || got.EndType != strategy.Satisfied || got.BetMult != 1 {
		t.Fatal(got)
	}
	if got := b.Next(state, nil); !got.KeepPlay || got.BetMult != 1 {
		t.Fatal(got)
	}
}

func TestMonotoneRejectsInvalidSettings(t *testing.T) {
	valid := builtin.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 100, SatisfiedRatio: 2}
	for _, tc := range []struct {
		name    string
		change  func(*builtin.Monotone)
		initial int
	}{
		{"negative mode", func(m *builtin.Monotone) { m.BetMode = -1 }, 100},
		{"zero mult", func(m *builtin.Monotone) { m.BetMult = 0 }, 100},
		{"negative mult", func(m *builtin.Monotone) { m.BetMult = -1 }, 100},
		{"zero spins", func(m *builtin.Monotone) { m.MaxSpins = 0 }, 100},
		{"unknown choose", func(m *builtin.Monotone) { m.Choose = builtin.ChooseIter + 1 }, 100},
		{"zero ratio", func(m *builtin.Monotone) { m.SatisfiedRatio = 0 }, 100},
		{"unit ratio", func(m *builtin.Monotone) { m.SatisfiedRatio = 1 }, 100},
		{"zero balance", func(*builtin.Monotone) {}, 0},
		{"negative balance", func(*builtin.Monotone) {}, -1},
		{"overflow", func(*builtin.Monotone) {}, math.MaxInt},
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
	// MaxSpins has no upper bound here: the executor enforces its own spin limit.
	valid.MaxSpins = math.MaxInt
	s := valid.NewStrategy(math.MaxInt / 2)
	if s.FirstSpin(strategy.PlayerState{Balance: math.MaxInt / 2}).KeepPlay != true {
		t.Fatal("valid boundary rejected")
	}
}

// At a choice point Monotone only chooses, by its Choose; unset makes no
// choice. Each player starts its own iteration.
func TestMonotoneChoices(t *testing.T) {
	segment := &buf.SpinResult{ChooseNext: []int{3, 7}}
	state := strategy.PlayerState{Balance: 100}
	for mode, want := range map[builtin.Choose][]int{builtin.ChooseFirst: {0, 0, 0}, builtin.ChooseLast: {1, 1, 1}, builtin.ChooseIter: {0, 1, 0}} {
		f := builtin.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 10, SatisfiedRatio: 2, Choose: mode}
		a, b := f.NewStrategy(100), f.NewStrategy(100)
		for i, w := range want {
			d := a.Next(state, segment)
			if d.Choice == nil || *d.Choice != w || d.KeepPlay || d.Bet != (strategy.Bet{}) {
				t.Fatalf("mode %d choice %d: %+v", mode, i, d)
			}
		}
		if d := b.Next(state, segment); *d.Choice != want[0] {
			t.Fatalf("mode %d: the second player did not start its own iteration", mode)
		}
	}
	unset := builtin.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 10, SatisfiedRatio: 2}.NewStrategy(100)
	if d := unset.Next(state, segment); d != (strategy.Decision{}) {
		t.Fatalf("unset Choose made a decision %+v", d)
	}
	if d := unset.Next(state, &buf.SpinResult{IsGameEnd: true}); !d.KeepPlay || d.Choice != nil {
		t.Fatalf("a finished game did not decide the next wager: %+v", d)
	}
}
