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

// OptimalDir is the conventional local-only demo optimal output root.
// Generated gacha pairs in this directory are intentionally not versioned.
const OptimalDir = "demo/optimal"

func New() (*catalog.Catalog, error) {
	return catalog.New(demo_configs.FS)
}

func NewServerConfig() (*svrcfg.SvrCfg, error) {
	lab, err := NewProbLab()
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
	return newProbLab()
}

// NewProbLabWithOptimalDir builds the demo lab against optimal gacha pairs on
// disk instead of running unoptimized. Point root at the optimizer output —
// cmd/opt publishes each bet mode at
// <output.directory>/gacha/game_<gid>/mode_<mode>/<id>/ — set use_optimal and
// the gachas/seed_bank paths (relative to root) in the game config, and the lab
// maps the seed banks through mmap. This is how you exercise an optimizer run
// end to end without wiring problab.NewAuto by hand.
func NewProbLabWithOptimalDir(root string) (*problab.Problab, error) {
	return newProbLab(problab.WithOptimalDir(root))
}

func newProbLab(opts ...problab.ProblabOption) (*problab.Problab, error) {
	return problab.NewAuto(
		core.Default(),
		problab.Configs(demo_configs.FS),
		problab.Logics(demo_logic.Logics),
		opts...,
	)
}
