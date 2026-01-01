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

package api

import (
	"log/slog"

	"github.com/zintix-labs/problab/server/api/dev"
	"github.com/zintix-labs/problab/server/api/index"
	v1 "github.com/zintix-labs/problab/server/api/v1"
	"github.com/zintix-labs/problab/server/netsvr"
	"github.com/zintix-labs/problab/server/netsvr/middleware"
	"github.com/zintix-labs/problab/server/svrcfg"
)

// RegisterRoutes registers HTTP routes for Problab based on SvrCfg.Mode.
//
// ModeDev (lab/dev):
//   - Enables developer tooling endpoints (simulation, stat, dev panel, etc.).
//   - Intended for local development, benchmarking, debugging, and demos.
//
// ModeProd (production-safe):
//   - Exposes only minimal, production-safe endpoints (e.g. spin/health).
//   - Use this mode when embedding Problab into a real backend service.
//
// NOTE:
// The problab repo's built-in cmd/svr is designed as a lab server (ModeDev).
// Production services should typically be assembled in a separate project
// (scaffold) and run with ModeProd by default.
func RegisterRoutes(svr netsvr.NetSvr, sCfg *svrcfg.SvrCfg) error {
	registerMiddleware(svr, sCfg.Log) // 1. middleware
	registerIndex(svr)                // 2. landing page

	// 3. dev tools (disabled in production)
	if sCfg.Mode == svrcfg.ModeDev {
		dev.Register(svr, sCfg)
	}

	// 4. v1 API
	return registerV1API(svr, sCfg)
}

// registerMiddleware installs common middleware.
func registerMiddleware(svr netsvr.NetSvr, log *slog.Logger) {
	svr.Use(middleware.RequestID)
	svr.Use(middleware.AccessLog(log))
	svr.Use(middleware.Recover)
	svr.Use(middleware.Compression)
}

// registerIndex mounts the landing page.
func registerIndex(svr netsvr.NetSvr) {
	svr.Get("/", index.IndexHandlerFn)
}

// registerV1API mounts v1 endpoints.
// When enableSim is true, simulation/tooling endpoints are also exposed.
func registerV1API(svr netsvr.NetSvr, sCfg *svrcfg.SvrCfg) error {
	r, err := v1.NewSpinHandler(sCfg)
	if err != nil {
		return err
	}

	var s *v1.SimHandler
	if sCfg.Mode == svrcfg.ModeDev {
		simHandler, err := v1.NewSimHandler(sCfg)
		if err != nil {
			return err
		}
		s = simHandler
	}

	svr.Group("/v1", func(vOne netsvr.NetRouter) {
		// Production-safe endpoints
		vOne.Get("/spin", r.Spin)
		vOne.Post("/spin", r.Spin)

		if sCfg.Mode == svrcfg.ModeProd {
			return
		}

		// Simulation / tooling endpoints (dev only)
		vOne.Get("/sim", s.Sim)
		vOne.Get("/simplayer", s.SimPlayers)

		vOne.Post("/simbycfg", s.SetByJson)
		vOne.Post("/sim", s.Sim)
		vOne.Post("/simplayer", s.SimPlayers)
		vOne.Post("/stat", v1.Stat)
	})
	return nil
}
