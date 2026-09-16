// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

const (
	collectionDescriptorBatch    = 8192
	collectionDescriptorRowGroup = 65536
)

// CollectionDescriptorReport describes advisory I/O, never a runtime artifact.
type CollectionDescriptorReport struct {
	State    string        `json:"state"`
	Path     string        `json:"path"`
	Records  uint64        `json:"records"`
	Bytes    int64         `json:"bytes"`
	Duration time.Duration `json:"duration_ns"`
	Error    string        `json:"error,omitempty"`
}

// The nullable column has the same schema for collected and optimized output.
type collectionDescriptorRow struct {
	RecordIndex   int64    `parquet:"record_index"`
	ClassID       int32    `parquet:"class_id"`
	ClassName     string   `parquet:"class_name"`
	Probability   *float64 `parquet:"probability,optional"`
	WinMultiplier float64  `parquet:"win_multiplier"`
	Win           int64    `parquet:"win"`
	Bet           int64    `parquet:"bet"`
}

// Exporters borrow collection read-only; record indexes follow the bank visitor.
type collectionDescriptorExporter func(context.Context, CollectedProblem, CollectionBankReport) (CollectionDescriptorReport, error)

type descriptorFile interface {
	io.Writer
	Name() string
	Sync() error
	Close() error
	Chmod(os.FileMode) error
}

// Narrow filesystem seams allow real encoder failures without changing Tuner's
// public constructor or exposing an external optimizer adapter framework.
type collectionDescriptorWriter struct {
	createTemp    func(string, string) (descriptorFile, error)
	rename        func(string, string) error
	syncDirectory func(string) error
}

func (w collectionDescriptorWriter) Write(ctx context.Context, collected CollectedProblem, bank CollectionBankReport) (report CollectionDescriptorReport, err error) {
	started := time.Now()
	report.State = "WARNING"
	defer func() {
		report.Duration = time.Since(started)
		if err != nil {
			if report.Path != "" {
				err = fmt.Errorf("export collection descriptor %q: %w", report.Path, err)
			}
			report.Error = err.Error()
		}
	}()
	if !filepath.IsAbs(bank.Path) || filepath.Ext(bank.Path) != ".bin" {
		return report, fmt.Errorf("descriptor bank path must be absolute with .bin extension: %q", bank.Path)
	}
	report.Path = strings.TrimSuffix(bank.Path, ".bin") + ".parquet"
	err = validateDescriptorCollection(ctx, collected, bank)
	if err != nil {
		return report, err
	}
	create := w.createTemp
	if create == nil {
		create = func(dir, pattern string) (descriptorFile, error) { return os.CreateTemp(dir, pattern) }
	}
	dir := filepath.Dir(report.Path)
	file, err := create(dir, ".collection-descriptor-*")
	if err != nil {
		return report, fmt.Errorf("create descriptor: %w", err)
	}
	committed, closed := false, false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if !committed {
			_ = os.Remove(file.Name())
		}
	}()
	output := &descriptorOutput{ctx: ctx, writer: file}
	writer := parquet.NewGenericWriter[collectionDescriptorRow](output,
		parquet.Compression(&zstd.Codec{Concurrency: 1}),
		parquet.MaxRowsPerRowGroup(collectionDescriptorRowGroup))
	// Reset releases buffered pages even after an encoder/output error, without
	// flushing an incomplete file. Row data is bounded; footer metadata is O(groups).
	defer writer.Reset(io.Discard)
	batch := make([]collectionDescriptorRow, 0, collectionDescriptorBatch)
	flush := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := writer.Write(batch)
		if err == nil && n != len(batch) {
			err = io.ErrShortWrite
		}
		clear(batch)
		batch = batch[:0]
		return err
	}
	var index int64
	err = visitCanonicalCollectionSamples(collected, func(ci, _ int, sample CollectedSample) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !finiteDescriptorNumber(sample.Win) || sample.Win < 0 || sample.TotalWin < 0 || !utf8.ValidString(collected.Classes[ci].Intent.Name) || float64(sample.TotalWin)/float64(collected.BetUnit) != sample.Win {
			return fmt.Errorf("descriptor record %d has invalid win multiplier", index)
		}
		row := collectionDescriptorRow{RecordIndex: index, ClassID: int32(ci), ClassName: collected.Classes[ci].Intent.Name, WinMultiplier: sample.Win, Win: sample.TotalWin, Bet: int64(collected.BetUnit)}
		batch = append(batch, row)
		index++
		if len(batch) == cap(batch) {
			return flush()
		}
		return nil
	})
	if err != nil {
		return report, fmt.Errorf("stream descriptor: %w", err)
	}
	if len(batch) > 0 {
		if err = flush(); err != nil {
			return report, fmt.Errorf("flush descriptor: %w", err)
		}
	}
	if err = writer.Close(); err != nil {
		return report, fmt.Errorf("finalize descriptor: %w", err)
	}
	if err = file.Chmod(0o644); err != nil {
		return report, fmt.Errorf("chmod descriptor: %w", err)
	}
	if err = file.Sync(); err != nil {
		return report, fmt.Errorf("sync descriptor: %w", err)
	}
	closed = true
	if err = file.Close(); err != nil {
		return report, fmt.Errorf("close descriptor: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	rename := w.rename
	if rename == nil {
		rename = os.Rename
	}
	if err = rename(file.Name(), report.Path); err != nil {
		return report, fmt.Errorf("replace descriptor: %w", err)
	}
	committed = true
	syncDir := w.syncDirectory
	if syncDir == nil {
		syncDir = syncCollectionBankDirectory
	}
	if err = syncDir(dir); err != nil {
		return report, fmt.Errorf("descriptor is visible at %q but durability is unconfirmed: %w", report.Path, err)
	}
	report.State, report.Records, report.Bytes = "COMPLETED", uint64(index), output.bytes
	return report, nil
}

type descriptorOutput struct {
	ctx    context.Context
	writer io.Writer
	bytes  int64
}

func (w *descriptorOutput) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.writer.Write(p)
	w.bytes += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

func finiteDescriptorNumber(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func validateDescriptorCollection(ctx context.Context, c CollectedProblem, bank CollectionBankReport) error {
	if ctx == nil {
		return fmt.Errorf("descriptor context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.SnapshotLength <= 0 || c.BetUnit <= 0 {
		return fmt.Errorf("invalid descriptor snapshot length or bet")
	}
	count, err := validateCanonicalCollection(ctx, c)
	if err != nil {
		return err
	}
	if count > math.MaxInt64 || count != bank.SeedCount || bank.SeedLength != c.SnapshotLength ||
		count > uint64(math.MaxInt64)/uint64(c.SnapshotLength) || bank.Bytes != int64(count*uint64(c.SnapshotLength)) {
		return fmt.Errorf("descriptor count/snapshot length/bytes do not match saved bank")
	}
	if uint64(len(c.Classes)) > uint64(math.MaxInt32)+1 {
		return fmt.Errorf("descriptor Class index overflows int32")
	}
	return nil
}
