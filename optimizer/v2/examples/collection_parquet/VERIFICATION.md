# Export-only verification — 2026-09-11

Environment: Go 1.25.2, macOS arm64 / Apple M3; parquet-go v0.32.0;
Python 3.14.7 with PyArrow 23.0.1. The commands below run from the repository root.
Use a writable `GOCACHE` if the default cache is restricted.

```sh
go test ./...
go test -race ./optimizer/v2 ./cmd/opt
golangci-lint run ./optimizer/v2/... ./cmd/opt/...
git diff --check

# This explicitly enables the independently implemented Python hash verifier.
PROBLAB_PARQUET_PYTHON=/path/to/python go test ./optimizer/v2 \
  -run 'TestDescriptorPythonInteroperability|TestDatasetIDGolden' -count=1 -v

PROBLAB_PARQUET_MEMORY=1 go test ./optimizer/v2 \
  -run TestCollectionDescriptorMemoryProfile -count=1 -v
go test ./optimizer/v2 -run '^$' -bench BenchmarkCollectionDescriptor -benchtime=1x -count=1
```

All passed. Python verification covers normal, partial, distinct and empty
catalogs with the bank removed. It also re-encodes with PyArrow, no compression
and one row per group, then verifies the unchanged Dataset ID. The literal Go
golden was framed independently with Python `struct.pack`; it includes a GID of
2^64-1. Signed zero, the smallest positive float64, repeated payouts and Unicode
Class names retain their exact values.

Create, data write, footer write, file sync, close, rename, directory sync and
cancellation failures are injected. Tests check bank preservation, temporary
cleanup, existing-catalog preservation before rename, and visible-but-unconfirmed
durability after rename. Warning reports survive without a CLI reporter; failure
events do not falsely announce completion. Prepare support failure and duplicate
stops retain their catalogs and do not send a recovery collection to the LP.

Full demo runs with the real exporter, a no-op exporter and a failing exporter
produce identical bank bytes and model/solution/artifact hashes. No collection
hot-path, Prepare, LP, PRNG or artifact algorithm was changed. Input/exported
ordering and the complete original collection are checked for mutation.

## Synthetic resource audit

These are single-run smoke measurements, not production throughput promises.
The fixture has one Class, repeated names and 100 repeating payout values, and
therefore compresses well. Bank creation/source allocation is outside timing.

| Rows | Export time | Parquet bytes | Cumulative allocated bytes |
| ---: | ---: | ---: | ---: |
| 100,000 | 20.69 ms | 138,678 | 23,198,432 |
| 2,000,000 | 188.22 ms | 2,159,999 | 23,336,712 |

Allocation totals are **not peak heap**. A separate instrumented test collects
garbage and samples live heap at file writes; its execution time is not used for
the throughput figures above.

| Rows | Source sample payload | Row batch bound | Row groups | Footer wire bytes | Decoded footer logical payload | Sampled extra live heap |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 100,000 | 5,700,000 | 327,680 | 2 | 1,575 | 6,665 | 40,889,976 |
| 2,000,000 | 114,000,000 | 327,680 | 31 | 12,889 | 81,024 | 21,988,600 |

The batch bound counts row structs (strings borrow existing immutable backing
storage); codec/page buffers are additional and included in sampled overhead.
Footer logical payload is measured separately from a decoded metadata structure,
excluding allocator padding, spare capacity and shared-backing effects; it is not
an exact attribution of writer heap. Sampling is not a continuous RSS maximum.
Codec pools and warm/cold allocation explain why the smaller run can show more
overhead. Tests verify every row group is at most 65,536 rows. Necessary footer
metadata may grow with group count; all row data must not accumulate in memory.

## Scope

Only collection catalog export is implemented. External probability import,
RTP/alias validation for external results and publication remain separate work.
Exporter warnings are advisory; shared-context cancellation can still stop the
original pipeline. Bank and catalog are individually atomic, not a two-file
transaction.
