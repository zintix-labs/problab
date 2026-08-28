// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package v2 contains the second-generation Problab math-intent optimizer.
//
// The package deliberately separates three kinds of input:
//
//   - RunPlan selects a game, exactly one bet mode, engine, deterministic seed,
//     collection budget, and output directory.
//   - MathIntent records the designer's hard mathematical requirements and
//     group-level soft intent.
//   - EngineOptions controls bounded numerical work without changing designer
//     intent.
//
// Configuration is strict and versioned. LoadConfig rejects unknown YAML keys,
// additional YAML documents, and semantic contradictions before collection.
// A successfully resolved plan is self-contained, so later optimizer stages do
// not retain or mutate the maps and slices owned by Config.
//
// Expected mathematical failures are values, not Go errors. RunResult.Status
// and RunResult.Diagnostics describe infeasible or numerically inconclusive
// runs. Go errors are reserved for cancellation, I/O, dependency failures, and
// broken internal contracts.
//
// The current candidate policy is intentionally evaluator "none": the package
// emits one deterministic canonical LP representative and explicitly records
// that player-experience optimality is not claimed. The solve order proves hard
// feasibility, minimizes Main Group profile deviation, maximizes supported
// Other-bucket visibility, maximizes supported sibling visibility inside Main
// Groups, and only then selects the canonical bucket-probability vector. The two
// visibility stages are neutral soft refinements and never rewrite designer hard
// intent. Before publication, every
// seed-bank snapshot is replayed through raw game logic and semantic constraints
// are checked from effective alias marginals and replayed payouts. The verified
// mode is then persisted independently. Only when every mode for the game is
// present does the publisher create a manifest, load the staged bundle through
// the production Artifact v1 reader, and atomically expose it to runtime.
package v2
