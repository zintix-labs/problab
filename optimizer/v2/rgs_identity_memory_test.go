// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// This is a full collect/export run, unlike the repeated-snapshot encoder
// microbenchmark. Source collection memory is excluded from additional heap.
func TestRGSIdentityPipelineMemoryProfile(t *testing.T) {
	if os.Getenv("PROBLAB_RGS_MEMORY") != "1" {
		t.Skip("set PROBLAB_RGS_MEMORY=1")
	}
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "identity-memory")
	for _, n := range []uint64{10000, 100000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var baseline, peak uint64
			started := false
			tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected}, WithReporter(auditReporterFunc(func(e StageEvent) {
				if e.Stage == "rgs-collected" && e.State == "started" {
					// Two cycles also retire prior sync.Pool victim caches.
					runtime.GC()
					runtime.GC()
					var m runtime.MemStats
					runtime.ReadMemStats(&m)
					baseline = m.HeapAlloc
					started = true
				}
			})))
			plan := &tuner.config.Plans[0]
			plan.Collection.Workers = 1
			plan.Collection.MaxSpins = n * 10
			intent := tuner.config.Intents[plan.Intent]
			intent.Classes = append([]ClassIntent(nil), intent.Classes...)
			intent.Classes[0].Collect.Samples = n
			tuner.config.Intents[plan.Intent] = intent
			tuner.rgsIO.create = func(path string) (descriptorFile, error) {
				f, err := os.Create(path)
				if err != nil {
					return nil, err
				}
				return &identityHeapFile{File: f, peak: &peak}, nil
			}
			start := time.Now()
			r, err := tuner.Run(context.Background(), RunRequest{PlanID: plan.ID})
			if err != nil || r.Status != StatusExported {
				t.Fatalf("status=%s err=%v diagnostics=%v", r.Status, err, r.Diagnostics)
			}
			if !started || r.Report.RGSExports[0].Converter != "identity" {
				t.Fatal("identity export not measured")
			}
			file, err := os.Open(r.Report.RGSExports[0].Files[0].Path)
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
			var count, rawBytes uint64
			for {
				line, err := reader.ReadBytes('\n')
				if err == io.EOF {
					if len(line) != 0 {
						t.Fatal("truncated line")
					}
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(line, []byte(`"start_b64u"`)) || !bytes.Contains(line, []byte(`"after_b64u"`)) {
					t.Fatal("missing full DTO replay states")
				}
				count++
				rawBytes += uint64(len(line))
			}
			if count != n {
				t.Fatalf("records=%d want=%d", count, n)
			}
			var additional uint64
			if peak > baseline {
				additional = peak - baseline
			}
			if additional > 128<<20 {
				t.Fatalf("unexpected extra live heap: %d", additional)
			}
			t.Logf("records=%d time=%s raw_bytes=%d compressed_bytes=%d source_baseline=%d sampled_live_peak=%d additional_live_heap=%d", n, time.Since(start), rawBytes, r.Report.RGSExports[0].Files[0].Bytes, baseline, peak, additional)
		})
	}
}

type identityHeapFile struct {
	*os.File
	peak   *uint64
	writes int
}

func (f *identityHeapFile) Write(b []byte) (int, error) {
	f.writes++
	if f.writes == 1 || f.writes%32 == 0 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		if m.HeapAlloc > *f.peak {
			*f.peak = m.HeapAlloc
		}
	}
	return f.File.Write(b)
}
