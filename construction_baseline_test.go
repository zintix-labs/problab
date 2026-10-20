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
// 錄製：PROBLAB_RECORD_BASELINE=b0 go test -run TestConstructionBaseline .
// 一般執行只讀不寫，並先驗證 B0 的 SHA-256 未被改動。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// b0CasesSHA256 是 B0 cases.json 的 SHA-256。B0 不可重錄覆蓋；若真的需要重錄，
// 必須是獨立、經使用者同意的 commit，並同時更新此常數與 b0.sha256。
const b0CasesSHA256 = "70d6b7bbcc69bf631d9ad1e297d154dd800ebe410503f7b04ed37db79008b56c"

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

	ss = fresh()
	report, _, err = ss.SimMP(0, baselineSimMPRounds, baselineSimMPWorkers, false)
	if err != nil {
		t.Fatalf("%s: SimMP: %v", key, err)
	}
	rec.Stats["simmp"] = canonicalInts(t, report)
	return rec
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
	rec := &baselineRecord{Key: key, Kind: "runtime", RuntimeGames: map[string]*baselineRun{}}
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
		acc := newRunAccumulator()
		calls := fixtureSnapshotCalls.Load()
		for range baselineRuntimeSpins {
			res, err := rt.Spin(ctx, &dto.SpinRequest{
				UID: "baseline", GameName: gs.GameName, GameId: id,
				Bet: gs.BetUnits[0], BetMode: 0, BetMult: 1,
			})
			if err != nil {
				t.Fatalf("%s: runtime spin %d: %v", key, id, err)
			}
			acc.addSpin(t, res, nil)
		}
		rec.RuntimeGames[fmt.Sprintf("game_%d", id)] = acc.finish(fixtureSnapshotCalls.Load() - calls)
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
		recordConstructionBaseline(t, cases, "b0")
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

func recordConstructionBaseline(t *testing.T, cases []baselineCase, name string) {
	records := recordAll(t, cases, true)
	again := recordAll(t, cases, true)
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
	for _, c := range cases {
		t.Run(c.Key, func(t *testing.T) {
			got := c.record(t)
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

func jsonEqual(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}
