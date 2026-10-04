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

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/tag"
)

type machine interface {
	SpinOffline(int, int) (*buf.SpinResult, error)
	UsesOptimal() bool
}
type compiledEvent struct {
	lo, hi int
	tags   []tag.IsTag
}
type compiledGroup struct {
	factory StrategyFactory
	initial int
	events  []compiledEvent
}
type session struct {
	state     PlayerState
	buckets   []BucketStat
	hits      []uint64
	unmatched uint64
	end       EndType
	capped    bool
}

// Run executes sequential complete-result sessions. It borrows Lab and never
// closes it. The returned report retains committed players even on error.
func Run(ctx context.Context, c Config, o Options) (Report, error) {
	var err error
	c, err = normalizeConfig(c)
	if err != nil {
		return Report{Status: Failed, Error: err.Error()}, err
	}
	if ctx == nil {
		err = fmt.Errorf("nil context")
		return initialReport(c, Failed, err), err
	}
	if o.Lab == nil {
		err = fmt.Errorf("Lab is required")
		return initialReport(c, Failed, err), err
	}
	if err = ctx.Err(); err != nil {
		return initialReport(c, Canceled, err), err
	}
	m, err := constructMachine(c, o)
	if err != nil {
		return initialReport(c, Failed, err), err
	}
	return runMachine(ctx, c, o, m, append([]int(nil), m.BetUnits...))
}

// Construction happens before any player starts. Game builders and third-party
// PRNG factories are application callbacks too; do not let a panic bypass the
// partial-report/error contract.
func constructMachine(c Config, o Options) (m *problab.Machine, err error) {
	defer func() {
		if p := recover(); p != nil {
			if cause, ok := p.(error); ok {
				err = fmt.Errorf("game %d construction panic: %w", c.GameID, cause)
			} else {
				err = fmt.Errorf("game %d construction panic: %v", c.GameID, p)
			}
		}
	}()
	return o.Lab.NewMachineWithSeedBytes(c.GameID, c.Seed.Bytes(), false)
}

func initialReport(c Config, status Status, err error) Report {
	r := Report{Config: c, Status: status, Groups: make([]GroupReport, len(c.Analysis))}
	if err != nil {
		r.Error = err.Error()
	}
	for i, g := range c.Analysis {
		r.Groups[i] = GroupReport{Config: g, Status: NotStarted, Buckets: bucketStats(g.WinBuckets), Events: make([]EventStat, len(g.Events))}
	}
	return r
}
func compile(c Config, o Options, units []int) ([]compiledGroup, error) {
	if len(units) == 0 {
		return nil, fmt.Errorf("game has no bet units")
	}
	for _, u := range units {
		if u <= 0 {
			return nil, fmt.Errorf("game has invalid bet unit")
		}
	}
	out := make([]compiledGroup, len(c.Analysis))
	for i, g := range c.Analysis {
		f, ok := o.Strategies.Get(c.GameID, g.Player.Strategy)
		if !ok || nilLike(f) {
			return nil, fmt.Errorf("group %q: strategy %q not registered for game %d", g.Name, g.Player.Strategy, c.GameID)
		}
		initial, err := multiply(g.Player.InitBet, units[0])
		if err != nil {
			return nil, fmt.Errorf("group %q initial balance: %w", g.Name, err)
		}
		cg := compiledGroup{factory: f, initial: initial}
		for _, e := range g.Events {
			lo, hi, err := rangeBuckets(g.WinBuckets, e.WinRange)
			if err != nil {
				return nil, err
			}
			ce := compiledEvent{lo: lo, hi: hi}
			for _, name := range e.Tags {
				fn := o.Tags[c.GameID][name]
				if fn == nil {
					return nil, fmt.Errorf("group %q event %q: tag %q not registered; provide Options.Tags", g.Name, e.Name, name)
				}
				ce.tags = append(ce.tags, fn)
			}
			cg.events = append(cg.events, ce)
		}
		out[i] = cg
	}
	return out, nil
}

func runMachine(ctx context.Context, c Config, o Options, m machine, units []int) (r Report, err error) {
	c, err = normalizeConfig(c)
	if err != nil {
		return Report{Status: Failed, Error: err.Error()}, err
	}
	r = initialReport(c, Running, nil)
	groups, e := compile(c, o, units)
	if e != nil {
		r.Status = Failed
		r.Error = e.Error()
		return r, e
	}
	for _, u := range units {
		source := "native"
		if m.UsesOptimal() {
			source = "optimal"
		}
		r.Modes = append(r.Modes, ModeSource{u, source})
	}
	for i, cg := range groups {
		r.Groups[i].InitialBalance = cg.initial
	}
	for i, cg := range groups {
		g := &r.Groups[i]
		g.InitialBalance = cg.initial
		if err = ctx.Err(); err != nil {
			r.Status = Canceled
			r.Error = err.Error()
			return r, err
		}
		g.Status = Running
		err = emit(o.Reporter, ProgressEvent{"start", i, g.Config.Name, 0, g.Config.Player.Players})
		for p := 0; err == nil && p < g.Config.Player.Players; p++ {
			if err = ctx.Err(); err != nil {
				break
			}
			g.Started++
			var s session
			s, err = play(ctx, m, units, cg, g.Config, p)
			if err != nil {
				break
			}
			if err = ctx.Err(); err != nil {
				break
			}
			if err = commit(g, s); err != nil {
				err = fmt.Errorf("group %q player %d commit: %w", g.Config.Name, p, err)
				break
			}
			err = emit(o.Reporter, ProgressEvent{"progress", i, g.Config.Name, g.Completed, g.Config.Player.Players})
			if err == nil {
				err = ctx.Err()
			}
		}
		g.Status = Completed
		kind := "completed"
		if err != nil {
			g.Status = Failed
			kind = "failed"
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				g.Status = Canceled
				kind = "canceled"
			}
			g.Error = err.Error()
		}
		summarize(g)
		terminalErr := emit(o.Reporter, ProgressEvent{kind, i, g.Config.Name, g.Completed, g.Config.Player.Players})
		if terminalErr != nil {
			err = errors.Join(err, terminalErr)
			g.Status = Failed
			g.Error = err.Error()
		}
		if err != nil {
			r.Status = g.Status
			r.Error = err.Error()
			return r, err
		}
	}
	r.Status = Completed
	return r, nil
}
func emit(r Reporter, e ProgressEvent) (err error) {
	if nilLike(r) {
		return nil
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("group %q reporter %s panic: %v", e.GroupName, e.Kind, p)
		}
	}()
	r.Report(e)
	return nil
}
func play(ctx context.Context, m machine, units []int, cg compiledGroup, g GroupConfig, player int) (s session, err error) {
	op := "factory"
	eventIndex, tagIndex := 0, 0
	defer func() {
		if p := recover(); p != nil {
			if cause, ok := p.(error); ok {
				err = fmt.Errorf("panic: %w", cause)
			} else {
				err = fmt.Errorf("panic: %v", p)
			}
		}
		if err != nil {
			if op == "tag" {
				op = fmt.Sprintf("event %q tag %d", g.Events[eventIndex].Name, tagIndex)
			}
			err = fmt.Errorf("group %q player %d spin %d %s: %w", g.Name, player, s.state.Spins, op, err)
		}
	}()
	s = session{state: PlayerState{Balance: cg.initial}, buckets: bucketStats(g.WinBuckets), hits: make([]uint64, len(cg.events))}
	if err = ctx.Err(); err != nil {
		return s, err
	}
	strategy := cg.factory.NewStrategy(cg.initial)
	if nilLike(strategy) {
		return s, fmt.Errorf("factory returned nil strategy")
	}
	if err = ctx.Err(); err != nil {
		return s, err
	}
	op = "FirstSpin"
	decision := strategy.FirstSpin(s.state)
	for {
		if err = ctx.Err(); err != nil {
			return s, err
		}
		op = "decision"
		if !decision.KeepPlay {
			if decision.EndType != Other && decision.EndType != Satisfied && decision.EndType != Dissatisfied {
				return s, fmt.Errorf("invalid strategy exit %d", decision.EndType)
			}
			s.end = decision.EndType
			return s, nil
		}
		if s.state.Spins == MaxSpinsPerPlayer {
			s.end = Other
			s.capped = true
			return s, nil
		}
		b := decision.Bet
		if b.BetMode < 0 || b.BetMode >= len(units) {
			return s, fmt.Errorf("invalid bet mode %d", b.BetMode)
		}
		cost, e := multiply(units[b.BetMode], b.BetMult)
		if e != nil {
			return s, e
		}
		if s.state.Balance < cost {
			s.end = Bust
			return s, nil
		}
		op = "SpinOffline"
		if err = ctx.Err(); err != nil {
			return s, err
		}
		result, e := m.SpinOffline(b.BetMode, b.BetMult)
		if e != nil {
			return s, e
		}
		if err = ctx.Err(); err != nil {
			return s, err
		}
		if result == nil || !result.IsGameEnd {
			return s, fmt.Errorf("requires complete spin result")
		}
		if result.State != nil && !emptyCheckpoint(result.State.Checkpoint) {
			return s, fmt.Errorf("checkpoint games are not supported")
		}
		if result.TotalWin < 0 || result.Bet != cost || result.BetMode != b.BetMode || result.BetMult != b.BetMult {
			return s, fmt.Errorf("invalid result accounting")
		}
		op = "settlement"
		balance, e := add(s.state.Balance-cost, result.TotalWin)
		if e != nil {
			return s, e
		}
		tb, e := add(s.state.TotalBet, cost)
		if e != nil {
			return s, e
		}
		tw, e := add(s.state.TotalWin, result.TotalWin)
		if e != nil {
			return s, e
		}
		s.state = PlayerState{balance, s.state.Spins + 1, tb, tw}
		x := float64(result.TotalWin) / float64(cost)
		bi := bucketIndex(g.WinBuckets, x)
		s.buckets[bi].Count++
		if !s.buckets[bi].ObservedMax.Defined || x > s.buckets[bi].ObservedMax.Value {
			s.buckets[bi].ObservedMax = Estimate{x, true}
		}
		matched := false
		for j, ev := range cg.events {
			if bi < ev.lo || bi >= ev.hi {
				continue
			}
			ok := true
			for k, fn := range ev.tags {
				op = "tag"
				eventIndex, tagIndex = j, k
				if err = ctx.Err(); err != nil {
					return s, err
				}
				v := fn(result)
				if err = ctx.Err(); err != nil {
					return s, err
				}
				if !v {
					ok = false
					break
				}
			}
			if ok {
				s.hits[j]++
				matched = true
				break
			}
		}
		if !matched {
			s.unmatched++
		}
		if err = ctx.Err(); err != nil {
			return s, err
		}
		op = "Next"
		decision = strategy.Next(s.state, result)
	}
}
func emptyCheckpoint(v any) bool {
	return v == nil || (reflect.ValueOf(v).Kind() == reflect.Pointer && reflect.ValueOf(v).IsNil())
}
func multiply(a, b int) (int, error) {
	if a <= 0 || b <= 0 || a > int(^uint(0)>>1)/b {
		return 0, fmt.Errorf("invalid or overflowing wager")
	}
	return a * b, nil
}
func add(a, b int) (int, error) {
	if a < 0 || b < 0 || a > int(^uint(0)>>1)-b {
		return 0, fmt.Errorf("integer accounting overflow")
	}
	return a + b, nil
}
func fits(a, b uint64) bool { return a <= math.MaxUint64-b }
func exitIndex(e EndType) int {
	switch e {
	case Bust:
		return 0
	case Satisfied:
		return 1
	case Dissatisfied:
		return 2
	default:
		return 3
	}
}
func commit(g *GroupReport, s session) error {
	pairs := [][2]uint64{{g.CompleteSpins, uint64(s.state.Spins)}, {g.TotalBet, uint64(s.state.TotalBet)}, {g.TotalWin, uint64(s.state.TotalWin)}, {g.Unmatched, s.unmatched}, {g.Exits[exitIndex(s.end)], 1}}
	if s.capped {
		pairs = append(pairs, [2]uint64{g.MaxSpinReached, 1})
	}
	if s.state.TotalBet == 0 {
		pairs = append(pairs, [2]uint64{g.UndefinedRTPPlayers, 1})
	}
	for i, b := range s.buckets {
		pairs = append(pairs, [2]uint64{g.Buckets[i].Count, b.Count})
	}
	for i, h := range s.hits {
		f := min(h, 3)
		pairs = append(pairs, [2]uint64{g.Events[i].Hits, h}, [2]uint64{g.Events[i].Counts[f], 1})
	}
	for _, p := range pairs {
		if !fits(p[0], p[1]) {
			return fmt.Errorf("group accounting overflow")
		}
	}
	g.CompleteSpins += uint64(s.state.Spins)
	g.TotalBet += uint64(s.state.TotalBet)
	g.TotalWin += uint64(s.state.TotalWin)
	g.Unmatched += s.unmatched
	g.Exits[exitIndex(s.end)]++
	if s.capped {
		g.MaxSpinReached++
	}
	for i, b := range s.buckets {
		g.Buckets[i].Count += b.Count
		if b.ObservedMax.Defined && (!g.Buckets[i].ObservedMax.Defined || b.ObservedMax.Value > g.Buckets[i].ObservedMax.Value) {
			g.Buckets[i].ObservedMax = b.ObservedMax
		}
	}
	for i, h := range s.hits {
		g.Events[i].Hits += h
		g.Events[i].Counts[min(h, 3)]++
	}
	if s.state.TotalBet == 0 {
		g.UndefinedRTPPlayers++
	} else {
		g.rtps = append(g.rtps, float64(s.state.TotalWin)/float64(s.state.TotalBet))
	}
	g.Completed++
	return nil
}
