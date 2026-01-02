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

package main

import (
	"flag"
	"fmt"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/server"
	"github.com/zintix-labs/problab/server/logger"
	"github.com/zintix-labs/problab/server/svrcfg"
)

// This command is intentionally a "lab server" entrypoint for the problab repo.
// It enables all developer endpoints by default.
// For production deployments, use a separate scaffold project and run ModeProd.
func main() {
	cfg, err := loadConfigFromFlags()
	if err != nil {
		fmt.Println(err)
	}
	server.Run(cfg)
}

type config struct {
	LogMode     string
	SlotBufSize int
}

func loadConfigFromFlags() (*svrcfg.SvrCfg, error) {
	cfg := new(config)
	flag.StringVar(&cfg.LogMode, "log-mode", "ModeDev", "log mode: ModeDev|ModeProd|ModeSilence")
	flag.IntVar(&cfg.SlotBufSize, "buf", 3, "number of machine instances per game")

	flag.Parse()

	log, _ := logger.NewAsync(4096, cfg.norm())

	lab, err := problab.NewAuto(
		core.Default(),
		problab.Configs(demo_configs.FS),
		problab.Logics(demo_logic.Logics),
	)
	if err != nil {
		return nil, err
	}
	sCfg := &svrcfg.SvrCfg{
		Log:         log,
		SlotBufSize: cfg.SlotBufSize,
		Problab:     lab,
		Mode:        svrcfg.ModeDev,
	}
	return sCfg, nil
}

func (cfg *config) norm() logger.LogMode {
	switch cfg.LogMode {
	case "ModeDev":
		return logger.ModeDev
	case "ModeProd":
		return logger.ModeProd
	case "ModeSilence":
		return logger.ModeSilence
	default:
		return logger.ModeDev
	}
}
