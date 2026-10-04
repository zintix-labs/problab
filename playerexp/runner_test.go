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
	"context"
	"errors"
	"math"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/tag"
	"github.com/zintix-labs/problab/spec"
)

type factoryFunc func(int) Strategy

func (f factoryFunc) NewStrategy(n int) Strategy { return f(n) }

type strategyFuncs struct {
	first func(PlayerState) Decision
	next  func(PlayerState, *buf.SpinResult) Decision
}

func (s *strategyFuncs) FirstSpin(p PlayerState) Decision               { return s.first(p) }
func (s *strategyFuncs) Next(p PlayerState, r *buf.SpinResult) Decision { return s.next(p, r) }

type fakeMachine struct {
	units []int
	spins int
	fn    func(int, int, int) (*buf.SpinResult, error)
}

func (m *fakeMachine) UsesOptimal() bool { return false }
func (m *fakeMachine) SpinOffline(mode, mult int) (*buf.SpinResult, error) {
	m.spins++
	if m.fn != nil {
		return m.fn(mode, mult, m.spins)
	}
	return &buf.SpinResult{Bet: m.units[mode] * mult, BetMode: mode, BetMult: mult, TotalWin: m.units[mode] * mult, IsGameEnd: true}, nil
}

type reporterFunc func(ProgressEvent)

func (f reporterFunc) Report(e ProgressEvent) { f(e) }
func fixture(n int) (Config, Options, *fakeMachine) {
	c := Config{Analysis: []GroupConfig{{Name: "test", Player: PlayerConfig{Players: n, InitBet: 500, Strategy: "s"}, WinBuckets: []float64{0, .5, 1, 2, 5, 10}, Events: []EventConfig{{Name: "any", WinRange: WinRange{}}}}}}
	o := Options{Strategies: NewStrategyRegistry()}
	m := &fakeMachine{units: []int{1, 100}}
	return c, o, m
}
func fixedFactory(spins int) factoryFunc {
	return func(int) Strategy {
		return &strategyFuncs{first: func(PlayerState) Decision { return Decision{KeepPlay: true, Bet: Bet{0, 1}} }, next: func(p PlayerState, _ *buf.SpinResult) Decision {
			if p.Spins >= spins {
				return Decision{EndType: Satisfied}
			}
			return Decision{KeepPlay: true, Bet: Bet{0, 1}}
		}}
	}
}
func register(t testing.TB, o *Options, f StrategyFactory) {
	t.Helper()
	if err := o.Strategies.Register(0, "s", f); err != nil {
		t.Fatal(err)
	}
}

func TestTagDiagnosticsDoNotAllocatePerPredicate(t *testing.T) {
	c, _, m := fixture(1)
	cg := compiledGroup{factory: fixedFactory(1), initial: 500,
		events: []compiledEvent{{lo: 0, hi: len(c.Analysis[0].WinBuckets) + 1}}}
	measure := func(n int) float64 {
		cg.events[0].tags = make([]tag.IsTag, n)
		for i := range cg.events[0].tags {
			cg.events[0].tags[i] = func(*buf.SpinResult) bool { return true }
		}
		return testing.AllocsPerRun(100, func() {
			if _, err := play(context.Background(), m, m.units, cg, c.Analysis[0], 0); err != nil {
				t.Fatal(err)
			}
		})
	}
	one, many := measure(1), measure(20)
	if many > one {
		t.Fatalf("tag count adds allocations: one=%g twenty=%g", one, many)
	}
}

func TestStateSettlementAndDynamicRTP(t *testing.T) {
	c, o, m := fixture(1)
	seen := []PlayerState{}
	register(t, &o, factoryFunc(func(initial int) Strategy {
		if initial != 500 {
			t.Fatal(initial)
		}
		return &strategyFuncs{first: func(p PlayerState) Decision {
			seen = append(seen, p)
			p.Balance = 0
			return Decision{KeepPlay: true, Bet: Bet{1, 1}}
		}, next: func(p PlayerState, _ *buf.SpinResult) Decision {
			seen = append(seen, p)
			if p.Spins == 1 {
				return Decision{KeepPlay: true, Bet: Bet{0, 2}}
			}
			return Decision{EndType: Dissatisfied}
		}}
	}))
	m.fn = func(mode, mult, n int) (*buf.SpinResult, error) {
		win := 30
		if n == 2 {
			win = 4
		}
		return &buf.SpinResult{Bet: m.units[mode] * mult, BetMode: mode, BetMult: mult, TotalWin: win, IsGameEnd: true}, nil
	}
	r, err := runMachine(context.Background(), c, o, m, m.units)
	if err != nil {
		t.Fatal(err)
	}
	want := []PlayerState{{Balance: 500}, {430, 1, 100, 30}, {432, 2, 102, 34}}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("%+v", seen)
	}
	g := r.Groups[0]
	if g.PooledRTP.Value != 34.0/102 || g.RTP[0].Value != 34.0/102 || g.Exits[2] != 1 || g.TotalWin != 34 {
		t.Fatalf("%+v", g)
	}
}

func TestPlayerLimitAndDecisionPrecedence(t *testing.T) {
	for _, leave := range []bool{false, true} {
		c, o, m := fixture(1)
		calls := 0
		register(t, &o, factoryFunc(func(int) Strategy {
			return &strategyFuncs{first: func(PlayerState) Decision { return Decision{KeepPlay: true, Bet: Bet{0, 1}, EndType: EndType(255)} }, next: func(p PlayerState, _ *buf.SpinResult) Decision {
				calls++
				if p.Spins == MaxSpinsPerPlayer {
					if leave {
						return Decision{EndType: Dissatisfied, Bet: Bet{-1, -1}}
					}
					return Decision{KeepPlay: true, Bet: Bet{-1, -1}}
				}
				return Decision{KeepPlay: true, Bet: Bet{0, 1}}
			}}
		}))
		r, e := runMachine(context.Background(), c, o, m, m.units)
		if e != nil {
			t.Fatal(e)
		}
		g := r.Groups[0]
		if m.spins != 20000 || calls != 20000 || g.Completed != 1 {
			t.Fatalf("spins=%d calls=%d", m.spins, calls)
		}
		if leave {
			if g.Exits[2] != 1 || g.MaxSpinReached != 0 {
				t.Fatal(g.Exits)
			}
		} else if g.Exits[3] != 1 || g.MaxSpinReached != 1 {
			t.Fatal(g.Exits)
		}
	}
}

func TestDecisionValidation(t *testing.T) {
	cases := []struct {
		name string
		d    Decision
		bad  bool
		bust bool
	}{
		{"zero", Decision{}, false, false}, {"ignoredBet", Decision{Bet: Bet{-1, -1}}, false, false},
		{"strategyBust", Decision{EndType: Bust}, true, false}, {"unknown", Decision{EndType: 255}, true, false},
		{"mode", Decision{KeepPlay: true, Bet: Bet{-1, 1}}, true, false}, {"mult", Decision{KeepPlay: true, Bet: Bet{0, 0}}, true, false},
		{"overflow", Decision{KeepPlay: true, Bet: Bet{1, math.MaxInt}}, true, false}, {"unaffordable", Decision{KeepPlay: true, Bet: Bet{1, 6}}, false, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c, o, m := fixture(1)
			register(t, &o, factoryFunc(func(int) Strategy { return &strategyFuncs{first: func(PlayerState) Decision { return tt.d }} }))
			r, e := runMachine(context.Background(), c, o, m, m.units)
			if (e != nil) != tt.bad {
				t.Fatal(e)
			}
			if m.spins != 0 {
				t.Fatal("unexpected spin")
			}
			if tt.bust && r.Groups[0].Exits[0] != 1 {
				t.Fatal("missing bust")
			}
		})
	}
}

func TestPreflightAndRegistry(t *testing.T) {
	c, o, m := fixture(2)
	fCalls := 0
	register(t, &o, factoryFunc(func(int) Strategy { fCalls++; return nil }))
	c.Analysis = append(c.Analysis, c.Analysis[0])
	c.Analysis[1].Name = "later"
	c.Analysis[1].Player.Strategy = "missing"
	if _, e := runMachine(context.Background(), c, o, m, m.units); e == nil || fCalls != 0 || m.spins != 0 {
		t.Fatal(e, fCalls, m.spins)
	}
	var nilFactory factoryFunc
	if e := o.Strategies.Register(0, "nil", nilFactory); e == nil {
		t.Fatal("typed nil")
	}
	if e := o.Strategies.Register(0, "s", fixedFactory(1)); e == nil {
		t.Fatal("duplicate")
	}
	if e := o.Strategies.Register(1, "s", fixedFactory(1)); e != nil {
		t.Fatal(e)
	}
	c.Analysis = c.Analysis[:1]
	c.Analysis[0].Events[0].Tags = []string{"fg"}
	o.Tags = map[spec.GID]map[string]tag.IsTag{1: {"fg": func(*buf.SpinResult) bool { return true }}}
	if _, e := runMachine(context.Background(), c, o, m, m.units); e == nil || fCalls != 0 {
		t.Fatal("cross-game tag leak")
	}
}

func TestAllGroupsPreflightBalancesAndTags(t *testing.T) {
	for _, which := range []string{"tag", "initial-overflow", "unit"} {
		t.Run(which, func(t *testing.T) {
			c, o, m := fixture(1)
			created := 0
			register(t, &o, factoryFunc(func(int) Strategy { created++; return fixedFactory(1).NewStrategy(500) }))
			c.Analysis = append(c.Analysis, c.Analysis[0])
			c.Analysis[1].Name = "invalid-later"
			switch which {
			case "tag":
				c.Analysis[1].Events = []EventConfig{{Name: "missing", Tags: []string{"unknown"}}}
			case "initial-overflow":
				m.units[0] = 2
				c.Analysis[1].Player.InitBet = math.MaxInt
			case "unit":
				m.units[1] = 0
			}
			r, e := runMachine(context.Background(), c, o, m, m.units)
			if e == nil || created != 0 || m.spins != 0 || r.Groups[0].Status != NotStarted {
				t.Fatal(r, e)
			}
		})
	}
}

func TestIncompleteResultsAndCheckpoint(t *testing.T) {
	var ptr *int
	for i, cp := range []any{nil, ptr, struct{}{}, map[string]int{}, []int{}, "null"} {
		c, o, m := fixture(1)
		register(t, &o, fixedFactory(1))
		m.fn = func(mode, mult, n int) (*buf.SpinResult, error) {
			return &buf.SpinResult{Bet: 1, BetMult: 1, IsGameEnd: true, State: &buf.SpinState{Checkpoint: cp}}, nil
		}
		r, e := runMachine(context.Background(), c, o, m, m.units)
		if (e != nil) != (i >= 2) {
			t.Fatalf("%d %v", i, e)
		}
		if i >= 2 && r.Groups[0].Completed != 0 {
			t.Fatal("committed invalid player")
		}
	}
	for _, r := range []*buf.SpinResult{nil, {Bet: 1, BetMult: 1}, {Bet: 2, BetMult: 1, IsGameEnd: true}, {Bet: 1, BetMult: 1, IsGameEnd: true, TotalWin: -1}} {
		c, o, m := fixture(1)
		register(t, &o, fixedFactory(1))
		m.fn = func(int, int, int) (*buf.SpinResult, error) { return r, nil }
		if _, e := runMachine(context.Background(), c, o, m, m.units); e == nil {
			t.Fatal("accepted invalid result")
		}
	}
}

func TestPartialCancellationAndPanics(t *testing.T) {
	for _, operation := range []string{"cancel", "factory", "first", "next", "tag", "game", "reporter"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, o, m := fixture(3)
			c.Analysis = append(c.Analysis, c.Analysis[0])
			c.Analysis[1].Name = "later"
			created := 0
			register(t, &o, factoryFunc(func(int) Strategy {
				created++
				if created == 2 && operation == "factory" {
					panic("factory failure")
				}
				n := created
				return &strategyFuncs{first: func(PlayerState) Decision {
					if n == 2 && operation == "first" {
						panic("first failure")
					}
					return Decision{KeepPlay: true, Bet: Bet{0, 1}}
				}, next: func(p PlayerState, _ *buf.SpinResult) Decision {
					if n == 2 {
						if operation == "cancel" {
							cancel()
						}
						if operation == "next" {
							panic("next failure")
						}
					}
					return Decision{EndType: Satisfied}
				}}
			}))
			c.Analysis[0].Events[0].Tags = []string{"x"}
			o.Tags = map[spec.GID]map[string]tag.IsTag{0: {"x": func(*buf.SpinResult) bool {
				if created == 2 && operation == "tag" {
					panic("tag failure")
				}
				return true
			}}}
			m.fn = func(mode, mult, n int) (*buf.SpinResult, error) {
				if created == 2 && operation == "game" {
					panic("game failure")
				}
				return &buf.SpinResult{Bet: 1, BetMult: 1, IsGameEnd: true}, nil
			}
			var events []ProgressEvent
			o.Reporter = reporterFunc(func(e ProgressEvent) {
				events = append(events, e)
				if operation == "reporter" && e.Kind == "progress" {
					panic("reporter failure")
				}
			})
			r, e := runMachine(ctx, c, o, m, m.units)
			if e == nil {
				t.Fatal("no error")
			}
			if operation == "tag" && !strings.Contains(e.Error(), "event \"any\" tag 0") {
				t.Fatalf("missing lazy tag diagnostic: %v", e)
			}
			if operation == "cancel" && !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
			g := r.Groups[0]
			if g.Completed != 1 || g.CompleteSpins != 1 || g.TotalBet != 1 || r.Groups[1].Status != NotStarted {
				t.Fatalf("%+v", g)
			}
			if len(events) < 2 || events[len(events)-1].Completed != 1 {
				t.Fatal(events)
			}
		})
	}
}

func TestEventsAndReproducibility(t *testing.T) {
	c, o, m := fixture(4)
	created := 0
	register(t, &o, factoryFunc(func(int) Strategy {
		spins := created
		created++
		if spins == 0 {
			return &strategyFuncs{first: func(PlayerState) Decision { return Decision{} }}
		}
		return fixedFactory(spins).NewStrategy(500)
	}))
	c.Analysis[0].Events = append(c.Analysis[0].Events, EventConfig{Name: "shadowed"})
	r, e := runMachine(context.Background(), c, o, m, m.units)
	if e != nil {
		t.Fatal(e)
	}
	g := r.Groups[0]
	if g.Events[0].Counts != [4]uint64{1, 1, 1, 1} || g.Events[0].Hits != 6 || g.Events[1].Counts[0] != 4 || g.CompleteSpins != 6 || g.UndefinedRTPPlayers != 1 {
		t.Fatalf("%+v", g)
	}
	created = 0
	m.spins = 0
	r2, e := runMachine(context.Background(), c, o, m, m.units)
	if e != nil || !reflect.DeepEqual(r, r2) {
		t.Fatal("not reproducible", e)
	}
}

func TestOrderedTagsANDAndUnmatched(t *testing.T) {
	c, o, m := fixture(1)
	register(t, &o, fixedFactory(1))
	c.Analysis[0].Events = []EventConfig{{Name: "first", Tags: []string{"a", "b", "never"}}, {Name: "second", Tags: []string{"c"}}}
	var calls []string
	o.Tags = map[spec.GID]map[string]tag.IsTag{0: {
		"a":     func(*buf.SpinResult) bool { calls = append(calls, "a"); return true },
		"b":     func(*buf.SpinResult) bool { calls = append(calls, "b"); return false },
		"never": func(*buf.SpinResult) bool { t.Fatal("AND did not short circuit"); return false },
		"c":     func(*buf.SpinResult) bool { calls = append(calls, "c"); return false },
	}}
	r, e := runMachine(context.Background(), c, o, m, m.units)
	if e != nil || !reflect.DeepEqual(calls, []string{"a", "b", "c"}) || r.Groups[0].Unmatched != 1 || r.Groups[0].Events[0].Counts[0] != 1 {
		t.Fatal(r, calls, e)
	}
}

func TestGroupCommitOverflow(t *testing.T) {
	g := GroupReport{TotalBet: math.MaxUint64, Buckets: bucketStats([]float64{0})}
	s := session{state: PlayerState{TotalBet: 1}, buckets: bucketStats([]float64{0})}
	before := g
	if e := commit(&g, s); e == nil || !reflect.DeepEqual(g, before) {
		t.Fatal("non-atomic overflow", e)
	}
}

func TestCancellationBoundariesAndNilStrategies(t *testing.T) {
	for _, where := range []string{"before", "factory", "first", "spin", "tag", "next", "progress"} {
		t.Run(where, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, o, m := fixture(2)
			if where == "before" {
				cancel()
			}
			register(t, &o, factoryFunc(func(int) Strategy {
				if where == "factory" {
					cancel()
				}
				return &strategyFuncs{first: func(PlayerState) Decision {
					if where == "first" {
						cancel()
					}
					return Decision{KeepPlay: true, Bet: Bet{0, 1}}
				}, next: func(PlayerState, *buf.SpinResult) Decision {
					if where == "next" {
						cancel()
					}
					return Decision{}
				}}
			}))
			m.fn = func(int, int, int) (*buf.SpinResult, error) {
				if where == "spin" {
					cancel()
				}
				return &buf.SpinResult{IsGameEnd: true, Bet: 1, BetMult: 1}, nil
			}
			c.Analysis[0].Events[0].Tags = []string{"x"}
			o.Tags = map[spec.GID]map[string]tag.IsTag{0: {"x": func(*buf.SpinResult) bool {
				if where == "tag" {
					cancel()
				}
				return true
			}}}
			o.Reporter = reporterFunc(func(e ProgressEvent) {
				if where == "progress" && e.Kind == "progress" {
					cancel()
				}
			})
			r, e := runMachine(ctx, c, o, m, m.units)
			want := 0
			if where == "progress" {
				want = 1
			}
			if !errors.Is(e, context.Canceled) || r.Status != Canceled || r.Groups[0].Completed != want || r.Groups[0].TotalBet != uint64(want) {
				t.Fatal(r, e)
			}
		})
	}
	for _, typed := range []bool{false, true} {
		c, o, m := fixture(1)
		register(t, &o, factoryFunc(func(int) Strategy {
			if typed {
				var p *strategyFuncs
				return p
			}
			return nil
		}))
		if r, e := runMachine(context.Background(), c, o, m, m.units); e == nil || r.Groups[0].Completed != 0 || m.spins != 0 {
			t.Fatal(r, e)
		}
	}
}

func TestPlayerStreamingMemory(t *testing.T) {
	var reports []Report
	for _, progress := range []bool{false, true} {
		c, o, m := fixture(100000)
		register(t, &o, fixedFactory(10))
		if progress {
			o.Reporter = reporterFunc(func(ProgressEvent) {})
		}
		runtime.GC()
		var a, b runtime.MemStats
		runtime.ReadMemStats(&a)
		start := time.Now()
		r, e := runMachine(context.Background(), c, o, m, m.units)
		runtime.ReadMemStats(&b)
		if e != nil {
			t.Fatal(e)
		}
		reports = append(reports, r)
		t.Logf("progress=%v players=100000 spins=%d elapsed=%s allocated=%d allocations=%d heap_delta=%d", progress, m.spins, time.Since(start), b.TotalAlloc-a.TotalAlloc, b.Mallocs-a.Mallocs, int64(b.HeapAlloc)-int64(a.HeapAlloc))
		if m.spins != 1000000 {
			t.Fatal(m.spins)
		}
	}
	if !reflect.DeepEqual(reports[0], reports[1]) {
		t.Fatal("progress changed results")
	}
}

func TestPlayerMemoryRetentionBound(t *testing.T) {
	for _, size := range [][2]int{{1000, 1000}, {100000, 10}} {
		c, o, m := fixture(size[0])
		register(t, &o, fixedFactory(size[1]))
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		var peak uint64
		o.Reporter = reporterFunc(func(e ProgressEvent) {
			if e.Kind == "progress" && e.Completed%(size[0]/10) == 0 {
				runtime.GC()
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				if ms.HeapAlloc > before.HeapAlloc {
					peak = max(peak, ms.HeapAlloc-before.HeapAlloc)
				}
			}
		})
		r, e := runMachine(context.Background(), c, o, m, m.units)
		if e != nil {
			t.Fatal(e)
		}
		runtime.KeepAlive(r)
		// One million results retained would exceed this by a wide margin.
		// The larger-player case needs only O(players) scalar RTP storage.
		if peak > 16<<20 {
			t.Fatalf("retained heap too large: %d", peak)
		}
		t.Logf("players=%d spins/player=%d sampled_live_heap_growth=%d", size[0], size[1], peak)
	}
}
