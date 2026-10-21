// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"testing"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/core"
)

// 執行入口的效能基準：Simulator 經由
// SpinInternal 的吞吐，以及 playerexp 經由 SpinOffline 的吞吐。

func benchmarkMachine(b *testing.B, optimal bool) *Machine {
	b.Helper()
	opts := []ProblabOption{}
	cfg := Configs(testManifestConfigFS(b))
	if optimal {
		opts = append(opts, WithOptimalFS(testManifestFS(b, false)))
	} else {
		cfg = Configs(demo_configs.FS)
	}
	lab, err := NewAuto(core.Default(), cfg, Logics(demo_logic.Logics), opts...)
	if err != nil {
		b.Fatalf("NewAuto: %v", err)
	}
	b.Cleanup(func() { _ = lab.Close() })
	m, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(42)})
	if err != nil {
		b.Fatal(err)
	}
	return m
}

func BenchmarkSpinInternal(b *testing.B) {
	for _, c := range []struct {
		name    string
		optimal bool
	}{{"off", false}, {"manifest", true}} {
		b.Run(c.name, func(b *testing.B) {
			m := benchmarkMachine(b, c.optimal)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				m.SpinInternal(0)
			}
		})
	}
}

func BenchmarkSpinOffline(b *testing.B) {
	for _, c := range []struct {
		name    string
		optimal bool
	}{{"off", false}, {"manifest", true}} {
		b.Run(c.name, func(b *testing.B) {
			m := benchmarkMachine(b, c.optimal)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := m.SpinOffline(0, 1); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
