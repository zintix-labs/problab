// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"
	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/core"
)

func TestRGSReorderedParquetPairsByRecordIndex(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "reordered")
	index := int64(0)
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSOptimized}, WithResultConverter(func(r dto.SpinResult) (json.RawMessage, error) {
		b, err := json.Marshal(struct {
			Index int64
			Win   int
		}{index, r.TotalWin})
		index++
		return b, err
	}))
	r, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := readDescriptor(t, r.Report.RGSExports[0].Files[1].Path)
	lines := readRGSJSON(t, r.Report.RGSExports[0].Files[0].Path)
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	path := filepath.Join(t.TempDir(), "reordered.parquet")
	if err := parquet.WriteFile(path, rows); err != nil {
		t.Fatal(err)
	}
	shuffled, err := parquet.ReadFile[collectionDescriptorRow](path)
	if err != nil {
		t.Fatal(err)
	}
	if shuffled[0].RecordIndex == 0 {
		t.Fatal("fixture was not reordered")
	}
	for _, row := range shuffled {
		var payload struct {
			Index int64
			Win   int
		}
		if err := json.Unmarshal(lines[row.RecordIndex], &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Index != row.RecordIndex || int64(payload.Win) != row.Win {
			t.Fatal("index pairing lost")
		}
	}
}

func TestUnfinishedCollectionIsNotVerified(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "unfinished")
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputOptimalArtifactV1})
	tuner.config.Plans[0].Collection.MaxSpins = 1
	tuner.config.Plans[0].Collection.Workers = 1
	r, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	if r.Succeeded() || r.Report.Verification.Pass {
		t.Fatalf("unexpected verification: %+v", r.Report.Verification)
	}
}

func TestRGSStagingCreationFailureRemovesEmptyRunDirectory(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "staging-cleanup")
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected})
	var root string
	tuner.rgsIO.mkdirTemp = func(parent, pattern string) (string, error) {
		if pattern != "export_" {
			return "", fmt.Errorf("staging creation fault")
		}
		path, err := os.MkdirTemp(parent, pattern)
		root = path
		return path, err
	}
	_, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
	if err == nil || root == "" {
		t.Fatal("expected staging failure")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("empty run directory retained: %v", err)
	}
}

func TestRGSOptimizationStateAfterVerifiedPublicationFailure(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "completion-boundary")
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			formats := []OutputFormat{OutputRGSOptimized}
			if native {
				formats = append(formats, OutputOptimalGacha)
			}
			recorder := &recordingReporter{}
			tuner := rgsTestTuner(t, base, formats, WithReporter(recorder))
			tuner.rgsIO.rename = func(string, string) error { return fmt.Errorf("commit fault") }
			r, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
			want := "COMPLETED"
			if native {
				want = "FAILED"
			}
			if err == nil || r.Status != StatusInternalError || r.Report.OptimizationState != want {
				t.Fatalf("state=%s err=%v", r.Report.OptimizationState, err)
			}
			b := r.Report.RGSExports[0]
			if b.Verification == nil || !b.Verification.Pass || b.State != "FAILED" || len(b.Files) != 0 {
				t.Fatalf("B evidence=%+v", b)
			}
			for _, event := range recorder.events {
				if strings.HasPrefix(event.Stage, "materialize-verify") {
					t.Fatal("native verification ran after failed B")
				}
			}
		})
	}
}

func TestRGSNativeFilesMatchMixedOutput(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "native-byte-parity")
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected, OutputRGSOptimized, OutputOptimalGacha, OutputOptimalArtifactV1})
	r, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ArtifactPaths) != len(base.ArtifactPaths) {
		t.Fatal("different native file set")
	}
	originals := map[string][]byte{}
	for _, p := range base.ArtifactPaths {
		rel, e := filepath.Rel(base.Report.Plan.Plan.Output.Directory, p)
		if e != nil {
			t.Fatal(e)
		}
		originals[rel], e = os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, p := range r.ArtifactPaths {
		rel, e := filepath.Rel(r.Report.Plan.Plan.Output.Directory, p)
		if e != nil {
			t.Fatal(e)
		}
		actual, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		want, exists := originals[rel]
		if !exists || !bytes.Equal(actual, want) {
			t.Fatalf("native bytes changed: %s", rel)
		}
	}
}

func TestRGSOptimizedMultipleClasses(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "multiple-classes")
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSOptimized})
	plan := base.Report.Plan
	disabled := false
	plan.Plan.Collection = CollectionOptions{Workers: 1, BatchSize: 64, MaxSpins: 10000}
	plan.Intent = cloneMathIntent(plan.Intent)
	plan.Intent.Overall.CV = NumericRange{Min: 0, Max: 1000}
	plan.Intent.Classes = []ClassIntent{
		{Name: "zero", Weight: 500000, Collect: CollectIntent{Samples: 32, WinRange: ClosedInterval{0, 0}}, Design: ClassDesign{Median: ClosedInterval{0, 0}, Subjective: SubjectiveIntent{Intent: &disabled}}},
		{Name: "positive 測試", Weight: 500000, Collect: CollectIntent{Samples: 32, WinRange: ClosedInterval{.00001, 20000}}, Design: ClassDesign{Median: ClosedInterval{.00001, 20000}, Subjective: SubjectiveIntent{Intent: &disabled}}},
	}
	c, diags, err := tuner.collector.Collect(context.Background(), plan, 0)
	if err != nil || diags.StopsRun() {
		t.Fatalf("fixture: %v %v", err, diags)
	}
	for ci, class := range c.Classes {
		var sum compensatedSum
		for _, s := range class.Samples {
			sum.Add(s.Win)
		}
		plan.Intent.Classes[ci].Design.Exp = sum.Value() / float64(len(class.Samples))
	}
	tuner.config.Plans[0].Collection = plan.Plan.Collection
	tuner.config.Intents[plan.Plan.Intent] = plan.Intent
	if err := tuner.config.Validate(); err != nil {
		t.Fatal(err)
	}
	r, err := tuner.Run(context.Background(), RunRequest{PlanID: plan.Plan.ID})
	if err != nil || !r.Succeeded() {
		t.Fatalf("%v %v", err, r.Diagnostics)
	}
	rows, _ := readDescriptor(t, r.Report.RGSExports[0].Files[1].Path)
	if len(rows) != 64 {
		t.Fatal(len(rows))
	}
	for i, row := range rows {
		ci := i / 32
		if row.ClassID != int32(ci) || row.ClassName != plan.Intent.Classes[ci].Name || row.RecordIndex != int64(i) {
			t.Fatalf("row %+v", row)
		}
	}
	// Adapter-level sparse support preserves original declaration ordinals:
	// a successful collector cannot have an empty quota-positive Class.
	compiled := CompiledModel{Prepared: PreparedProblem{Plan: plan, Classes: []PreparedClass{{}, {}, {}}}}
	empty := ClassIntent{Name: "empty", Collect: CollectIntent{WinRange: ClosedInterval{0, 20000}}}
	compiled.Prepared.Plan.Intent.Classes = []ClassIntent{plan.Intent.Classes[0], empty, plan.Intent.Classes[1]}
	samples := []MaterializedSample{{ClassID: "zero"}, {ClassID: "positive 測試"}}
	iterator, err := optimizedRGSRows(compiled, samples, []float64{.5, .5})
	if err != nil {
		t.Fatal(err)
	}
	ids := []int32{}
	if err := iterator(func(row rgsSourceRow) error { ids = append(ids, row.row.ClassID); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 0 || ids[1] != 2 {
		t.Fatal(ids)
	}
	samples[1].ClassID = "missing"
	iterator, err = optimizedRGSRows(compiled, samples, []float64{.5, .5})
	if err != nil {
		t.Fatal(err)
	}
	if err = iterator(func(rgsSourceRow) error { return nil }); err == nil || !strings.Contains(err.Error(), "unknown Class") {
		t.Fatal(err)
	}
	compiled.Prepared.Plan.Intent.Classes[1].Name = "zero"
	if _, err = optimizedRGSRows(compiled, samples, []float64{.5, .5}); err == nil || !strings.Contains(err.Error(), "ambiguous Class") {
		t.Fatal(err)
	}
}

type replayFaultFactory struct {
	core.PRNGFactory
	fault string
	cause error
}

func (f *replayFaultFactory) New(seed []byte) (core.PRNG, error) {
	rng, err := f.PRNGFactory.New(seed)
	if err != nil {
		return nil, err
	}
	return &replayFaultPRNG{PRNG: rng, factory: f}, nil
}

type replayFaultPRNG struct {
	core.PRNG
	factory  *replayFaultFactory
	restored bool
}

func (r *replayFaultPRNG) SnapshotFormat() string { return core.SnapshotFormatOfPRNG(r.PRNG) }
func (r *replayFaultPRNG) Restore(b []byte) error {
	if r.factory.fault == "restore" {
		return r.factory.cause
	}
	err := r.PRNG.Restore(b)
	if err == nil {
		r.restored = true
	}
	return err
}
func (r *replayFaultPRNG) Snapshot() ([]byte, error) {
	if r.factory.fault == "snapshot" && r.restored {
		return nil, r.factory.cause
	}
	return r.PRNG.Snapshot()
}

type auditReporterFunc func(StageEvent)

func (f auditReporterFunc) Report(event StageEvent) { f(event) }

func TestRGSReplayErrorsPreserveOperationalCause(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "replay-errors")
	for _, kind := range []string{"restore", "snapshot"} {
		t.Run(kind, func(t *testing.T) {
			cause := errors.New("third-party dependency failure")
			factory := &replayFaultFactory{PRNGFactory: core.Default(), cause: cause}
			lab, err := problab.NewAuto(factory, problab.Configs(demo_configs.FS), problab.Logics(demo_logic.Logics))
			if err != nil {
				t.Fatal(err)
			}
			defer lab.Close()
			tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSOptimized}, WithReporter(auditReporterFunc(func(event StageEvent) {
				if event.Stage == "rgs-optimized" && event.State == "started" {
					factory.fault = kind
				}
			})))
			tuner.lab = lab
			tuner.collector.Lab = lab
			r, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID})
			if !errors.Is(err, cause) || r.Status != StatusInternalError {
				t.Fatalf("status=%s err=%v", r.Status, err)
			}
			if r.Report.RGSExports[0].State != "FAILED" || len(r.Report.RGSExports[0].Files) != 0 {
				t.Fatal(r.Report.RGSExports)
			}
		})
	}
}

func TestRGSReplayMismatchClassification(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "mismatch")
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSOptimized})
	c, diags, err := tuner.collector.Collect(context.Background(), base.Report.Plan, 0)
	if err != nil || diags.StopsRun() {
		t.Fatalf("%v %v", err, diags)
	}
	for _, kind := range []string{"length", "multiplier"} {
		s := c.Classes[0].Samples[0]
		if kind == "length" {
			s.Snapshot = s.Snapshot[:1]
		} else {
			s.Win++
		}
		r := RGSExportReport{Family: "optimized", Total: 1}
		root := ""
		p := 1.0
		err = tuner.exportRGS(context.Background(), base.Report.Plan, c.BetUnit, &root, &r, func(visit func(rgsSourceRow) error) error {
			return visit(rgsSourceRow{row: collectionDescriptorRow{Probability: &p, WinMultiplier: s.Win}, snapshot: s.Snapshot})
		}, nil)
		var invalid *rgsVerificationError
		if !errors.As(err, &invalid) {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	d := rgsPointViolationDiagnostic()
	if strings.Contains(d.Message, "alias") || strings.Contains(d.Message, "nothing was") {
		t.Fatal(d.Message)
	}
}
