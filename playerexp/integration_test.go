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
	if gh.IsSim || r.StartState != nil || r.Cycle != 0 || r.HasChoice {
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
	rg := playerexp.NewStrategyRegistry()
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

func (f observedFactory) NewStrategy(int) playerexp.Strategy {
	*f.players++
	return &observedStrategy{f: f}
}

type observedStrategy struct {
	f     observedFactory
	spins int
}

func (s *observedStrategy) FirstSpin(p playerexp.PlayerState) playerexp.Decision {
	if p.Spins != 0 || s.spins != 0 {
		s.f.t.Fatal("player history leaked")
	}
	return playerexp.Decision{KeepPlay: true, Bet: playerexp.Bet{BetMode: 0, BetMult: 1}}
}
func (s *observedStrategy) Next(p playerexp.PlayerState, r *buf.SpinResult) playerexp.Decision {
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
		return playerexp.Decision{EndType: playerexp.Satisfied}
	}
	return playerexp.Decision{KeepPlay: true, Bet: playerexp.Bet{BetMode: 0, BetMult: 2}}
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
		rg := playerexp.NewStrategyRegistry()
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
		m, e := lab.NewMachineWithSeedBytes(0, c.Seed.Bytes(), false)
		if e != nil {
			t.Fatal("Run closed borrowed Lab", e)
		}
		for i, want := range starts {
			raw, e := m.SpinOffline(0, 1+i%2)
			if e != nil || !bytes.Equal(raw.State.StartCoreSnap, want) {
				t.Fatal("stream reset", i, e)
			}
		}
	}
}
