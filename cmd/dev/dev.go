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

// Command dev opens ProbLab Dev, the per-segment visual panel, for the demo
// games. It is also the reference assembly for applications that run the panel
// on their own games: replace the configs, logics and plugins below.
package main

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"runtime"
	"time"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_devgames"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/server"
	"github.com/zintix-labs/problab/server/logger"
	"github.com/zintix-labs/problab/server/svrcfg"
)

func main() {
	runDevPanel()
}

func runDevPanel() {
	lab, err := newLab()
	if err != nil {
		log.Fatal("new problab failed: " + err.Error())
	}
	defer func() { _ = lab.Close() }()

	scfg := &svrcfg.SvrCfg{
		Log:         logger.NewDefaultAsyncLogger(logger.ModeDev),
		SlotBufSize: 1,
		Problab:     lab,
		// ModePanel mounts only /dev: no /v1 endpoints and no machine pools.
		Mode: svrcfg.ModePanel,
		// Game-specific drawing (<gid>.js). Problab ships none; a game without a
		// plugin is shown with the common panel. os.DirFS("devgames") instead of
		// an embed.FS picks up plugin edits on reload without a restart.
		DevGames: demo_devgames.FS,
	}

	url := "http://localhost:5808/dev"
	go func() {
		// Wait until the server is actually listening, then open the browser.
		if err := waitForTCP(":5808", 5*time.Second); err != nil {
			log.Fatal("dev server not ready:" + err.Error())
		}
		if err := openBrowser(url); err != nil {
			log.Fatal("open browser failed:" + err.Error())
		}
	}()
	server.Run(scfg)
}

// newLab assembles the engine with New, RegisterAll and Freeze instead of
// NewAuto: NewAuto preloads every game's optimal artifact at startup, while
// this loads each game's artifact on its first spin. Use the same PRNG
// factory, configs, logics and optimal directory as the production engine.
// Production servers should keep NewAuto, which fails fast on a broken
// artifact and keeps the first request from waiting on a load.
func newLab() (*problab.Problab, error) {
	lab, err := problab.New(
		core.Default(),
		problab.Configs(demo_configs.FS),
		problab.Logics(demo_logic.Logics),
		// problab.WithOptimalDir("<optimal root>"), // when games use optimal
	)
	if err != nil {
		return nil, err
	}
	if err := lab.RegisterAll(); err != nil {
		_ = lab.Close()
		return nil, err
	}
	lab.Freeze()
	return lab, nil
}

func waitForTCP(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := "127.0.0.1" + addr
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", url, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
