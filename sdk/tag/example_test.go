// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package tag_test

import (
	"fmt"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/tag"
)

func ExampleRegistry_BitSet() {
	registry, err := tag.NewRegistry(map[string]tag.IsTag{
		"win": func(result *buf.SpinResult) bool { return result.TotalWin > 0 },
	})
	if err != nil {
		panic(err)
	}
	bits, err := registry.BitSet("win")
	if err != nil {
		panic(err)
	}
	mask, err := bits.Mask("win")
	if err != nil {
		panic(err)
	}
	hits := bits.Tagging(&buf.SpinResult{TotalWin: 30})
	fmt.Println(hits&mask == mask)
	// Output: true
}
