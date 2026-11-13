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
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"syscall"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/demo/demo_devgames"
	devcli "github.com/zintix-labs/problab/server/api/dev/cli"
)

// Application-owned injection points. A private project replaces these sources;
// the execution/lifecycle code below stays unchanged.
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

// The application owns its Lab and plugins; the dev panel facade serves them in
// svrcfg.ModePanel (only /dev, no machine pools) until Ctrl+C.
func main() {
	code, err := run(os.Args[1:], os.Stdout)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cmd/dev: %v\n", err)
	}
	if code != 0 {
		os.Exit(code)
	}
}

func run(args []string, output io.Writer) (code int, err error) {
	flags := flag.NewFlagSet("cmd/dev", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	addr := flags.String("addr", devcli.DefaultAddr, "listen address")
	open := flags.Bool("open", true, "open the panel in the default browser")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 1, fmt.Errorf("cmd/dev accepts only -addr <address> and -open=<bool>")
	}
	lab, err := pLab()
	if err != nil {
		return 1, err
	}
	defer func() {
		err = errors.Join(err, lab.Close())
		if err != nil {
			code = 1
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return devcli.Run(ctx, devcli.Options{Lab: lab, DevGames: devGames, Addr: *addr, OpenBrowser: *open, Output: output})
}
