// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
)

func rgsTestTuner(t *testing.T, base RunResult, formats []OutputFormat, options ...TunerOption) *Tuner {
	t.Helper()
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	plan := base.Report.Plan.Plan
	plan.Output = OutputOptions{Directory: t.TempDir(), Format: formats}
	config := Config{Version: ConfigVersion, Plans: []RunPlan{plan}, Intents: map[string]MathIntent{plan.Intent: base.Report.Plan.Intent}, EngineOptions: base.Report.Plan.EngineOptions}
	tuner, err := NewTuner(config, lab, options...)
	if err != nil {
		t.Fatal(err)
	}
	return tuner
}

func readRGSJSON(t *testing.T, path string) [][]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder, err := zstd.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	reader := bufio.NewReader(decoder)
	var rows [][]byte
	for {
		line, e := reader.ReadBytes('\n')
		if e == io.EOF {
			if len(line) != 0 {
				t.Fatal("missing LF")
			}
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		rows = append(rows, bytes.TrimSuffix(line, []byte{'\n'}))
	}
	return rows
}

func TestRGSRoutingAndPairing(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "rgs-routing")
	for _, formats := range [][]OutputFormat{{OutputRGSCollected}, {OutputRGSOptimized}, {OutputOptimalGacha, OutputRGSOptimized, OutputRGSCollected, OutputOptimalArtifactV1}} {
		t.Run(fmt.Sprint(formats), func(t *testing.T) {
			calls := 0
			recorder := &recordingReporter{}
			tuner := rgsTestTuner(t, base, formats, WithReporter(recorder), WithResultConverter(func(r dto.SpinResult) (json.RawMessage, error) {
				calls++
				return json.Marshal(struct {
					Win int `json:"win"`
					Bet int `json:"bet"`
				}{r.TotalWin, r.Bet})
			}))
			native := len(nativeOutputs(tuner.config.Plans[0].Output).Format) > 0
			if !native {
				tuner.writerFactory = func(OutputOptions) ArtifactPublisher { t.Fatal("native publisher called"); return nil }
			}
			result, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
			if err != nil || !result.Succeeded() {
				t.Fatalf("%+v %v", result, err)
			}
			expectedCalls := 0
			for _, r := range result.Report.RGSExports {
				if r.State != "COMPLETED" || r.Records != 256 || r.Converter != "custom" {
					t.Fatalf("%+v", r)
				}
				expectedCalls += int(r.Records)
				var rows []collectionDescriptorRow
				if r.Family == "collected" {
					rows, _ = readDescriptor(t, result.Report.Collection.Descriptor.Path)
				} else {
					if len(r.Files) != 2 || r.Verification == nil || !r.Verification.Pass {
						t.Fatalf("%+v", r)
					}
					rows, _ = readDescriptor(t, r.Files[1].Path)
				}
				if !strings.HasSuffix(r.Files[0].Path, "results.jsonl.zst") {
					t.Fatal(r.Files)
				}
				lines := readRGSJSON(t, r.Files[0].Path)
				if len(lines) != len(rows) {
					t.Fatal("count mismatch")
				}
				var sum compensatedSum
				for i, line := range lines {
					var payout struct {
						Win int64
						Bet int64
					}
					if err := json.Unmarshal(line, &payout); err != nil {
						t.Fatal(err)
					}
					row := rows[i]
					if row.RecordIndex != int64(i) || row.Win != payout.Win || row.Bet != payout.Bet || row.WinMultiplier != float64(payout.Win)/float64(payout.Bet) {
						t.Fatal("pairing mismatch")
					}
					if r.Family == "collected" {
						if row.Probability != nil {
							t.Fatal("collected probability")
						}
					} else {
						if row.Probability == nil {
							t.Fatal("missing probability")
						}
						sum.Add(*row.Probability)
					}
				}
				if r.Family == "optimized" && math.Abs(sum.Value()-1) > 1e-12 {
					t.Fatal(sum.Value())
				}
			}
			if calls != expectedCalls {
				t.Fatalf("converter %d != %d", calls, expectedCalls)
			}
			if len(formats) == 1 && formats[0] == OutputRGSCollected {
				if result.Status != StatusExported || result.Report.OptimizationState != "NOT_REQUESTED" || len(result.Report.Modes) != 0 || result.Report.Verification.Pass || result.Report.Candidates.Generated != 0 {
					t.Fatalf("%+v", result.Report)
				}
				for _, e := range recorder.events {
					if strings.HasPrefix(e.Stage, "prepare") || strings.HasPrefix(e.Stage, "solve") || strings.HasPrefix(e.Stage, "compile") {
						t.Fatal(e)
					}
				}
			} else if result.Report.OptimizationState != "COMPLETED" {
				t.Fatal(result.Report.OptimizationState)
			}
			if native && (result.Report.SolutionHash != base.Report.SolutionHash || result.Report.Collection.Bank.SHA256 != base.Report.Collection.Bank.SHA256) {
				t.Fatal("mixed changed native math/collection")
			}
			for _, mode := range result.Report.Modes {
				want := DistributionSourcePointProbabilities
				if native {
					want = DistributionSourceAliasMarginals
				}
				if mode.Distribution.Source != want {
					t.Fatalf("distribution source=%q want=%q", mode.Distribution.Source, want)
				}
			}
		})
	}
}

func TestRGSConverterValidationAndTransactions(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "rgs-faults")
	for _, test := range []struct {
		name string
		raw  json.RawMessage
		fail bool
	}{
		{"pretty", []byte(" {\n \"text\": \"測試\\nline\" } "), false},
		{"null", []byte("null"), false}, {"scalar", []byte("42"), false},
		{"large", json.RawMessage(strconvQuote(strings.Repeat("x", 100000))), false},
		{"empty", nil, true}, {"invalid", []byte("{"), true}, {"multiple", []byte("{}{}"), true}, {"utf8", []byte{'"', 0xff, '"'}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected, OutputRGSOptimized}, WithResultConverter(func(dto.SpinResult) (json.RawMessage, error) { return test.raw, nil }))
			result, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
			if test.fail {
				if err == nil || result.Status != StatusInternalError || result.Report.RGSExports[0].State != "FAILED" || result.Report.RGSExports[1].State != "SKIPPED" || len(result.Report.RGSExports[0].Files) != 0 || result.Report.OptimizationState != "NOT_STARTED" {
					t.Fatalf("%+v %v", result, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var compact bytes.Buffer
				if err := json.Compact(&compact, test.raw); err != nil {
					t.Fatal(err)
				}
				for _, r := range result.Report.RGSExports {
					for _, line := range readRGSJSON(t, r.Files[0].Path) {
						if !bytes.Equal(line, compact.Bytes()) {
							t.Fatal("changed payload")
						}
					}
				}
			}
		})
	}
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected}, WithResultConverter(func(dto.SpinResult) (json.RawMessage, error) { cancel(); return []byte("{}"), nil }))
		r, err := tuner.Run(ctx, RunRequest{PlanID: base.Report.Plan.Plan.ID})
		if !errors.Is(err, context.Canceled) || r.Report.RGSExports[0].Records != 0 || len(r.Report.RGSExports[0].Files) != 0 {
			t.Fatalf("%+v %v", r, err)
		}
	})
	t.Run("B-fails-keeps-C", func(t *testing.T) {
		calls := 0
		tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected, OutputRGSOptimized, OutputOptimalGacha}, WithResultConverter(func(dto.SpinResult) (json.RawMessage, error) {
			calls++
			if calls > 256 {
				return nil, fmt.Errorf("B conversion failure")
			}
			return []byte("{}"), nil
		}))
		r, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
		if err == nil || r.Report.RGSExports[0].State != "COMPLETED" || r.Report.RGSExports[1].State != "FAILED" || r.Report.Publication != nil {
			t.Fatalf("%+v %v", r, err)
		}
		if _, err := os.Stat(r.Report.RGSExports[0].Files[0].Path); err != nil {
			t.Fatal(err)
		}
	})
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestRGSRequiredCatalogAndIOFailures(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "rgs-io")
	for _, fault := range []string{"catalog", "create", "write", "sync", "close", "rename", "post-rename-sync"} {
		t.Run(fault, func(t *testing.T) {
			tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected})
			switch fault {
			case "catalog":
				tuner.collectionDescriptorExporter = func(context.Context, CollectedProblem, CollectionBankReport) (CollectionDescriptorReport, error) {
					return CollectionDescriptorReport{}, fmt.Errorf("catalog fault")
				}
			case "create":
				tuner.rgsIO.create = func(string) (descriptorFile, error) { return nil, fmt.Errorf("create fault") }
			case "write", "sync", "close":
				tuner.rgsIO.create = func(path string) (descriptorFile, error) {
					f, e := os.Create(path)
					if e != nil {
						return nil, e
					}
					return &descriptorFaultFile{File: f, fault: fault}, nil
				}
			case "rename":
				tuner.rgsIO.rename = func(string, string) error { return fmt.Errorf("rename fault") }
			case "post-rename-sync":
				tuner.rgsIO.syncDir = func(path string) error {
					if strings.HasPrefix(filepath.Base(path), "export_") {
						return fmt.Errorf("sync fault")
					}
					return nil
				}
			}
			r, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
			if err == nil {
				t.Fatal("fault succeeded")
			}
			export := r.Report.RGSExports[0]
			if fault == "post-rename-sync" {
				if export.State != "COMMITTED_UNSYNCED" || len(export.Files) != 1 {
					t.Fatalf("%+v", export)
				}
			} else if len(export.Files) != 0 {
				t.Fatal(export)
			}
			if r.Report.Collection == nil {
				t.Fatal("lost saved bank")
			}
		})
	}
}

func TestRGSProbabilityAndSchema(t *testing.T) {
	for _, p := range [][]float64{{-.1, 1.1}, {math.NaN(), 1}, {math.Inf(1), 0}, {0, 0}, {.1, .1}} {
		samples := make([]MaterializedSample, len(p))
		for i := range p {
			samples[i].Probability = p[i]
		}
		if _, err := normalizeRGSProbabilities(samples, 1e-9); err == nil {
			t.Fatal(p)
		}
	}
	p, err := normalizeRGSProbabilities([]MaterializedSample{{Probability: 0}, {Probability: 1 + 1e-10}}, 1e-9)
	if err != nil || !reflect.DeepEqual(p, []float64{0, 1}) {
		t.Fatalf("%v %v", p, err)
	}
	zero := 0.0
	rows := []collectionDescriptorRow{{Win: 1<<53 + 1, Bet: 30, WinMultiplier: math.SmallestNonzeroFloat64}, {RecordIndex: 1, Probability: &zero, Win: 50, Bet: 30, WinMultiplier: 50.0 / 30}}
	path := filepath.Join(t.TempDir(), "bits.parquet")
	if err := parquet.WriteFile(path, rows); err != nil {
		t.Fatal(err)
	}
	got, _ := readDescriptor(t, path)
	if got[0].Probability != nil || got[1].Probability == nil || *got[1].Probability != 0 || got[0].Win != rows[0].Win || math.Float64bits(got[0].WinMultiplier) != math.Float64bits(rows[0].WinMultiplier) {
		t.Fatal(got)
	}
}

func TestRGSIdentityReplayStateAndExactPayout(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "rgs-state")
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected}, WithResultConverter(nil))
	r, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	lines := readRGSJSON(t, r.Report.RGSExports[0].Files[0].Path)
	bank, err := os.ReadFile(r.Report.Collection.Bank.Path)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := readDescriptor(t, r.Report.Collection.Descriptor.Path)
	replay, err := newRGSReplay(tuner.lab, r.Report.Plan, int(rows[0].Bet))
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range lines {
		snapshot := bank[i*replay.snapshotLength : (i+1)*replay.snapshotLength]
		if replay.previous != nil {
			replay.previous.State.Checkpoint = json.RawMessage(`{"old":true}`)
			replay.machine.SpinRequest.Choice = 99
			replay.machine.SpinRequest.HasChoice = true
			replay.machine.SpinRequest.Cycle = 4
			replay.machine.SpinRequest.StartState = &buf.StartState{Checkpoint: "stale"}
		}
		actual, e := replay.spin(context.Background(), snapshot, rows[i].WinMultiplier)
		if e != nil {
			t.Fatal(e)
		}
		if int64(actual.TotalWin) != rows[i].Win {
			t.Fatal("integer payout changed")
		}
		d, e := dto.NewSpinResultDTO(actual)
		if e != nil {
			t.Fatal(e)
		}
		expected, e := json.Marshal(d)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(expected, line) || d.State.StartCoreSnapB64U != corefmt.EncodeBase64URL(snapshot) || d.State.AfterCoreSnapB64U == "" {
			t.Fatalf("replay identity mismatch at %d", i)
		}
	}
	// Replaying the same legacy raw bank populates TotalWin directly.
	plan := r.Report.Plan
	plan.Plan.Collection.CollectedSeed = []string{r.Report.Collection.Bank.Path}
	collector := NewCollector(tuner.lab)
	collector.WorkingDirectory = t.TempDir()
	c, diags, e := collector.Collect(context.Background(), plan, 0)
	if e != nil || diags.StopsRun() {
		t.Fatalf("%v %v", e, diags)
	}
	i := 0
	e = visitCanonicalCollectionSamples(c, func(_, _ int, s CollectedSample) error {
		if s.TotalWin != rows[i].Win {
			t.Fatal("legacy replay integer payout changed")
		}
		i++
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	prepared, diags, e := PrepareProblem(plan, c)
	if e != nil || diags.StopsRun() {
		t.Fatalf("prepare: %v %v", e, diags)
	}
	for _, class := range prepared.Classes {
		for _, bucket := range class.Buckets {
			for _, sample := range bucket.Samples {
				if sample.TotalWin != rows[sample.Sequence].Win {
					t.Fatal("Prepare lost raw integer payout")
				}
			}
		}
	}
}

func TestRGSPostCollectionFailureRetainsReports(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "rgs-later-failure")
	for _, kind := range []string{"prepare", "publish", "B-second-file", "B-close"} {
		t.Run(kind, func(t *testing.T) {
			tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected, OutputRGSOptimized, OutputOptimalGacha})
			switch kind {
			case "prepare":
				intent := tuner.config.Intents[base.Report.Plan.Plan.Intent]
				intent.Classes[0].Design.Risk = &RiskIntent{Rounds: 100, Collision: CollisionIntent{Max: .000001}}
				tuner.config.Intents[base.Report.Plan.Plan.Intent] = intent
			case "publish":
				tuner.writerFactory = func(OutputOptions) ArtifactPublisher { return nil }
			case "B-second-file", "B-close":
				tuner.rgsIO.create = func(path string) (descriptorFile, error) {
					if filepath.Base(path) == "distribution.parquet" {
						if kind == "B-second-file" {
							return nil, fmt.Errorf("second file fault")
						}
						f, e := os.Create(path)
						if e != nil {
							return nil, e
						}
						return &descriptorFaultFile{File: f, fault: "close"}, nil
					}
					return os.Create(path)
				}
			}
			r, e := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
			if r.Succeeded() || r.Report.RGSExports[0].State != "COMPLETED" {
				t.Fatalf("%+v %v", r, e)
			}
			if kind == "publish" && (r.Report.OptimizationState != "COMPLETED" || r.Report.RGSExports[1].State != "COMPLETED") {
				t.Fatal("publication failure lost math completion")
			}
			if _, e := os.Stat(r.Report.RGSExports[0].Files[0].Path); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestRGSPythonInteroperability(t *testing.T) {
	python := os.Getenv("PROBLAB_PARQUET_PYTHON")
	if python == "" {
		t.Skip("set PROBLAB_PARQUET_PYTHON")
	}
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "rgs-python")
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected, OutputRGSOptimized})
	r, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	script := `import sys,json,math,io,struct,tempfile,pathlib
import pandas as pd
import pyarrow.parquet as pq
import zstandard as z
a,b,c,d=sys.argv[1:]
assert pq.read_schema(a)==pq.read_schema(c)
for path,results,optimized in [(a,b,False),(c,d,True)]:
 t=pd.read_parquet(path,engine="pyarrow",dtype_backend="pyarrow")
 assert t.probability.isna().all() if not optimized else t.probability.notna().all()
 if optimized: assert abs(math.fsum(t.probability)-1)<1e-12
 with open(results,"rb") as f:
  with z.ZstdDecompressor().stream_reader(f) as stream:
   lines=list(io.TextIOWrapper(stream,encoding="utf-8"))
 assert len(lines)==len(t)
 for i,line in enumerate(lines):
  row=t.iloc[i];r=json.loads(line)
  assert row.record_index==i and row.win==r["win"] and row.bet==r["bet"]
  assert row.win_multiplier==r["win"]/r["bet"]
 with tempfile.TemporaryDirectory() as directory:
  reordered=pathlib.Path(directory)/"reordered.parquet"
  t.iloc[::-1].to_parquet(reordered,engine="pyarrow",compression="zstd",index=False)
  shuffled=pd.read_parquet(reordered,engine="pyarrow",dtype_backend="pyarrow")
  assert shuffled.iloc[0].record_index==len(t)-1
  for _,row in shuffled.iterrows():
   result=json.loads(lines[int(row.record_index)])
   assert row.win==result["win"] and row.bet==result["bet"]
 print("records",len(t),"optimized",optimized,"reordered pairing verified")
`
	out, e := exec.Command(python, "-c", script, r.Report.Collection.Descriptor.Path, r.Report.RGSExports[0].Files[0].Path, r.Report.RGSExports[1].Files[1].Path, r.Report.RGSExports[1].Files[0].Path).CombinedOutput()
	if e != nil {
		t.Fatalf("%v: %s", e, out)
	}
	t.Log(string(out))
}

func TestRGSStreamingMemoryProfile(t *testing.T) {
	if os.Getenv("PROBLAB_RGS_MEMORY") != "1" {
		t.Skip("set PROBLAB_RGS_MEMORY=1")
	}
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "rgs-memory")
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected}, WithResultConverter(func(r dto.SpinResult) (json.RawMessage, error) { return json.Marshal(struct{ Win int }{r.TotalWin}) }))
	c, diags, err := NewCollector(tuner.lab).Collect(context.Background(), base.Report.Plan, 0)
	if err != nil || diags.StopsRun() {
		t.Fatalf("%v %v", err, diags)
	}
	sample := c.Classes[0].Samples[0]
	for _, n := range []uint64{1000, 100000, 1000000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var file *descriptorHeapFile
			tuner.rgsIO.create = func(path string) (descriptorFile, error) {
				f, e := os.Create(path)
				if e != nil {
					return nil, e
				}
				file = &descriptorHeapFile{File: f}
				return file, nil
			}
			root := ""
			r := RGSExportReport{Family: "collected", Total: n}
			rows := func(visit func(rgsSourceRow) error) error {
				for i := uint64(0); i < n; i++ {
					if e := visit(rgsSourceRow{row: collectionDescriptorRow{RecordIndex: int64(i), WinMultiplier: sample.Win, Bet: int64(c.BetUnit), Win: sample.TotalWin}, snapshot: sample.Snapshot}); e != nil {
						return e
					}
				}
				return nil
			}
			start := time.Now()
			if e := tuner.exportRGS(context.Background(), base.Report.Plan, c.BetUnit, &root, &r, rows, nil); e != nil {
				t.Fatal(e)
			}
			t.Logf("records=%d time=%s compressed_bytes=%d live_heap=%d CollectedSample_bytes=%d", n, time.Since(start), r.Files[0].Bytes, file.peak, unsafe.Sizeof(CollectedSample{}))
		})
	}
}

func TestRGSPointSemanticDamage(t *testing.T) {
	compiled, solution := verificationTestFixture(t)
	source, err := ExpandSolution(compiled, solution, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"valid", "probability", "class", "bucket", "win", "snapshot"} {
		samples := append([]MaterializedSample(nil), source...)
		p, err := normalizeRGSProbabilities(samples, 1e-9)
		if err != nil {
			t.Fatal(err)
		}
		switch kind {
		case "probability":
			p[0] += .01
			p[1] -= .01
		case "class":
			samples[0].ClassID = "unknown"
		case "bucket":
			samples[0].BucketIndex = 99
		case "win":
			samples[0].Win++
		case "snapshot":
			samples[0].Snapshot = []byte{99}
		}
		v := verifyRGSPoints(compiled, solution, samples, p)
		if v.Pass != (kind == "valid") {
			t.Fatalf("%s: %+v", kind, v)
		}
	}
}

func TestRGSOutputFormatValidation(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "rgs-config")
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected})
	for _, formats := range [][]OutputFormat{{}, {"rgs-customer"}, {"rgs-collected-zstd"}, {"rgs-optimized-zstd"}, {OutputRGSCollected, OutputRGSCollected}} {
		c := tuner.config
		c.Plans = append([]RunPlan(nil), c.Plans...)
		c.Plans[0].Output.Format = formats
		if err := c.Validate(); err == nil {
			t.Fatalf("accepted %v", formats)
		}
	}
	if !StatusExported.Valid() || !StatusExported.Success() || (Diagnostic{Status: StatusExported}).StopsRun() {
		t.Fatal("EXPORTED status contract")
	}
}

func TestCollectionIntegerPayoutAcceptance(t *testing.T) {
	if unsafe.Sizeof(int(0)) < 8 {
		t.Skip("large integer fixture requires 64-bit int")
	}
	for _, win := range []int64{0, 50, 1<<53 + 1} {
		snapshot := []byte{1, 2}
		sample := acceptedCollectionSample("class", float64(win)/30, int(win), snapshot, 7)
		snapshot[0] = 9
		if sample.TotalWin != win || sample.Snapshot[0] != 1 || sample.Sequence != 7 {
			t.Fatal(sample)
		}
		c := CollectedProblem{SnapshotLength: 2, BetUnit: 30, Classes: []CollectedClass{{Intent: ClassIntent{Name: "class"}, Samples: []CollectedSample{sample, sample}}}}
		c.Classes[0].Samples[1].Sequence = 8
		_, recovery, err := auditCollectedReplayIdentities(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if len(recovery.Classes[0].Samples) != 1 || recovery.Classes[0].Samples[0].TotalWin != win {
			t.Fatal("recovery lost raw payout")
		}
	}
}

func TestLegacyCatalogReader(t *testing.T) {
	python := os.Getenv("PROBLAB_PARQUET_PYTHON")
	if python == "" {
		t.Skip("set PROBLAB_PARQUET_PYTHON")
	}
	path := filepath.Join(t.TempDir(), "legacy.parquet")
	script := `import pyarrow as a,pyarrow.parquet as p,sys
p.write_table(a.table({"record_index":a.array([0],type=a.int64()),"class_id":a.array([0],type=a.int32()),"class_name":["legacy"],"win_multiplier":[1.5]}),sys.argv[1])
`
	if out, e := exec.Command(python, "-c", script, path).CombinedOutput(); e != nil {
		t.Fatalf("%v: %s", e, out)
	}
	out, e := exec.Command(python, "examples/collection_parquet/read_catalog.py", path).CombinedOutput()
	if e != nil || !bytes.Contains(out, []byte(`"legacy": true`)) || !bytes.Contains(out, []byte("exact win/bet unavailable")) {
		t.Fatalf("%v: %s", e, out)
	}
}
