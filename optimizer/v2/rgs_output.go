// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
	pzstd "github.com/parquet-go/parquet-go/compress/zstd"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
)

// RGSExportReport records publication, not certification of converter payloads.
// Converter describes injection provenance only; custom does not mean redacted.
type RGSExportReport struct {
	Format       OutputFormat        `json:"format"`
	Family       string              `json:"family"`
	BetMode      int                 `json:"bet_mode"`
	State        string              `json:"state"`
	Records      uint64              `json:"records"`
	Total        uint64              `json:"total"`
	Files        []RGSFileReport     `json:"files,omitempty"`
	Duration     time.Duration       `json:"duration_ns"`
	Converter    string              `json:"converter"`
	Error        string              `json:"error,omitempty"`
	Verification *VerificationReport `json:"verification,omitempty"`
}

type RGSFileReport struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Bytes int64  `json:"bytes"`
}

type rgsSourceRow struct {
	row      collectionDescriptorRow
	snapshot []byte
	validate func(*buf.SpinResult) error
}
type rgsRows func(func(rgsSourceRow) error) error

// Private filesystem seams keep publication fault-injectable without adding
// another public publisher framework.
type rgsIO struct {
	mkdirTemp func(string, string) (string, error)
	create    func(string) (descriptorFile, error)
	rename    func(string, string) error
	syncDir   func(string) error
}

func preflightRGSDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return fmt.Errorf("RGS output directory %q: %w", directory, err)
	}
	probe, err := os.MkdirTemp(directory, ".rgs-preflight-")
	if err != nil {
		return fmt.Errorf("RGS output directory %q is not writable: %w", directory, err)
	}
	if err = os.Remove(probe); err != nil {
		return fmt.Errorf("remove RGS preflight probe: %w", err)
	}
	return nil
}

func hasOutput(plan ResolvedPlan, format OutputFormat) bool {
	for _, f := range plan.Plan.Output.Format {
		if f == format {
			return true
		}
	}
	return false
}
func nativeOutputs(output OutputOptions) OutputOptions {
	result := OutputOptions{Directory: output.Directory}
	for _, f := range output.Format {
		if f == OutputOptimalArtifactV1 || f == OutputOptimalGacha {
			result.Format = append(result.Format, f)
		}
	}
	return result
}
func initializeRGSReports(report *RunReport, custom bool) {
	converter := "identity"
	if custom {
		converter = "custom"
	}
	for _, f := range report.Plan.Plan.Output.Format {
		family := ""
		if f == OutputRGSCollected {
			family = "collected"
		}
		if f == OutputRGSOptimized {
			family = "optimized"
		}
		if family != "" {
			report.RGSExports = append(report.RGSExports, RGSExportReport{Format: f, Family: family, BetMode: report.Plan.Plan.Target.BetModes[0], State: "PENDING", Converter: converter})
		}
	}
}
func rgsReport(report *RunReport, format OutputFormat) *RGSExportReport {
	for i := range report.RGSExports {
		if report.RGSExports[i].Format == format {
			return &report.RGSExports[i]
		}
	}
	return nil
}

func collectedRGSRows(c CollectedProblem) rgsRows {
	return func(visit func(rgsSourceRow) error) error {
		var index int64
		return visitCanonicalCollectionSamples(c, func(ci, _ int, s CollectedSample) error {
			row := rgsSourceRow{row: collectionDescriptorRow{RecordIndex: index, ClassID: int32(ci), ClassName: c.Classes[ci].Intent.Name, WinMultiplier: s.Win, Win: s.TotalWin, Bet: int64(c.BetUnit)}, snapshot: s.Snapshot}
			row.validate = func(result *buf.SpinResult) error {
				if int64(result.TotalWin) != s.TotalWin {
					return fmt.Errorf("integer TotalWin differs from collection")
				}
				return nil
			}
			index++
			return visit(row)
		})
	}
}

// exportRGS publishes a family atomically. P's catalog remains a separate
// successful collection output; B's distribution is part of this transaction.
func (t *Tuner) exportRGS(ctx context.Context, plan ResolvedPlan, bet int, root *string, report *RGSExportReport, rows rgsRows, verify func() VerificationReport) (err error) {
	started := time.Now()
	report.State = "WRITING"
	stage := "rgs-" + report.Family
	lastProgress := time.Time{}
	emit := func(state string) {
		if t.reporter != nil {
			t.reporter.Report(StageEvent{Stage: stage, State: state, BetMode: report.BetMode, Records: report.Records, TotalRecords: report.Total, Duration: time.Since(started), Message: report.Error})
		}
	}
	emit("started")
	defer func() {
		report.Duration = time.Since(started)
		if err != nil {
			err = fmt.Errorf("%s game=%d mode=%d record_index=%d output=%q: %w", stage, uint64(plan.Plan.Target.Game), report.BetMode, report.Records, *root, err)
			report.Error = err.Error()
			if report.State != "COMMITTED_UNSYNCED" {
				report.State = "FAILED"
			}
			emit("failed")
		} else {
			emit("completed")
		}
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	if report.Total == 0 || report.Total > math.MaxInt64 {
		return fmt.Errorf("invalid RGS record count %d", report.Total)
	}
	mkdirTemp := t.rgsIO.mkdirTemp
	if mkdirTemp == nil {
		mkdirTemp = os.MkdirTemp
	}
	if *root == "" {
		parent := filepath.Join(plan.Plan.Output.Directory, "rgs", fmt.Sprintf("game_%d", uint64(plan.Plan.Target.Game)), fmt.Sprintf("mode_%d", report.BetMode))
		parent, err = filepath.Abs(parent)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(parent, 0755); err != nil {
			return err
		}
		*root, err = mkdirTemp(parent, "export_")
		if err != nil {
			return err
		}
		// Registered before staging cleanup, so it runs afterwards. Remove only
		// our empty run directory, never recursively remove a committed family.
		runDirectory := *root
		defer func() {
			if err != nil {
				_ = os.Remove(runDirectory)
			}
		}()
	}
	staging, err := mkdirTemp(*root, "."+report.Family+"-")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()
	create := t.rgsIO.create
	if create == nil {
		create = func(path string) (descriptorFile, error) {
			return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		}
	}
	jsonFile, err := create(filepath.Join(staging, "results.jsonl.zst"))
	if err != nil {
		return err
	}
	defer func() { _ = jsonFile.Close() }()
	compressed, err := zstd.NewWriter(&descriptorOutput{ctx: ctx, writer: jsonFile}, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return err
	}
	// Close on failure only to release encoder resources; staging never commits.
	encoderClosed := false
	defer func() {
		if !encoderClosed {
			_ = compressed.Close()
		}
	}()
	var parquetFile descriptorFile
	var table *parquet.GenericWriter[collectionDescriptorRow]
	batch := make([]collectionDescriptorRow, 0, collectionDescriptorBatch)
	flush := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, e := table.Write(batch)
		if e == nil && n != len(batch) {
			e = io.ErrShortWrite
		}
		clear(batch)
		batch = batch[:0]
		return e
	}
	if report.Family == "optimized" {
		parquetFile, err = create(filepath.Join(staging, "distribution.parquet"))
		if err != nil {
			return err
		}
		defer func() { _ = parquetFile.Close() }()
		table = parquet.NewGenericWriter[collectionDescriptorRow](&descriptorOutput{ctx: ctx, writer: parquetFile}, parquet.Compression(&pzstd.Codec{Concurrency: 1}), parquet.MaxRowsPerRowGroup(collectionDescriptorRowGroup))
		defer table.Reset(io.Discard)
	}
	replay, err := newRGSReplay(t.lab, plan, bet)
	if err != nil {
		return err
	}
	err = rows(func(source rgsSourceRow) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := source.row
		if row.RecordIndex < 0 || uint64(row.RecordIndex) != report.Records || !utf8.ValidString(row.ClassName) || row.ClassID < 0 {
			return fmt.Errorf("invalid row identity")
		}
		if table != nil && (row.Probability == nil || !isFinite(*row.Probability) || *row.Probability < 0 || *row.Probability > 1) {
			return fmt.Errorf("optimized probability must be non-null and in [0,1]")
		}
		if table == nil && row.Probability != nil {
			return fmt.Errorf("collected probability must be null")
		}
		result, e := replay.spin(ctx, source.snapshot, row.WinMultiplier)
		if e != nil {
			var mismatch *rgsReplayMismatch
			if table != nil && errors.As(e, &mismatch) {
				return &rgsVerificationError{report: finalizeVerification([]VerificationCheck{textVerificationCheck("rgs.snapshot_runtime_replay", false, e.Error(), "snapshot reproduces modeled payout and bet")})}
			}
			return e
		}
		if source.validate != nil {
			if e = source.validate(result); e != nil {
				return e
			}
		}
		// Capture scalar math BEFORE a custom converter sees any borrowed buffers.
		row.Win, row.Bet = int64(result.TotalWin), int64(result.Bet)
		converted, e := dto.NewSpinResultDTO(result)
		if e != nil {
			return fmt.Errorf("DTO: %w", e)
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		raw, e := t.resultConverter(converted)
		if e != nil {
			return fmt.Errorf("converter: %w", e)
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if !utf8.Valid(raw) || !json.Valid(raw) {
			return fmt.Errorf("converter returned invalid UTF-8/JSON")
		}
		var compact bytes.Buffer
		if e = json.Compact(&compact, raw); e != nil {
			return e
		}
		compact.WriteByte('\n')
		if _, e = compressed.Write(compact.Bytes()); e != nil {
			return fmt.Errorf("write results.jsonl.zst: %w", e)
		}
		if table != nil {
			batch = append(batch, row)
			if len(batch) == cap(batch) {
				if e = flush(); e != nil {
					return e
				}
			}
		}
		report.Records++
		if time.Since(lastProgress) >= 250*time.Millisecond {
			emit("progress")
			lastProgress = time.Now()
		}
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	if report.Records != report.Total {
		return fmt.Errorf("row count=%d want=%d", report.Records, report.Total)
	}
	if verify != nil {
		v := verify()
		report.Verification = &v
		if !v.Pass {
			return &rgsVerificationError{report: v}
		}
	}
	if table != nil {
		if len(batch) > 0 {
			if err = flush(); err != nil {
				return err
			}
		}
		if err = table.Close(); err != nil {
			return fmt.Errorf("Parquet footer: %w", err)
		}
		if err = parquetFile.Sync(); err != nil {
			return err
		}
		if err = parquetFile.Close(); err != nil {
			return err
		}
	}
	encoderClosed = true
	if err = compressed.Close(); err != nil {
		return fmt.Errorf("ZSTD close: %w", err)
	}
	if err = jsonFile.Sync(); err != nil {
		return err
	}
	if err = jsonFile.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	dest := filepath.Join(*root, report.Family)
	if _, e := os.Lstat(dest); !os.IsNotExist(e) {
		return fmt.Errorf("refuse existing family destination %q", dest)
	}
	// Measure before rename so visible output reports cannot be lost to a stat error.
	names := []string{"results.jsonl.zst"}
	if table != nil {
		names = append(names, "distribution.parquet")
	}
	files := make([]RGSFileReport, 0, len(names))
	for _, name := range names {
		info, e := os.Stat(filepath.Join(staging, name))
		if e != nil {
			return e
		}
		kind := "results"
		if name == "distribution.parquet" {
			kind = "distribution"
		}
		files = append(files, RGSFileReport{Path: filepath.Join(dest, name), Kind: kind, Bytes: info.Size()})
	}
	syncDir := t.rgsIO.syncDir
	if syncDir == nil {
		syncDir = syncCollectionBankDirectory
	}
	if err = syncDir(staging); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	rename := t.rgsIO.rename
	if rename == nil {
		rename = os.Rename
	}
	if err = rename(staging, dest); err != nil {
		return err
	}
	committed = true
	report.Files = files
	if err = syncDir(*root); err != nil {
		report.State = "COMMITTED_UNSYNCED"
		return err
	}
	report.State = "COMPLETED"
	return nil
}

type rgsVerificationError struct{ report VerificationReport }

func (e *rgsVerificationError) Error() string {
	return "RGS point distribution or replay validation failed"
}
