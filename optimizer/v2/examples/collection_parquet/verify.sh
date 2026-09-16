#!/bin/sh
# Full acceptance entry: requires an existing Python environment; installs nothing.
set -eu
cd "$(dirname "$0")/../../../.."
: "${PROBLAB_PARQUET_PYTHON:?Set to Python with pyarrow, pandas and zstandard installed}"
export PROBLAB_PARQUET_PYTHON
"$PROBLAB_PARQUET_PYTHON" -c 'import pyarrow, pandas, zstandard'
unformatted=$(gofmt -l dto optimizer/v2 cmd/opt)
if [ -n "$unformatted" ]; then
  printf 'Unformatted Go files:\n%s\n' "$unformatted"
  exit 1
fi
go test ./... -count=1
go test -race ./dto ./optimizer/v2/... ./cmd/opt -count=1
go vet ./...
golangci-lint run ./dto/... ./optimizer/v2/... ./cmd/opt/...
PROBLAB_PARQUET_MEMORY=1 PROBLAB_RGS_MEMORY=1 go test ./optimizer/v2 -run MemoryProfile -count=1 -v
git diff --check
