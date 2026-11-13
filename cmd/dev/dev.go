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
	"fmt"
	"io/fs"
	"log"
	"net"
	"os/exec"
	"runtime"
	"time"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/demo/demo_devgames"
	"github.com/zintix-labs/problab/server"
	"github.com/zintix-labs/problab/server/logger"
	"github.com/zintix-labs/problab/server/svrcfg"
)

// Application-owned injection points. A private project replaces these sources;
// the server assembly and lifecycle code below stays unchanged.
var (
	// pLab must construct your project's games with the same PRNG factory,
	// configs, logics and optimal setting as production, assembled with
	// problab.New, RegisterAll and Freeze (see demo.NewProbLabForDev). Each
	// game's optimal artifact is then loaded on its first spin. problab.NewAuto
	// also works, but preloads every game's artifact at startup.
	// This command owns and closes the returned Lab.
	pLab func() (*problab.Problab, error) = demo.NewProbLabForDev

	// devGames holds the panel plugins, one <gid>.js per game at its root, that
	// draw game-specific details (see demo/demo_devgames). Problab ships none;
	// a game without a plugin, or a nil devGames, uses the common panel only.
	// An embed.FS needs a restart after editing a plugin; os.DirFS("devgames")
	// picks up edits on browser reload.
	devGames fs.FS = demo_devgames.FS
)

// The server runs in ModePanel: only the landing page and /dev, with no /v1
// endpoints and no machine pools, so nothing preloads optimal artifacts.
func main() {
	runDevPanel()
}

func runDevPanel() {
	lab, err := pLab()
	if err != nil {
		log.Fatal("new problab failed: " + err.Error())
	}
	defer func() { _ = lab.Close() }()

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
	server.Run(&svrcfg.SvrCfg{
		Log:         logger.NewDefaultAsyncLogger(logger.ModeDev),
		SlotBufSize: 1,
		Problab:     lab,
		Mode:        svrcfg.ModePanel,
		DevGames:    devGames,
	})
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
