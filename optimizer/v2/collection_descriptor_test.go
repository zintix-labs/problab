// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/uncompressed"
	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/spec"
)

func descriptorFixture() CollectedProblem {
	c := CollectedProblem{Game: spec.GID(^uint(0)), BetUnit: 100, SnapshotLength: 1}
	for i, name := range []string{"first 測試", "empty", "last"} {
		c.Classes = append(c.Classes, CollectedClass{Intent: ClassIntent{Name: name,
			Collect: CollectIntent{Samples: 10, WinRange: ClosedInterval{0, 100}, Tags: TagFilters{Matches: []string{"tag"}}}}})
		var wins []float64
		if i == 0 {
			wins = []float64{99, math.Copysign(0, -1), .125, math.SmallestNonzeroFloat64}
		}
		if i == 2 {
			wins = []float64{5, 5}
		}
		for j, win := range wins {
			c.Classes[i].Samples = append(c.Classes[i].Samples, CollectedSample{ClassID: name, Win: win, Snapshot: []byte{byte(i*10 + j + 1)}, Sequence: uint64(j*7 + 2)})
		}
	}
	c.Evidence = CollectionEvidence{
		ReplaySources: []CollectionReplaySourceReport{{ConfiguredPath: "input.bin", ResolvedPath: "/fixture/input.bin", State: CollectionReplayUsed, EndReason: CollectionReplayEndExhausted, TotalRecords: 3, Records: 3, Accepted: 2, Duplicates: 1, StreamCursor: 4, StreamCursorRecognized: true}},
		Classes: []CollectionClassEvidence{
			{Name: "first 測試", Requested: 10, ReplayAccepted: 2, FreshAccepted: 2, Accepted: 4},
			{Name: "empty", Requested: 10},
			{Name: "last", Requested: 10, FreshAccepted: 2, Accepted: 2},
		}, ReplayRecords: 3, ReplayDuplicates: 1, ReplayAccepted: 2, FreshSpins: 9, FreshAccepted: 4,
	}
	c.Spins = 9
	return c
}

func saveDescriptorFixture(t *testing.T, c CollectedProblem, partial, distinct bool) CollectionBankReport {
	t.Helper()
	path := filepath.Join(t.TempDir(), "seed_bank_123_s4.bin")
	if distinct {
		path = strings.TrimSuffix(path, ".bin") + ".distinct.bin"
	}
	bank, err := (collectionBankWriter{}).Write(context.Background(), path, c, partial, distinct)
	if err != nil {
		t.Fatal(err)
	}
	return bank
}

func readDescriptor(t *testing.T, path string) ([]collectionDescriptorRow, collectionDescriptorMetadata) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parquet.OpenFile(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	fields := file.Schema().Fields()
	want := []string{"record_index", "class_id", "class_name", "win_multiplier"}
	if len(fields) != len(want) {
		t.Fatal(file.Schema())
	}
	for i, field := range fields {
		if field.Name() != want[i] || !field.Required() {
			t.Fatal(file.Schema())
		}
	}
	if fields[0].Type().Kind() != parquet.Int64 || fields[1].Type().Kind() != parquet.Int32 || fields[2].Type().LogicalType().String() != "STRING" || fields[3].Type().Kind() != parquet.Double {
		t.Fatal(file.Schema())
	}
	value, ok := file.Lookup(collectionDescriptorKey)
	if !ok {
		t.Fatal("missing application metadata")
	}
	var metadata collectionDescriptorMetadata
	if err := json.Unmarshal([]byte(value), &metadata); err != nil {
		t.Fatal(err)
	}
	rows, err := parquet.Read[collectionDescriptorRow](bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return rows, metadata
}

func assertDescriptorMatchesBank(t *testing.T, report CollectionRunReport) {
	t.Helper()
	d := report.Descriptor
	if d == nil || d.State != "COMPLETED" || d.Error != "" {
		t.Fatalf("descriptor=%+v", d)
	}
	rows, meta := readDescriptor(t, d.Path)
	if uint64(len(rows)) != report.Bank.SeedCount || d.Records != report.Bank.SeedCount || meta.BankSHA256 != report.Bank.SHA256 || meta.Distinct != report.Bank.Distinct || meta.Partial != report.Bank.Partial || meta.DatasetID != d.DatasetID {
		t.Fatalf("descriptor/bank mismatch: %+v %+v", meta, report.Bank)
	}
	if len(meta.Classes) != len(report.Evidence.Classes) {
		t.Fatal("Class metadata missing")
	}
	for i, class := range meta.Classes {
		if class.Requested != report.Evidence.Classes[i].Requested {
			t.Fatalf("Class %d original quota changed: %+v", i, class)
		}
		var retained uint64
		for _, row := range rows {
			if row.ClassID == int32(i) {
				retained++
			}
		}
		if class.Retained != retained {
			t.Fatalf("Class %d retained=%d rows=%d", i, class.Retained, retained)
		}
	}
}

func TestCollectionDescriptorSchemaOrderFloatBitsAndImmutability(t *testing.T) {
	c := descriptorFixture()
	if len(c.Evidence.ReplaySources) == 0 || len(c.Evidence.Classes) == 0 || c.Evidence.ReplayAccepted == 0 || c.Evidence.FreshAccepted == 0 {
		t.Fatal("immutability fixture must exercise non-empty evidence")
	}
	before, _ := json.Marshal(c)
	bank := saveDescriptorFixture(t, c, true, false)
	d, err := (collectionDescriptorWriter{}).Write(context.Background(), c, bank)
	if err != nil {
		t.Fatal(err)
	}
	rows, m := readDescriptor(t, d.Path)
	if m.Game != fmt.Sprint(uint64(c.Game)) || len(m.Classes) != 3 || m.Classes[1].Retained != 0 || m.Classes[0].Tags.Mismatches == nil {
		t.Fatalf("metadata=%+v", m)
	}
	index := 0
	_ = visitCanonicalCollectionSamples(c, func(ci, _ int, s CollectedSample) error {
		row := rows[index]
		if row.RecordIndex != int64(index) || row.ClassID != int32(ci) || row.ClassName != s.ClassID || math.Float64bits(row.WinMultiplier) != math.Float64bits(s.Win) {
			t.Fatalf("row %d=%+v sample=%+v", index, row, s)
		}
		index++
		return nil
	})
	after, _ := json.Marshal(c)
	if !bytes.Equal(before, after) {
		t.Fatal("export mutated collection")
	}
	info, _ := os.Stat(d.Path)
	if d.Bytes != info.Size() || d.Duration <= 0 {
		t.Fatalf("I/O report=%+v", d)
	}
}

func TestCollectionDescriptorZeroRows(t *testing.T) {
	c := descriptorFixture()
	for i := range c.Classes {
		c.Classes[i].Samples = nil
	}
	bank := saveDescriptorFixture(t, c, true, false)
	d, err := (collectionDescriptorWriter{}).Write(context.Background(), c, bank)
	if err != nil {
		t.Fatal(err)
	}
	rows, m := readDescriptor(t, d.Path)
	if len(rows) != 0 || len(m.Classes) != 3 || m.RecordCount != 0 {
		t.Fatalf("metadata=%+v rows=%v", m, rows)
	}
}

func TestDatasetIDGolden(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("golden exercises uint64 GID on a 64-bit platform")
	}
	c := descriptorFixture()
	bank := saveDescriptorFixture(t, c, true, false)
	d, err := (collectionDescriptorWriter{}).Write(context.Background(), c, bank)
	if err != nil {
		t.Fatal(err)
	}
	// Independently framed with Python struct.pack from the explicit fixture:
	// bank bytes 01 02 03 04 15 16, GID=2^64-1, flags=(true,false).
	const want = "sha256:e7cb652f76918ffd01c5667f2ea2d307a88539387a3305f37a02ee2f38c4ee2a"
	if d.DatasetID != want {
		t.Fatalf("Dataset ID=%s want=%s", d.DatasetID, want)
	}
}

func TestDatasetIDScope(t *testing.T) {
	base := descriptorFixture()
	bank := saveDescriptorFixture(t, base, true, false)
	write := func(c CollectedProblem, b CollectionBankReport) string {
		t.Helper()
		d, err := (collectionDescriptorWriter{}).Write(context.Background(), c, b)
		if err != nil {
			t.Fatal(err)
		}
		return d.DatasetID
	}
	id := write(base, bank)
	for _, test := range []struct {
		name   string
		change func(*CollectedProblem, *CollectionBankReport)
		same   bool
	}{
		{"path", func(_ *CollectedProblem, b *CollectionBankReport) {
			b.Path = filepath.Join(filepath.Dir(b.Path), "other.bin")
		}, true},
		{"timestamp", func(_ *CollectedProblem, b *CollectionBankReport) {
			b.Path = filepath.Join(filepath.Dir(b.Path), "seed_bank_999999999_s4.bin")
		}, true},
		{"LP", func(c *CollectedProblem, _ *CollectionBankReport) {
			c.Classes[0].Intent.Weight++
			c.Classes[0].Intent.Design.Exp++
		}, true},
		{"sequence", func(c *CollectedProblem, _ *CollectionBankReport) {
			for i := range c.Classes[0].Samples {
				c.Classes[0].Samples[i].Sequence += 100
			}
		}, true},
		{"win", func(c *CollectedProblem, _ *CollectionBankReport) { c.Classes[0].Samples[0].Win = 98 }, false},
		{"predicate", func(c *CollectedProblem, _ *CollectionBankReport) {
			c.Classes[0].Intent.Collect.Tags.Matches = []string{"another"}
		}, false},
		{"quota", func(c *CollectedProblem, _ *CollectionBankReport) { c.Classes[0].Intent.Collect.Samples++ }, false},
		{"flags", func(_ *CollectedProblem, b *CollectionBankReport) { b.Distinct = true }, false},
		{"digest", func(_ *CollectedProblem, b *CollectionBankReport) { b.SHA256 = strings.Repeat("0", 64) }, false},
		{"game", func(c *CollectedProblem, _ *CollectionBankReport) { c.Game-- }, false},
		{"Class-order", func(c *CollectedProblem, _ *CollectionBankReport) {
			c.Classes[0], c.Classes[2] = c.Classes[2], c.Classes[0]
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := descriptorFixture()
			b := bank
			test.change(&c, &b)
			if (write(c, b) == id) != test.same {
				t.Fatal("unexpected Dataset ID equality")
			}
		})
	}
}

type descriptorFaultFile struct {
	*os.File
	fault  string
	cancel context.CancelFunc
}

// Arms only after metadata validation, so cancellation is injected into the
// actual row/batch traversal, not the preceding validation pass or Close I/O.
type descriptorCheckpointContext struct {
	context.Context
	cancel           context.CancelFunc
	armed            bool
	checks, cancelAt int
}

func (c *descriptorCheckpointContext) Err() error {
	if c.armed {
		c.checks++
		if c.checks == c.cancelAt {
			c.cancel()
		}
	}
	return c.Context.Err()
}

func TestDescriptorCancellation(t *testing.T) {
	for _, test := range []struct {
		name       string
		checkpoint int
	}{
		{"next-row-after-first-batch", collectionDescriptorBatch + 2},
		{"second-batch-flush", 2*collectionDescriptorBatch + 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := descriptorFixture()
			c.Classes[0].Samples = make([]CollectedSample, 3*collectionDescriptorBatch)
			for i := range c.Classes[0].Samples {
				c.Classes[0].Samples[i] = CollectedSample{ClassID: c.Classes[0].Intent.Name, Win: float64(i % 100), Snapshot: []byte{byte(i)}, Sequence: uint64(i)}
			}
			bank := saveDescriptorFixture(t, c, true, false)
			bankBytes, err := os.ReadFile(bank.Path)
			if err != nil {
				t.Fatal(err)
			}
			before, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &descriptorCheckpointContext{Context: base, cancel: cancel, cancelAt: test.checkpoint}
			writer := collectionDescriptorWriter{createTemp: func(dir, pattern string) (descriptorFile, error) {
				file, err := os.CreateTemp(dir, pattern)
				ctx.armed = true
				return file, err
			}}
			d, err := writer.Write(ctx, c, bank)
			if !errors.Is(err, context.Canceled) || d.State != "WARNING" || d.DatasetID != "" || !strings.Contains(err.Error(), "stream descriptor") {
				t.Fatalf("not an interrupted row stream: %+v %v", d, err)
			}
			if ctx.checks != test.checkpoint || ctx.checks <= collectionDescriptorBatch {
				t.Fatalf("unbounded or premature stop: checks=%d", ctx.checks)
			}
			after, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("cancel mutated collection/evidence")
			}
			saved, err := os.ReadFile(bank.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(saved, bankBytes) {
				t.Fatal("cancel changed bank")
			}
			entries, err := os.ReadDir(filepath.Dir(bank.Path))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != filepath.Base(bank.Path) {
				t.Fatalf("cancel left output/temp files: %v", entries)
			}
		})
	}
}

func TestCollectionDescriptorPartialAndDistinct(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		t.Run(fmt.Sprint(distinct), func(t *testing.T) {
			c := descriptorFixture()
			if distinct {
				c.Classes[0].Samples = c.Classes[0].Samples[:2]
			} // Recovery retains original evidence and quotas.
			bank := saveDescriptorFixture(t, c, true, distinct)
			d, err := (collectionDescriptorWriter{}).Write(context.Background(), c, bank)
			if err != nil {
				t.Fatal(err)
			}
			_, m := readDescriptor(t, d.Path)
			for i, class := range c.Classes {
				if m.Classes[i].Requested != class.Intent.Collect.Samples || m.Classes[i].Retained != uint64(len(class.Samples)) {
					t.Fatalf("Class %d quota/retained changed: %+v", i, m.Classes[i])
				}
			}
			if !m.Partial || m.Distinct != distinct {
				t.Fatalf("flags=%+v", m)
			}
		})
	}
}

func TestDatasetIDEncodingIndependent(t *testing.T) {
	c := descriptorFixture()
	bank := saveDescriptorFixture(t, c, true, false)
	d, err := (collectionDescriptorWriter{}).Write(context.Background(), c, bank)
	if err != nil {
		t.Fatal(err)
	}
	rows, m := readDescriptor(t, d.Path)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(d.Path), "uncompressed.parquet")
	if err := parquet.WriteFile(path, rows, parquet.Compression(&uncompressed.Codec{}), parquet.MaxRowsPerRowGroup(1), parquet.KeyValueMetadata(collectionDescriptorKey, string(raw))); err != nil {
		t.Fatal(err)
	}
	// Filesystem time is not dataset provenance, either.
	if err := os.Chtimes(path, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	reencoded, metadata := readDescriptor(t, path)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pf, err := parquet.OpenFile(file, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.RowGroups()) != len(rows) {
		t.Fatal("row group variation not exercised")
	}
	if pf.Metadata().RowGroups[0].Columns[0].MetaData.Codec != (&uncompressed.Codec{}).CompressionCodec() {
		t.Fatal("compression variation not exercised")
	}
	originalBytes, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	newBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(originalBytes, newBytes) {
		t.Fatal("encoding did not change")
	}
	_, h, err := descriptorMetadata(context.Background(), c, bank)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range reencoded {
		hashDescriptorRow(h, row)
	}
	if got := "sha256:" + fmt.Sprintf("%x", h.Sum(nil)); got != d.DatasetID || metadata.DatasetID != got {
		t.Fatalf("re-encoded ID=%s want=%s", got, d.DatasetID)
	}
}

func TestCollectorOperationalErrorDoesNotExportDescriptor(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	output := t.TempDir()
	recorder := &recordingReporter{}
	tuner := newCollectionStageTuner(lab, output, recorder, 703)
	plan := collectionStagePlan(77, 2, 2, 2, output)
	plan.Plan.Collection.CollectedSeed = []string{filepath.Join(output, "seed_bank_1_s18446744073709551615.bin")}
	calls := 0
	tuner.collectionDescriptorExporter = func(context.Context, CollectedProblem, CollectionBankReport) (CollectionDescriptorReport, error) {
		calls++
		return CollectionDescriptorReport{}, nil
	}
	report := RunReport{}
	_, _, err = tuner.collectStage(context.Background(), plan, 0, &report)
	if err == nil || !strings.Contains(err.Error(), "overflows uint64") || calls != 0 || report.Collection != nil {
		t.Fatalf("early error exported: calls=%d report=%+v err=%v", calls, report.Collection, err)
	}
	for _, event := range recorder.events {
		if event.Stage == "collection-descriptor" || event.Stage == "collection-bank" {
			t.Fatalf("persistence event on early error: %+v", event)
		}
	}
	if err := filepath.WalkDir(output, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Errorf("early error created file: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *descriptorFaultFile) Write(p []byte) (int, error) {
	if f.cancel != nil {
		f.cancel()
		return 0, context.Canceled
	}
	if f.fault == "write" || f.fault == "footer" && len(p) >= 4 && string(p[len(p)-4:]) == "PAR1" && len(p) > 4 {
		return 0, errors.New("injected " + f.fault)
	}
	return f.File.Write(p)
}
func (f *descriptorFaultFile) Sync() error {
	if f.fault == "sync" {
		return errors.New("injected sync")
	}
	return f.File.Sync()
}
func (f *descriptorFaultFile) Close() error {
	err := f.File.Close()
	if f.fault == "close" {
		return errors.New("injected close")
	}
	return err
}

func TestDescriptorFailuresPreserveBankAndInput(t *testing.T) {
	for _, fault := range []string{"create", "write", "footer", "sync", "close", "rename", "directory-sync", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			c := descriptorFixture()
			before, _ := json.Marshal(c)
			bank := saveDescriptorFixture(t, c, true, false)
			bankBytes, _ := os.ReadFile(bank.Path)
			path := strings.TrimSuffix(bank.Path, ".bin") + ".parquet"
			if err := os.WriteFile(path, []byte("previous catalog"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writer := collectionDescriptorWriter{createTemp: func(dir, pattern string) (descriptorFile, error) {
				if fault == "create" {
					return nil, errors.New("injected create")
				}
				f, err := os.CreateTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				wrapped := &descriptorFaultFile{File: f, fault: fault}
				if fault == "cancel" {
					wrapped.cancel = cancel
				}
				return wrapped, nil
			}}
			if fault == "rename" {
				writer.rename = func(string, string) error { return errors.New("injected rename") }
			}
			if fault == "directory-sync" {
				writer.syncDirectory = func(string) error { return errors.New("injected directory-sync") }
			}
			d, err := writer.Write(ctx, c, bank)
			if err == nil || d.State != "WARNING" || d.Error == "" {
				t.Fatalf("report=%+v err=%v", d, err)
			}
			if fault == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("not context cancellation: %v", err)
			}
			after, _ := json.Marshal(c)
			if !bytes.Equal(before, after) {
				t.Fatal("input mutated")
			}
			saved, _ := os.ReadFile(bank.Path)
			if !bytes.Equal(saved, bankBytes) {
				t.Fatal("bank changed")
			}
			current, _ := os.ReadFile(path)
			if fault == "directory-sync" {
				if !strings.Contains(err.Error(), "visible") || bytes.Equal(current, []byte("previous catalog")) {
					t.Fatal("post-rename visibility missing")
				}
			} else if !bytes.Equal(current, []byte("previous catalog")) {
				t.Fatal("previous catalog replaced on failure")
			}
			entries, _ := os.ReadDir(filepath.Dir(bank.Path))
			if len(entries) != 2 {
				t.Fatalf("temporary files remain: %v", entries)
			}
		})
	}
}

func TestDescriptorValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*CollectedProblem, *CollectionBankReport)
	}{
		{"nan", func(c *CollectedProblem, _ *CollectionBankReport) { c.Classes[0].Samples[0].Win = math.NaN() }},
		{"inf", func(c *CollectedProblem, _ *CollectionBankReport) { c.Classes[0].Samples[0].Win = math.Inf(1) }},
		{"negative", func(c *CollectedProblem, _ *CollectionBankReport) { c.Classes[0].Samples[0].Win = -1 }},
		{"utf8", func(c *CollectedProblem, _ *CollectionBankReport) { c.Classes[1].Intent.Name = "\xff" }},
		{"tag", func(c *CollectedProblem, _ *CollectionBankReport) {
			c.Classes[1].Intent.Collect.Tags.Matches = []string{"\xff"}
		}},
		{"count", func(_ *CollectedProblem, b *CollectionBankReport) { b.SeedCount++ }},
		{"overflow", func(_ *CollectedProblem, b *CollectionBankReport) { b.SeedCount = math.MaxUint64 }},
		{"bytes", func(_ *CollectedProblem, b *CollectionBankReport) { b.Bytes++ }},
		{"snapshot", func(c *CollectedProblem, _ *CollectionBankReport) { c.Classes[0].Samples[0].Snapshot = nil }},
		{"path", func(_ *CollectedProblem, b *CollectionBankReport) { b.Path = "relative.bin" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := descriptorFixture()
			bank := saveDescriptorFixture(t, c, false, false)
			test.change(&c, &bank)
			d, err := (collectionDescriptorWriter{}).Write(context.Background(), c, bank)
			if err == nil || d.State != "WARNING" {
				t.Fatalf("accepted invalid input: %+v %v", d, err)
			}
		})
	}
}

func TestDescriptorPythonInteroperability(t *testing.T) {
	python := os.Getenv("PROBLAB_PARQUET_PYTHON")
	if python == "" {
		t.Skip("set PROBLAB_PARQUET_PYTHON to a Python with PyArrow for independent verification")
	}
	for _, mode := range []string{"normal", "partial", "distinct", "empty"} {
		t.Run(mode, func(t *testing.T) {
			c := descriptorFixture()
			if mode == "empty" {
				for i := range c.Classes {
					c.Classes[i].Samples = nil
				}
			}
			bank := saveDescriptorFixture(t, c, mode != "normal", mode == "distinct")
			d, err := (collectionDescriptorWriter{}).Write(context.Background(), c, bank)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(bank.Path); err != nil {
				t.Fatal(err)
			} // The external consumer only has Parquet.
			out, err := exec.Command(python, "examples/collection_parquet/read_catalog.py", d.Path).CombinedOutput()
			if err != nil {
				t.Fatalf("Python: %v\n%s", err, out)
			}
			if !bytes.Contains(out, []byte(d.DatasetID)) {
				t.Fatalf("missing independently verified ID: %s", out)
			}
			// Re-encode with another writer, no compression and one row/group.
			// Dataset identity binds logical content, not a Parquet encoding.
			reencoded := filepath.Join(filepath.Dir(d.Path), "python.parquet")
			out, err = exec.Command(python, "-c", "import pyarrow.parquet as p,sys; p.write_table(p.read_table(sys.argv[1]),sys.argv[2],compression='NONE',row_group_size=1)", d.Path, reencoded).CombinedOutput()
			if err != nil {
				t.Fatalf("Python re-encode: %v\n%s", err, out)
			}
			out, err = exec.Command(python, "examples/collection_parquet/read_catalog.py", reencoded).CombinedOutput()
			if err != nil {
				t.Fatalf("Python re-encoded ID: %v\n%s", err, out)
			}
		})
	}
}

func TestExporterPreservesOptimizationResults(t *testing.T) {
	seed := Int64Seed(4127483647)
	normal := runCompleteProductionPipelineWithExporter(t, seed, "descriptor-parity", nil)
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			other := runCompleteProductionPipelineWithExporter(t, seed, "descriptor-parity", func(context.Context, CollectedProblem, CollectionBankReport) (CollectionDescriptorReport, error) {
				if fail {
					return CollectionDescriptorReport{}, errors.New("injected failure")
				}
				return CollectionDescriptorReport{}, nil
			})
			if normal.Report.ModelHash != other.Report.ModelHash || normal.Report.SolutionHash != other.Report.SolutionHash || normal.Report.ArtifactHash != other.Report.ArtifactHash {
				t.Fatal("export changed optimization hashes")
			}
			a, _ := os.ReadFile(normal.Report.Collection.Bank.Path)
			b, _ := os.ReadFile(other.Report.Collection.Bank.Path)
			if !bytes.Equal(a, b) {
				t.Fatal("export changed bank")
			}
		})
	}
	assertDescriptorMatchesBank(t, *normal.Report.Collection)
}

func TestDescriptorFailureIsAdvisoryWithoutReporter(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	output := t.TempDir()
	tuner := newCollectionStageTuner(lab, output, nil, 700)
	plan := collectionStagePlan(77, 1, 2, 2, output)
	tuner.collectionDescriptorExporter = func(_ context.Context, c CollectedProblem, b CollectionBankReport) (CollectionDescriptorReport, error) {
		return CollectionDescriptorReport{Path: strings.TrimSuffix(b.Path, ".bin") + ".parquet"}, errors.New("injected exporter failure")
	}
	report := RunReport{}
	collected, diagnostics, err := tuner.collectStage(context.Background(), plan, 0, &report)
	if err != nil || diagnostics.StopsRun() || len(collected.Classes[0].Samples) != 2 || report.Collection.Descriptor.State != "WARNING" {
		t.Fatalf("result=%+v diagnostics=%v err=%v", report.Collection, diagnostics, err)
	}
	if _, err := os.Stat(report.Collection.Bank.Path); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingReporter{}
	tuner.reporter = recorder
	_, _, err = tuner.collectStage(context.Background(), plan, 0, &RunReport{})
	if err != nil {
		t.Fatal(err)
	}
	bankCompleted, warnings := false, 0
	for _, event := range recorder.events {
		if event.Stage == "collection-bank" && event.State == "completed" {
			bankCompleted = true
		}
		if event.Stage == "collection-descriptor" {
			if !bankCompleted || event.State != "warning" || event.Message == "" {
				t.Fatalf("bad descriptor warning: %+v", event)
			}
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("warnings=%d", warnings)
	}
}

func TestTunerExportsBeforePrepareFailure(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	output := t.TempDir()
	tuner := newCollectionStageTuner(lab, output, nil, 701)
	plan := collectionStagePlan(77, 1, 2, 2, output)
	disabled := false
	plan.Intent.Classes[0].Weight = ClassWeightBase
	plan.Intent.Classes[0].Design.Subjective.Intent = &disabled
	plan.Intent.Classes[0].Design.Risk = &RiskIntent{Rounds: 100, Collision: CollisionIntent{Max: 0.01}}
	report := RunReport{}
	c, diags, err := tuner.collectStage(context.Background(), plan, 0, &report)
	if err != nil || diags.StopsRun() {
		t.Fatalf("collect: %v %v", err, diags)
	}
	assertDescriptorMatchesBank(t, *report.Collection)
	before, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	_, diags, err = tuner.prepareStage(plan, c, &report)
	if err != nil {
		t.Fatal(err)
	}
	if !diags.StopsRun() {
		t.Fatal("expected insufficient risk support at Prepare")
	}
	after, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("collection changed")
	}
}

type descriptorHeapFile struct {
	*os.File
	peak uint64
}

func (f *descriptorHeapFile) Write(p []byte) (int, error) {
	// Separate resource audit, not a timing benchmark: collect transient garbage
	// at output boundaries to measure retained heap while the writer is alive.
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if m.HeapAlloc > f.peak {
		f.peak = m.HeapAlloc
	}
	return f.File.Write(p)
}

func TestCollectionDescriptorMemoryProfile(t *testing.T) {
	if os.Getenv("PROBLAB_PARQUET_MEMORY") != "1" {
		t.Skip("set PROBLAB_PARQUET_MEMORY=1 for multi-million-row retained-heap audit")
	}
	for _, n := range []int{100_000, 2_000_000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			c := descriptorFixture()
			c.Classes = c.Classes[:1]
			c.Classes[0].Intent.Collect.Samples = uint64(n)
			c.Classes[0].Samples = make([]CollectedSample, n)
			for i := range c.Classes[0].Samples {
				c.Classes[0].Samples[i] = CollectedSample{ClassID: c.Classes[0].Intent.Name, Win: float64(i%100) / 3, Sequence: uint64(i), Snapshot: []byte{1}}
			}
			bank := saveDescriptorFixture(t, c, false, false)
			var measured *descriptorHeapFile
			writer := collectionDescriptorWriter{createTemp: func(dir, pattern string) (descriptorFile, error) {
				f, err := os.CreateTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				measured = &descriptorHeapFile{File: f}
				return measured, nil
			}}
			runtime.GC()
			var baseline runtime.MemStats
			runtime.ReadMemStats(&baseline)
			d, err := writer.Write(context.Background(), c, bank)
			if err != nil {
				t.Fatal(err)
			}
			runtime.KeepAlive(c)
			f, err := os.Open(d.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			pf, err := parquet.OpenFile(f, d.Bytes, parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true))
			if err != nil {
				t.Fatal(err)
			}
			for _, group := range pf.RowGroups() {
				if group.NumRows() > collectionDescriptorRowGroup {
					t.Fatal("unbounded row group")
				}
			}
			var tail [8]byte
			if _, err := f.ReadAt(tail[:], d.Bytes-8); err != nil {
				t.Fatal(err)
			}
			// Logical reachable metadata payload, not allocator/RSS. It is measured
			// separately from the fixed row batch and the GC-sampled total overhead.
			footerPayload := descriptorMetadataPayload(reflect.ValueOf(pf.Metadata()), make(map[uintptr]bool))
			extra := int64(measured.peak) - int64(baseline.HeapAlloc)
			t.Logf("rows=%d source-sample-payload=%d batch-payload-bound=%d row-groups=%d footer-wire=%d footer-decoded-payload=%d sampled-extra-live-heap=%d file-bytes=%d", n, n*(int(reflect.TypeOf(CollectedSample{}).Size())+1), collectionDescriptorBatch*int(reflect.TypeOf(collectionDescriptorRow{}).Size()), len(pf.RowGroups()), binary.LittleEndian.Uint32(tail[:4]), footerPayload, extra, d.Bytes)
		})
	}
}

// Counts reachable Go values without pretending to measure allocator padding.
func descriptorMetadataPayload(v reflect.Value, seen map[uintptr]bool) uintptr {
	if !v.IsValid() {
		return 0
	}
	n := v.Type().Size()
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() && !seen[v.Pointer()] {
			seen[v.Pointer()] = true
			n += descriptorMetadataPayload(v.Elem(), seen)
		}
	case reflect.Interface:
		if !v.IsNil() {
			n += descriptorMetadataPayload(v.Elem(), seen)
		}
	case reflect.String:
		n += uintptr(v.Len())
	case reflect.Slice:
		if !v.IsNil() && !seen[v.Pointer()] {
			seen[v.Pointer()] = true
			n += uintptr(v.Len()) * v.Type().Elem().Size()
			for i := 0; i < v.Len(); i++ {
				n += descriptorMetadataPayload(v.Index(i), seen) - v.Type().Elem().Size()
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			n += descriptorMetadataPayload(v.Field(i), seen) - v.Field(i).Type().Size()
		}
	}
	return n
}

// Source memory is allocated before timing. B/op is cumulative allocation, not
// peak memory; TestCollectionDescriptorMemoryProfile audits retained heap.
func BenchmarkCollectionDescriptor(b *testing.B) {
	for _, n := range []int{100_000, 2_000_000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			c := descriptorFixture()
			c.Classes = c.Classes[:1]
			c.Classes[0].Intent.Collect.Samples = uint64(n)
			c.Classes[0].Samples = make([]CollectedSample, n)
			for i := range c.Classes[0].Samples {
				c.Classes[0].Samples[i] = CollectedSample{ClassID: c.Classes[0].Intent.Name, Win: float64(i%100) / 3, Sequence: uint64(i), Snapshot: []byte{1}}
			}
			bank, err := (collectionBankWriter{}).Write(context.Background(), filepath.Join(b.TempDir(), "benchmark.bin"), c, false, false)
			if err != nil {
				b.Fatal(err)
			}
			runtime.GC()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d, err := (collectionDescriptorWriter{}).Write(context.Background(), c, bank)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(d.Bytes), "file-bytes")
				b.ReportMetric(float64(n), "rows")
			}
		})
	}
}

var _ io.Writer = (*descriptorFaultFile)(nil)
