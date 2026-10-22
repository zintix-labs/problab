// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package tag_test

import (
	"fmt"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/tag"
)

func ExampleGameTagCatalog_CompileBy() {
	catalog := tag.GameTagCatalog{
		1: {
			"win": func(result *buf.SpinResult) bool { return result.TotalWin > 0 },
			"fg":  func(result *buf.SpinResult) bool { return result.GameModeCount > 1 },
		},
	}
	// Preparation: compile the names in use and each condition once.
	tagger, err := catalog.CompileBy(1, "win", "fg")
	if err != nil {
		panic(err)
	}
	winWithoutFG, err := tagger.Want("win")
	if err != nil {
		panic(err)
	}
	fg, err := tagger.Want("fg")
	if err != nil {
		panic(err)
	}
	tags := tagger.NewMasks() // owned and reused by this worker

	// Hot path: tag a result, then compare masks only.
	tagger.Tag(tags, &buf.SpinResult{TotalWin: 30, GameModeCount: 1})
	fmt.Println(tagger.Match(tags, winWithoutFG) && tagger.None(tags, fg), tagger.ToNames(tags))
	// Output: true [win]
}
