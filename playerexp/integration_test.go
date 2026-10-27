// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package playerexp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/playerexp"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/sdk/strategy"
	"github.com/zintix-labs/problab/sdk/strategy/builtin"
	"github.com/zintix-labs/problab/sdk/tag"
	"github.com/zintix-labs/problab/spec"
)

type observedFactory struct {
	t             *testing.T
	starts        *[][]byte
	players       *int
	next          *int
	requireExtend bool
}

type extensionProbe struct{ t *testing.T }

func (g extensionProbe) GetResult(r *buf.SpinRequest, gh *slot.Game) *buf.SpinResult {
	if gh.OmitExtend() || r.Checkpoint != nil || len(r.ChooseNext) != 0 || r.Cycle != 0 {
		g.t.Fatal("not a clean non-simulator new spin")
	}
	// This type/logic deliberately has no DTO renderer registration.
	return &buf.SpinResult{Bet: r.Bet, BetMode: r.BetMode, BetMult: r.BetMult, IsGameEnd: true,
		GameModeList: []*buf.GameModeResult{{ActResults: []buf.ActResult{{ExtendResult: struct{ Value int }{7}}}}}}
}
func TestUnregisteredExtensionReachesStrategyAndTag(t *testing.T) {
	raw, e := demo_configs.FS.ReadFile("game_0_demonormal.yaml")
	if e != nil {
		t.Fatal(e)
	}
	raw = []byte(strings.Replace(string(raw), "logic_key: demo_normal", "logic_key: playerexp_extension_probe", 1))
	reg := slot.NewLogicRegistry()
	constructionFault := errors.New("builder failure")
	panicBuild := false
	if e = reg.Register("playerexp_extension_probe", func(*slot.Game) (slot.GameLogic, error) {
		if panicBuild {
			panic(constructionFault)
		}
		return extensionProbe{t}, nil
	}); e != nil {
		t.Fatal(e)
	}
	lab, e := problab.NewAuto(core.Default(), problab.Configs(fstest.MapFS{"game.yaml": &fstest.MapFile{Data: raw}}), problab.Logics(reg))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = lab.Close() }()
	var starts [][]byte
	players, next, tags := 0, 0, 0
	rg := strategy.NewRegistry()
	if e = rg.Register(0, "s", observedFactory{t, &starts, &players, &next, true}); e != nil {
		t.Fatal(e)
	}
	c := playerexp.Config{Analysis: []playerexp.GroupConfig{{Name: "ext", Player: playerexp.PlayerConfig{Players: 1, InitBet: 10, Strategy: "s"}, WinBuckets: []float64{0}, Events: []playerexp.EventConfig{{Name: "ext", Tags: []string{"extended"}}}}}}
	r, e := playerexp.Run(context.Background(), c, playerexp.Options{Lab: lab, Strategies: rg, Tags: map[spec.GID]map[string]tag.IsTag{0: {"extended": func(r *buf.SpinResult) bool {
		tags++
		if !hasExtend(r) {
			t.Fatal("lost raw extension")
		}
		return true
	}}}})
	if e != nil || next != 2 || tags != 2 || r.Groups[0].Events[0].Counts[2] != 1 {
		t.Fatal(r, e)
	}
	panicBuild = true
	r, e = playerexp.Run(context.Background(), c, playerexp.Options{Lab: lab, Strategies: rg})
	if !errors.Is(e, constructionFault) || r.Status != playerexp.Failed || r.Groups[0].Started != 0 {
		t.Fatal("construction panic escaped report boundary", r, e)
	}
}

func (f observedFactory) NewStrategy(int) strategy.Strategy {
	*f.players++
	return &observedStrategy{f: f}
}

type observedStrategy struct {
	f     observedFactory
	spins int
}

func (s *observedStrategy) FirstSpin(p strategy.PlayerState) strategy.Decision {
	if p.Spins != 0 || s.spins != 0 {
		s.f.t.Fatal("player history leaked")
	}
	return strategy.Decision{KeepPlay: true, Bet: strategy.Bet{BetMode: 0, BetMult: 1}}
}
func (s *observedStrategy) Next(p strategy.PlayerState, r *buf.SpinResult) strategy.Decision {
	s.spins++
	*s.f.next++
	if p.Spins != s.spins {
		s.f.t.Fatal("settlement wrong")
	}
	if s.f.requireExtend && !hasExtend(r) {
		s.f.t.Fatal("strategy missing raw Extend")
	}
	*s.f.starts = append(*s.f.starts, append([]byte(nil), r.State.StartCoreSnap...))
	if s.spins == 2 {
		return strategy.Decision{EndType: strategy.Satisfied}
	}
	return strategy.Decision{KeepPlay: true, Bet: strategy.Bet{BetMode: 0, BetMult: 2}}
}
func hasExtend(r *buf.SpinResult) bool {
	for _, m := range r.GameModeList {
		for _, a := range m.ActResults {
			if a.ExtendResult != nil {
				return true
			}
		}
	}
	return false
}
func TestRealRunReproducibilityAndContinuousStream(t *testing.T) {
	lab, e := demo.NewProbLab()
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = lab.Close() }()
	c, e := playerexp.ParseConfig([]byte(`game_id: 0
seed: 77
analysis:
  - name: first
    player: {players: 100, init_bet: 300, strategy: s}
    win_buckets: [0, 1, 2]
    events: [{event: ext, win_range: [0, null], tags: [extended]}]
  - name: second
    player: {players: 100, init_bet: 300, strategy: s}
    win_buckets: [0, 1, 2]
`))
	if e != nil {
		t.Fatal(e)
	}
	var previous playerexp.Report
	for repeat := 0; repeat < 2; repeat++ {
		var starts [][]byte
		players, next, tags, extended := 0, 0, 0, 0
		rg := strategy.NewRegistry()
		if e = rg.Register(0, "s", observedFactory{t, &starts, &players, &next, false}); e != nil {
			t.Fatal(e)
		}
		r, e := playerexp.Run(context.Background(), c, playerexp.Options{Lab: lab, Strategies: rg, Tags: map[spec.GID]map[string]tag.IsTag{0: {"extended": func(r *buf.SpinResult) bool {
			tags++
			if hasExtend(r) {
				extended++
			}
			return true
		}}}})
		if e != nil || players != 200 || next != 400 || tags != 200 || extended == 0 || r.Status != playerexp.Completed || r.Modes[0].Source != "native" {
			t.Fatal(e, players, next, tags, r.Status)
		}
		if repeat > 0 && !reflect.DeepEqual(previous, r) {
			t.Fatal("same seed not reproducible")
		}
		previous = r
		// Independent direct replay proves no reseed at player/group boundaries.
		m, e := lab.NewMachine(0, problab.MachineConfig{Seed: problab.SeedBytes(c.Seed.Bytes())})
		if e != nil {
			t.Fatal("Run closed borrowed Lab", e)
		}
		for i, want := range starts {
			raw := m.SpinUnsafe(problab.SpinInput{BetMode: 0, BetMult: 1 + i%2, CaptureSnapshots: true})
			if !bytes.Equal(raw.State.StartCoreSnap, want) {
				t.Fatal("stream reset", i)
			}
		}
	}
}

// BenchmarkRunWithEventTags measures a playerexp run whose events use tags.
func BenchmarkRunWithEventTags(b *testing.B) {
	lab, err := demo.NewProbLab()
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	c, err := playerexp.ParseConfig([]byte(`game_id: 0
seed: 7
analysis:
  - name: g
    player: {players: 20, init_bet: 300, strategy: flat}
    win_buckets: [0, 1, 2, 5]
    events:
      - {event: fg_big, win_range: [2, null], tags: [fg, big]}
      - {event: win, win_range: [0, null], tags: [win]}
      - {event: rest, win_range: [0, null]}
`))
	if err != nil {
		b.Fatal(err)
	}
	rg := strategy.NewRegistry()
	if err := rg.Register(0, "flat", builtin.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 200, SatisfiedRatio: 3}); err != nil {
		b.Fatal(err)
	}
	tags := map[spec.GID]map[string]tag.IsTag{0: {
		"fg":  func(sr *buf.SpinResult) bool { return sr.GameModeCount > 1 },
		"win": func(sr *buf.SpinResult) bool { return sr.TotalWin > 0 },
		"big": func(sr *buf.SpinResult) bool { return sr.TotalWin >= 3*sr.Bet },
	}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := playerexp.Run(context.Background(), c, playerexp.Options{Lab: lab, Strategies: rg, Tags: tags}); err != nil {
			b.Fatal(err)
		}
	}
}

// builtin.Monotone no longer limits MaxSpins: a value above MaxSpinsPerPlayer is
// cut short by the executor, which ends the player as Other (capped) while the
// strategy still asks to wager.
func TestMonotoneBeyondTheSpinLimitIsCapped(t *testing.T) {
	lab, e := demo.NewProbLab()
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = lab.Close() }()
	reg := strategy.NewRegistry()
	if e = reg.Register(0, "long", builtin.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 30000, SatisfiedRatio: 1 << 20}); e != nil {
		t.Fatal(e)
	}
	c := playerexp.Config{GameID: 0, Analysis: []playerexp.GroupConfig{{Name: "long", Player: playerexp.PlayerConfig{Players: 1, InitBet: 1 << 30, Strategy: "long"}, WinBuckets: []float64{0}}}}
	r, e := playerexp.Run(context.Background(), c, playerexp.Options{Lab: lab, Strategies: reg})
	if e != nil {
		t.Fatal(e)
	}
	g := r.Groups[0]
	if g.CompleteSpins != playerexp.MaxSpinsPerPlayer || g.MaxSpinReached != 1 {
		t.Fatalf("spins %d capped %d", g.CompleteSpins, g.MaxSpinReached)
	}
}

// observingFactory wraps a factory and records what its strategies see at game
// ends and choice points.
type observingFactory struct {
	inner strategy.Factory
	obs   *choiceObservations
}

type choiceObservations struct {
	games, choicePoints, repeated int
	totalWin                      int
	err                           string
}

func (f observingFactory) NewStrategy(initial int) strategy.Strategy {
	return &observingStrategy{inner: f.inner.NewStrategy(initial), obs: f.obs}
}

type observingStrategy struct {
	inner strategy.Strategy
	obs   *choiceObservations
	last  strategy.PlayerState
}

func (s *observingStrategy) FirstSpin(p strategy.PlayerState) strategy.Decision {
	s.last = p
	return s.inner.FirstSpin(p)
}

func (s *observingStrategy) Next(p strategy.PlayerState, r *buf.SpinResult) strategy.Decision {
	o := s.obs
	if !r.IsGameEnd {
		o.choicePoints++
		// Paid but not settled: the wager is taken, the game is not counted yet.
		if p.Spins != s.last.Spins || p.TotalWin != s.last.TotalWin || p.TotalBet <= s.last.TotalBet {
			o.err = fmt.Sprintf("choice point ledger %+v after %+v", p, s.last)
		}
		return s.inner.Next(p, r)
	}
	o.games++
	o.totalWin += r.TotalWin
	if p.Spins != s.last.Spins+1 || p.TotalWin != s.last.TotalWin+r.TotalWin {
		o.err = fmt.Sprintf("settled ledger %+v after %+v win %d", p, s.last, r.TotalWin)
	}
	if repeatedMode(r) {
		o.repeated++
	}
	s.last = p
	return s.inner.Next(p, r)
}

func repeatedMode(r *buf.SpinResult) bool {
	seen := map[int]bool{}
	for _, g := range r.GameModeList {
		if seen[g.GameModeId] {
			return true
		}
		seen[g.GameModeId] = true
	}
	return false
}

// Choice games run to the end of every game with the built-in choices: demo_2
// carries a checkpoint between segments, demo_3 retriggers A→B→A. Every game is
// settled once, and the event tag is evaluated once on the whole game, also when
// a game mode repeats inside it.
func TestChoiceGamesWithBuiltinChoices(t *testing.T) {
	lab, e := demo.NewProbLab()
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = lab.Close() }()
	for _, gid := range []spec.GID{2, 3} {
		for _, choose := range []builtin.Choose{builtin.ChooseFirst, builtin.ChooseLast, builtin.ChooseIter} {
			obs := &choiceObservations{}
			reg := strategy.NewRegistry()
			f := builtin.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 3000, SatisfiedRatio: 50, Choose: choose}
			if e := reg.Register(gid, "m", observingFactory{f, obs}); e != nil {
				t.Fatal(e)
			}
			calls := 0
			tags := tag.GameTagCatalog{gid: {"repeat": func(r *buf.SpinResult) bool { calls++; return repeatedMode(r) }}}
			c := playerexp.Config{GameID: gid, Seed: playerexp.Int64Seed(5), Analysis: []playerexp.GroupConfig{{Name: "m",
				Player: playerexp.PlayerConfig{Players: 3, InitBet: 1000, Strategy: "m"}, WinBuckets: []float64{0},
				Events: []playerexp.EventConfig{{Name: "repeat", WinRange: playerexp.WinRange{Min: 0}, Tags: []string{"repeat"}}}}}}
			r, e := playerexp.Run(context.Background(), c, playerexp.Options{Lab: lab, Strategies: reg, Tags: tags})
			if e != nil {
				t.Fatalf("game %d choose %d: %v", gid, choose, e)
			}
			g := r.Groups[0]
			if obs.err != "" || obs.choicePoints == 0 || uint64(obs.games) != g.CompleteSpins || g.TotalWin != uint64(obs.totalWin) || g.TotalBet != g.CompleteSpins*40 {
				t.Fatalf("game %d choose %d: %+v group spins %d bet %d win %d", gid, choose, *obs, g.CompleteSpins, g.TotalBet, g.TotalWin)
			}
			if uint64(calls) != g.CompleteSpins || g.Events[0].Hits != uint64(obs.repeated) {
				t.Fatalf("game %d choose %d: tag calls %d games %d hits %d repeated %d", gid, choose, calls, g.CompleteSpins, g.Events[0].Hits, obs.repeated)
			}
			if gid == 3 && choose == builtin.ChooseIter && obs.repeated == 0 {
				t.Fatal("no game repeated a mode; the multi-segment tag case was not exercised")
			}
		}
	}
}
