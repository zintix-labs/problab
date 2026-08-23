// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"context"
	"testing"

	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/core"
)

func TestOptimalLifecycleClosesPoolsBeforeArtifact(t *testing.T) {
	lab, err := NewAuto(
		core.Default(),
		Configs(testLegacyConfigFS(t)),
		Logics(demo_logic.Logics),
		WithOptimalFS(testOptimalFS(t)),
	)
	if err != nil {
		t.Fatalf("NewAuto: %v", err)
	}
	machine, err := lab.NewMachineWithSeed(0, 1, true)
	if err != nil {
		t.Fatalf("NewMachineWithSeed: %v", err)
	}
	artifact := machine.optimal

	runtime, err := lab.BuildRuntime(2)
	if err != nil {
		t.Fatalf("BuildRuntime: %v", err)
	}
	runtime.Close()
	for _, id := range runtime.ids {
		if !runtime.pools[id].Closed() {
			t.Fatalf("pool %d was not closed with runtime", id)
		}
	}
	if _, err := runtime.Spin(context.Background(), nil); err == nil {
		t.Fatal("closed runtime accepted a spin")
	}
	if artifact.Closed() {
		t.Fatal("runtime close must not close Problab-owned artifact")
	}

	if err := lab.Close(); err != nil {
		t.Fatalf("Problab.Close: %v", err)
	}
	if err := lab.Close(); err != nil {
		t.Fatalf("second Problab.Close: %v", err)
	}
	if !lab.Closed() || !artifact.Closed() {
		t.Fatal("Problab close did not close its artifact store")
	}
	if _, err := lab.NewMachineWithSeed(0, 2, true); err == nil {
		t.Fatal("closed Problab created a new machine")
	}
}
