# Content-only catalogs and RGS exports

A complete, duplicate-free collection writes a seven-column catalog beside its
ordinary seed bank. Partial/distinct recovery banks do **not** produce catalogs.

| Column | Type | Meaning |
| --- | --- | --- |
| record_index | int64 | Zero-based position in this family's result library |
| class_id | int32 | Original Class declaration ordinal |
| class_name | string | Original Class name |
| probability | nullable float64 | Collected: all null; optimized: finite point probabilities |
| win_multiplier | float64 | Original multiplier |
| win | int64 | Exact integer payout, not reconstructed from the multiplier |
| bet | int64 | Unit bet |

The two RGS output formats are `rgs-collected` (no LP) and `rgs-optimized`
(after LP). Both always produce `results.jsonl.zst`; optimized also produces
`distribution.parquet`. Collected uses the catalog beside its bank.
Parquet uses internal ZSTD: it is **not** a `.parquet.zst` file.

```python
import pandas as pd
table = pd.read_parquet("distribution.parquet", engine="pyarrow",
                        dtype_backend="pyarrow")
```

For bounded-memory reading use `pyarrow.parquet.ParquetFile(path).iter_batches()`.
Run `python read_catalog.py PATH` for a schema/order check. A legacy four-column
file is identified explicitly; the reader never fabricates missing win/bet.

The original catalog row i, bank record i, and collected JSONL line i align.
Optimized row i aligns with **its own** JSONL line i. Do not mix the families.
Sorting externally is allowed if record_index is retained. It is not a byte offset.

There is no DatasetID, application metadata, sidecar, or external probability
importer. Keep the run report and matching files together; an isolated export
directory cannot prove which collection catalog belongs to it.

JSONL rows are compact JSON values followed by LF, compressed as one streaming
ZSTD output. They can be decoded incrementally; no custom seek/chunk index is supplied.
Use `WithResultConverter` to inject a platform encoder (see ../rgs).
The default `dto.IdentityConverter` includes PRNG snapshots and checkpoint:
remove sensitive state before sharing with non-owners. Custom payload payout
semantics are the converter author's responsibility, not verified by the LP.

No CDF, integer-weight quantization or AliasTable is exported on the RGS route.
External optimizers and samplers own those choices and their numerical accuracy.
