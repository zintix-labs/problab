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
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/demo/demo_logic/game_tags"
	"github.com/zintix-labs/problab/dto"
	optimizercli "github.com/zintix-labs/problab/optimizer/v2/cli"
)

const embeddedConfigName = "opt_cfg.yaml"

// These two package variables are the command's only application-owned
// injection points. A fork repoints them at its own implementation without
// editing runV2, optimizer/v2, or the embedded YAML; the values below are the
// defaults that ship with the demo build.
var (
	// gameTags is a game_tags.GameTagCatalog, i.e.
	// map[spec.GID]map[string]tag.IsTag, where each tag.IsTag is a
	// func(*buf.SpinResult) bool predicate. WithCollectionTags stores it on the
	// Collector and resolves it per plan against that plan's target game, so a
	// GID no plan targets is simply unused and a game absent from the catalog
	// has no tags. The inner map keys are the names
	// that classes[].collect.tags.matches / .mismatches reference in
	// opt_cfg.yaml; bg and fg are demo-defined predicates, not built-ins.
	//
	// To supply your own, build a catalog in your own package and assign it
	// here, mirroring demo/demo_logic/game_tags:
	//
	//	// mygame/tags.go
	//	func IsFreeSpins(sr *buf.SpinResult) bool { ... }
	//	var Tags = game_tags.GameTagCatalog{
	//		7: {"free_spins": IsFreeSpins},
	//	}
	//
	//	gameTags game_tags.GameTagCatalog = mygame.Tags
	gameTags game_tags.GameTagCatalog = game_tags.GameTags

	// useConverter is a dto.ResultConverter, i.e.
	// func(dto.SpinResult) (json.RawMessage, error). WithResultConverter binds
	// exactly one per Tuner, and both RGS families (rgs-collected and
	// rgs-optimized) call it once per replayed record. The returned bytes are
	// compacted and written verbatim as one line of results.jsonl.zst, so each
	// call must return exactly one valid UTF-8 JSON value; a converter error or
	// invalid JSON stops the Run instead of skipping the record. It cannot
	// affect the Parquet distribution or the native artifact_v1/gacha output.
	//
	// The default dto.IdentityConverter emits the whole dto.SpinResult, which
	// carries every record's start/after PRNG snapshots and checkpoint — that
	// is the complete seed material, so do not hand its output to a party that
	// must not receive it. Assigning nil selects the same default. Either way
	// the run report records converter=identity; any other function reports
	// converter=custom, which states provenance only, not that replay state was
	// actually removed.
	//
	// To supply your own, implement the function in your own package and assign
	// it here; optimizer/v2/examples/rgs shows one that drops the replay state:
	//
	//	useConverter dto.ResultConverter = resultconverter.Convert
	useConverter dto.ResultConverter = dto.IdentityConverter
)

// main is deliberately a thin composition root: it loads the command-owned
// embedded config and owns Problab and signal lifetimes. The shared CLI facade
// runs the plans and renders results; optimizer/v2 retains all math and output
// contracts for CLI and programmatic callers.
func main() {
	exitCode, err := runV2(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cmd/opt: %v\n", err)
		exitCode = 1
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runV2 owns command lifecycle and returns a stable process classification:
// zero for successful OPTIMAL or EXPORTED results, two for an expected typed
// non-success Run status, and one plus an error for operational failures.
func runV2(arguments []string, _ io.Writer, stderr io.Writer) (int, error) {
	if len(arguments) != 0 {
		return 1, fmt.Errorf(
			"cmd/opt does not accept command-line parameters; edit embedded %q instead (got %s)",
			embeddedConfigName,
			strings.Join(arguments, " "),
		)
	}

	raw, err := embeddedV2Config.ReadFile(embeddedConfigName)
	if err != nil {
		return 1, fmt.Errorf("read embedded config: %w", err)
	}
	lab, err := demo.NewProbLab()
	if err != nil {
		return 1, fmt.Errorf("construct demo Problab: %w", err)
	}
	defer func() { _ = lab.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return optimizercli.Run(ctx, optimizercli.Options{
		Config:          raw,
		ConfigSource:    fmt.Sprintf("embedded %q", embeddedConfigName),
		Lab:             lab,
		Output:          stderr,
		CollectionTags:  gameTags,
		ResultConverter: useConverter,
	})
}
