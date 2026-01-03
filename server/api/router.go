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

	switch sCfg.Mode {
	case svrcfg.ModeDev:
		if err := registerDev(svr, sCfg); err != nil {
			return err
		}
	case svrcfg.ModeProd:
		if err := registerProd(svr, sCfg); err != nil {
			return err
		}
	default:
		if err := registerProd(svr, sCfg); err != nil {
			return err
		}
	}
	return nil
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

// registerDev mounts dev APIs
func registerDev(svr netsvr.NetSvr, sCfg *svrcfg.SvrCfg) error {
	// dev panel
	dev.Register(svr, sCfg)
	r, err := v1.NewSpinHandler(sCfg)
	if err != nil {
		return err
	}
	s, err := v1.NewSimHandler(sCfg)
	if err != nil {
		return err
	}

	// others
	svr.Group("/v1", func(rt netsvr.NetRouter) {
		rt.Get("/spin", r.Spin)
		rt.Post("/spin", r.Spin)
		rt.Get("/health", r.Health)
		rt.Get("/poolmetrics", r.PoolMetrics)
		rt.Get("/sim", s.Sim)
		rt.Get("/simplayer", s.SimPlayers)
		rt.Post("/simbycfg", s.SimByJson)
		rt.Post("/sim", s.Sim)
		rt.Post("/simplayer", s.SimPlayers)
		rt.Post("/stat", v1.Stat)
	})
	return nil
}

// registerProd mounts prod APIs
func registerProd(svr netsvr.NetSvr, sCfg *svrcfg.SvrCfg) error {
	r, err := v1.NewSpinHandler(sCfg)
	if err != nil {
		return err
	}
	// others
	svr.Group("/v1", func(rt netsvr.NetRouter) {
		rt.Get("/spin", r.Spin)
		rt.Post("/spin", r.Spin)
		rt.Get("/health", r.Health)
		rt.Get("/poolmetrics", r.PoolMetrics)
	})
	return nil
}
