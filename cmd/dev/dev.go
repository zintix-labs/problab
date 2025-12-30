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
