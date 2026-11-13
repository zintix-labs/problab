// Copyright 2026 Zintix Labs
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

// Package cli runs the ProbLab Dev panel: it serves the landing page and /dev in
// svrcfg.ModePanel, with no /v1 endpoints and no machine pools, until the
// context ends. A command only supplies the Lab and its plugins; see cmd/dev.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/server/api"
	"github.com/zintix-labs/problab/server/netsvr"
	"github.com/zintix-labs/problab/server/svrcfg"
)

// DefaultAddr is the listen address when Options.Addr is empty.
const DefaultAddr = ":5808"

const (
	readyTimeout    = 5 * time.Second
	shutdownTimeout = 5 * time.Second
)

// Options exposes only application composition. Lab remains caller-owned.
type Options struct {
	Lab         *problab.Problab // required; borrowed, the caller closes it
	DevGames    fs.FS            // <gid>.js plugins and an optional theme.css at its root; nil uses the common panel only
	Addr        string           // listen address; empty selects DefaultAddr
	OpenBrowser bool             // open the panel in the default browser once it is listening
	Output      io.Writer        // nil suppresses text (the panel URL and browser warnings)
	Log         *slog.Logger     // request log; nil selects the server's default
}

// Run serves the panel until ctx ends and then shuts the server down, waiting up
// to five seconds for open connections. It returns 0 after a stop requested
// through ctx, and 1 with an error when the options are invalid or the server
// cannot start or stops on its own. The command owns signals and Lab.Close.
func Run(ctx context.Context, o Options) (int, error) {
	if o.Lab == nil {
		return 1, errors.New("dev panel: Lab is required")
	}
	addr := o.Addr
	if addr == "" {
		addr = DefaultAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1, fmt.Errorf("dev panel: invalid address %q: %w", addr, err)
	}
	cfg := &svrcfg.SvrCfg{Log: o.Log, SlotBufSize: 1, Problab: o.Lab, Mode: svrcfg.ModePanel, DevGames: o.DevGames}
	if err := cfg.Vaild(); err != nil {
		return 1, err
	}
	svr := netsvr.NewChiServer(addr)
	if err := api.RegisterRoutes(svr, cfg); err != nil {
		return 1, err
	}

	stopped := make(chan error, 1)
	go func() { stopped <- svr.Run() }()
	shutdown := func() error {
		sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return svr.Shutdown(sctx)
	}

	if err := waitReady(ctx, net.JoinHostPort(dialHost(host), port), stopped); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			_ = shutdown()
			return 0, nil
		}
		_ = shutdown()
		return 1, err
	}
	url := "http://" + net.JoinHostPort(browseHost(host), port) + "/dev"
	printf(o.Output, "ProbLab Dev: %s\n", url)
	if o.OpenBrowser {
		if err := openBrowser(url); err != nil {
			printf(o.Output, "open browser failed: %v\n", err)
		}
	}

	select {
	case <-ctx.Done():
		// A stop requested through ctx is not a failure: connections still open
		// after shutdownTimeout are dropped when the process exits.
		if err := shutdown(); err != nil {
			printf(o.Output, "dev panel: some connections did not close within %s\n", shutdownTimeout)
		}
		return 0, nil
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return 0, nil
		}
		return 1, fmt.Errorf("dev panel: %w", err)
	}
}

// waitReady waits until addr accepts connections. A server that stops first
// (for example, the port is in use) returns its error.
func waitReady(ctx context.Context, addr string, stopped <-chan error) error {
	deadline := time.Now().Add(readyTimeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case err := <-stopped:
			return fmt.Errorf("dev panel: %w", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("dev panel: %s not ready after %s", addr, readyTimeout)
		}
	}
}

// dialHost is the host used to probe the listener: an unspecified address
// (":5808", "0.0.0.0", "::") listens on loopback too.
func dialHost(host string) string {
	if isUnspecified(host) {
		return "127.0.0.1"
	}
	return host
}

// browseHost is the host shown in the panel URL.
func browseHost(host string) string {
	if isUnspecified(host) {
		return "localhost"
	}
	return host
}

func isUnspecified(host string) bool {
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
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

func printf(w io.Writer, format string, args ...any) {
	if w != nil {
		_, _ = fmt.Fprintf(w, format, args...)
	}
}
