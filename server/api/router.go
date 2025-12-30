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

// RegisterRoutes 註冊
func RegisterRoutes(svr netsvr.NetSvr, sCfg *svrcfg.SvrCfg) {
	registerMiddleware(svr, sCfg.Log) // 1. 註冊 middleware
	registerIndex(svr)                // 2. 註冊主頁
	dev.Register(svr, sCfg)           // 3. 開發者工具頁
	registerV1API(svr, sCfg)          // 4. 註冊 v1 api
}

// 註冊 middleware
func registerMiddleware(svr netsvr.NetSvr, log *slog.Logger) {
	svr.Use(middleware.RequestID)
	svr.Use(middleware.AccessLog(log))
	svr.Use(middleware.Recover)
	svr.Use(middleware.Compression)
}

// 註冊主頁
func registerIndex(svr netsvr.NetSvr) {
	svr.Get("/", index.IndexHandlerFn)
}

// 註冊 v1 api
func registerV1API(svr netsvr.NetSvr, sCfg *svrcfg.SvrCfg) error {
	r, err := v1.NewSpinHandler(sCfg)
	if err != nil {
		return err
	}
	s, err := v1.NewSimHandler(sCfg.Problab)
	if err != nil {
		return err
	}
	svr.Group("/v1", func(vOne netsvr.NetRouter) {
		vOne.Get("/spin", r.Spin)
		vOne.Get("/sim", s.Sim)
		vOne.Get("/simplayer", s.SimPlayers)

		vOne.Post("/simbycfg", s.SetByJson)
		vOne.Post("/spin", r.Spin)
		vOne.Post("/sim", s.Sim)
		vOne.Post("/simplayer", s.SimPlayers)
		vOne.Post("/stat", v1.Stat)
	})
	return nil
}
