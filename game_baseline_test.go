// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

// B0 is the consumer baseline recorded before results are assembled into whole
// games, without changing any production code. It is a temporary acceptance
// tool and is removed after the change is accepted.
//
// Record: PROBLAB_B0=record go test -run TestGameBaseline .
// A normal run first verifies the SHA-256 of the baseline file, then compares.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

const (
	b0Dir        = "testdata/game_baseline"
	b0Env        = "PROBLAB_B0"
	b0FirstLines = 3
)

// b0CasesSHA256 pins b0/cases.json. The baseline must not be re-recorded in
// place; a re-record needs its own approved commit that also changes this
// constant.
const b0CasesSHA256 = "1d8e83e1856c9e4c81f8790abac536ac3d361ed19e12662cc8660dec5a759d04"

// ---------------------------------------------------------------------------
// Record format
// ---------------------------------------------------------------------------

type b0File struct {
	Schema string            `json:"schema"`
	Cases  map[string]*b0Run `json:"cases"`
}

// b0Run digests every line of a case with a chained SHA-256; First keeps the
// first lines to help locate a difference.
type b0Run struct {
	Count  int      `json:"count"`
	Digest string   `json:"digest"`
	First  []string `json:"first"`
}

type b0Acc struct {
	sum []byte
	run b0Run
}

func (a *b0Acc) add(line string) {
	next := sha256.Sum256(append(append([]byte(nil), a.sum...), line...))
	a.sum = next[:]
	a.run.Count++
	if len(a.run.First) < b0FirstLines {
		a.run.First = append(a.run.First, line)
	}
}

func (a *b0Acc) finish() *b0Run {
	a.run.Digest = hex.EncodeToString(a.sum)
	return &a.run
}

// b0JSON keeps the complete JSON of v, floats included: the statistics are not
// changed, so derived values must stay byte-identical on the same platform.
func b0JSON(t testing.TB, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func b0ErrText(err error) string {
	if err == nil {
		return ""
	}
	if e, ok := errs.AsErr(err); ok {
		return errs.ErrLv(e.ErrLv) + ":" + err.Error()
	}
	return "plain:" + err.Error()
}

func b0Core(t testing.TB, m *Machine) string {
	t.Helper()
	snap, err := m.SnapshotCore()
	if err != nil {
		t.Fatalf("snapshot machine: %v", err)
	}
	return hex.EncodeToString(snap)
}

// addDTO records a production Spin: the complete DTO (including both
// snapshots), the error and the machine RNG after the call.
func (a *b0Acc) addDTO(t testing.TB, m *Machine, res dto.SpinResult, err error) {
	t.Helper()
	body := "-"
	if err == nil {
		raw, e := json.Marshal(res)
		if e != nil {
			t.Fatalf("marshal DTO: %v", e)
		}
		sum := sha256.Sum256(raw)
		body = hex.EncodeToString(sum[:])
	}
	a.add(fmt.Sprintf("%s|%s|%s", body, b0ErrText(err), b0Core(t, m)))
}

// ---------------------------------------------------------------------------
// Environments
// ---------------------------------------------------------------------------

type b0EnvSpec struct {
	name    string
	optimal bool
	games   []spec.GID
}

var b0Envs = []b0EnvSpec{
	{name: "off-chacha", optimal: false, games: []spec.GID{0, 1}},
	{name: "manifest-chacha", optimal: true, games: []spec.GID{0}},
}

// lab builds a fresh Lab each time so seed and reseed entropy start over and
// every case is independent of the others.
func (e b0EnvSpec) lab(t testing.TB) *Problab {
	t.Helper()
	factory, err := core.NewChaCha20Factory(newDetReader("b0-reseed:" + e.name))
	if err != nil {
		t.Fatal(err)
	}
	opts := []ProblabOption{WithSeedEntropy(newDetReader("b0-seed:" + e.name))}
	var cfg fs.FS
	if e.optimal {
		cfg = testManifestConfigFS(t)
		opts = append(opts, WithOptimalFS(testManifestFS(t, false)))
	} else {
		files := fstest.MapFS{}
		for _, name := range []string{"game_0_demonormal.yaml", "game_1_democascade.yaml"} {
			raw, err := fs.ReadFile(demo_configs.FS, name)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			files[name] = &fstest.MapFile{Data: raw}
		}
		cfg = files
	}
	lab, err := NewAuto(factory, Configs(cfg), Logics(demo_logic.Logics), opts...)
	if err != nil {
		t.Fatalf("%s: build lab: %v", e.name, err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

func (e b0EnvSpec) simulator(t testing.TB, id spec.GID) *Simulator {
	t.Helper()
	s, err := e.lab(t).NewSimulator(id, MachineConfig{Seed: SeedInt64(42)})
	if err != nil {
		t.Fatalf("%s: NewSimulator %d: %v", e.name, id, err)
	}
	return s
}

// ---------------------------------------------------------------------------
// Cases
// ---------------------------------------------------------------------------

type b0Case struct {
	key string
	run func(t testing.TB) *b0Run
}

func b0Cases() []b0Case {
	var out []b0Case
	add := func(key string, run func(t testing.TB) *b0Run) {
		out = append(out, b0Case{key: key, run: run})
	}
	for _, env := range b0Envs {
		for _, id := range env.games {
			prefix := fmt.Sprintf("%s/g%d", env.name, id)
			add(prefix+"/sim", func(t testing.TB) *b0Run { return b0SimRun(t, env, id) })
			add(prefix+"/simmp-2w", func(t testing.TB) *b0Run { return b0SimMPRun(t, env, id) })
			add(prefix+"/simplayers-1w", func(t testing.TB) *b0Run { return b0SimPlayersRun(t, env, id) })
			add(prefix+"/devsim", func(t testing.TB) *b0Run { return b0DevSimRun(t, env, id) })
			add(prefix+"/spin-dto", func(t testing.TB) *b0Run { return b0SpinDTORun(t, env, id) })
		}
	}
	return out
}

// Simulator.reset truncates rBuf and sBuf after every run, so the cases read
// them within capacity.

// sim: the complete report, the complete recorder and the final machine RNG.
func b0SimRun(t testing.TB, env b0EnvSpec, id spec.GID) *b0Run {
	acc := &b0Acc{}
	s := env.simulator(t, id)
	report, _, err := s.Sim(0, 2000, false)
	if err != nil {
		t.Fatalf("%s: Sim: %v", env.name, err)
	}
	acc.add("report|" + b0JSON(t, report))
	acc.add("recorder|" + b0JSON(t, s.rBuf[:1][0]))
	acc.add("core|" + b0Core(t, s.mBuf[0]))
	return acc.finish()
}

// simmp-2w: each SimMP worker owns its machine and recorder and runs a fixed
// number of rounds, so per-worker results do not depend on scheduling.
func b0SimMPRun(t testing.TB, env b0EnvSpec, id spec.GID) *b0Run {
	acc := &b0Acc{}
	s := env.simulator(t, id)
	report, _, err := s.SimMP(0, 1000, 2, false)
	if err != nil {
		t.Fatalf("%s: SimMP: %v", env.name, err)
	}
	acc.add("report|" + b0JSON(t, report))
	for w := range 2 {
		acc.add(fmt.Sprintf("recorder_w%d|%s", w, b0JSON(t, s.rBuf[:2][w])))
		acc.add(fmt.Sprintf("core_w%d|%s", w, b0Core(t, s.mBuf[w])))
	}
	return acc.finish()
}

// simplayers-1w: one worker, so players run in a fixed order on one machine.
func b0SimPlayersRun(t testing.TB, env b0EnvSpec, id spec.GID) *b0Run {
	acc := &b0Acc{}
	s := env.simulator(t, id)
	const players = 5
	report, est, _, err := s.SimPlayers(1, players, 50, 0, 200, false)
	if err != nil {
		t.Fatalf("%s: SimPlayers: %v", env.name, err)
	}
	acc.add("report|" + b0JSON(t, report))
	acc.add("estimator|" + b0JSON(t, est))
	for p := range players {
		acc.add(fmt.Sprintf("recorder_p%d|%s", p, b0JSON(t, s.rBuf[:players][p])))
		acc.add(fmt.Sprintf("player_report_p%d|%s", p, b0JSON(t, s.sBuf[:players][p])))
	}
	acc.add("core|" + b0Core(t, s.mBuf[0]))
	return acc.finish()
}

// devsim: Sim and RestoreSim from the construction snapshot; both run through
// the DevSimulator's Simulator.
func b0DevSimRun(t testing.TB, env b0EnvSpec, id spec.GID) *b0Run {
	acc := &b0Acc{}
	build := func() *DevSimulator {
		d, err := env.lab(t).NewDevSimulator(id, 42)
		if err != nil {
			t.Fatalf("%s: NewDevSimulator: %v", env.name, err)
		}
		return d
	}
	before := corefmt.EncodeBase64URL(build().before)
	steps := []struct {
		name string
		run  func(*DevSimulator) (any, error)
	}{
		{"sim", func(d *DevSimulator) (any, error) { return d.Sim(0, 2000) }},
		{"restore_sim", func(d *DevSimulator) (any, error) { return d.RestoreSim(before, 0, 2000) }},
	}
	for _, step := range steps {
		d := build()
		out, err := step.run(d)
		if err != nil {
			t.Fatalf("%s: devsim %s: %v", env.name, step.name, err)
		}
		acc.add(step.name + "|" + b0JSON(t, out))
		acc.add(step.name + "_recorder|" + b0JSON(t, d.sim.rBuf[:1][0]))
		acc.add(step.name + "_core|" + b0Core(t, d.sim.mBuf[0]))
	}
	return acc.finish()
}

// spin-dto: consecutive production Spins with two multipliers, then a replay of
// the first spin from its start snapshot.
func b0SpinDTORun(t testing.TB, env b0EnvSpec, id spec.GID) *b0Run {
	acc := &b0Acc{}
	m, err := env.lab(t).NewMachine(id, MachineConfig{Seed: SeedInt64(42)})
	if err != nil {
		t.Fatalf("%s: NewMachine %d: %v", env.name, id, err)
	}
	request := func(mult int) *dto.SpinRequest {
		return &dto.SpinRequest{
			UID: "b0", GameName: m.gameName, GameId: m.gameId,
			Bet: m.BetUnits[0] * mult, BetMode: 0, BetMult: mult,
		}
	}
	first := ""
	for i := range 40 {
		res, err := m.Spin(request(1 + i%2))
		acc.addDTO(t, m, res, err)
		if i == 0 {
			if err != nil {
				t.Fatalf("%s: first Spin: %v", env.name, err)
			}
			first = res.State.StartCoreSnapB64U
		}
	}
	replay := request(1)
	replay.StartState = &dto.StartState{StartCoreSnapB64U: first}
	res, err := m.Spin(replay)
	acc.addDTO(t, m, res, err)
	return acc.finish()
}

// TestGameBaselineSimPlayersMultiWorker cannot be recorded: with two workers
// the job channel assigns players by scheduling, which changes every player's
// results. It asserts invariants that hold under any assignment instead.
func TestGameBaselineSimPlayersMultiWorker(t *testing.T) {
	if testing.Short() {
		t.Skip("consumer baseline is skipped under -short")
	}
	const players, rounds = 20, 200
	for _, env := range b0Envs {
		s := env.simulator(t, 0)
		report, _, _, err := s.SimPlayers(2, players, 50, 0, rounds, false)
		if err != nil {
			t.Fatalf("%s: SimPlayers: %v", env.name, err)
		}
		// Simulator.reset truncates the buffers after the run; read them within capacity.
		if cap(s.sBuf) < players {
			t.Fatalf("%s: %d player reports, want %d", env.name, cap(s.sBuf), players)
		}
		sum := 0
		for p, r := range s.sBuf[:players] {
			if r == nil {
				t.Fatalf("%s: player %d has no report", env.name, p)
			}
			if r.Summary.Rounds < 1 || r.Summary.Rounds > rounds {
				t.Fatalf("%s: player %d played %d rounds, want 1..%d", env.name, p, r.Summary.Rounds, rounds)
			}
			if r.Player.Bust && r.Player.Cashout {
				t.Fatalf("%s: player %d is both bust and cashed out", env.name, p)
			}
			if r.Player.Alive != !(r.Player.Bust || r.Player.Cashout) {
				t.Fatalf("%s: player %d alive flag is inconsistent", env.name, p)
			}
			sum += r.Summary.Rounds
		}
		if report.Summary.Rounds != sum {
			t.Fatalf("%s: merged rounds %d, sum of players %d", env.name, report.Summary.Rounds, sum)
		}
	}
}

// ---------------------------------------------------------------------------
// Record and compare
// ---------------------------------------------------------------------------

func TestGameBaseline(t *testing.T) {
	mode := os.Getenv(b0Env)
	if mode == "" && testing.Short() {
		t.Skip("consumer baseline comparison is skipped under -short")
	}
	cases := b0Cases()
	switch mode {
	case "":
		compareGameBaseline(t, cases)
	case "record":
		recordGameBaseline(t, cases)
	default:
		t.Fatalf("unknown %s=%q", b0Env, mode)
	}
}

func b0RecordAll(t *testing.T, cases []b0Case) map[string]*b0Run {
	out := make(map[string]*b0Run, len(cases))
	for _, c := range cases {
		if _, dup := out[c.key]; dup {
			t.Fatalf("duplicate case key %q", c.key)
		}
		out[c.key] = c.run(t)
	}
	return out
}

func marshalB0File(runs map[string]*b0Run) ([]byte, error) {
	raw, err := json.MarshalIndent(b0File{Schema: "problab-game-baseline/v1", Cases: runs}, "", " ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func recordGameBaseline(t *testing.T, cases []b0Case) {
	first, err := marshalB0File(b0RecordAll(t, cases))
	if err != nil {
		t.Fatal(err)
	}
	second, err := marshalB0File(b0RecordAll(t, cases))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two recordings on the same commit differ; the baseline is not reproducible")
	}
	dir := filepath.Join(b0Dir, "b0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cases.json"), first, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(first)
	manifest := fmt.Sprintf("%x  cases.json\n", sum)
	if err := os.WriteFile(filepath.Join(b0Dir, "b0.sha256"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded B0: %d cases, SHA-256=%x", len(cases), sum)
}

func compareGameBaseline(t *testing.T, cases []b0Case) {
	raw, err := os.ReadFile(filepath.Join(b0Dir, "b0", "cases.json"))
	if err != nil {
		t.Fatalf("read B0: %v", err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(raw))
	manifest, err := os.ReadFile(filepath.Join(b0Dir, "b0.sha256"))
	if err != nil {
		t.Fatalf("read b0.sha256: %v", err)
	}
	if !strings.HasPrefix(string(manifest), sum+" ") {
		t.Fatal("cases.json does not match b0.sha256; the baseline was modified")
	}
	if sum != b0CasesSHA256 {
		t.Fatalf("cases.json SHA-256=%s does not match the pinned constant %q", sum, b0CasesSHA256)
	}
	var file b0File
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode B0: %v", err)
	}
	// The case key set must equal the baseline so a dropped case cannot silently
	// stop running.
	keys := map[string]bool{}
	for _, c := range cases {
		keys[c.key] = true
	}
	var missing, extra []string
	for k := range file.Cases {
		if !keys[k] {
			missing = append(missing, k)
		}
	}
	for k := range keys {
		if _, ok := file.Cases[k]; !ok {
			extra = append(extra, k)
		}
	}
	if len(missing)+len(extra) != 0 {
		sort.Strings(missing)
		sort.Strings(extra)
		t.Fatalf("case keys differ from B0: missing=%v extra=%v", missing, extra)
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			got := c.run(t)
			if want := file.Cases[c.key]; !reflect.DeepEqual(got, want) {
				t.Fatalf("differs from B0\n got=%+v\nwant=%+v", got, want)
			}
		})
	}
}
