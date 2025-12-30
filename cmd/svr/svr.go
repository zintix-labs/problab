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
		core.NewDefault(),
		problab.Configs(demo_configs.FS),
		problab.Logics(demo_logic.Reg),
	)
	if err != nil {
		return nil, err
	}
	sCfg := &svrcfg.SvrCfg{
		Log:         log,
		SlotBufSize: cfg.SlotBufSize,
		Problab:     lab,
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
