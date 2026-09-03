// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package core

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"runtime"
	"sync"
	"testing"
)

func TestChaCha20SeedMaterialIsOpaqueAndDeterministic(t *testing.T) {
	factory, err := NewChaCha20Factory(bytes.NewReader(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range [][]byte{
		nil,
		{},
		[]byte("plain string"),
		[]byte("Unicode 種子 🎰"),
		bytes.Repeat([]byte{0xa5}, 4096),
	} {
		a, err := factory.New(seed)
		if err != nil {
			t.Fatalf("New(%q): %v", seed, err)
		}
		b, err := factory.New(append([]byte(nil), seed...))
		if err != nil {
			t.Fatal(err)
		}
		aSnapshot, _ := a.Snapshot()
		bSnapshot, _ := b.Snapshot()
		if !bytes.Equal(aSnapshot, bSnapshot) {
			t.Fatalf("same seed %q produced different snapshots", seed)
		}
		for range 32 {
			if a.Uint64() != b.Uint64() {
				t.Fatalf("same seed %q produced different output", seed)
			}
		}
	}
}

func TestChaCha20FactoryDoesNotRetainCallerSeed(t *testing.T) {
	factory := Default()
	seed := []byte("caller-owned-seed")
	rng, err := factory.New(seed)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := rng.Snapshot()
	for i := range seed {
		seed[i] ^= 0xff
	}
	after, _ := rng.Snapshot()
	if !bytes.Equal(before, after) {
		t.Fatal("Factory retained caller seed storage")
	}
}

func TestChaCha20SnapshotCodecGolden(t *testing.T) {
	rng, err := Default().New(EncodeInt64Seed(0))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := rng.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	const want = "50434801ddec9008a3773829eca3c6f5f07d3f65b8b4b0a7cc66d30f6ab6a44fcc5ad2f1a137ef09ffb4a73fa0c95de70000000000"
	if got := hex.EncodeToString(snapshot); got != want {
		t.Fatalf("snapshot=%s, want %s", got, want)
	}
}

func TestChaCha20DeriveSeedSeparatesDomainAndIndex(t *testing.T) {
	parent := []byte("parent-seed")
	original := append([]byte(nil), parent...)
	factory := Default()
	a, err := factory.DeriveSeed(parent, StreamID{Domain: "a", Index: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := factory.DeriveSeed(parent, StreamID{Domain: "b", Index: 1})
	c, _ := factory.DeriveSeed(parent, StreamID{Domain: "a", Index: 2})
	aAgain, _ := factory.DeriveSeed(parent, StreamID{Domain: "a", Index: 1})
	const deriveGolden = "9b8ecca27baff043c9bffe352ab13321aa7866b66b1e8876723b74015546f81d"
	if got := hex.EncodeToString(a); got != deriveGolden {
		t.Fatalf("derive golden=%s, want %s", got, deriveGolden)
	}
	if bytes.Equal(a, b) || bytes.Equal(a, c) {
		t.Fatal("ChaCha20 derivation did not separate domain and index")
	}
	if !bytes.Equal(a, aAgain) {
		t.Fatal("ChaCha20 derivation is not deterministic")
	}
	if !bytes.Equal(parent, original) {
		t.Fatal("DeriveSeed modified parent")
	}
	if _, err := factory.DeriveSeed(parent, StreamID{}); err == nil {
		t.Fatal("DeriveSeed accepted empty domain")
	}
}

func TestChaCha20GenerateSeedUsesReaderSynchronously(t *testing.T) {
	want := bytes.Repeat([]byte{0x7b}, 32)
	got, err := Default().GenerateSeed(bytes.NewReader(want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("GenerateSeed=%x, want %x", got, want)
	}
	got[0] = 0
	if want[0] != 0x7b {
		t.Fatal("GenerateSeed returned reader-owned storage")
	}
	if _, err := Default().GenerateSeed(bytes.NewReader(make([]byte, 31))); err == nil {
		t.Fatal("GenerateSeed accepted a short reader")
	}
	if _, err := Default().GenerateSeed(nil); err == nil {
		t.Fatal("GenerateSeed accepted nil reader")
	}
	if _, err := NewChaCha20Factory(nil); err == nil {
		t.Fatal("NewChaCha20Factory accepted nil reader")
	}
}

func TestChaCha20ReseedIsDeterministicAndFailureAtomic(t *testing.T) {
	fresh := bytes.Repeat([]byte{0x42}, 32)
	factoryA, _ := NewChaCha20Factory(bytes.NewReader(fresh))
	factoryB, _ := NewChaCha20Factory(bytes.NewReader(fresh))
	a, _ := factoryA.New([]byte("root"))
	b, _ := factoryB.New([]byte("root"))
	for range 5 {
		a.Uint64()
		b.Uint64()
	}
	before, _ := a.Snapshot()
	if err := a.Reseed(); err != nil {
		t.Fatal(err)
	}
	if err := b.Reseed(); err != nil {
		t.Fatal(err)
	}
	afterA, _ := a.Snapshot()
	afterB, _ := b.Snapshot()
	nextA, nextB := a.Uint64(), b.Uint64()
	const snapshotGolden = "50434801a42713d38795d170bee698d7f01b8a0c5ac376cf34b04080175caf2075661cff9fed440c032a3ad6541d1eaf0000000000"
	if got := hex.EncodeToString(afterA); got != snapshotGolden {
		t.Fatalf("reseed snapshot=%s, want %s", got, snapshotGolden)
	}
	if nextA != 0x8bc9f4b573161676 {
		t.Fatalf("post-reseed output=%016x", nextA)
	}
	if bytes.Equal(before, afterA) {
		t.Fatal("successful Reseed did not change state")
	}
	if !bytes.Equal(afterA, afterB) || nextA != nextB {
		t.Fatal("fixed reseed inputs were not deterministic")
	}

	shortFactory, _ := NewChaCha20Factory(bytes.NewReader(make([]byte, 31)))
	short, _ := shortFactory.New([]byte("root"))
	short.Uint64()
	shortBefore, _ := short.Snapshot()
	if err := short.Reseed(); err == nil {
		t.Fatal("Reseed accepted short entropy")
	}
	shortAfter, _ := short.Snapshot()
	if !bytes.Equal(shortBefore, shortAfter) {
		t.Fatal("failed Reseed partially changed state")
	}
}

func TestChaCha20RestoreRetainsBoundEntropyProvider(t *testing.T) {
	first := bytes.Repeat([]byte{0x11}, 32)
	second := bytes.Repeat([]byte{0x22}, 32)
	factory, _ := NewChaCha20Factory(bytes.NewReader(append(first, second...)))
	rng, _ := factory.New([]byte("root"))
	original, _ := rng.Snapshot()
	if err := rng.Reseed(); err != nil {
		t.Fatal(err)
	}
	if err := rng.Restore(original); err != nil {
		t.Fatal(err)
	}
	if err := rng.Reseed(); err != nil {
		t.Fatalf("Reseed after Restore lost provider: %v", err)
	}

	controlFactory, _ := NewChaCha20Factory(bytes.NewReader(second))
	control, _ := controlFactory.New([]byte("root"))
	if err := control.Restore(original); err != nil {
		t.Fatal(err)
	}
	if err := control.Reseed(); err != nil {
		t.Fatal(err)
	}
	got, _ := rng.Snapshot()
	want, _ := control.Snapshot()
	if !bytes.Equal(got, want) {
		t.Fatal("Restore replaced or serialized the entropy provider")
	}
}

func TestChaCha20FactorySerializesSharedReseedProvider(t *testing.T) {
	reader := new(overlapDetectReader)
	factory, err := NewChaCha20Factory(reader)
	if err != nil {
		t.Fatal(err)
	}
	const count = 32
	prngs := make([]PRNG, count)
	for i := range prngs {
		prngs[i], err = factory.New(EncodeInt64Seed(int64(i)))
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	wg.Add(count)
	for _, rng := range prngs {
		go func() {
			defer wg.Done()
			if err := rng.Reseed(); err != nil {
				t.Errorf("Reseed: %v", err)
			}
		}()
	}
	wg.Wait()
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.overlap {
		t.Fatal("Factory allowed concurrent reads from shared reseed provider")
	}
}

func TestSnapshotFormatHelpersPropagateFactoryErrors(t *testing.T) {
	format, err := SnapshotFormatOf(PCG64(), EncodeInt64Seed(1))
	if err != nil {
		t.Fatal(err)
	}
	if format != "go.math/rand/v2.PCG.MarshalBinary/v1" {
		t.Fatalf("format=%q", format)
	}
	if _, err := SnapshotFormatOf(nil, nil); err == nil {
		t.Fatal("SnapshotFormatOf accepted nil Factory")
	}
	if _, err := SnapshotFormatOf(failingFactory{err: errors.New("sentinel")}, nil); !errors.Is(err, errSentinel) {
		t.Fatalf("SnapshotFormatOf did not propagate error: %v", err)
	}
	if _, err := SnapshotFormatOf(failingFactory{}, nil); err == nil {
		t.Fatal("SnapshotFormatOf accepted nil PRNG")
	}
}

var errSentinel = errors.New("factory sentinel")

type failingFactory struct{ err error }

func (f failingFactory) New([]byte) (PRNG, error) {
	if f.err != nil {
		return nil, errSentinel
	}
	return nil, nil
}
func (failingFactory) GenerateSeed(io.Reader) ([]byte, error) { return nil, nil }
func (failingFactory) DeriveSeed([]byte, StreamID) ([]byte, error) {
	return nil, nil
}

type overlapDetectReader struct {
	mu      sync.Mutex
	reading bool
	overlap bool
}

func (r *overlapDetectReader) Read(dst []byte) (int, error) {
	r.mu.Lock()
	if r.reading {
		r.overlap = true
	}
	r.reading = true
	r.mu.Unlock()
	for range 20 {
		runtime.Gosched()
	}
	for i := range dst {
		dst[i] = byte(i)
	}
	r.mu.Lock()
	r.reading = false
	r.mu.Unlock()
	return len(dst), nil
}
