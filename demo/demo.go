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
		core.NewDefault(),
		problab.Configs(demo_configs.FS),
		problab.Logics(demo_logic.Reg),
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
