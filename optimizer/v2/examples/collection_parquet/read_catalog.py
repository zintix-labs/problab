"""Read content-only catalogs or optimized distributions without a game runtime."""
import argparse
import json
import math
import pyarrow as pa
import pyarrow.parquet as pq

NAMES = ["record_index", "class_id", "class_name", "probability",
         "win_multiplier", "win", "bet"]

def read_catalog(path):
    catalog = pq.ParquetFile(path)
    names = catalog.schema_arrow.names
    if names == ["record_index", "class_id", "class_name", "win_multiplier"]:
        return {"records": catalog.metadata.num_rows, "legacy": True,
                "warning": "Legacy four-column catalog: exact win/bet unavailable; not the seven-column contract."}
    expected = pa.schema([
        pa.field("record_index", pa.int64(), False),
        pa.field("class_id", pa.int32(), False),
        pa.field("class_name", pa.string(), False),
        pa.field("probability", pa.float64(), True),
        pa.field("win_multiplier", pa.float64(), False),
        pa.field("win", pa.int64(), False),
        pa.field("bet", pa.int64(), False),
    ])
    if not catalog.schema_arrow.equals(expected, check_metadata=False):
        raise ValueError("unexpected seven-column schema")
    count, nulls = 0, 0
    for batch in catalog.iter_batches(batch_size=8192):
        for row in batch.to_pylist():
            if row["record_index"] != count:
                raise ValueError("original catalog order must be contiguous")
            p = row["probability"]
            nulls += p is None
            if p is not None and (not math.isfinite(p) or not 0 <= p <= 1):
                raise ValueError("invalid probability")
            if row["win"] < 0 or row["bet"] <= 0:
                raise ValueError("invalid integer payout/bet")
            if not math.isfinite(row["win_multiplier"]) or row["win_multiplier"] < 0:
                raise ValueError("invalid multiplier")
            count += 1
    if nulls not in (0, count):
        raise ValueError("mixed null and populated probabilities")
    return {"records": count, "probability": "unspecified" if nulls == count else "specified",
            "legacy": False}

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("catalog")
    print(json.dumps(read_catalog(parser.parse_args().catalog), indent=2))
