// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

// E0 是三個執行入口（Spin、SpinInternal、SpinOffline）收斂到共用私有核心之前，
// 修改任何正式程式碼前錄製的執行路徑基準。它是階段性驗收工具：驗收並提交後移除。
//
// 錄製：PROBLAB_RECORD_EXEC_BASELINE=e0 go test -run TestExecutionBaseline .
// 一般執行只讀不寫，並先驗證基準檔的 SHA-256 未被改動。

import (
	"bytes"
	"context"
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
	execBaselineDir   = "testdata/execution_baseline"
	execBaselineEnv   = "PROBLAB_RECORD_EXEC_BASELINE"
	execBaselineSpins = 20
	execFirstLines    = 3
)

// execE0CasesSHA256 固定 e0/cases.json 的 SHA-256。基準不得重錄覆蓋；若真的需要
// 重錄，必須是獨立、經使用者同意的 commit，並同時修改此常數。
const execE0CasesSHA256 = "aff5b0e50dbf079a474964ad73be04c8e10b8abea386b316ac0e191dff0f0402"

// ---------------------------------------------------------------------------
// 記錄格式
// ---------------------------------------------------------------------------

type execFile struct {
	Schema string              `json:"schema"`
	Cases  map[string]*execRun `json:"cases"`
}

// execRun 以逐行串接的 SHA-256 摘要一個案例的所有呼叫；First 保留前幾行方便定位差異。
type execRun struct {
	Count  int      `json:"count"`
	Digest string   `json:"digest"`
	First  []string `json:"first"`
}

type execAcc struct {
	sum []byte
	run execRun
}

func (a *execAcc) add(line string) {
	next := sha256.Sum256(append(append([]byte(nil), a.sum...), line...))
	a.sum = next[:]
	a.run.Count++
	if len(a.run.First) < execFirstLines {
		a.run.First = append(a.run.First, line)
	}
}

func (a *execAcc) finish() *execRun {
	a.run.Digest = hex.EncodeToString(a.sum)
	return &a.run
}

func execErrText(err error) string {
	if err == nil {
		return ""
	}
	if e, ok := errs.AsErr(err); ok {
		return errs.ErrLv(e.ErrLv) + ":" + err.Error()
	}
	return "plain:" + err.Error()
}

func execDTOHash(t testing.TB, res dto.SpinResult) string {
	t.Helper()
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal DTO: %v", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func execCore(t testing.TB, m *Machine) string {
	t.Helper()
	snap, err := m.SnapshotCore()
	if err != nil {
		t.Fatalf("snapshot machine: %v", err)
	}
	return hex.EncodeToString(snap)
}

// addDTO 記錄一次回傳 DTO 的呼叫：結果摘要（含起始與結束快照）、錯誤、呼叫後的機台 RNG。
func (a *execAcc) addDTO(t testing.TB, m *Machine, res dto.SpinResult, err error) {
	t.Helper()
	hash := "-"
	if err == nil {
		hash = execDTOHash(t, res)
	}
	a.add(fmt.Sprintf("%s|%s|%s", hash, execErrText(err), execCore(t, m)))
}

// addBuf 記錄一次回傳 *buf.SpinResult 的呼叫；結果以 dto.NewSpinResultDTO 轉換後計算。
func (a *execAcc) addBuf(t testing.TB, m *Machine, sr *buf.SpinResult, err error) {
	t.Helper()
	hash := "-"
	switch {
	case err != nil:
	case sr == nil:
		hash = "nil"
	default:
		res, e := dto.NewSpinResultDTO(sr)
		if e != nil {
			t.Fatalf("NewSpinResultDTO: %v", e)
		}
		hash = execDTOHash(t, res)
	}
	a.add(fmt.Sprintf("%s|%s|%s", hash, execErrText(err), execCore(t, m)))
}

// execInts 只保留 JSON 中的整數欄位與非數值欄位；衍生浮點數不比對，避免不同
// 平台的浮點細節影響基準。
func execInts(t testing.TB, v any) string {
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
	ints, err := json.Marshal(execKeepInts(tree))
	if err != nil {
		t.Fatalf("marshal ints: %v", err)
	}
	return string(ints)
}

func execKeepInts(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if kept := execKeepInts(val); kept != nil {
				out[k] = kept
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, val := range x {
			out = append(out, execKeepInts(val))
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
// 環境組合
// ---------------------------------------------------------------------------

type execEnv struct {
	name    string
	factory string // chacha | pcg
	optimal string // off | manifest | legacy
	games   []spec.GID
}

var execEnvs = []execEnv{
	{name: "off-chacha", factory: "chacha", optimal: "off", games: []spec.GID{0, 1}},
	{name: "off-pcg", factory: "pcg", optimal: "off", games: []spec.GID{0}},
	{name: "manifest-chacha", factory: "chacha", optimal: "manifest", games: []spec.GID{0}},
	{name: "manifest-pcg", factory: "pcg", optimal: "manifest", games: []spec.GID{0}},
	{name: "legacy-chacha", factory: "chacha", optimal: "legacy", games: []spec.GID{0}},
}

// lab 每次建立全新的 Lab：seed entropy 與 reseed entropy 都從頭開始，因此每個案例
// 彼此獨立，新增或調整案例不會改變其他案例的結果。
func (e execEnv) lab(t testing.TB) *Problab {
	t.Helper()
	var factory core.PRNGFactory = core.PCG64()
	if e.factory == "chacha" {
		f, err := core.NewChaCha20Factory(newDetReader("exec-reseed:" + e.name))
		if err != nil {
			t.Fatal(err)
		}
		factory = f
	}
	opts := []ProblabOption{WithSeedEntropy(newDetReader("exec-seed:" + e.name))}
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

// machine 建立新 Lab 上的一台 Machine。det 為 deterministic（固定 seed），prod 為
// production（以測試輔助函式重現單台 production 的建立流程）。
func (e execEnv) machine(t testing.TB, id spec.GID, kind string) *Machine {
	t.Helper()
	lab := e.lab(t)
	var (
		m   *Machine
		err error
	)
	if kind == "prod" {
		m, err = newProductionMachineForTest(lab, id)
	} else {
		m, err = lab.NewMachine(id, MachineConfig{Seed: SeedInt64(42)})
	}
	if err != nil {
		t.Fatalf("%s: build %s machine %d: %v", e.name, kind, id, err)
	}
	return m
}

func execRequest(m *Machine, mode, mult int) *dto.SpinRequest {
	return &dto.SpinRequest{
		UID: "e0", GameName: m.gameName, GameId: m.gameId,
		Bet: m.BetUnits[mode] * mult, BetMode: mode, BetMult: mult,
	}
}

func execReplayRequest(m *Machine, mode int, startB64 string) *dto.SpinRequest {
	r := execRequest(m, mode, 1)
	r.StartState = &dto.StartState{StartCoreSnapB64U: startB64}
	return r
}

// ---------------------------------------------------------------------------
// 案例
// ---------------------------------------------------------------------------

type execCase struct {
	key string
	run func(t testing.TB) *execRun
}

func execCases() []execCase {
	var out []execCase
	add := func(key string, run func(t testing.TB) *execRun) {
		out = append(out, execCase{key: key, run: run})
	}
	for _, env := range execEnvs {
		for _, id := range env.games {
			for _, kind := range []string{"det", "prod"} {
				prefix := fmt.Sprintf("%s/g%d/%s", env.name, id, kind)
				add(prefix+"/spin", func(t testing.TB) *execRun { return execSpinRun(t, env, id, kind) })
				add(prefix+"/replay", func(t testing.TB) *execRun { return execReplayRun(t, env, id, kind) })
				add(prefix+"/errors", func(t testing.TB) *execRun { return execErrorRun(t, env, id, kind) })
			}
			prefix := fmt.Sprintf("%s/g%d/det", env.name, id)
			add(prefix+"/internal", func(t testing.TB) *execRun { return execInternalRun(t, env, id) })
			add(prefix+"/offline", func(t testing.TB) *execRun { return execOfflineRun(t, env, id) })
			add(prefix+"/mixed", func(t testing.TB) *execRun { return execMixedRun(t, env, id) })
		}
	}
	for _, env := range execEnvs {
		if env.name != "off-chacha" && env.name != "manifest-chacha" {
			continue
		}
		add(env.name+"/g0/sim", func(t testing.TB) *execRun { return execSimRun(t, env) })
		add(env.name+"/g0/devsim", func(t testing.TB) *execRun { return execDevSimRun(t, env) })
		add(env.name+"/g0/runtime", func(t testing.TB) *execRun { return execRuntimeRun(t, env) })
	}
	return out
}

// spin：每個 bet mode 連續 Spin，不帶快照。
func execSpinRun(t testing.TB, env execEnv, id spec.GID, kind string) *execRun {
	acc := &execAcc{}
	probe := env.machine(t, id, kind)
	for mode := range probe.BetUnits {
		m := env.machine(t, id, kind)
		for range execBaselineSpins {
			res, err := m.Spin(execRequest(m, mode, 1))
			acc.addDTO(t, m, res, err)
		}
	}
	return acc.finish()
}

// replay：以 spin 各轉的起始快照，在另一台新機台上逐一以外部快照 Spin。
func execReplayRun(t testing.TB, env execEnv, id spec.GID, kind string) *execRun {
	acc := &execAcc{}
	probe := env.machine(t, id, kind)
	for mode := range probe.BetUnits {
		src := env.machine(t, id, kind)
		starts := make([]string, 0, execBaselineSpins)
		for range execBaselineSpins {
			res, err := src.Spin(execRequest(src, mode, 1))
			if err != nil {
				t.Fatalf("replay source spin: %v", err)
			}
			starts = append(starts, res.State.StartCoreSnapB64U)
		}
		m := env.machine(t, id, kind)
		for _, s := range starts {
			before := execCore(t, m)
			res, err := m.Spin(execReplayRequest(m, mode, s))
			acc.addDTO(t, m, res, err)
			acc.add(fmt.Sprintf("unchanged=%t", before == execCore(t, m)))
		}
	}
	return acc.finish()
}

// internal：每個 bet mode 連續 SpinInternal。
func execInternalRun(t testing.TB, env execEnv, id spec.GID) *execRun {
	acc := &execAcc{}
	probe := env.machine(t, id, "det")
	for mode := range probe.BetUnits {
		m := env.machine(t, id, "det")
		for range execBaselineSpins {
			acc.addBuf(t, m, m.SpinInternal(mode), nil)
		}
	}
	return acc.finish()
}

// offline：SpinOffline，倍數 1 與 3。
func execOfflineRun(t testing.TB, env execEnv, id spec.GID) *execRun {
	acc := &execAcc{}
	probe := env.machine(t, id, "det")
	for mode := range probe.BetUnits {
		for _, mult := range []int{1, 3} {
			m := env.machine(t, id, "det")
			for range execBaselineSpins {
				sr, err := m.SpinOffline(mode, mult)
				acc.addBuf(t, m, sr, err)
			}
		}
	}
	return acc.finish()
}

// mixed：同一台機台依固定順序交錯呼叫三個入口，檢查跨入口的 RNG 延續。
func execMixedRun(t testing.TB, env execEnv, id spec.GID) *execRun {
	acc := &execAcc{}
	m := env.machine(t, id, "det")
	first, err := m.Spin(execRequest(m, 0, 1))
	acc.addDTO(t, m, first, err)
	if err != nil {
		t.Fatalf("mixed first spin: %v", err)
	}
	s1 := first.State.StartCoreSnapB64U
	steps := []func(){
		func() { acc.addBuf(t, m, m.SpinInternal(0), nil) },
		func() { sr, e := m.SpinOffline(0, 2); acc.addBuf(t, m, sr, e) },
		func() { r, e := m.Spin(execReplayRequest(m, 0, s1)); acc.addDTO(t, m, r, e) },
		func() { acc.addBuf(t, m, m.SpinInternal(0), nil) },
		func() { sr, e := m.SpinOffline(0, 1); acc.addBuf(t, m, sr, e) },
		func() { r, e := m.Spin(execRequest(m, 0, 1)); acc.addDTO(t, m, r, e) },
		func() { r, e := m.Spin(execReplayRequest(m, 0, s1)); acc.addDTO(t, m, r, e) },
		func() { sr, e := m.SpinOffline(0, 3); acc.addBuf(t, m, sr, e) },
		func() { acc.addBuf(t, m, m.SpinInternal(0), nil) },
		func() { r, e := m.Spin(execRequest(m, 0, 1)); acc.addDTO(t, m, r, e) },
		func() { acc.addBuf(t, m, m.SpinInternal(0), nil) },
	}
	if len(m.BetUnits) > 1 {
		steps = append(steps,
			func() { r, e := m.Spin(execRequest(m, 1, 1)); acc.addDTO(t, m, r, e) },
			func() { acc.addBuf(t, m, m.SpinInternal(1), nil) },
			func() { sr, e := m.SpinOffline(1, 2); acc.addBuf(t, m, sr, e) },
		)
	}
	for _, step := range steps {
		step()
	}
	return acc.finish()
}

// errors：可預期的輸入錯誤。記錄錯誤等級、訊息與呼叫後的機台 RNG，最後再正常 Spin
// 一次，確認錯誤沒有消耗 RNG。
func execErrorRun(t testing.TB, env execEnv, id spec.GID, kind string) *execRun {
	acc := &execAcc{}
	m := env.machine(t, id, kind)
	spinErr := func(mutate func(*dto.SpinRequest)) {
		r := execRequest(m, 0, 1)
		mutate(r)
		res, err := m.Spin(r)
		if err == nil {
			t.Fatalf("%s: expected Spin error", env.name)
		}
		acc.addDTO(t, m, res, err)
	}
	spinErr(func(r *dto.SpinRequest) { r.GameId++ })
	spinErr(func(r *dto.SpinRequest) { r.GameName = "not_this_game" })
	spinErr(func(r *dto.SpinRequest) { r.Bet++ })
	spinErr(func(r *dto.SpinRequest) { r.BetMode = len(m.BetUnits) })
	spinErr(func(r *dto.SpinRequest) { r.StartState = &dto.StartState{StartCoreSnapB64U: "!!!"} })
	spinErr(func(r *dto.SpinRequest) {
		r.StartState = &dto.StartState{StartCoreSnapB64U: corefmt.EncodeBase64URL([]byte{1, 2, 3})}
	})
	spinErr(func(r *dto.SpinRequest) { r.StartState = &dto.StartState{Checkpoint: json.RawMessage(`{}`)} })
	if kind == "det" {
		for _, b := range [][2]int{{-1, 1}, {len(m.BetUnits), 1}, {0, 0}, {0, -1}, {0, int(^uint(0) >> 1)}} {
			sr, err := m.SpinOffline(b[0], b[1])
			if err == nil {
				t.Fatalf("%s: expected SpinOffline error for %v", env.name, b)
			}
			acc.addBuf(t, m, sr, err)
		}
	} else {
		sr, err := m.SpinOffline(0, 1)
		if err == nil {
			t.Fatalf("%s: production SpinOffline accepted", env.name)
		}
		acc.addBuf(t, m, sr, err)
	}
	res, err := m.Spin(execRequest(m, 0, 1))
	acc.addDTO(t, m, res, err)
	return acc.finish()
}

// sim：Sim 與 SimMP 的 recorder 原始整數計數器，以及 worker Machine 的最終 RNG 狀態。
func execSimRun(t testing.TB, env execEnv) *execRun {
	acc := &execAcc{}
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
	acc.add("sim|" + execInts(t, report))
	acc.add("sim_basic|" + execInts(t, s.rBuf[:1][0].Basic))
	acc.add("sim_core|" + execCore(t, s.mBuf[0]))

	s = build()
	report, _, err = s.SimMP(0, 400, 2, false)
	if err != nil {
		t.Fatalf("%s: SimMP: %v", env.name, err)
	}
	acc.add("simmp|" + execInts(t, report))
	for w, r := range s.rBuf[:2] {
		acc.add(fmt.Sprintf("simmp_basic_w%d|%s", w, execInts(t, r.Basic)))
	}
	for w, m := range s.mBuf {
		acc.add(fmt.Sprintf("simmp_core_w%d|%s", w, execCore(t, m)))
	}
	return acc.finish()
}

// devsim：Spins、Sim，以及以建構時快照 RestoreSpins、RestoreSim 的結果。
func execDevSimRun(t testing.TB, env execEnv) *execRun {
	acc := &execAcc{}
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
		{"spins", func(d *DevSimulator) (any, error) { return d.Spins(0, 10) }},
		{"sim", func(d *DevSimulator) (any, error) { return d.Sim(0, 200) }},
		{"restore_spins", func(d *DevSimulator) (any, error) { return d.RestoreSpins(before, 0, 10) }},
		{"restore_sim", func(d *DevSimulator) (any, error) { return d.RestoreSim(before, 0, 200) }},
	}
	for _, step := range steps {
		out, err := step.run(build())
		if err != nil {
			t.Fatalf("%s: devsim %s: %v", env.name, step.name, err)
		}
		acc.add(step.name + "|" + execInts(t, out))
	}
	return acc.finish()
}

// runtime：BuildRuntime（pool 2 台）連續 Spin 的結果，以及池內機台的狀態。
func execRuntimeRun(t testing.TB, env execEnv) *execRun {
	acc := &execAcc{}
	lab := env.lab(t)
	rt, err := lab.BuildRuntime(2)
	if err != nil {
		t.Fatalf("%s: BuildRuntime: %v", env.name, err)
	}
	defer rt.Close()
	gs, err := lab.cat.GameSettingById(0)
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		res, err := rt.Spin(context.Background(), &dto.SpinRequest{
			UID: "e0", GameName: gs.GameName, GameId: 0,
			Bet: gs.BetUnits[0], BetMode: 0, BetMult: 1,
		})
		hash := "-"
		if err == nil {
			hash = execDTOHash(t, res)
		}
		acc.add(hash + "|" + execErrText(err))
	}
	for _, line := range execPoolStates(t, rt.pools[0]) {
		acc.add("pool|" + line)
	}
	return acc.finish()
}

// execPoolStates 依序取出池內所有可用機台記錄狀態後原樣放回，FIFO 順序不變。
func execPoolStates(t testing.TB, p *MachinePool) []string {
	t.Helper()
	n := len(p.pool)
	machines := make([]*Machine, 0, n)
	out := make([]string, 0, n)
	for range n {
		m := <-p.pool
		machines = append(machines, m)
		out = append(out, fmt.Sprintf("%d|%d|%s", m.poolSlot, m.poolGen, execCore(t, m)))
	}
	for _, m := range machines {
		p.pool <- m
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// 錄製與比對
// ---------------------------------------------------------------------------

func TestExecutionBaseline(t *testing.T) {
	mode := os.Getenv(execBaselineEnv)
	if mode == "" && testing.Short() {
		t.Skip("執行路徑基準比對在 -short 下略過")
	}
	cases := execCases()
	switch mode {
	case "":
		compareExecutionBaseline(t, cases)
	case "e0":
		recordExecutionBaseline(t, cases)
	default:
		t.Fatalf("unknown %s=%q", execBaselineEnv, mode)
	}
}

func execRecordAll(t *testing.T, cases []execCase) map[string]*execRun {
	out := make(map[string]*execRun, len(cases))
	for _, c := range cases {
		if _, dup := out[c.key]; dup {
			t.Fatalf("duplicate case key %q", c.key)
		}
		out[c.key] = c.run(t)
	}
	return out
}

func marshalExecFile(runs map[string]*execRun) ([]byte, error) {
	raw, err := json.MarshalIndent(execFile{Schema: "problab-execution-baseline/v1", Cases: runs}, "", " ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func recordExecutionBaseline(t *testing.T, cases []execCase) {
	first, err := marshalExecFile(execRecordAll(t, cases))
	if err != nil {
		t.Fatal(err)
	}
	second, err := marshalExecFile(execRecordAll(t, cases))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("同一個 commit 上兩次錄製的結果不同，基準不可重現")
	}
	dir := filepath.Join(execBaselineDir, "e0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cases.json"), first, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(first)
	manifest := fmt.Sprintf("%x  cases.json\n", sum)
	if err := os.WriteFile(filepath.Join(execBaselineDir, "e0.sha256"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("已錄製 E0：%d 個案例，SHA-256=%x", len(cases), sum)
}

// loadExecBaseline 驗證基準檔與 SHA-256 檔、程式中的常數三者一致後載入。
func loadExecBaseline(t testing.TB, dir, file, manifestName, wantSHA string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(raw))
	manifest, err := os.ReadFile(filepath.Join(execBaselineDir, manifestName))
	if err != nil {
		t.Fatalf("read %s: %v", manifestName, err)
	}
	if !strings.HasPrefix(string(manifest), sum+" ") {
		t.Fatalf("%s 的內容與 %s 不符，基準已被改動", file, manifestName)
	}
	if wantSHA == "" || sum != wantSHA {
		t.Fatalf("%s 的 SHA-256=%s 與程式中固定的常數 %q 不符", file, sum, wantSHA)
	}
	return raw
}

func compareExecutionBaseline(t *testing.T, cases []execCase) {
	raw := loadExecBaseline(t, filepath.Join(execBaselineDir, "e0"), "cases.json", "e0.sha256", execE0CasesSHA256)
	var file execFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode E0: %v", err)
	}
	// 案例鍵集合必須與基準完全相同，避免案例產生器漏掉一列時該案例默默不再執行。
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
		t.Fatalf("案例鍵與 E0 不同：基準有但未執行=%v，執行了但基準沒有=%v", missing, extra)
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			got := c.run(t)
			want := file.Cases[c.key]
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("與 E0 不同\n got=%+v\nwant=%+v", got, want)
			}
		})
	}
}
