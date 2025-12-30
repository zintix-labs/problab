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
	"log"
	"net"
	"os/exec"
	"runtime"
	"time"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/server"
)

func main() {
	runDevPanel()
}

func runDevPanel() {
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
	scfg, err := demo.NewServerConfig()
	if err != nil {
		log.Fatal("set server configs error:" + err.Error())
	}
	server.Run(scfg)
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
