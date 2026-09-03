// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package core

import (
	cryptorand "crypto/rand"
	"fmt"
	"io"
	"sync"

	coreinternal "github.com/zintix-labs/problab/sdk/core/internal"
)

type synchronizedReader struct {
	mu sync.Mutex
	r  io.Reader
}

func (r *synchronizedReader) Read(dst []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.r.Read(dst)
}

// ChaCha20Factory creates deterministic ChaCha20 streams and binds each PRNG
// to the factory's synchronized reseed entropy provider.
type ChaCha20Factory struct {
	reseedEntropy io.Reader
}

// Default returns Problab's cryptographic default PRNG factory.
func Default() *ChaCha20Factory {
	factory, err := NewChaCha20Factory(cryptorand.Reader)
	if err != nil {
		panic(err)
	}
	return factory
}

// DefaultPRNG retains the former exported type name for migration. Its Factory
// methods use the new byte-seed contract, and its zero value binds
// crypto/rand.Reader when constructing PRNGs.
type DefaultPRNG = ChaCha20Factory

// NewChaCha20Factory creates a factory using entropy for every PRNG.Reseed
// call. Reads are serialized so providers do not need to be concurrency-safe.
func NewChaCha20Factory(entropy io.Reader) (*ChaCha20Factory, error) {
	if entropy == nil {
		return nil, fmt.Errorf("chacha20 factory: entropy reader is nil")
	}
	return &ChaCha20Factory{reseedEntropy: &synchronizedReader{r: entropy}}, nil
}

func (f *ChaCha20Factory) entropy() io.Reader {
	if f == nil || f.reseedEntropy == nil {
		return cryptorand.Reader
	}
	return f.reseedEntropy
}

func (f *ChaCha20Factory) New(seed []byte) (PRNG, error) {
	rng, err := coreinternal.NewChaCha20(seed, f.entropy())
	if err != nil {
		return nil, fmt.Errorf("chacha20 factory: new PRNG: %w", err)
	}
	return rng, nil
}

func (*ChaCha20Factory) GenerateSeed(entropy io.Reader) ([]byte, error) {
	if entropy == nil {
		return nil, fmt.Errorf("chacha20 factory: entropy reader is nil")
	}
	seed := make([]byte, 32)
	if _, err := io.ReadFull(entropy, seed); err != nil {
		return nil, fmt.Errorf("chacha20 factory: generate seed: %w", err)
	}
	return seed, nil
}

func (*ChaCha20Factory) DeriveSeed(parent []byte, stream StreamID) ([]byte, error) {
	if stream.Domain == "" {
		return nil, fmt.Errorf("chacha20 factory: stream domain is empty")
	}
	seed, err := coreinternal.DeriveChaCha20Seed(parent, stream.Domain, stream.Index)
	if err != nil {
		return nil, fmt.Errorf("chacha20 factory: derive seed: %w", err)
	}
	return seed, nil
}
