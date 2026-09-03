// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package core

import (
	cryptorand "crypto/rand"
	"fmt"
	"io"
	"math"
	"math/big"

	coreinternal "github.com/zintix-labs/problab/sdk/core/internal"
)

// PCG64Factory preserves Problab's legacy int64 PCG stream and snapshot
// format. Its Reseed hook is intentionally a compatibility no-op; PCG64 is not
// a cryptographic PRNG and this factory makes no GLI compliance claim.
type PCG64Factory struct{}

// PCG64 returns the explicit legacy-compatible PRNG factory.
func PCG64() *PCG64Factory { return &PCG64Factory{} }

func (*PCG64Factory) New(seed []byte) (PRNG, error) {
	decoded, err := DecodeInt64Seed(seed)
	if err != nil {
		return nil, fmt.Errorf("pcg64 factory: %w", err)
	}
	return coreinternal.NewPCG64WithSeed(decoded), nil
}

func (*PCG64Factory) GenerateSeed(entropy io.Reader) ([]byte, error) {
	if entropy == nil {
		return nil, fmt.Errorf("pcg64 factory: entropy reader is nil")
	}
	seed, err := cryptorand.Int(entropy, big.NewInt(math.MaxInt64))
	if err != nil {
		return nil, fmt.Errorf("pcg64 factory: generate seed: %w", err)
	}
	return EncodeInt64Seed(seed.Int64()), nil
}

func (*PCG64Factory) DeriveSeed(parent []byte, stream StreamID) ([]byte, error) {
	if stream.Domain == "" {
		return nil, fmt.Errorf("pcg64 factory: stream domain is empty")
	}
	decoded, err := DecodeInt64Seed(parent)
	if err != nil {
		return nil, fmt.Errorf("pcg64 factory: derive parent: %w", err)
	}
	derived := coreinternal.DerivePCG64Seed(decoded, stream.Index)
	return EncodeInt64Seed(derived), nil
}
