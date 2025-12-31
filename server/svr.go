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

package server

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/server/api"
	"github.com/zintix-labs/problab/server/app"
	"github.com/zintix-labs/problab/server/netsvr"
	"github.com/zintix-labs/problab/server/svrcfg"
)

// Run is the default server entrypoint (assembler + runtime starter).
//
// It validates the provided SvrCfg, creates a default HTTP server adapter,
// registers routes, and starts the app lifecycle.
//
// IMPORTANT:
//   - Route exposure is controlled by SvrCfg.Mode.
//   - ModeDev enables developer tooling endpoints (sim/stat/dev panel).
//   - ModeProd exposes production-safe endpoints only (e.g. spin/health).
//
// This package does NOT assume any file path/env strategy.
// All dependencies must be explicitly injected via SvrCfg.
//
// Note:
// In the problab repository itself, the bundled cmd/svr is intended for
// local development / benchmarking / demo (ModeDev by default).
// For real production deployments, prefer assembling your own service
// (e.g. via scaffold) and run with ModeProd.
func Run(sCfg *svrcfg.SvrCfg) {
	if err := sCfg.Vaild(); err != nil {
		// 防止外層傳入的logger不可用
		fmt.Fprintln(os.Stderr, err)
		return
	}
	// Server
	svr := netsvr.NewChiServerDefault()

	// Register routes
	if err := api.RegisterRoutes(svr, sCfg); err != nil {
		sCfg.Log.Error("register route error:" + err.Error())
		return
	}

	// 運行
	app := app.NewWith(svr)
	sCfg.Log.Info("[problab] listening on http://localhost" + svr.Address())
	if err := app.Run(); err != nil {
		sCfg.Log.Error("app stopped:", slog.Any("err", err))
	}
}

// RunWithSvr is the same as Run, but lets callers inject a custom NetSvr
// (router adapter / listener / server lifecycle integration).
//
// Route exposure is still controlled by SvrCfg.Mode (dev vs prod).
func RunWithSvr(sCfg *svrcfg.SvrCfg, svr netsvr.NetSvr) {
	if err := sCfg.Vaild(); err != nil {
		// 防止外層傳入的logger不可用
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if svr == nil {
		sCfg.Log.Error(errs.NewFatal("svr is required").Error())
		return
	} else {
		if s, ok := svr.(*netsvr.ChiAdapter); ok && !s.Ready() {
			sCfg.Log.Error(errs.NewFatal("default server is not ready").Error())
			return
		}
	}

	// Register routes
	if err := api.RegisterRoutes(svr, sCfg); err != nil {
		sCfg.Log.Error("register route error:" + err.Error())
		return
	}

	// 運行
	app := app.NewWith(svr)
	sCfg.Log.Info("[problab] listening")
	if err := app.Run(); err != nil {
		sCfg.Log.Error("app stopped:", slog.Any("err", err))
	}
}
