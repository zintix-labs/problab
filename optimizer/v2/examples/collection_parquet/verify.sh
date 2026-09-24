#!/bin/sh
# Copyright 2026 Zintix Labs
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

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
