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

// 執行入口的效能基準：Simulator 與 optimizer 經由不擷取快照的 SpinUnsafe 的吞吐，
// 以及 playerexp 經由擷取快照的 SpinUnsafe 的吞吐。

func benchmarkLab(b *testing.B, optimal bool) *Problab {
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
	return lab
}

func benchmarkMachine(b *testing.B, optimal bool) *Machine {
	b.Helper()
	m, err := benchmarkLab(b, optimal).NewMachine(0, MachineConfig{Seed: SeedInt64(42)})
	if err != nil {
		b.Fatal(err)
	}
	return m
}

func BenchmarkSpinUnsafe(b *testing.B) {
	for _, c := range []struct {
		name    string
		optimal bool
	}{{"off", false}, {"manifest", true}} {
		b.Run(c.name, func(b *testing.B) {
			m := benchmarkMachine(b, c.optimal)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1})
			}
		})
	}
}

func BenchmarkSpinUnsafeCapture(b *testing.B) {
	for _, c := range []struct {
		name    string
		optimal bool
	}{{"off", false}, {"manifest", true}} {
		b.Run(c.name, func(b *testing.B) {
			m := benchmarkMachine(b, c.optimal)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: true})
			}
		})
	}
}

// BenchmarkSimMP 量測 Simulator 兩個 worker 的整體吞吐；每次操作為一轉。
func BenchmarkSimMP(b *testing.B) {
	for _, c := range []struct {
		name    string
		optimal bool
	}{{"off", false}, {"manifest", true}} {
		b.Run(c.name, func(b *testing.B) {
			s, err := benchmarkLab(b, c.optimal).NewSimulator(0, MachineConfig{Seed: SeedInt64(42)})
			if err != nil {
				b.Fatal(err)
			}
			rounds := max(b.N/2, 1)
			b.ReportAllocs()
			b.ResetTimer()
			if _, _, err := s.SimMP(0, rounds, 2, false); err != nil {
				b.Fatal(err)
			}
		})
	}
}
