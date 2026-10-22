// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

// U0 is the consumer baseline recorded before SpinInternal and SpinOffline are
// replaced by SpinUnsafe, without changing any production code. It is a
// temporary acceptance tool and is removed after the migration is accepted.
//
// Record: PROBLAB_RECORD_SPIN_BASELINE=u0 go test -run TestSpinBaseline .
// A normal run only reads and first verifies the SHA-256 of the baseline file.

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
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

const (
	spinBaselineDir   = "testdata/spin_baseline"
	spinBaselineEnv   = "PROBLAB_RECORD_SPIN_BASELINE"
	spinBaselineSpins = 20
	spinFirstLines    = 3
)

// spinU0CasesSHA256 pins u0/cases.json. The baseline must not be re-recorded
// in place; a re-record needs its own user-approved commit that also changes
// this constant.
const spinU0CasesSHA256 = "20bae09ba5e6061626e649e8b373b238c90d135798425ffdcb224426a2c1cb49"

// ---------------------------------------------------------------------------
// Record format
// ---------------------------------------------------------------------------

type spinFile struct {
	Schema string              `json:"schema"`
	Cases  map[string]*spinRun `json:"cases"`
}

// spinRun digests every line of a case with a chained SHA-256; First keeps the
// first lines to help locate a difference.
type spinRun struct {
	Count  int      `json:"count"`
	Digest string   `json:"digest"`
	First  []string `json:"first"`
}

type spinAcc struct {
	sum []byte
	run spinRun
}

func (a *spinAcc) add(line string) {
	next := sha256.Sum256(append(append([]byte(nil), a.sum...), line...))
	a.sum = next[:]
	a.run.Count++
	if len(a.run.First) < spinFirstLines {
		a.run.First = append(a.run.First, line)
	}
}

func (a *spinAcc) finish() *spinRun {
	a.run.Digest = hex.EncodeToString(a.sum)
	return &a.run
}

func spinErrText(err error) string {
	if err == nil {
		return ""
	}
	if e, ok := errs.AsErr(err); ok {
		return errs.ErrLv(e.ErrLv) + ":" + err.Error()
	}
	return "plain:" + err.Error()
}

func spinDTOHash(t testing.TB, res dto.SpinResult) string {
	t.Helper()
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal DTO: %v", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func spinCore(t testing.TB, m *Machine) string {
	t.Helper()
	snap, err := m.SnapshotCore()
	if err != nil {
		t.Fatalf("snapshot machine: %v", err)
	}
	return hex.EncodeToString(snap)
}

// addDTO records a call returning a DTO: the result digest (including start and
// after snapshots), the error, and the machine RNG after the call.
func (a *spinAcc) addDTO(t testing.TB, m *Machine, res dto.SpinResult, err error) {
	t.Helper()
	hash := "-"
	if err == nil {
		hash = spinDTOHash(t, res)
	}
	a.add(fmt.Sprintf("%s|%s|%s", hash, spinErrText(err), spinCore(t, m)))
}

// addBuf records a call returning *buf.SpinResult, converted with
// dto.NewSpinResultDTO, plus the raw State snapshots so a stale snapshot is
// visible even if a converter dropped it.
func (a *spinAcc) addBuf(t testing.TB, m *Machine, sr *buf.SpinResult, err error) {
	t.Helper()
	hash, state := "-", "-"
	switch {
	case err != nil:
	case sr == nil:
		hash = "nil"
	default:
		res, e := dto.NewSpinResultDTO(sr)
		if e != nil {
			t.Fatalf("NewSpinResultDTO: %v", e)
		}
		hash = spinDTOHash(t, res)
		state = "nil-state"
		if sr.State != nil {
			state = hex.EncodeToString(sr.State.StartCoreSnap) + "/" + hex.EncodeToString(sr.State.AfterCoreSnap)
		}
	}
	a.add(fmt.Sprintf("%s|%s|%s|%s", hash, state, spinErrText(err), spinCore(t, m)))
}

// spinInts keeps only integer and non-numeric JSON fields; derived floats are
// not compared so platform float details cannot affect the baseline.
func spinInts(t testing.TB, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal stat: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		t.Fatalf("decode stat: %v", err)
	}
	ints, err := json.Marshal(spinKeepInts(tree))
	if err != nil {
		t.Fatalf("marshal ints: %v", err)
	}
	return string(ints)
}

func spinKeepInts(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if kept := spinKeepInts(val); kept != nil {
				out[k] = kept
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, val := range x {
			out = append(out, spinKeepInts(val))
		}
		return out
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return x
		}
		return nil
	default:
		return x
	}
}

// ---------------------------------------------------------------------------
// Environments
// ---------------------------------------------------------------------------

type spinEnv struct {
	name    string
	factory string // chacha | pcg
	optimal string // off | manifest | legacy
	games   []spec.GID
}

var spinEnvs = []spinEnv{
	{name: "off-chacha", factory: "chacha", optimal: "off", games: []spec.GID{0, 1}},
	{name: "off-pcg", factory: "pcg", optimal: "off", games: []spec.GID{0}},
	{name: "manifest-chacha", factory: "chacha", optimal: "manifest", games: []spec.GID{0}},
	{name: "manifest-pcg", factory: "pcg", optimal: "manifest", games: []spec.GID{0}},
	{name: "legacy-chacha", factory: "chacha", optimal: "legacy", games: []spec.GID{0}},
}

// lab builds a fresh Lab each time so seed and reseed entropy start over and
// every case is independent of the others.
func (e spinEnv) lab(t testing.TB) *Problab {
	t.Helper()
	var factory core.PRNGFactory = core.PCG64()
	if e.factory == "chacha" {
		f, err := core.NewChaCha20Factory(newDetReader("spin-reseed:" + e.name))
		if err != nil {
			t.Fatal(err)
		}
		factory = f
	}
	opts := []ProblabOption{WithSeedEntropy(newDetReader("spin-seed:" + e.name))}
	var cfg fs.FS
	switch {
	case e.optimal == "manifest" && e.factory == "pcg":
		cfg = testManifestConfigFS(t)
		opts = append(opts, WithOptimalFS(testPCGManifestFS(t)))
	case e.optimal == "manifest":
		cfg = testManifestConfigFS(t)
		opts = append(opts, WithOptimalFS(testManifestFS(t, false)))
	case e.optimal == "legacy":
		cfg = testLegacyConfigFS(t)
		opts = append(opts, WithOptimalFS(testOptimalFS(t)))
	default:
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

func (e spinEnv) machine(t testing.TB, id spec.GID) *Machine {
	t.Helper()
	m, err := e.lab(t).NewMachine(id, MachineConfig{Seed: SeedInt64(42)})
	if err != nil {
		t.Fatalf("%s: build machine %d: %v", e.name, id, err)
	}
	return m
}

func spinRequest(m *Machine, mode, mult int) *dto.SpinRequest {
	return &dto.SpinRequest{
		UID: "u0", GameName: m.gameName, GameId: m.gameId,
		Bet: m.BetUnits[mode] * mult, BetMode: mode, BetMult: mult,
	}
}

func spinReplayRequest(m *Machine, mode int, startB64 string) *dto.SpinRequest {
	r := spinRequest(m, mode, 1)
	r.StartState = &dto.StartState{StartCoreSnapB64U: startB64}
	return r
}

// ---------------------------------------------------------------------------
// Cases
// ---------------------------------------------------------------------------

type spinCase struct {
	key string
	run func(t testing.TB) *spinRun
}

func spinCases() []spinCase {
	var out []spinCase
	add := func(key string, run func(t testing.TB) *spinRun) {
		out = append(out, spinCase{key: key, run: run})
	}
	for _, env := range spinEnvs {
		for _, id := range env.games {
			prefix := fmt.Sprintf("%s/g%d", env.name, id)
			add(prefix+"/internal", func(t testing.TB) *spinRun { return spinInternalRun(t, env, id) })
			add(prefix+"/offline", func(t testing.TB) *spinRun { return spinOfflineRun(t, env, id) })
			add(prefix+"/mixed", func(t testing.TB) *spinRun { return spinMixedRun(t, env, id) })
			add(prefix+"/offline-stale", func(t testing.TB) *spinRun { return spinOfflineStaleRun(t, env, id) })
		}
	}
	for _, env := range spinEnvs {
		if env.name != "off-chacha" && env.name != "manifest-chacha" {
			continue
		}
		add(env.name+"/g0/sim", func(t testing.TB) *spinRun { return spinSimRun(t, env) })
		add(env.name+"/g0/devsim", func(t testing.TB) *spinRun { return spinDevSimRun(t, env) })
	}
	return out
}

// internal: consecutive SpinInternal calls for every bet mode.
func spinInternalRun(t testing.TB, env spinEnv, id spec.GID) *spinRun {
	acc := &spinAcc{}
	probe := env.machine(t, id)
	for mode := range probe.BetUnits {
		m := env.machine(t, id)
		for range spinBaselineSpins {
			acc.addBuf(t, m, m.SpinInternal(mode), nil)
		}
	}
	return acc.finish()
}

// offline: SpinOffline with multipliers 1 and 3.
func spinOfflineRun(t testing.TB, env spinEnv, id spec.GID) *spinRun {
	acc := &spinAcc{}
	probe := env.machine(t, id)
	for mode := range probe.BetUnits {
		for _, mult := range []int{1, 3} {
			m := env.machine(t, id)
			for range spinBaselineSpins {
				sr, err := m.SpinOffline(mode, mult)
				acc.addBuf(t, m, sr, err)
			}
		}
	}
	return acc.finish()
}

// mixed: interleaves the three entries on one machine in a fixed order to check
// RNG continuity across entries.
func spinMixedRun(t testing.TB, env spinEnv, id spec.GID) *spinRun {
	acc := &spinAcc{}
	m := env.machine(t, id)
	first, err := m.Spin(spinRequest(m, 0, 1))
	acc.addDTO(t, m, first, err)
	if err != nil {
		t.Fatalf("mixed first spin: %v", err)
	}
	s1 := first.State.StartCoreSnapB64U
	steps := []func(){
		func() { acc.addBuf(t, m, m.SpinInternal(0), nil) },
		func() { sr, e := m.SpinOffline(0, 2); acc.addBuf(t, m, sr, e) },
		func() { r, e := m.Spin(spinReplayRequest(m, 0, s1)); acc.addDTO(t, m, r, e) },
		func() { acc.addBuf(t, m, m.SpinInternal(0), nil) },
		func() { sr, e := m.SpinOffline(0, 1); acc.addBuf(t, m, sr, e) },
		func() { r, e := m.Spin(spinRequest(m, 0, 1)); acc.addDTO(t, m, r, e) },
		func() { r, e := m.Spin(spinReplayRequest(m, 0, s1)); acc.addDTO(t, m, r, e) },
		func() { sr, e := m.SpinOffline(0, 3); acc.addBuf(t, m, sr, e) },
		func() { acc.addBuf(t, m, m.SpinInternal(0), nil) },
		func() { r, e := m.Spin(spinRequest(m, 0, 1)); acc.addDTO(t, m, r, e) },
		func() { acc.addBuf(t, m, m.SpinInternal(0), nil) },
	}
	if len(m.BetUnits) > 1 {
		steps = append(steps,
			func() { r, e := m.Spin(spinRequest(m, 1, 1)); acc.addDTO(t, m, r, e) },
			func() { acc.addBuf(t, m, m.SpinInternal(1), nil) },
			func() { sr, e := m.SpinOffline(1, 2); acc.addBuf(t, m, sr, e) },
		)
	}
	for _, step := range steps {
		step()
	}
	return acc.finish()
}

// offline-stale: snapshot-capturing and non-capturing calls alternate on one
// machine, so a result after a capturing call must not carry its snapshots.
func spinOfflineStaleRun(t testing.TB, env spinEnv, id spec.GID) *spinRun {
	acc := &spinAcc{}
	m := env.machine(t, id)
	for _, step := range []string{"off1", "int", "off2", "int", "int", "off1", "off3", "int"} {
		switch step {
		case "int":
			acc.addBuf(t, m, m.SpinInternal(0), nil)
		default:
			mult := int(step[3] - '0')
			sr, err := m.SpinOffline(0, mult)
			acc.addBuf(t, m, sr, err)
		}
	}
	return acc.finish()
}

// sim: Sim, SimMP and single-worker SimPlayers reports, the raw recorder
// counters (including the integer square sums) and the final RNG of each
// worker machine. SimPlayers uses one worker because the shared job
// channel assigns players by scheduling when there are several workers.
func spinSimRun(t testing.TB, env spinEnv) *spinRun {
	acc := &spinAcc{}
	build := func() *Simulator {
		s, err := env.lab(t).NewSimulator(0, MachineConfig{Seed: SeedInt64(42)})
		if err != nil {
			t.Fatalf("%s: NewSimulator: %v", env.name, err)
		}
		return s
	}
	s := build()
	report, _, err := s.Sim(0, 200, false)
	if err != nil {
		t.Fatalf("%s: Sim: %v", env.name, err)
	}
	acc.add("sim|" + spinInts(t, report))
	acc.add("sim_recorder|" + spinInts(t, s.rBuf[:1][0]))
	acc.add("sim_core|" + spinCore(t, s.mBuf[0]))

	s = build()
	report, _, err = s.SimMP(0, 400, 2, false)
	if err != nil {
		t.Fatalf("%s: SimMP: %v", env.name, err)
	}
	acc.add("simmp|" + spinInts(t, report))
	for w, r := range s.rBuf[:2] {
		acc.add(fmt.Sprintf("simmp_recorder_w%d|%s", w, spinInts(t, r)))
	}
	for w, m := range s.mBuf {
		acc.add(fmt.Sprintf("simmp_core_w%d|%s", w, spinCore(t, m)))
	}

	s = build()
	report, est, _, err := s.SimPlayers(1, 5, 50, 0, 100, false)
	if err != nil {
		t.Fatalf("%s: SimPlayers: %v", env.name, err)
	}
	acc.add("simplayers|" + spinInts(t, report))
	acc.add("simplayers_est|" + spinInts(t, est))
	for p, r := range s.rBuf[:5] {
		acc.add(fmt.Sprintf("simplayers_recorder_p%d|%s", p, spinInts(t, r)))
	}
	acc.add("simplayers_core|" + spinCore(t, s.mBuf[0]))
	return acc.finish()
}

// devsim: Sim and RestoreSim from the construction snapshot; both run through
// the DevSimulator's Simulator.
func spinDevSimRun(t testing.TB, env spinEnv) *spinRun {
	acc := &spinAcc{}
	build := func() *DevSimulator {
		d, err := env.lab(t).NewDevSimulator(0, 42)
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
		{"sim", func(d *DevSimulator) (any, error) { return d.Sim(0, 200) }},
		{"restore_sim", func(d *DevSimulator) (any, error) { return d.RestoreSim(before, 0, 200) }},
	}
	for _, step := range steps {
		d := build()
		out, err := step.run(d)
		if err != nil {
			t.Fatalf("%s: devsim %s: %v", env.name, step.name, err)
		}
		acc.add(step.name + "|" + spinInts(t, out))
		acc.add(step.name + "_recorder|" + spinInts(t, d.sim.rBuf[:1][0]))
		acc.add(step.name + "_core|" + spinCore(t, d.sim.mBuf[0]))
	}
	return acc.finish()
}

// ---------------------------------------------------------------------------
// Record and compare
// ---------------------------------------------------------------------------

func TestSpinBaseline(t *testing.T) {
	mode := os.Getenv(spinBaselineEnv)
	if mode == "" && testing.Short() {
		t.Skip("consumer baseline comparison is skipped under -short")
	}
	cases := spinCases()
	switch mode {
	case "":
		compareSpinBaseline(t, cases)
	case "u0":
		recordSpinBaseline(t, cases)
	default:
		t.Fatalf("unknown %s=%q", spinBaselineEnv, mode)
	}
}

func spinRecordAll(t *testing.T, cases []spinCase) map[string]*spinRun {
	out := make(map[string]*spinRun, len(cases))
	for _, c := range cases {
		if _, dup := out[c.key]; dup {
			t.Fatalf("duplicate case key %q", c.key)
		}
		out[c.key] = c.run(t)
	}
	return out
}

func marshalSpinFile(runs map[string]*spinRun) ([]byte, error) {
	raw, err := json.MarshalIndent(spinFile{Schema: "problab-spin-baseline/v1", Cases: runs}, "", " ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func recordSpinBaseline(t *testing.T, cases []spinCase) {
	first, err := marshalSpinFile(spinRecordAll(t, cases))
	if err != nil {
		t.Fatal(err)
	}
	second, err := marshalSpinFile(spinRecordAll(t, cases))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two recordings on the same commit differ; the baseline is not reproducible")
	}
	dir := filepath.Join(spinBaselineDir, "u0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cases.json"), first, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(first)
	manifest := fmt.Sprintf("%x  cases.json\n", sum)
	if err := os.WriteFile(filepath.Join(spinBaselineDir, "u0.sha256"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded U0: %d cases, SHA-256=%x", len(cases), sum)
}

func compareSpinBaseline(t *testing.T, cases []spinCase) {
	raw, err := os.ReadFile(filepath.Join(spinBaselineDir, "u0", "cases.json"))
	if err != nil {
		t.Fatalf("read U0: %v", err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(raw))
	manifest, err := os.ReadFile(filepath.Join(spinBaselineDir, "u0.sha256"))
	if err != nil {
		t.Fatalf("read u0.sha256: %v", err)
	}
	if !strings.HasPrefix(string(manifest), sum+" ") {
		t.Fatal("cases.json does not match u0.sha256; the baseline was modified")
	}
	if sum != spinU0CasesSHA256 {
		t.Fatalf("cases.json SHA-256=%s does not match the pinned constant %q", sum, spinU0CasesSHA256)
	}
	var file spinFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode U0: %v", err)
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
		t.Fatalf("case keys differ from U0: missing=%v extra=%v", missing, extra)
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			got := c.run(t)
			if want := file.Cases[c.key]; !reflect.DeepEqual(got, want) {
				t.Fatalf("differs from U0\n got=%+v\nwant=%+v", got, want)
			}
		})
	}
}
