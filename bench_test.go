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

package problab

import (
	"io"
	"testing"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
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

// BenchmarkSim 量測 Simulator 單一 worker 的吞吐（執行加上記錄）；每次操作為一轉。
func BenchmarkSim(b *testing.B) {
	for _, c := range []struct {
		name    string
		optimal bool
	}{{"off", false}, {"manifest", true}} {
		b.Run(c.name, func(b *testing.B) {
			s, err := benchmarkLab(b, c.optimal).NewSimulator(0, MachineConfig{Seed: SeedInt64(42)})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			if _, _, err := s.Sim(0, max(b.N, 1), false); err != nil {
				b.Fatal(err)
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
		if _, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(int64(i + 1))}); err != nil {
			b.Fatalf("NewMachine: %v", err)
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
	machine, err := newProductionMachineForTest(lab, 0)
	if err != nil {
		b.Fatal(err)
	}
	request := &dto.SpinRequest{
		GameName: "demo_normal", GameId: 0,
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
