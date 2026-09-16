# Verification record — 2026-09-16

This record covers the seven-column content-only catalog and both fixed-ZSTD
RGS routes. It replaces the earlier DatasetID/import-contract verification notes.

## Reproduction

Full acceptance (requires an existing Python environment; enables rather than
silently skipping interoperability and memory checks):

```sh
PROBLAB_PARQUET_PYTHON=/path/to/python sh optimizer/v2/examples/collection_parquet/verify.sh
```

Individual checks:

```sh
gofmt -l dto optimizer/v2 cmd/opt
go test ./... -count=1
go test -race ./dto ./optimizer/v2/... ./cmd/opt
go vet ./...
golangci-lint run ./dto/... ./optimizer/v2/... ./cmd/opt/...
PROBLAB_PARQUET_PYTHON=/path/to/python go test ./optimizer/v2 -run 'PythonInteroperability|LegacyCatalogReader' -count=1 -v
PROBLAB_PARQUET_MEMORY=1 PROBLAB_RGS_MEMORY=1 go test ./optimizer/v2 -run 'MemoryProfile' -count=1 -v
```

Python verification uses PyArrow 25.0.1, pandas 3.0.5 and zstandard 0.25.0
in an isolated temporary environment. Both Parquet families are read directly
with pandas' PyArrow dtype backend, without manually decompressing Parquet.
JSONL is decompressed incrementally and paired by record_index. Legacy files
are identified as missing exact win/bet, not silently upgraded.

## Regression baseline

Before production edits, a fixed seed 4127483647 demo run captured both native
formats. `TestRGSNativePreChangeBaseline` asserts the model/solution/collection
hashes and SHA256 of manifest, mode descriptor, gacha, seed, probability and
alias files. The baseline test passes after implementation.

`TestRGSNativeFilesMatchMixedOutput` additionally compares every native file
byte-for-byte between A-only and A+B+C, independently of report hashes.

Collection SHA256:
`f12afcc525d4d4b5c29970d3710e62f5674175a8b80e49bbb3ade8244d3b3ade`.

The engine report version intentionally changes to intent-lp-v2.8.0.
Config schema version, stable ordering and native formats do not change.
The main repository contains an English demo YAML only; no unrelated commercial
repository or Chinese YAML was changed.

## Streaming measurements

Local arm64 test fixture; measurements are not performance guarantees.

| Output fixture | Rows | Measured output heap | File bytes |
| --- | ---: | ---: | ---: |
| Collection Parquet | 100,000 | 42,660,104 additional live bytes | 141,796 |
| Collection Parquet | 2,000,000 | 23,737,920 additional live bytes | 2,226,095 |
| RGS JSONL.zst | 100,000 | 21,158,272 total sampled live bytes | 145 |
| RGS JSONL.zst | 1,000,000 | 21,164,352 total sampled live bytes | 1,195 |

Parquet measurements subtract the source collection; RGS numbers are total live
heap and are **not** comparable directly to that column. RGS repeats one valid
demo snapshot and uses a tiny converter, intentionally isolating bounded writer
memory. Its compression ratio is not representative of a commercial result bank.
Elapsed RGS fixture time was approximately 0.47 s / 4.66 s respectively.
Buffers, ZSTD windows and Parquet footer metadata account for bounded overhead.

On this 64-bit build CollectedSample is 64 bytes, previously 56: preserving the
raw TotalWin adds 8 bytes per sample (about 400 MB for 50 million samples),
excluding slice backing storage and snapshots. No full DTO/JSON array is retained.

### Full IdentityConverter pipeline (review follow-up)

`TestRGSIdentityPipelineMemoryProfile` runs real collection through the default
IdentityConverter, without injecting a synthetic row iterator or tiny converter.
Every decompressed JSONL row is checked for start/after replay state. The demo
source contains distinct collected snapshots; measurements remain demo-specific.

| Collected records | Raw JSONL bytes | ZSTD bytes | Additional sampled live heap | Run + streamed readback |
| ---: | ---: | ---: | ---: | ---: |
| 10,000 | 9,784,677 | 739,535 | 20,766,976 | 0.23 s |
| 100,000 | 98,069,199 | 7,392,937 | 20,780,480 | 2.10 s |

Baseline is measured after two GC cycles at RGS start (retiring previous pool
caches); sampled heap is measured periodically during compressed writes with GC.
This excludes the live source baseline, not all runtime overhead, and is not peak
RSS or a bound for arbitrarily large individual game results. The test asserts a
128 MiB extra-live-heap guard for this fixture only.

## Boundaries verified

- Complete collection only: partial/distinct retain recovery banks and stop.
- Collected-only skips LP; optimized-only skips native alias publication.
- Mixed order is collected, LP, optimized, native, irrespective of YAML order.
- Every family converts each record once and publishes atomically.
- Converter errors, invalid JSON, cancellation, file/rename failures preserve
  earlier committed output; post-rename sync failure reports visible paths.
- Optimized probabilities are normalized once and independently checked against
  the hard model; native verification still uses effective alias marginals.
- IdentityConverter is byte-identical to json.Marshal(dto.SpinResult), including
  Extend/checkpoint/start/after state. Custom payload semantics are not certified.
- No external probability importer, standalone bank converter, CDF or new loader.

The memory audits and Python tests are opt-in; an ordinary test run skipping them
must not be described as rerunning those checks.

## Review follow-up verification

The full acceptance entry was executed successfully, including formatting,
all-package tests, race, vet, lint, actual Python interoperability and all memory
profiles. Both Go and Python now physically rewrite reversed Parquet rows and
join results by record_index. B has a real multi-Class integration fixture plus
empty/sparse, unknown and ambiguous Class adapter coverage.

Permanent regressions also cover nil CLI writers, failed/staging-failed export
directory cleanup, operational Restore/Snapshot error chains, definite replay
mismatches, and B-only versus mixed-family publication failure states. The two
temporary audit files were renamed into permanent tests without discarding their
failure scenarios.

Intentional report changes include distribution.source and false Verification.Pass
when collection stops before verification. B-only diagnostic CSVs use
distribution_points_mode_<mode>.csv; native diagnostic CSV names and bytes remain
unchanged. No LP algorithm or native artifact format was changed by these fixes.
