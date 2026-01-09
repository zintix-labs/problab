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

package demo

import (
	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/catalog"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/server/logger"
	"github.com/zintix-labs/problab/server/svrcfg"
)

func New() (*catalog.Catalog, error) {
	return catalog.New(demo_configs.FS)
}

func NewServerConfig() (*svrcfg.SvrCfg, error) {
	lab, err := problab.NewAuto(
		core.Default(),
		problab.Configs(demo_configs.FS),
		problab.Logics(demo_logic.Logics),
	)
	if err != nil {
		return nil, errs.NewFatal("new problab failed:" + err.Error())
	}
	scfg := &svrcfg.SvrCfg{
		Log:         logger.NewDefaultAsyncLogger(logger.ModeDev),
		SlotBufSize: 1,
		Problab:     lab,
	}
	return scfg, nil
}

func NewProbLab() (*problab.Problab, error) {
	return problab.NewAuto(
		core.Default(),
		problab.Configs(demo_configs.FS),
		problab.Logics(demo_logic.Logics),
	)
}
