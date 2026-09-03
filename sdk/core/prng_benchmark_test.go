// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package core

import "testing"

func BenchmarkPRNGUint64(b *testing.B) {
	benchmarks := []struct {
		name    string
		factory PRNGFactory
	}{
		{"PCG64", PCG64()},
		{"ChaCha20", Default()},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			rng, err := benchmark.factory.New(EncodeInt64Seed(1))
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			var result uint64
			for range b.N {
				result = rng.Uint64()
			}
			benchmarkUint64Sink = result
		})
	}
}

func BenchmarkPRNGUintN(b *testing.B) {
	benchmarks := []struct {
		name    string
		factory PRNGFactory
	}{
		{"PCG64", PCG64()},
		{"ChaCha20", Default()},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			rng, err := benchmark.factory.New(EncodeInt64Seed(1))
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			var result uint
			for range b.N {
				result = rng.UintN(97)
			}
			benchmarkUintSink = result
		})
	}
}

func BenchmarkPRNGIntN(b *testing.B) {
	benchmarks := []struct {
		name    string
		factory PRNGFactory
	}{
		{"PCG64", PCG64()},
		{"ChaCha20", Default()},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			rng, err := benchmark.factory.New(EncodeInt64Seed(1))
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			var result int
			for range b.N {
				result = rng.IntN(97)
			}
			benchmarkIntSink = result
		})
	}
}

func BenchmarkPRNGFloat64(b *testing.B) {
	benchmarks := []struct {
		name    string
		factory PRNGFactory
	}{
		{"PCG64", PCG64()},
		{"ChaCha20", Default()},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			rng, err := benchmark.factory.New(EncodeInt64Seed(1))
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			var result float64
			for range b.N {
				result = rng.Float64()
			}
			benchmarkFloat64Sink = result
		})
	}
}

func BenchmarkChaCha20RestoreHeavy(b *testing.B) {
	for _, draws := range []int{6, 7, 8} {
		b.Run(string(rune('0'+draws))+"Draws", func(b *testing.B) {
			rng, err := Default().New(EncodeInt64Seed(1))
			if err != nil {
				b.Fatal(err)
			}
			snapshot, err := rng.Snapshot()
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := rng.Restore(snapshot); err != nil {
					b.Fatal(err)
				}
				for range draws {
					benchmarkUint64Sink = rng.Uint64()
				}
				if _, err := rng.Snapshot(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPRNGReseed(b *testing.B) {
	chaChaFactory, err := NewChaCha20Factory(benchmarkZeroReader{})
	if err != nil {
		b.Fatal(err)
	}
	benchmarks := []struct {
		name    string
		factory PRNGFactory
	}{
		{"PCG64NoOp", PCG64()},
		{"ChaCha20", chaChaFactory},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			rng, err := benchmark.factory.New(EncodeInt64Seed(1))
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := rng.Reseed(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type benchmarkZeroReader struct{}

func (benchmarkZeroReader) Read(dst []byte) (int, error) {
	clear(dst)
	return len(dst), nil
}

var (
	benchmarkUint64Sink  uint64
	benchmarkUintSink    uint
	benchmarkIntSink     int
	benchmarkFloat64Sink float64
)
