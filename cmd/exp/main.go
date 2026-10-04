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

package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/demo/demo_strategies"
	"github.com/zintix-labs/problab/demo/demo_tags"
	"github.com/zintix-labs/problab/playerexp"
	expcli "github.com/zintix-labs/problab/playerexp/cli"
	"github.com/zintix-labs/problab/sdk/tag"
)

//go:embed exp_cfg.yaml
var config []byte

// Application-owned injection points. A private project replaces these sources
// and its embedded YAML; the execution/lifecycle code below stays unchanged.
var (
	// strategyCatalog maps game IDs to named StrategyFactory lists.
	// Define strategies in your package, assemble its catalog like
	// demo/demo_strategies/reg.go, then assign mystrategies.Strategies here.
	// These are factories, never shared player Strategy instances. Registration
	// validates duplicate names and nil factories before any player runs.
	strategyCatalog playerexp.StrategyCatalog = demo_strategies.Catalog

	// gameTags supplies the predicates referenced by events[].tags in YAML.
	// Names are game-scoped; bg/fg are demo tags, not engine built-ins.
	gameTags tag.GameTagCatalog = demo_tags.Catalog

	// pLab must construct your project's games and desired native/optimal
	// runtime. Replacing strategies alone does not replace the demo games.
	// This command owns and closes the returned Lab.
	pLab func() (*problab.Problab, error) = demo.NewProbLab
)

// The application owns its Lab, game-scoped tags and strategy factories.
// Replace these demo composition points in a commercial game project.
func main() {
	code, err := run(os.Args[1:], os.Stderr)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cmd/exp: %v\n", err)
	}
	if code != 0 {
		os.Exit(code)
	}
}
func run(args []string, output io.Writer) (code int, err error) {
	if len(args) != 0 {
		return 1, fmt.Errorf("cmd/exp does not accept arguments; edit embedded exp_cfg.yaml")
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
	strategies, err := strategyCatalog.BuildRegistry()
	if err != nil {
		return 1, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return expcli.Run(ctx, expcli.Options{Config: config, ConfigSource: `embedded "exp_cfg.yaml"`, Lab: lab, Strategies: strategies, Tags: gameTags, Output: output})
}
