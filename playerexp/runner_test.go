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
	"fmt"
	"math"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/strategy"
	"github.com/zintix-labs/problab/sdk/tag"
	"github.com/zintix-labs/problab/spec"
)

// The strategy contract moved to sdk/strategy; these test-only aliases keep the
// executor tests in this package readable.
type (
	Strategy        = strategy.Strategy
	StrategyFactory = strategy.Factory
	PlayerState     = strategy.PlayerState
	Decision        = strategy.Decision
	Bet             = strategy.Bet
	EndType         = strategy.EndType
)

const (
	Other        = strategy.Other
	Bust         = strategy.Bust
	Satisfied    = strategy.Satisfied
	Dissatisfied = strategy.Dissatisfied
)

var NewStrategyRegistry = strategy.NewRegistry

type factoryFunc func(int) Strategy

func (f factoryFunc) NewStrategy(n int) Strategy { return f(n) }

type strategyFuncs struct {
	first func(PlayerState) Decision
	next  func(PlayerState, *buf.SpinResult) Decision
}

func (s *strategyFuncs) FirstSpin(p PlayerState) Decision               { return s.first(p) }
func (s *strategyFuncs) Next(p PlayerState, r *buf.SpinResult) Decision { return s.next(p, r) }

type fakeMachine struct {
	units  []int
	spins  int
	fn     func(int, int, int) *buf.SpinResult
	runner *fakeRunner
}

func (m *fakeMachine) UsesOptimal() bool { return false }

func (m *fakeMachine) newRunner(maxSegments int) (gameRunner, error) {
	m.runner = &fakeRunner{m: m, max: maxSegments}
	return m.runner, nil
}

// fakeRunner plays each segment with fakeMachine.SpinUnsafe and mimics the
// BetRunner contract the executor relies on: a nil game result is
// ErrGameRejected, an unfinished segment stays pending until Next, Bet refuses
// to start while a game is pending, and the segment limit holds.
type fakeRunner struct {
	m          *fakeMachine
	max, segs  int
	mode, mult int
	pending    bool
	choices    []int
}

func (r *fakeRunner) Reset() { r.pending = false }
func (r *fakeRunner) Bet(in problab.SpinInput, _ problab.ChooseMode) (*buf.SpinResult, error) {
	if r.pending {
		return nil, problab.ErrBetInProgress
	}
	r.mode, r.mult, r.segs = in.BetMode, in.BetMult, 0
	return r.segment()
}
func (r *fakeRunner) Next(choice int) (*buf.SpinResult, error) {
	r.choices = append(r.choices, choice)
	if r.segs >= r.max {
		return nil, problab.ErrSegmentLimit
	}
	return r.segment()
}
func (r *fakeRunner) segment() (*buf.SpinResult, error) {
	sr := r.m.SpinUnsafe(problab.SpinInput{BetMode: r.mode, BetMult: r.mult})
	if sr == nil {
		return nil, problab.ErrGameRejected
	}
	sr.Cycle = r.segs
	r.segs++
	r.pending = !sr.IsGameEnd
	return sr, nil
}

// withMode 替假結果補上一個贏分等於總贏分的 GameModeResult，符合「非 nil 的合法結果至少
// 包含一個 GameModeResult」的約定；刻意不合法的假結果也用它，讓每筆只違反一個前提。
func withMode(sr *buf.SpinResult) *buf.SpinResult {
	sr.GameModeList = []*buf.GameModeResult{{TotalWin: sr.TotalWin}}
	sr.GameModeCount = 1
	return sr
}
func (m *fakeMachine) SpinUnsafe(in problab.SpinInput) *buf.SpinResult {
	m.spins++
	mode, mult := in.BetMode, in.BetMult
	if m.fn != nil {
		return m.fn(mode, mult, m.spins)
	}
	return withMode(&buf.SpinResult{Bet: m.units[mode] * mult, BetMode: mode, BetMult: mult, TotalWin: m.units[mode] * mult, IsGameEnd: true})
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
		return &strategyFuncs{first: func(PlayerState) Decision { return Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 1}} }, next: func(p PlayerState, _ *buf.SpinResult) Decision {
			if p.Spins >= spins {
				return Decision{EndType: Satisfied}
			}
			return Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 1}}
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
		defs := map[string]tag.IsTag{}
		names := make([]string, n)
		for i := range names {
			names[i] = fmt.Sprintf("t%02d", i)
			defs[names[i]] = func(*buf.SpinResult) bool { return true }
		}
		tagger, err := tag.GameTagCatalog{0: defs}.CompileBy(0, names...)
		if err != nil {
			t.Fatal(err)
		}
		cg.tagger = tagger
		if cg.events[0].want, err = tagger.Want(names...); err != nil {
			t.Fatal(err)
		}
		runner, _ := m.newRunner(problab.DefaultMaxSegments)
		return testing.AllocsPerRun(100, func() {
			if _, err := play(context.Background(), runner, m.units, cg, c.Analysis[0], 0); err != nil {
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
			return Decision{KeepPlay: true, Bet: Bet{BetMode: 1, BetMult: 1}}
		}, next: func(p PlayerState, _ *buf.SpinResult) Decision {
			seen = append(seen, p)
			if p.Spins == 1 {
				return Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 2}}
			}
			return Decision{EndType: Dissatisfied}
		}}
	}))
	m.fn = func(mode, mult, n int) *buf.SpinResult {
		win := 30
		if n == 2 {
			win = 4
		}
		return withMode(&buf.SpinResult{Bet: m.units[mode] * mult, BetMode: mode, BetMult: mult, TotalWin: win, IsGameEnd: true})
	}
	r, err := runMachine(context.Background(), c, o, m, m.units)
	if err != nil {
		t.Fatal(err)
	}
	want := []PlayerState{{Balance: 500}, {Balance: 430, Spins: 1, TotalBet: 100, TotalWin: 30}, {Balance: 432, Spins: 2, TotalBet: 102, TotalWin: 34}}
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
			return &strategyFuncs{first: func(PlayerState) Decision {
				return Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 1}, EndType: EndType(255)}
			}, next: func(p PlayerState, _ *buf.SpinResult) Decision {
				calls++
				if p.Spins == MaxSpinsPerPlayer {
					if leave {
						return Decision{EndType: Dissatisfied, Bet: Bet{BetMode: -1, BetMult: -1}}
					}
					return Decision{KeepPlay: true, Bet: Bet{BetMode: -1, BetMult: -1}}
				}
				return Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 1}}
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
		{"zero", Decision{}, false, false}, {"ignoredBet", Decision{Bet: Bet{BetMode: -1, BetMult: -1}}, false, false},
		{"strategyBust", Decision{EndType: Bust}, true, false}, {"unknown", Decision{EndType: 255}, true, false},
		{"mode", Decision{KeepPlay: true, Bet: Bet{BetMode: -1, BetMult: 1}}, true, false}, {"mult", Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 0}}, true, false},
		{"overflow", Decision{KeepPlay: true, Bet: Bet{BetMode: 1, BetMult: math.MaxInt}}, true, false}, {"unaffordable", Decision{KeepPlay: true, Bet: Bet{BetMode: 1, BetMult: 6}}, false, true},
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
	// A checkpoint is carried between segments by the runner; playerexp no
	// longer refuses a finished game that has one.
	var ptr *int
	for i, cp := range []any{nil, ptr, struct{}{}, map[string]int{}, []int{}, "null"} {
		c, o, m := fixture(1)
		register(t, &o, fixedFactory(1))
		m.fn = func(mode, mult, n int) *buf.SpinResult {
			return withMode(&buf.SpinResult{Bet: 1, BetMult: 1, IsGameEnd: true, State: &buf.SpinState{Checkpoint: cp}})
		}
		r, e := runMachine(context.Background(), c, o, m, m.units)
		if e != nil || r.Groups[0].Completed != 1 {
			t.Fatalf("%d %v", i, e)
		}
	}
	// nil 由 runner 拒絕；未結束而策略沒有選擇；其餘各只違反一個前提：投注錯誤、負贏分。
	for _, r := range []*buf.SpinResult{nil, withMode(&buf.SpinResult{Bet: 1, BetMult: 1}), withMode(&buf.SpinResult{Bet: 2, BetMult: 1, IsGameEnd: true}), withMode(&buf.SpinResult{Bet: 1, BetMult: 1, IsGameEnd: true, TotalWin: -1})} {
		c, o, m := fixture(1)
		register(t, &o, fixedFactory(1))
		m.fn = func(int, int, int) *buf.SpinResult { return r }
		if _, e := runMachine(context.Background(), c, o, m, m.units); e == nil {
			t.Fatal("accepted invalid result")
		}
	}
}

// A nil game result is rejected by the runner; the session reports
// ErrGameRejected, labelled with the Bet operation.
func TestNilGameResultIsRejected(t *testing.T) {
	c, o, m := fixture(1)
	register(t, &o, fixedFactory(1))
	m.fn = func(int, int, int) *buf.SpinResult { return nil }
	_, e := runMachine(context.Background(), c, o, m, m.units)
	if !errors.Is(e, problab.ErrGameRejected) || !strings.Contains(e.Error(), "spin 0 Bet:") {
		t.Fatalf("error=%v", e)
	}
}

// An unfinished game stops at a choice point; a strategy that makes no choice
// there is reported with the cycle and ErrInvalidDecision.
func TestUnfinishedGameNeedsAChoice(t *testing.T) {
	c, o, m := fixture(1)
	register(t, &o, fixedFactory(1))
	m.fn = func(mode, mult, n int) *buf.SpinResult {
		return withMode(&buf.SpinResult{Bet: m.units[mode] * mult, BetMode: mode, BetMult: mult, TotalWin: 1, ChooseNext: []int{3}})
	}
	_, e := runMachine(context.Background(), c, o, m, m.units)
	if !errors.Is(e, ErrInvalidDecision) || !strings.Contains(e.Error(), "spin 0 cycle 0 Next: choice required at a choice point") {
		t.Fatalf("error=%v", e)
	}
}

// Every player resets the runner: a game left pending does not reach the next
// player's first wager.
func TestPlayResetsRunnerForEachPlayer(t *testing.T) {
	c, _, m := fixture(1)
	cg := compiledGroup{factory: fixedFactory(2), initial: 500,
		events: []compiledEvent{{lo: 0, hi: len(c.Analysis[0].WinBuckets) + 1}}}
	runner, _ := m.newRunner(problab.DefaultMaxSegments)
	m.runner.pending = true
	s, err := play(context.Background(), runner, m.units, cg, c.Analysis[0], 0)
	if err != nil {
		t.Fatalf("the next player saw the pending game: %v", err)
	}
	if s.state.Spins != 2 {
		t.Fatalf("spins=%d, want 2", s.state.Spins)
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
					return Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 1}}
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
			m.fn = func(mode, mult, n int) *buf.SpinResult {
				if created == 2 && operation == "game" {
					panic("game failure")
				}
				return withMode(&buf.SpinResult{Bet: 1, BetMult: 1, IsGameEnd: true})
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
			// Tags are evaluated as one batch per round, so a panic is located at
			// the batch, not at an event (an accepted change of batch evaluation).
			if operation == "tag" && !strings.Contains(e.Error(), "spin 1 tags: panic: tag failure") {
				t.Fatalf("missing batch tag diagnostic: %v", e)
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

// TestBatchTagsANDAndUnmatched: every referenced tag is evaluated once per
// round in first-occurrence order (no short circuit), then events are matched
// in order with AND semantics; unmatched rounds are still counted.
func TestBatchTagsANDAndUnmatched(t *testing.T) {
	c, o, m := fixture(1)
	register(t, &o, fixedFactory(1))
	c.Analysis[0].Events = []EventConfig{{Name: "first", Tags: []string{"a", "b", "late"}}, {Name: "second", Tags: []string{"c"}}}
	var calls []string
	o.Tags = map[spec.GID]map[string]tag.IsTag{0: {
		"a":    func(*buf.SpinResult) bool { calls = append(calls, "a"); return true },
		"b":    func(*buf.SpinResult) bool { calls = append(calls, "b"); return false },
		"late": func(*buf.SpinResult) bool { calls = append(calls, "late"); return true },
		"c":    func(*buf.SpinResult) bool { calls = append(calls, "c"); return false },
	}}
	r, e := runMachine(context.Background(), c, o, m, m.units)
	if e != nil || !reflect.DeepEqual(calls, []string{"a", "b", "late", "c"}) || r.Groups[0].Unmatched != 1 || r.Groups[0].Events[0].Counts[0] != 1 {
		t.Fatal(r, calls, e)
	}
}

// TestTagsEvaluatedOncePerRound: a duplicated tag is evaluated once, and tags
// are evaluated even when no event's win range matches the round.
func TestTagsEvaluatedOncePerRound(t *testing.T) {
	c, o, m := fixture(1)
	register(t, &o, fixedFactory(3))
	c.Analysis[0].Events = []EventConfig{{Name: "never", WinRange: WinRange{Min: 0, Max: new(float64)}, Tags: []string{"x", "x"}}}
	calls := 0
	o.Tags = map[spec.GID]map[string]tag.IsTag{0: {"x": func(*buf.SpinResult) bool { calls++; return true }}}
	r, e := runMachine(context.Background(), c, o, m, m.units)
	if e != nil || calls != 3 || r.Groups[0].Events[0].Counts[0] != 1 { // the only player hit the event zero times
		t.Fatal(r, calls, e)
	}
}

// TestEmptyTagNameIsRejected: the catalog never resolves an empty name, even
// when a non-nil predicate is registered under it (an accepted change).
func TestEmptyTagNameIsRejected(t *testing.T) {
	c, o, m := fixture(1)
	register(t, &o, fixedFactory(1))
	c.Analysis[0].Events = []EventConfig{{Name: "empty", Tags: []string{""}}}
	o.Tags = map[spec.GID]map[string]tag.IsTag{0: {"": func(*buf.SpinResult) bool { return true }}}
	if _, e := runMachine(context.Background(), c, o, m, m.units); e == nil || !strings.Contains(e.Error(), `event "empty": tag "" not registered`) {
		t.Fatal(e)
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
					return Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 1}}
				}, next: func(PlayerState, *buf.SpinResult) Decision {
					if where == "next" {
						cancel()
					}
					return Decision{}
				}}
			}))
			m.fn = func(int, int, int) *buf.SpinResult {
				if where == "spin" {
					cancel()
				}
				return withMode(&buf.SpinResult{IsGameEnd: true, Bet: 1, BetMult: 1})
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

// choiceGame stops the first segment of every game at a choice point with the
// given options; the next segment ends the game with a win of 3.
func choiceGame(m *fakeMachine, options ...int) {
	m.fn = func(mode, mult, n int) *buf.SpinResult {
		cost := m.units[mode] * mult
		if m.runner.segs == 0 {
			return withMode(&buf.SpinResult{Bet: cost, BetMode: mode, BetMult: mult, TotalWin: 1, ChooseNext: options})
		}
		return withMode(&buf.SpinResult{Bet: cost, BetMode: mode, BetMult: mult, TotalWin: 3, IsGameEnd: true})
	}
}

// choosing plays games of the fixture with a fixed wager and answers every
// choice point with at; seen records each Next call.
func choosing(games int, at func(PlayerState, *buf.SpinResult) Decision, seen *[]string) factoryFunc {
	return func(int) Strategy {
		return &strategyFuncs{first: func(PlayerState) Decision { return Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 1}} },
			next: func(p PlayerState, r *buf.SpinResult) Decision {
				if seen != nil {
					*seen = append(*seen, fmt.Sprintf("end=%v %+v win=%d", r.IsGameEnd, p, r.TotalWin))
				}
				if !r.IsGameEnd {
					return at(p, r)
				}
				if p.Spins >= games {
					return Decision{EndType: Satisfied}
				}
				return Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 1}}
			}}
	}
}

// A choice point receives the segment and the paid-but-unsettled ledger; the
// game is settled once, and the whole game reaches Next afterwards.
func TestChoicePointLedgerAndSettlement(t *testing.T) {
	c, o, m := fixture(1)
	choiceGame(m, 3, 7)
	var seen []string
	register(t, &o, choosing(2, func(PlayerState, *buf.SpinResult) Decision { return strategy.ChoiceAt(1) }, &seen))
	r, e := runMachine(context.Background(), c, o, m, m.units)
	if e != nil {
		t.Fatal(e)
	}
	want := []string{
		"end=false {Balance:499 Spins:0 TotalBet:1 TotalWin:0} win=1",
		"end=true {Balance:502 Spins:1 TotalBet:1 TotalWin:3} win=3",
		"end=false {Balance:501 Spins:1 TotalBet:2 TotalWin:3} win=1",
		"end=true {Balance:504 Spins:2 TotalBet:2 TotalWin:6} win=3",
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("calls\n%v\nwant\n%v", seen, want)
	}
	if !reflect.DeepEqual(m.runner.choices, []int{1, 1}) || r.Groups[0].CompleteSpins != 2 || r.Groups[0].TotalBet != 2 || r.Groups[0].TotalWin != 6 {
		t.Fatalf("choices %v group %+v", m.runner.choices, r.Groups[0])
	}
}

// A paid game is finished even when the balance left is zero; Bust is only
// decided before the next wager.
func TestNoBustInsideAGame(t *testing.T) {
	c, o, m := fixture(1)
	c.Analysis[0].Player.InitBet = 1
	m.units = []int{500, 100}
	choiceGame(m, 3)
	m.fn = func(mode, mult, n int) *buf.SpinResult {
		if m.runner.segs == 0 {
			return withMode(&buf.SpinResult{Bet: 500, BetMode: mode, BetMult: mult, ChooseNext: []int{3}})
		}
		return withMode(&buf.SpinResult{Bet: 500, BetMode: mode, BetMult: mult, IsGameEnd: true})
	}
	var seen []string
	register(t, &o, choosing(5, func(PlayerState, *buf.SpinResult) Decision { return strategy.ChoiceAt(0) }, &seen))
	r, e := runMachine(context.Background(), c, o, m, m.units)
	if e != nil || len(seen) != 2 || seen[0] != "end=false {Balance:0 Spins:0 TotalBet:500 TotalWin:0} win=0" || r.Groups[0].Exits[0] != 1 {
		t.Fatalf("%v %v %v", e, seen, r.Groups[0].Exits)
	}
}

// Each kind of invalid decision is a strategy error with a cause errors.Is can
// recognise; none is taken as a player exit.
func TestInvalidDecisions(t *testing.T) {
	two := 2
	for name, c := range map[string]struct {
		at     func(PlayerState, *buf.SpinResult) Decision
		first  Decision
		want   string
		target error
	}{
		"no choice":        {func(PlayerState, *buf.SpinResult) Decision { return Decision{} }, Decision{}, "cycle 0 Next: choice required at a choice point", ErrInvalidDecision},
		"wager at a point": {func(PlayerState, *buf.SpinResult) Decision { d := strategy.ChoiceAt(0); d.KeepPlay = true; return d }, Decision{}, "cycle 0 Next: only Choice may be set", ErrInvalidDecision},
		"exit at a point": {func(PlayerState, *buf.SpinResult) Decision {
			d := strategy.ChoiceAt(0)
			d.EndType = Satisfied
			return d
		}, Decision{}, "only Choice may be set", ErrInvalidDecision},
		"out of range":    {func(PlayerState, *buf.SpinResult) Decision { return strategy.ChoiceAt(2) }, Decision{}, "cycle 0 Next: choice 2 not in [0, 2)", problab.ErrChoiceOutOfRange},
		"negative":        {func(PlayerState, *buf.SpinResult) Decision { return strategy.ChoiceAt(-1) }, Decision{}, "choice -1 not in [0, 2)", problab.ErrChoiceOutOfRange},
		"choice at start": {nil, Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 1}, Choice: &two}, "spin 0 decision: choice outside a choice point", ErrInvalidDecision},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, o, m := fixture(1)
			choiceGame(m, 3, 7)
			f := choosing(1, c.at, nil)
			if c.at == nil {
				f = func(int) Strategy {
					return &strategyFuncs{first: func(PlayerState) Decision { return c.first }, next: func(PlayerState, *buf.SpinResult) Decision { return Decision{} }}
				}
			}
			register(t, &o, f)
			r, e := runMachine(context.Background(), cfg, o, m, m.units)
			if !errors.Is(e, c.target) || !strings.Contains(e.Error(), c.want) || r.Groups[0].Completed != 0 {
				t.Fatalf("error %v", e)
			}
		})
	}
}

// Errors from the runner keep their cause: a game rejected after a choice and
// the segment limit.
func TestRunnerErrorsKeepTheirCause(t *testing.T) {
	cfg, o, m := fixture(1)
	choiceGame(m, 3, 7)
	m.fn = func(mode, mult, n int) *buf.SpinResult {
		if m.runner.segs == 0 {
			return withMode(&buf.SpinResult{Bet: 1, BetMode: mode, BetMult: mult, ChooseNext: []int{3}})
		}
		return nil
	}
	register(t, &o, choosing(1, func(PlayerState, *buf.SpinResult) Decision { return strategy.ChoiceAt(0) }, nil))
	if _, e := runMachine(context.Background(), cfg, o, m, m.units); !errors.Is(e, problab.ErrGameRejected) || !strings.Contains(e.Error(), "cycle 1 Choose") {
		t.Fatalf("rejected: %v", e)
	}

	cfg, o, m = fixture(1)
	cfg.Analysis[0].Player.MaxSegments = 1
	choiceGame(m, 3, 7)
	register(t, &o, choosing(1, func(PlayerState, *buf.SpinResult) Decision { return strategy.ChoiceAt(0) }, nil))
	if _, e := runMachine(context.Background(), cfg, o, m, m.units); !errors.Is(e, problab.ErrSegmentLimit) {
		t.Fatalf("limit: %v", e)
	}
}

// The order of decisions at a game boundary is unchanged: at the spin limit an
// invalid wager still ends the player as capped, not as a strategy error.
func TestSpinLimitBeforeWagerValidation(t *testing.T) {
	c, o, m := fixture(1)
	register(t, &o, factoryFunc(func(int) Strategy {
		return &strategyFuncs{first: func(PlayerState) Decision { return Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 1}} },
			next: func(p PlayerState, _ *buf.SpinResult) Decision {
				if p.Spins == MaxSpinsPerPlayer {
					return Decision{KeepPlay: true, Bet: Bet{BetMode: 9, BetMult: -1}}
				}
				return Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 1}}
			}}
	}))
	r, e := runMachine(context.Background(), c, o, m, m.units)
	if e != nil || r.Groups[0].MaxSpinReached != 1 {
		t.Fatalf("%v capped %d", e, r.Groups[0].MaxSpinReached)
	}
}

// Cancelling inside Next, at a choice point or after a whole game, leaves the
// current player uncommitted and keeps the players already committed.
func TestCancelInsideNext(t *testing.T) {
	for _, atChoice := range []bool{true, false} {
		c, o, m := fixture(2)
		choiceGame(m, 3, 7)
		ctx, cancel := context.WithCancel(context.Background())
		players := 0
		register(t, &o, factoryFunc(func(int) Strategy {
			players++
			me := players
			return &strategyFuncs{first: func(PlayerState) Decision { return Decision{KeepPlay: true, Bet: Bet{BetMode: 0, BetMult: 1}} },
				next: func(p PlayerState, r *buf.SpinResult) Decision {
					if me == 2 && r.IsGameEnd != atChoice {
						cancel()
					}
					if !r.IsGameEnd {
						return strategy.ChoiceAt(0)
					}
					return Decision{EndType: Satisfied}
				}}
		}))
		r, e := runMachine(ctx, c, o, m, m.units)
		if !errors.Is(e, context.Canceled) || r.Groups[0].Completed != 1 || r.Groups[0].Started != 2 {
			t.Fatalf("at choice %v: %v completed %d started %d", atChoice, e, r.Groups[0].Completed, r.Groups[0].Started)
		}
	}
}
