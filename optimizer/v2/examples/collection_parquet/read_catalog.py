#!/usr/bin/env python3
"""Read and verify a Problab catalog without a bank, Go, or game runtime.

Requires pyarrow. This is a catalog reader, not a probability importer.
"""
import argparse
import hashlib
import json
import struct

import pyarrow as pa
import pyarrow.parquet as pq


def verify_catalog(path):
    catalog = pq.ParquetFile(path)
    # Preserve IEEE negative zero in JSON range bounds while retaining arbitrary
    # precision integer counts. Standard JSON parsers may decode literal -0 as 0.
    metadata = json.loads(catalog.metadata.metadata[b"problab.collection"],
                          parse_int=lambda s: -0.0 if s == "-0" else int(s))
    if metadata["schema"] != "problab.collection-outcomes/v1":
        raise ValueError("unsupported catalog schema")
    game = metadata["game"]
    if not isinstance(game, str) or not game.isascii() or not game.isdecimal() or str(int(game)) != game:
        raise ValueError("game must be a canonical decimal string")
    digest = hashlib.sha256()

    def number(fmt, value):
        digest.update(struct.pack("<" + fmt, value))

    def string(value):
        raw = value.encode("utf-8")
        number("Q", len(raw))
        digest.update(raw)

    string("problab.collection-outcomes/dataset-id/v1")
    string(metadata["schema"])
    bank_digest = bytes.fromhex(metadata["bank_sha256"])
    if len(bank_digest) != 32:
        raise ValueError("invalid bank SHA256")
    digest.update(bank_digest)
    number("Q", metadata["record_count"])
    number("Q", int(game))
    number("q", metadata["bet_mode"])
    number("q", metadata["bet_unit"])
    string(metadata["multiplier_unit"])
    string(metadata["ordering"])
    number("?", metadata["partial"])
    number("?", metadata["distinct"])
    string(metadata["sampling"])
    classes = metadata["classes"]
    number("Q", len(classes))
    for cls in classes:
        number("i", cls["class_id"])
        string(cls["class_name"])
        for bound in cls["win_range"]:
            number("d", bound)
        for key in ("matches", "mismatches"):
            tags = cls["tags"][key]
            number("Q", len(tags))
            for tag in tags:
                string(tag)
        number("Q", cls["requested"])
        number("Q", cls["retained"])

    columns = ["record_index", "class_id", "class_name", "win_multiplier"]
    if catalog.schema_arrow.names != columns:
        raise ValueError("unexpected catalog columns/order")
    for name, dtype in zip(columns, [pa.int64(), pa.int32(), pa.string(), pa.float64()]):
        if catalog.schema_arrow.field(name) != pa.field(name, dtype, nullable=False):
            raise ValueError(f"unexpected type or nullability: {name}")
    count = 0
    retained = [0] * len(classes)
    for batch in catalog.iter_batches(batch_size=8192, columns=columns):
        for index, class_id, name, win in zip(*(c.to_pylist() for c in batch.columns)):
            if index != count or not 0 <= class_id < len(classes) or classes[class_id]["class_name"] != name:
                raise ValueError("record index or Class mapping mismatch")
            number("q", index)
            number("i", class_id)
            string(name)
            number("d", win)
            retained[class_id] += 1
            count += 1
    if count != metadata["record_count"] or retained != [c["retained"] for c in classes]:
        raise ValueError("record count mismatch")
    actual = "sha256:" + digest.hexdigest()
    if actual != metadata["dataset_id"]:
        raise ValueError(f"Dataset ID mismatch: {actual}")
    return metadata


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("catalog")
    args = parser.parse_args()
    result = verify_catalog(args.catalog)
    print(json.dumps({k: result[k] for k in ("schema", "dataset_id", "record_count", "game", "partial", "distinct")}, indent=2))
