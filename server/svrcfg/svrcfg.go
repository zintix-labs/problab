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

package svrcfg

import (
	"log/slog"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/server/logger"
)

// RunMode controls which HTTP endpoints are exposed by the server router.
//
// The same Problab engine can be used in multiple contexts (lab/dev vs production).
// This flag lets you explicitly choose the exposure surface:
//
//   - ModeDev: local development / benchmarking / debugging (tooling endpoints enabled)
//   - ModeProd: production-safe exposure (minimal endpoints only)
//
// IMPORTANT:
// In the problab repository, the built-in cmd/svr is intended as a "lab server"
// and typically runs with ModeDev.
// For real deployments, assemble your own service (e.g. via scaffold) and run
// ModeProd by default.
type RunMode uint8

const (
	// ModeDev enables the full "lab" surface.
	//
	// This mode is intended for local development, benchmarking, and debugging.
	// It may expose developer tooling endpoints such as:
	//   - simulation / long-run endpoints
	//   - stats/report helpers
	//   - dev panel and per-spin JSON inspection
	//
	// Do NOT use this mode for public-facing production deployments.
	ModeDev RunMode = iota

	// ModeProd enables production-safe exposure only.
	//
	// This mode is intended for embedding Problab into a real backend service.
	// It should expose only minimal endpoints required by production traffic
	// (e.g. spin + health(todo)) and keep all tooling/simulation endpoints disabled.
	ModeProd
)

type SvrCfg struct {
	Log         *slog.Logger
	SlotBufSize int
	Problab     *problab.Problab
	Mode        RunMode
}

func (sc *SvrCfg) Vaild() error {
	if sc.Log != nil {
		if ah, ok := sc.Log.Handler().(*logger.AsyncHandler); ok && !ah.Ready() {
			return errs.NewFatal("nil default log handler: async handler is nil")
		}
	} else {
		// Keep quiet but valid: if caller doesn't provide a logger, use a safe default.
		sc.Log, _ = logger.NewAsync(1024, logger.ModeDev)
	}

	// Clamp SlotBufSize into a small range for resource control.
	// 1 <= SlotBufSize <= 10
	sc.SlotBufSize = max(1, sc.SlotBufSize)
	sc.SlotBufSize = min(10, sc.SlotBufSize)
	if sc.Problab == nil {
		return errs.NewFatal("problab is required")
	}
	return nil
}
