// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"io"
	"testing"

	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/core"
)

func BenchmarkMachineBuildWithSharedOptimal(b *testing.B) {
	lab, err := NewAuto(
		core.Default(),
		Configs(testManifestConfigFS(b)),
		Logics(demo_logic.Logics),
		WithOptimalFS(testManifestFS(b, false)),
	)
	if err != nil {
		b.Fatalf("NewAuto: %v", err)
	}
	defer func() { _ = lab.Close() }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := lab.NewMachineWithSeed(0, int64(i+1), true); err != nil {
			b.Fatalf("NewMachineWithSeed: %v", err)
		}
	}
	b.StopTimer()
	if got := lab.optimal.LoadCount(); got != 1 {
		b.Fatalf("artifact load count=%d, want 1", got)
	}
}

func BenchmarkOptimalProductionSpin(b *testing.B) {
	factory, err := core.NewChaCha20Factory(zeroBenchmarkReader{})
	if err != nil {
		b.Fatal(err)
	}
	lab, err := NewAuto(
		factory,
		Configs(testManifestConfigFS(b)),
		Logics(demo_logic.Logics),
		WithOptimalFS(testManifestFS(b, false)),
		WithSeedEntropy(zeroBenchmarkReader{}),
	)
	if err != nil {
		b.Fatalf("NewAuto: %v", err)
	}
	defer func() { _ = lab.Close() }()
	machine, err := lab.NewMachine(0, false)
	if err != nil {
		b.Fatal(err)
	}
	request := &dto.SpinRequest{
		UID: "benchmark", GameName: "demo_normal", GameId: 0,
		Bet: 40, BetMode: 0, BetMult: 1,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := machine.Spin(request); err != nil {
			b.Fatal(err)
		}
	}
}

type zeroBenchmarkReader struct{}

func (zeroBenchmarkReader) Read(dst []byte) (int, error) {
	clear(dst)
	return len(dst), nil
}

var _ io.Reader = zeroBenchmarkReader{}
