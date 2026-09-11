# Collection catalogs for external optimization

Every successfully saved collection bank gets a sibling `.parquet` catalog,
including partial and Class-local distinct recovery banks. The catalog is an
advisory, read-only export; failure does not invalidate the bank or change LP
inputs. There is no external probability importer in this release.

```sh
python -m pip install pyarrow==23.0.1
python optimizer/v2/examples/collection_parquet/read_catalog.py /path/to/seed_bank_123_s4.parquet
```

The example verifies the Dataset ID using only Parquet; no bank or game runtime
is required. Use `pyarrow.parquet.ParquetFile(path).iter_batches()` for bounded
processing, or `pyarrow.parquet.read_table(path)` when the full table fits memory.

| Required column | Type | Meaning |
| --- | --- | --- |
| record_index | int64 | Zero-based position in this saved bank, not spin Sequence |
| class_id | int32 | Class declaration index; empty Classes do not renumber others |
| class_name | UTF-8 string | Collection Class name |
| win_multiplier | float64 | Original TotalWin / Bet, without rounding or merging |

`problab.collection` contains JSON metadata: schema `problab.collection-outcomes/v1`,
Dataset ID, bank digest, counts, game (canonical uint64 decimal **string**), mode,
bet unit, partial/distinct flags and collection Class predicates/quotas. Other
integer metadata must be read with exact integer support, not a floating-point
JSON number parser. A multiplier of 0.96 means 96% RTP when averaged under the
chosen probabilities. Class row counts are quota-selected, **not natural event
frequencies**. Empty catalogs are valid datasets, not publishable distributions.

No snapshots, seed material, per-record tags or LP design constraints are exported.
External tools choose their own grouping, objectives and constraints. Dataset ID
binds content, not a signature or proof of game-version correctness. It includes
bank digest, collection predicates/quotas, flags and exact row bits; it excludes
path/time, compression and LP-only settings. The reader contains the v1 binary
hash framing; do not hash JSON text or Parquet file bytes as a substitute.

Future result exchange is reserved for a separate implementation: a Parquet with
`record_index` and unconditional `probability`, and `problab.probabilities` JSON
metadata containing schema `problab.sample-probabilities/v1`, source `dataset_id`
and declared `rtp`. This reader does not accept, optimize or publish such results.

The Go writer uses parquet-go v0.32.0, ZSTD concurrency 1, batches of 8192 and row
groups of at most 65536. Row buffers are bounded; necessary footer metadata grows
with row group count. Bank and catalog commit separately; transfer the completed
catalog and verify its Dataset ID, not just its filename.

See [verification commands and resource measurements](VERIFICATION.md).
