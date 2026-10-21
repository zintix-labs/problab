// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

// 建構收斂的一致性保護基準（PRD 0012、techspec §6）。
//
// B0 在修改任何正式程式碼之前，以當時的舊建構入口錄製；之後每個施工步驟都對
// B0 逐位元比對。比對依案例映射表（construction_baseline_cases_test.go）進行：
// 原模式保留的案例比對同一個 B0 案例；原模式被移除的案例（省略 Extend 的
// Machine）比對錄製 B0 時一併建立的參考案例。
//
// 錄製：PROBLAB_RECORD_BASELINE=b0（只限施工步驟 1）或 b1（舊入口刪除之後）
// go test -run TestConstructionBaseline .
// 一般執行只讀不寫，並先驗證 B0 的 SHA-256 未被改動。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
)

const (
	baselineDir       = "testdata/construction_baseline"
	baselineRecordEnv = "PROBLAB_RECORD_BASELINE"

	baselineMachineSpins = 2000
	baselineSimRounds    = 20000
	baselineSimMPWorkers = 4
	baselineSimMPRounds  = baselineSimRounds * baselineSimMPWorkers
	baselineDevSpins     = 100
	baselineDevSimRounds = 2000
	baselineRuntimeSpins = 300
	baselineRuntimePool  = 2
	baselineFirstLines   = 3
)

// b1CasesSHA256 是 B1 cases.json 的 SHA-256（PRD 0012 完成後錄製，供 PRD 0011 比對）。
// 空字串表示 B1 尚未錄製。
const b1CasesSHA256 = "932c5fd51aff967b2898eae3cf8d14889c938dbb6576ff2d7841e48645ad936f"

// b0CasesSHA256 是 B0 cases.json 的 SHA-256。B0 不可重錄覆蓋；若真的需要重錄，
// 必須是獨立、經使用者同意的 commit，並同時更新此常數與 b0.sha256。
const b0CasesSHA256 = "4d3805919e91e7c01d615087ff35475266b8cc99c15961d2789c0f2bc3e0579b"

// ---------------------------------------------------------------------------
// 記錄格式
// ---------------------------------------------------------------------------

type baselineFile struct {
	Schema string                     `json:"schema"`
	Cases  map[string]*baselineRecord `json:"cases"`
}

type baselineRecord struct {
	Key           string                   `json:"key"`
	Kind          string                   `json:"kind"`
	OmitExtend    *bool                    `json:"omit_extend,omitempty"`
	BuildSnapshot string                   `json:"build_snapshot,omitempty"`
	EntropyRead   int64                    `json:"entropy_read"`
	WorkerSeeds   []string                 `json:"worker_seeds,omitempty"`
	Runs          map[string]*baselineRun  `json:"runs,omitempty"`
	Stats         map[string]*baselineStat `json:"stats,omitempty"`
	RuntimeGames  map[string]*baselineRun  `json:"runtime_games,omitempty"`
	Err           *baselineErr             `json:"err,omitempty"`
	RunErrors     map[string]*baselineErr  `json:"run_errors,omitempty"`

	// WorkerStates 是 SimMP 跑完後每個 worker Machine 的 RNG 狀態（techspec C5）。
	WorkerStates []string `json:"worker_states,omitempty"`
	// RuntimePools 是 Runtime 各遊戲池內每台機台的 slot、generation 與 RNG 狀態，
	// 在建立後、正常 Spin 後、補機後各記一次（techspec C6）。
	RuntimePools    map[string][]baselinePoolMachine `json:"runtime_pools,omitempty"`
	RuntimeErrors   map[string]*baselineErr          `json:"runtime_errors,omitempty"`
	RuntimeRebuilds map[string]int32                 `json:"runtime_rebuilds,omitempty"`
}

type baselinePoolMachine struct {
	Slot     uint64 `json:"slot"`
	Gen      uint64 `json:"gen"`
	Snapshot string `json:"snapshot"`
}

type baselineRun struct {
	Count         int      `json:"count"`
	Digest        string   `json:"digest"`
	NoExtDigest   string   `json:"noext_digest"`
	ExtActs       int      `json:"ext_acts"`
	SnapshotCalls int64    `json:"snapshot_calls"`
	First         []string `json:"first"`
}

type baselineStat struct {
	SHA256 string `json:"sha256"`
	Ints   string `json:"ints"`
}

type baselineErr struct {
	Level       string `json:"level"`
	Message     string `json:"message"`
	EntropyRead int64  `json:"entropy_read"`
}

// ---------------------------------------------------------------------------
// 可重現的 entropy
// ---------------------------------------------------------------------------

// detReader 是無限長、可重現的 reader：第 n 個 32 位元組區塊為
// SHA-256(label || n)。它同時計數被讀取的位元組數。
type detReader struct {
	label   string
	counter uint64
	pending []byte
	read    int64
}

func newDetReader(label string) *detReader { return &detReader{label: label} }

func (r *detReader) Read(dst []byte) (int, error) {
	n := 0
	for n < len(dst) {
		if len(r.pending) == 0 {
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s#%d", r.label, r.counter)))
			r.counter++
			r.pending = sum[:]
		}
		c := copy(dst[n:], r.pending)
		r.pending = r.pending[c:]
		n += c
	}
	r.read += int64(n)
	return n, nil
}

// ---------------------------------------------------------------------------
// 每轉記錄
// ---------------------------------------------------------------------------

type runAccumulator struct {
	full, noExt *hashWriter
	run         baselineRun
}

type hashWriter struct{ sum []byte }

func newRunAccumulator() *runAccumulator {
	return &runAccumulator{full: &hashWriter{}, noExt: &hashWriter{}}
}

func (h *hashWriter) add(line string) {
	next := sha256.Sum256(append(append([]byte(nil), h.sum...), line...))
	h.sum = next[:]
}

func (a *runAccumulator) addSpin(t testing.TB, result dto.SpinResult, coreAfter []byte) {
	t.Helper()
	full, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal DTO: %v", err)
	}
	extActs := 0
	stripped := result
	stripped.GameModes = make([]dto.GameModeResultDTO, len(result.GameModes))
	for i, gm := range result.GameModes {
		acts := make([]dto.ActResultDTO, len(gm.ActResults))
		for j, act := range gm.ActResults {
			if act.ExtendResult != nil {
				extActs++
			}
			act.ExtendResult = nil
			acts[j] = act
		}
		gm.ActResults = acts
		stripped.GameModes[i] = gm
	}
	noExt, err := json.Marshal(stripped)
	if err != nil {
		t.Fatalf("marshal stripped DTO: %v", err)
	}
	fullSum := sha256.Sum256(full)
	noExtSum := sha256.Sum256(noExt)
	core := hex.EncodeToString(coreAfter)
	a.full.add(fmt.Sprintf("%x|%s|%d\n", fullSum, core, result.TotalWin))
	a.noExt.add(fmt.Sprintf("%x|%s|%d\n", noExtSum, core, result.TotalWin))
	a.run.Count++
	a.run.ExtActs += extActs
	if len(a.run.First) < baselineFirstLines {
		a.run.First = append(a.run.First, fmt.Sprintf("%x|%x|%s", fullSum[:8], noExtSum[:8], core))
	}
}

func (a *runAccumulator) finish(snapshotCalls int64) *baselineRun {
	a.run.Digest = hex.EncodeToString(a.full.sum)
	a.run.NoExtDigest = hex.EncodeToString(a.noExt.sum)
	a.run.SnapshotCalls = snapshotCalls
	return &a.run
}

func recordMachineSpin(t testing.TB, acc *runAccumulator, m *Machine, sr *buf.SpinResult) {
	t.Helper()
	d, err := dto.NewSpinResultDTO(sr)
	if err != nil {
		t.Fatalf("NewSpinResultDTO: %v", err)
	}
	after, err := m.SnapshotCore()
	if err != nil {
		t.Fatalf("snapshot after spin: %v", err)
	}
	acc.addSpin(t, d, after)
}

// ---------------------------------------------------------------------------
// 統計與錯誤
// ---------------------------------------------------------------------------

// canonicalInts 只保留 JSON 中的整數欄位；衍生浮點數不直接比對（techspec §6.1）。
func canonicalInts(t testing.TB, v any) *baselineStat {
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
	ints, err := json.Marshal(keepInts(tree))
	if err != nil {
		t.Fatalf("marshal ints: %v", err)
	}
	sum := sha256.Sum256(ints)
	return &baselineStat{SHA256: hex.EncodeToString(sum[:]), Ints: string(ints)}
}

func keepInts(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if kept := keepInts(val); kept != nil {
				out[k] = kept
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, val := range x {
			out = append(out, keepInts(val))
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

func baselineErrOf(err error, entropy *detReader) *baselineErr {
	level := "plain"
	if e, ok := errs.AsErr(err); ok {
		level = errs.ErrLv(e.ErrLv)
	}
	return &baselineErr{Level: level, Message: err.Error(), EntropyRead: entropy.read}
}

// ---------------------------------------------------------------------------
// 案例執行
// ---------------------------------------------------------------------------

func boolPtr(v bool) *bool { return &v }

func machineRequest(m *Machine, mode, mult int) *dto.SpinRequest {
	return &dto.SpinRequest{
		UID: "baseline", GameName: m.gameName, GameId: m.gameId,
		Bet: m.BetUnits[mode] * mult, BetMode: mode, BetMult: mult,
	}
}

// recordMachineCase 執行一個 Machine 案例（或其參考案例）。build 每次呼叫都必須
// 回傳一台全新、狀態相同的 Machine，以及它所屬環境的 seed entropy reader。
func recordMachineCase(t testing.TB, key string, build func() (*Machine, *detReader, error)) *baselineRecord {
	t.Helper()
	rec := &baselineRecord{Key: key, Kind: "machine", Runs: map[string]*baselineRun{}}
	m, entropy, err := build()
	if err != nil {
		rec.Kind = "error"
		rec.Err = baselineErrOf(err, entropy)
		return rec
	}
	snap, err := m.SnapshotCore()
	if err != nil {
		t.Fatalf("%s: build snapshot: %v", key, err)
	}
	rec.BuildSnapshot = hex.EncodeToString(snap)
	rec.EntropyRead = entropy.read
	rec.OmitExtend = boolPtr(baselineMachineOmitExtend(m))

	fresh := func(run string) *Machine {
		mm, _, err := build()
		if err != nil {
			t.Fatalf("%s/%s: rebuild: %v", key, run, err)
		}
		s, _ := mm.SnapshotCore()
		if hex.EncodeToString(s) != rec.BuildSnapshot {
			t.Fatalf("%s/%s: rebuild is not deterministic", key, run)
		}
		return mm
	}
	for mode := range m.BetUnits {
		name := fmt.Sprintf("spin/m%d", mode)
		mm := fresh(name)
		acc := newRunAccumulator()
		calls := fixtureSnapshotCalls.Load()
		var runErr error
		for range baselineMachineSpins {
			res, err := mm.Spin(machineRequest(mm, mode, 1))
			if err != nil {
				runErr = err
				break
			}
			after, _ := mm.SnapshotCore()
			acc.addSpin(t, res, after)
		}
		rec.Runs[name] = acc.finish(fixtureSnapshotCalls.Load() - calls)
		if runErr != nil {
			rec.addRunErr(name, runErr, entropy)
		}

		name = fmt.Sprintf("internal/m%d", mode)
		mm = fresh(name)
		acc = newRunAccumulator()
		calls = fixtureSnapshotCalls.Load()
		for range baselineMachineSpins {
			recordMachineSpin(t, acc, mm, mm.SpinInternal(mode))
		}
		rec.Runs[name] = acc.finish(fixtureSnapshotCalls.Load() - calls)

		for _, mult := range []int{1, 3} {
			name = fmt.Sprintf("offline/m%dx%d", mode, mult)
			mm = fresh(name)
			acc = newRunAccumulator()
			calls = fixtureSnapshotCalls.Load()
			runErr = nil
			for range baselineMachineSpins {
				sr, err := mm.SpinOffline(mode, mult)
				if err != nil {
					runErr = err
					break
				}
				recordMachineSpin(t, acc, mm, sr)
			}
			rec.Runs[name] = acc.finish(fixtureSnapshotCalls.Load() - calls)
			if runErr != nil {
				rec.addRunErr(name, runErr, entropy)
			}
		}
	}
	return rec
}

func (r *baselineRecord) addRunErr(run string, err error, entropy *detReader) {
	if r.RunErrors == nil {
		r.RunErrors = map[string]*baselineErr{}
	}
	e := baselineErrOf(err, entropy)
	e.EntropyRead = 0 // 執行期錯誤不涉及建構時的 entropy
	r.RunErrors[run] = e
}

func recordSimulatorCase(t testing.TB, key string, build func() (*Simulator, *detReader, error)) *baselineRecord {
	t.Helper()
	rec := &baselineRecord{Key: key, Kind: "simulator", Runs: map[string]*baselineRun{}, Stats: map[string]*baselineStat{}}
	s, entropy, err := build()
	if err != nil {
		rec.Kind = "error"
		rec.Err = baselineErrOf(err, entropy)
		return rec
	}
	snap, err := s.mBuf[0].SnapshotCore()
	if err != nil {
		t.Fatalf("%s: build snapshot: %v", key, err)
	}
	rec.BuildSnapshot = hex.EncodeToString(snap)
	rec.EntropyRead = entropy.read
	rec.OmitExtend = boolPtr(baselineMachineOmitExtend(s.mBuf[0]))
	for w := range baselineSimMPWorkers {
		seed, err := s.workerSeed(w)
		if err != nil {
			t.Fatalf("%s: worker seed: %v", key, err)
		}
		rec.WorkerSeeds = append(rec.WorkerSeeds, hex.EncodeToString(seed))
	}
	fresh := func() *Simulator {
		ss, _, err := build()
		if err != nil {
			t.Fatalf("%s: rebuild: %v", key, err)
		}
		return ss
	}

	ss := fresh()
	m := ss.mBuf[0]
	acc := newRunAccumulator()
	calls := fixtureSnapshotCalls.Load()
	for range baselineMachineSpins {
		recordMachineSpin(t, acc, m, m.SpinInternal(0))
	}
	rec.Runs["internal/m0"] = acc.finish(fixtureSnapshotCalls.Load() - calls)

	ss = fresh()
	report, _, err := ss.Sim(0, baselineSimRounds, false)
	if err != nil {
		t.Fatalf("%s: Sim: %v", key, err)
	}
	rec.Stats["sim"] = canonicalInts(t, report)
	// 報表中的平方和是除以投注單位平方的浮點數；直接比對 recorder 的原始整數計數器
	// （含三個平方和）。Sim 結束時只把 rBuf 的長度歸零，recorder 本身仍在底層陣列中。
	rec.Stats["sim_basic"] = canonicalInts(t, ss.rBuf[:1][0].Basic)

	ss = fresh()
	report, _, err = ss.SimMP(0, baselineSimMPRounds, baselineSimMPWorkers, false)
	if err != nil {
		t.Fatalf("%s: SimMP: %v", key, err)
	}
	rec.Stats["simmp"] = canonicalInts(t, report)
	for w, r := range ss.rBuf[:baselineSimMPWorkers] {
		rec.Stats[fmt.Sprintf("simmp_basic_w%d", w)] = canonicalInts(t, r.Basic)
	}
	for _, m := range ss.mBuf {
		state, err := m.SnapshotCore()
		if err != nil {
			t.Fatalf("%s: worker state: %v", key, err)
		}
		rec.WorkerStates = append(rec.WorkerStates, hex.EncodeToString(state))
	}
	return rec
}

// poolStates 依序取出池內所有可用機台記錄狀態後原樣放回，FIFO 順序不變。
func poolStates(t testing.TB, p *MachinePool) []baselinePoolMachine {
	t.Helper()
	n := len(p.pool)
	machines := make([]*Machine, 0, n)
	out := make([]baselinePoolMachine, 0, n)
	for range n {
		m := <-p.pool
		state, err := m.SnapshotCore()
		if err != nil {
			t.Fatalf("pool machine state: %v", err)
		}
		machines = append(machines, m)
		out = append(out, baselinePoolMachine{Slot: m.poolSlot, Gen: m.poolGen, Snapshot: hex.EncodeToString(state)})
	}
	for _, m := range machines {
		p.pool <- m
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out
}

func recordDevSimCase(t testing.TB, key string, build func() (*DevSimulator, *detReader, error)) *baselineRecord {
	t.Helper()
	rec := &baselineRecord{Key: key, Kind: "devsim", Stats: map[string]*baselineStat{}}
	d, entropy, err := build()
	if err != nil {
		rec.Kind = "error"
		rec.Err = baselineErrOf(err, entropy)
		return rec
	}
	rec.BuildSnapshot = hex.EncodeToString(d.before)
	rec.EntropyRead = entropy.read
	before := corefmt.EncodeBase64URL(d.before)
	steps := []struct {
		name string
		run  func(*DevSimulator) (any, error)
	}{
		{"spins", func(d *DevSimulator) (any, error) { return d.Spins(0, baselineDevSpins) }},
		{"sim", func(d *DevSimulator) (any, error) { return d.Sim(0, baselineDevSimRounds) }},
		{"restore_spins", func(d *DevSimulator) (any, error) { return d.RestoreSpins(before, 0, baselineDevSpins) }},
		{"restore_sim", func(d *DevSimulator) (any, error) { return d.RestoreSim(before, 0, baselineDevSimRounds) }},
	}
	for _, step := range steps {
		dd, _, err := build()
		if err != nil {
			t.Fatalf("%s/%s: rebuild: %v", key, step.name, err)
		}
		out, err := step.run(dd)
		if err != nil {
			t.Fatalf("%s/%s: %v", key, step.name, err)
		}
		rec.Stats[step.name] = canonicalInts(t, out)
	}
	return rec
}

func recordRuntimeCase(t testing.TB, key string, build func() (*SlotRuntime, *Problab, *detReader, error)) *baselineRecord {
	t.Helper()
	rec := &baselineRecord{
		Key: key, Kind: "runtime", RuntimeGames: map[string]*baselineRun{},
		RuntimePools: map[string][]baselinePoolMachine{}, RuntimeErrors: map[string]*baselineErr{},
		RuntimeRebuilds: map[string]int32{},
	}
	rt, lab, entropy, err := build()
	if err != nil {
		rec.Kind = "error"
		rec.Err = baselineErrOf(err, entropy)
		return rec
	}
	defer rt.Close()
	rec.EntropyRead = entropy.read
	ctx := context.Background()
	for _, id := range lab.IDs() {
		gs, err := lab.cat.GameSettingById(id)
		if err != nil {
			t.Fatalf("%s: setting %d: %v", key, id, err)
		}
		game := fmt.Sprintf("game_%d", id)
		pool := rt.pools[id]
		rec.RuntimePools[game+"/built"] = poolStates(t, pool)
		spins := func(run string, count int) {
			acc := newRunAccumulator()
			calls := fixtureSnapshotCalls.Load()
			for range count {
				res, err := rt.Spin(ctx, &dto.SpinRequest{
					UID: "baseline", GameName: gs.GameName, GameId: id,
					Bet: gs.BetUnits[0], BetMode: 0, BetMult: 1,
				})
				if err != nil {
					t.Fatalf("%s: runtime spin %d: %v", key, id, err)
				}
				acc.addSpin(t, res, nil)
			}
			rec.RuntimeGames[run] = acc.finish(fixtureSnapshotCalls.Load() - calls)
		}
		spins(game, baselineRuntimeSpins)
		rec.RuntimePools[game+"/after_spins"] = poolStates(t, pool)
		if id == fixtureGameID {
			// 觸發補機：fixture 遊戲在此倍數下 panic，池子淘汰該機台並以下一個
			// generation 的派生 seed 補一台新的 production Machine。
			_, err := rt.Spin(ctx, &dto.SpinRequest{
				UID: "baseline", GameName: gs.GameName, GameId: id,
				Bet: gs.BetUnits[0] * fixturePanicBetMult, BetMode: 0, BetMult: fixturePanicBetMult,
			})
			if err == nil {
				t.Fatalf("%s: forced panic spin succeeded", key)
			}
			e := baselineErrOf(err, entropy)
			e.EntropyRead = 0
			rec.RuntimeErrors[game+"/panic"] = e
			rec.RuntimePools[game+"/after_rebuild"] = poolStates(t, pool)
			spins(game+"/after_rebuild", baselineRuntimeSpins/2)
			rec.RuntimePools[game+"/after_rebuild_spins"] = poolStates(t, pool)
		}
		rec.RuntimeRebuilds[game] = pool.rebuild.Load()
	}
	if len(rec.RuntimeErrors) == 0 {
		rec.RuntimeErrors = nil // 與 JSON omitempty 載入後的表示一致
	}
	return rec
}

// ---------------------------------------------------------------------------
// 主測試
// ---------------------------------------------------------------------------

func TestConstructionBaseline(t *testing.T) {
	mode := os.Getenv(baselineRecordEnv)
	if mode == "" && testing.Short() {
		t.Skip("建構基準比對在 -short 下略過")
	}
	cases := baselineCases(t)
	switch mode {
	case "":
		compareConstructionBaseline(t, cases)
	case "b0":
		if baselineBuildersMigrated {
			t.Fatal(errBaselineRecordClosed)
		}
		recordConstructionBaseline(t, cases, "b0", true)
	case "b1":
		if !baselineBuildersMigrated {
			t.Fatal("B1 只能在舊入口刪除並完成遷移之後錄製")
		}
		recordConstructionBaseline(t, cases, "b1", false)
	default:
		t.Fatalf("unknown %s=%q", baselineRecordEnv, mode)
	}
}

func recordAll(t testing.TB, cases []baselineCase, withReferences bool) map[string]*baselineRecord {
	t.Helper()
	out := map[string]*baselineRecord{}
	for _, c := range cases {
		out[c.Key] = c.record(t)
		if withReferences && c.needsReference() && out[c.Key].Kind != "error" {
			ref := c.recordReference(t)
			if ref.BuildSnapshot != out[c.Key].BuildSnapshot || ref.EntropyRead != out[c.Key].EntropyRead {
				t.Fatalf("%s: 參考案例的建構指紋與舊入口案例不同", c.Key)
			}
			out[referenceKey(c.Key)] = ref
		}
	}
	return out
}

func recordConstructionBaseline(t *testing.T, cases []baselineCase, name string, withReferences bool) {
	records := recordAll(t, cases, withReferences)
	again := recordAll(t, cases, withReferences)
	first, err := marshalBaseline(records)
	if err != nil {
		t.Fatal(err)
	}
	second, err := marshalBaseline(again)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("同一個 commit 上兩次錄製的結果不同，基準不可重現")
	}
	dir := filepath.Join(baselineDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cases.json")
	if err := os.WriteFile(path, first, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(first)
	manifest := fmt.Sprintf("%x  cases.json\n", sum)
	if err := os.WriteFile(filepath.Join(baselineDir, name+".sha256"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("已錄製 %s：%d 個案例，SHA-256=%x", name, len(records), sum)
}

func marshalBaseline(records map[string]*baselineRecord) ([]byte, error) {
	file := baselineFile{Schema: "problab-construction-baseline/v1", Cases: records}
	raw, err := json.MarshalIndent(file, "", " ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func loadBaseline(t testing.TB, name, wantSHA string) map[string]*baselineRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(baselineDir, name, "cases.json"))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(raw))
	manifest, err := os.ReadFile(filepath.Join(baselineDir, name+".sha256"))
	if err != nil {
		t.Fatalf("read %s.sha256: %v", name, err)
	}
	if !strings.HasPrefix(string(manifest), sum+" ") {
		t.Fatalf("%s 的內容與 %s.sha256 不符，基準已被改動", name, name)
	}
	if wantSHA == "" || sum != wantSHA {
		t.Fatalf("%s 的 SHA-256=%s 與程式中固定的常數 %q 不符", name, sum, wantSHA)
	}
	var file baselineFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return file.Cases
}

func compareConstructionBaseline(t *testing.T, cases []baselineCase) {
	b0 := loadBaseline(t, "b0", b0CasesSHA256)
	var b1 map[string]*baselineRecord
	if b1CasesSHA256 != "" {
		b1 = loadBaseline(t, "b1", b1CasesSHA256)
	}
	// 案例鍵集合必須與基準完全相同：案例產生器若漏掉一列，該案例會默默不再執行，
	// 因此必須由「基準還有這一列」讓測試失敗。
	requireSameCaseKeys(t, "B0", cases, b0, true)
	if b1 != nil {
		requireSameCaseKeys(t, "B1", cases, b1, false)
	}
	for _, c := range cases {
		t.Run(c.Key, func(t *testing.T) {
			got := c.record(t)
			if b1 != nil {
				// B1 是本決策完成後的基準：同一個案例鍵、整筆紀錄完全相同，沒有任何例外
				// （包含預期為 nil 的 Extend、OmitExtend 旗標與 Snapshot 呼叫次數）。
				want, ok := b1[c.Key]
				if !ok {
					t.Fatalf("B1 缺少案例 %q", c.Key)
				}
				requireIdenticalRecord(t, "B1", got, want)
			}
			target, expectOmit := c.target()
			if own, ok := b0[c.Key]; ok && own.Kind == "error" {
				// B0 中本身就是錯誤的案例（例如 PCG64 不接受的 seed 長度），比對同一個錯誤。
				target, expectOmit = c.Key, nil
			}
			want, ok := b0[target]
			if !ok {
				t.Fatalf("B0 缺少比對基準 %q", target)
			}
			compareBaselineRecord(t, c, got, want, expectOmit)
		})
	}
}

func compareBaselineRecord(t *testing.T, c baselineCase, got, want *baselineRecord, expectOmit *bool) {
	t.Helper()
	if want.Kind == "error" || got.Kind == "error" {
		if !jsonEqual(got.Err, want.Err) {
			t.Fatalf("錯誤路徑不同：got=%+v want=%+v", got.Err, want.Err)
		}
		return
	}
	if got.BuildSnapshot != want.BuildSnapshot || got.EntropyRead != want.EntropyRead {
		t.Fatalf("建構指紋不同：got=(%s,%d) want=(%s,%d)", got.BuildSnapshot, got.EntropyRead, want.BuildSnapshot, want.EntropyRead)
	}
	if expectOmit != nil && (got.OmitExtend == nil || *got.OmitExtend != *expectOmit) {
		t.Fatalf("OmitExtend 不符映射表：got=%v want=%v", got.OmitExtend, *expectOmit)
	}
	if !jsonEqual(got.WorkerSeeds, want.WorkerSeeds) {
		t.Fatal("Simulator worker seed 派生不同")
	}
	if !jsonEqual(got.Stats, want.Stats) {
		t.Fatalf("統計整數計數器不同")
	}
	if !jsonEqual(got.RunErrors, want.RunErrors) {
		t.Fatalf("執行期錯誤不同：got=%v want=%v", got.RunErrors, want.RunErrors)
	}
	if !jsonEqual(got.WorkerStates, want.WorkerStates) {
		t.Fatal("Simulator worker Machine 的狀態不同")
	}
	if !jsonEqual(got.RuntimePools, want.RuntimePools) {
		t.Fatal("Runtime 池內機台的 slot、generation 或狀態不同")
	}
	if !jsonEqual(got.RuntimeErrors, want.RuntimeErrors) || !jsonEqual(got.RuntimeRebuilds, want.RuntimeRebuilds) {
		t.Fatalf("Runtime 補機流程不同：errors got=%v want=%v rebuilds got=%v want=%v",
			got.RuntimeErrors, want.RuntimeErrors, got.RuntimeRebuilds, want.RuntimeRebuilds)
	}
	compareRuns(t, "runtime", got.RuntimeGames, want.RuntimeGames, c.compareExtend(), false)
	compareRuns(t, "run", got.Runs, want.Runs, c.compareExtend(), c.fixtureOmitted())
}

func compareRuns(t *testing.T, label string, got, want map[string]*baselineRun, withExtend, fixtureOmitted bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s 數量不同：got=%d want=%d", label, len(got), len(want))
	}
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		g, w := got[name], want[name]
		if g == nil {
			t.Fatalf("%s %s 缺少", label, name)
		}
		if g.Count != w.Count || g.NoExtDigest != w.NoExtDigest {
			t.Fatalf("%s %s 的非 Extend 結果不同：got=%v want=%v", label, name, g.First, w.First)
		}
		if withExtend && (g.Digest != w.Digest || g.ExtActs != w.ExtActs || g.SnapshotCalls != w.SnapshotCalls) {
			t.Fatalf("%s %s 的完整結果（含 Extend 與 Snapshot 呼叫次數）不同", label, name)
		}
		if fixtureOmitted && g.SnapshotCalls != 0 {
			t.Fatalf("%s %s：省略模式下 Snapshot() 仍被呼叫 %d 次", label, name, g.SnapshotCalls)
		}
	}
}

// requireSameCaseKeys 確認目前的案例鍵集合與基準完全相同。B0 另含參考案例
// （ref/ 前綴），比對時排除。
func requireSameCaseKeys(t *testing.T, name string, cases []baselineCase, base map[string]*baselineRecord, skipRefs bool) {
	t.Helper()
	current := map[string]bool{}
	for _, c := range cases {
		current[c.Key] = true
	}
	var missing, extra []string
	for key := range base {
		if skipRefs && strings.HasPrefix(key, "ref/") {
			continue
		}
		if !current[key] {
			missing = append(missing, key)
		}
	}
	for key := range current {
		if _, ok := base[key]; !ok {
			extra = append(extra, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("%s 的案例鍵集合不同：基準有但未執行=%v；執行了但基準沒有=%v", name, missing, extra)
	}
}

// requireIdenticalRecord 要求整筆紀錄完全相同，並指出第一個不同的欄位。
func requireIdenticalRecord(t *testing.T, name string, got, want *baselineRecord) {
	t.Helper()
	if jsonEqual(got, want) {
		return
	}
	var g, w map[string]json.RawMessage
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	_ = json.Unmarshal(gb, &g)
	_ = json.Unmarshal(wb, &w)
	fields := map[string]bool{}
	for k := range g {
		fields[k] = true
	}
	for k := range w {
		fields[k] = true
	}
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !bytes.Equal(g[k], w[k]) {
			t.Fatalf("%s 紀錄不同：第一個不同的欄位是 %q", name, k)
		}
	}
	t.Fatalf("%s 紀錄不同", name)
}

var errBaselineRecordClosed = errors.New("B0 只能在施工步驟 1 的程式上錄製；舊入口已刪除，無法重建參考案例")

func jsonEqual(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}
