// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

const (
	collectionDescriptorSchema   = "problab.collection-outcomes/v1"
	collectionDescriptorKey      = "problab.collection"
	collectionDescriptorBatch    = 8192
	collectionDescriptorRowGroup = 65536
)

// CollectionDescriptorReport describes advisory I/O, never a runtime artifact.
type CollectionDescriptorReport struct {
	State     string        `json:"state"`
	Path      string        `json:"path"`
	DatasetID string        `json:"dataset_id,omitempty"`
	Records   uint64        `json:"records"`
	Bytes     int64         `json:"bytes"`
	Duration  time.Duration `json:"duration_ns"`
	Error     string        `json:"error,omitempty"`
}

type collectionDescriptorRow struct {
	RecordIndex   int64   `parquet:"record_index"`
	ClassID       int32   `parquet:"class_id"`
	ClassName     string  `parquet:"class_name"`
	WinMultiplier float64 `parquet:"win_multiplier"`
}

type collectionDescriptorClass struct {
	ClassID   int32          `json:"class_id"`
	ClassName string         `json:"class_name"`
	WinRange  ClosedInterval `json:"win_range"`
	Tags      TagFilters     `json:"tags"`
	Requested uint64         `json:"requested"`
	Retained  uint64         `json:"retained"`
}

type collectionDescriptorMetadata struct {
	Schema         string                      `json:"schema"`
	DatasetID      string                      `json:"dataset_id"`
	BankSHA256     string                      `json:"bank_sha256"`
	RecordCount    uint64                      `json:"record_count"`
	Game           string                      `json:"game"`
	BetMode        int                         `json:"bet_mode"`
	BetUnit        int                         `json:"bet_unit"`
	MultiplierUnit string                      `json:"multiplier_unit"`
	Ordering       string                      `json:"ordering"`
	Partial        bool                        `json:"partial"`
	Distinct       bool                        `json:"distinct"`
	Sampling       string                      `json:"sampling"`
	Classes        []collectionDescriptorClass `json:"classes"`
}

// PRD 0005 / collection-outcome-parquet-exchange: borrow collection read-only.
// Record indexes follow the bank visitor, not Sequence or payout order. Future
// probability import is deliberately separate from this exporter and the LP.
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
	metadata, digest, err := descriptorMetadata(ctx, collected, bank)
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
		if !finiteDescriptorNumber(sample.Win) || sample.Win < 0 {
			return fmt.Errorf("descriptor record %d has invalid win multiplier", index)
		}
		row := collectionDescriptorRow{index, int32(ci), collected.Classes[ci].Intent.Name, sample.Win}
		hashDescriptorRow(digest, row)
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
	metadata.DatasetID = "sha256:" + hex.EncodeToString(digest.Sum(nil))
	report.DatasetID = metadata.DatasetID
	raw, err := json.Marshal(metadata)
	if err != nil {
		return report, fmt.Errorf("encode descriptor metadata: %w", err)
	}
	writer.SetKeyValueMetadata(collectionDescriptorKey, string(raw))
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

func descriptorMetadata(ctx context.Context, c CollectedProblem, bank CollectionBankReport) (collectionDescriptorMetadata, *descriptorDigest, error) {
	m := collectionDescriptorMetadata{}
	if err := ctx.Err(); err != nil {
		return m, nil, err
	}
	count, err := validateCanonicalCollection(ctx, c)
	if err != nil {
		return m, nil, err
	}
	if count > math.MaxInt64 || count != bank.SeedCount || bank.SeedLength != c.SnapshotLength ||
		count > uint64(math.MaxInt64)/uint64(c.SnapshotLength) || bank.Bytes != int64(count*uint64(c.SnapshotLength)) {
		return m, nil, fmt.Errorf("descriptor count/snapshot length/bytes do not match saved bank")
	}
	if uint64(len(c.Classes)) > uint64(math.MaxInt32)+1 {
		return m, nil, fmt.Errorf("descriptor Class index overflows int32")
	}
	bankDigest, err := hex.DecodeString(bank.SHA256)
	if err != nil || len(bankDigest) != sha256.Size || bank.SHA256 != strings.ToLower(bank.SHA256) {
		return m, nil, fmt.Errorf("invalid bank SHA256")
	}
	m = collectionDescriptorMetadata{
		Schema: collectionDescriptorSchema, BankSHA256: bank.SHA256, RecordCount: count,
		Game: strconv.FormatUint(uint64(c.Game), 10), BetMode: c.BetMode, BetUnit: c.BetUnit,
		MultiplierUnit: "total_win_divided_by_bet", Ordering: "bank-record-index-v1",
		Partial: bank.Partial, Distinct: bank.Distinct, Sampling: "quota_selected_not_natural_frequency",
		Classes: make([]collectionDescriptorClass, 0, len(c.Classes)),
	}
	for i, class := range c.Classes {
		if err := ctx.Err(); err != nil {
			return m, nil, err
		}
		intent := class.Intent.Collect
		if !utf8.ValidString(class.Intent.Name) || !finiteDescriptorNumber(intent.WinRange[0]) || !finiteDescriptorNumber(intent.WinRange[1]) {
			return m, nil, fmt.Errorf("invalid descriptor Class %d name/range", i)
		}
		for _, tags := range [][]string{intent.Tags.Matches, intent.Tags.Mismatches} {
			for _, tag := range tags {
				if !utf8.ValidString(tag) {
					return m, nil, fmt.Errorf("invalid descriptor Class %d tag UTF-8", i)
				}
			}
		}
		m.Classes = append(m.Classes, collectionDescriptorClass{
			ClassID: int32(i), ClassName: class.Intent.Name, WinRange: intent.WinRange,
			Tags:      TagFilters{Matches: append([]string{}, intent.Tags.Matches...), Mismatches: append([]string{}, intent.Tags.Mismatches...)},
			Requested: intent.Samples, Retained: uint64(len(class.Samples)),
		})
	}
	h := &descriptorDigest{state: sha256.New()}
	descriptorHashString(h, "problab.collection-outcomes/dataset-id/v1")
	descriptorHashString(h, m.Schema)
	_, _ = h.Write(bankDigest)
	descriptorHashU64(h, count)
	descriptorHashU64(h, uint64(c.Game))
	descriptorHashU64(h, uint64(c.BetMode))
	descriptorHashU64(h, uint64(c.BetUnit))
	descriptorHashString(h, m.MultiplierUnit)
	descriptorHashString(h, m.Ordering)
	for _, b := range []bool{m.Partial, m.Distinct} {
		v := byte(0)
		if b {
			v = 1
		}
		_, _ = h.Write([]byte{v})
	}
	descriptorHashString(h, m.Sampling)
	descriptorHashU64(h, uint64(len(m.Classes)))
	for _, class := range m.Classes {
		descriptorHashI32(h, class.ClassID)
		descriptorHashString(h, class.ClassName)
		descriptorHashU64(h, math.Float64bits(class.WinRange[0]))
		descriptorHashU64(h, math.Float64bits(class.WinRange[1]))
		for _, tags := range [][]string{class.Tags.Matches, class.Tags.Mismatches} {
			descriptorHashU64(h, uint64(len(tags)))
			for _, tag := range tags {
				descriptorHashString(h, tag)
			}
		}
		descriptorHashU64(h, class.Requested)
		descriptorHashU64(h, class.Retained)
	}
	return m, h, nil
}

// Buffer framing bytes so per-record scalar/string hashing does not allocate
// one temporary heap object for every field in a multi-million-row collection.
type descriptorDigest struct {
	state  hash.Hash
	buffer [8192]byte
	used   int
}

func (h *descriptorDigest) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		copied := copy(h.buffer[h.used:], p)
		h.used += copied
		p = p[copied:]
		if h.used == len(h.buffer) {
			h.flush()
		}
	}
	return n, nil
}
func (h *descriptorDigest) flush()                   { _, _ = h.state.Write(h.buffer[:h.used]); h.used = 0 }
func (h *descriptorDigest) Sum(prefix []byte) []byte { h.flush(); return h.state.Sum(prefix) }

func descriptorHashU64(h *descriptorDigest, v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	_, _ = h.Write(b[:])
}
func descriptorHashI32(h *descriptorDigest, v int32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(v))
	_, _ = h.Write(b[:])
}
func descriptorHashString(h *descriptorDigest, s string) {
	descriptorHashU64(h, uint64(len(s)))
	_, _ = h.Write([]byte(s))
}
func hashDescriptorRow(h *descriptorDigest, row collectionDescriptorRow) {
	descriptorHashU64(h, uint64(row.RecordIndex))
	descriptorHashI32(h, row.ClassID)
	descriptorHashString(h, row.ClassName)
	descriptorHashU64(h, math.Float64bits(row.WinMultiplier))
}
