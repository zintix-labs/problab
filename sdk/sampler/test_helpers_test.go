// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package sampler

import (
	"testing"

	"github.com/zintix-labs/problab/sdk/core"
)

func newTestCore(t testing.TB, seed int64) *core.Core {
	t.Helper()
	rng, err := core.Default().New(core.EncodeInt64Seed(seed))
	if err != nil {
		t.Fatalf("create test PRNG: %v", err)
	}
	return core.New(rng)
}
