// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package internal

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/bits"

	"golang.org/x/crypto/chacha20"
)

const (
	chaCha20SnapshotSize = 53
	chaCha20BlockSize    = 64
	chaCha20WindowSize   = 512
	chaCha20WindowBlocks = chaCha20WindowSize / chaCha20BlockSize
	chaCha20SeedSize     = 32
	chaCha20StateSize    = chacha20.KeySize + chacha20.NonceSize
	chaCha20InitInfo     = "problab/chacha20/init/v1"
	chaCha20ReseedInfo   = "problab/chacha20/reseed/v1"
	chaCha20DeriveInfo   = "problab/chacha20/derive/v1"
)

var chaCha20SnapshotMagic = [4]byte{'P', 'C', 'H', 1}

// ChaCha20 adapts the Go-maintained ChaCha20 stream implementation to
// Problab's RAND and Restorable contracts. Serialized state always identifies
// the next unread byte, independent of the internal refill strategy.
type ChaCha20 struct {
	entropy      io.Reader
	key          [chacha20.KeySize]byte
	nonce        [chacha20.NonceSize]byte
	cipher       *chacha20.Cipher
	blockCounter uint32
	offset       uint8
	window       [chaCha20WindowSize]byte
	windowOffset uint16
	windowLength uint16
	refillBlocks uint8
}

func NewChaCha20(seed []byte, entropy io.Reader) (*ChaCha20, error) {
	if entropy == nil {
		return nil, fmt.Errorf("entropy reader is nil")
	}
	material, err := hkdf.Key(sha256.New, seed, nil, chaCha20InitInfo, chaCha20StateSize)
	if err != nil {
		return nil, fmt.Errorf("expand seed: %w", err)
	}
	rng := &ChaCha20{entropy: entropy, refillBlocks: chaCha20WindowBlocks}
	copy(rng.key[:], material[:chacha20.KeySize])
	copy(rng.nonce[:], material[chacha20.KeySize:])
	rng.cipher, err = chacha20.NewUnauthenticatedCipher(rng.key[:], rng.nonce[:])
	if err != nil {
		return nil, fmt.Errorf("construct cipher: %w", err)
	}
	return rng, nil
}

func DeriveChaCha20Seed(parent []byte, domain string, index uint64) ([]byte, error) {
	if uint64(len(domain)) > math.MaxUint32 {
		return nil, fmt.Errorf("stream domain is too long")
	}
	info := make([]byte, 0, len(chaCha20DeriveInfo)+4+len(domain)+8)
	info = append(info, chaCha20DeriveInfo...)
	var encoded [8]byte
	binary.BigEndian.PutUint32(encoded[:4], uint32(len(domain)))
	info = append(info, encoded[:4]...)
	info = append(info, domain...)
	binary.BigEndian.PutUint64(encoded[:], index)
	info = append(info, encoded[:]...)
	return hkdf.Key(sha256.New, parent, nil, string(info), chaCha20SeedSize)
}

func (*ChaCha20) SnapshotFormat() string {
	return "problab.chacha20.rfc8439/state-v1"
}

func (r *ChaCha20) Uint64() uint64 {
	// The terminal word would leave no canonical post-call state without
	// wrapping the nonce. Refuse it before consuming output.
	if r.offset == chaCha20BlockSize-8 && r.blockCounter == math.MaxUint32 && nonceMax(r.nonce) {
		panic("chacha20 PRNG: nonce space exhausted")
	}
	r.ensureWindow()
	value := binary.LittleEndian.Uint64(r.window[r.windowOffset : r.windowOffset+8])
	r.windowOffset += 8
	if r.offset < chaCha20BlockSize-8 {
		r.offset += 8
		return value
	}
	r.offset = 0
	if r.blockCounter < math.MaxUint32 {
		r.blockCounter++
		return value
	}
	incrementNonce(&r.nonce)
	r.blockCounter = 0
	r.windowOffset = 0
	r.windowLength = 0
	next, err := chacha20.NewUnauthenticatedCipher(r.key[:], r.nonce[:])
	if err != nil {
		panic("chacha20 PRNG: construct epoch cipher: " + err.Error())
	}
	r.cipher = next
	return value
}

func (r *ChaCha20) ensureWindow() {
	if r.windowOffset+8 <= r.windowLength {
		return
	}
	blocks := uint64(r.refillBlocks)
	if blocks == 0 {
		blocks = chaCha20WindowBlocks
	}
	remaining := uint64(math.MaxUint32) - uint64(r.blockCounter) + 1
	if blocks > remaining {
		blocks = remaining
	}
	length := int(blocks * chaCha20BlockSize)
	clear(r.window[:length])
	r.cipher.XORKeyStream(r.window[:length], r.window[:length])
	r.windowOffset = uint16(r.offset)
	r.windowLength = uint16(length)
	// Restore starts with one block to avoid producing bytes that a
	// restore-heavy Optimal spin will discard. Later refills use the wider
	// continuous-stream window.
	r.refillBlocks = chaCha20WindowBlocks
}

func (r *ChaCha20) UintN(max uint) uint {
	if max == 0 {
		return 0
	}
	return uint(r.uint64n(uint64(max)))
}

func (r *ChaCha20) IntN(max int) int {
	if max <= 0 {
		return -1
	}
	return int(r.uint64n(uint64(max)))
}

func (r *ChaCha20) Float64() float64 {
	return float64(r.Uint64()<<11>>11) / (1 << 53)
}

func (r *ChaCha20) uint64n(n uint64) uint64 {
	if is32bit && uint64(uint32(n)) == n {
		return uint64(r.uint32n(uint32(n)))
	}
	if n&(n-1) == 0 {
		return r.Uint64() & (n - 1)
	}
	hi, lo := bits.Mul64(r.Uint64(), n)
	if lo < n {
		threshold := -n % n
		for lo < threshold {
			hi, lo = bits.Mul64(r.Uint64(), n)
		}
	}
	return hi
}

func (r *ChaCha20) uint32n(n uint32) uint32 {
	if n&(n-1) == 0 {
		return uint32(r.Uint64()) & (n - 1)
	}
	x := r.Uint64()
	lo1a, lo0 := bits.Mul32(uint32(x), n)
	hi, lo1b := bits.Mul32(uint32(x>>32), n)
	lo1, carry := bits.Add32(lo1a, lo1b, 0)
	hi += carry
	if lo1 == 0 && lo0 < n {
		n64 := uint64(n)
		threshold := uint32(-n64 % n64)
		for lo1 == 0 && lo0 < threshold {
			x = r.Uint64()
			lo1a, lo0 = bits.Mul32(uint32(x), n)
			hi, lo1b = bits.Mul32(uint32(x>>32), n)
			lo1, carry = bits.Add32(lo1a, lo1b, 0)
			hi += carry
		}
	}
	return hi
}

func (r *ChaCha20) Snapshot() ([]byte, error) {
	state := make([]byte, chaCha20SnapshotSize)
	copy(state[:4], chaCha20SnapshotMagic[:])
	copy(state[4:36], r.key[:])
	copy(state[36:48], r.nonce[:])
	binary.BigEndian.PutUint32(state[48:52], r.blockCounter)
	state[52] = r.offset
	return state, nil
}

func (r *ChaCha20) Restore(state []byte) error {
	if len(state) != chaCha20SnapshotSize {
		return fmt.Errorf("snapshot length must be %d bytes, got %d", chaCha20SnapshotSize, len(state))
	}
	if string(state[:4]) != string(chaCha20SnapshotMagic[:]) {
		return fmt.Errorf("snapshot magic/version mismatch")
	}
	offset := state[52]
	if offset > chaCha20BlockSize-8 || offset%8 != 0 {
		return fmt.Errorf("snapshot offset is invalid: %d", offset)
	}
	var key [chacha20.KeySize]byte
	var nonce [chacha20.NonceSize]byte
	copy(key[:], state[4:36])
	copy(nonce[:], state[36:48])
	counter := binary.BigEndian.Uint32(state[48:52])
	cipher, err := chacha20.NewUnauthenticatedCipher(key[:], nonce[:])
	if err != nil {
		return fmt.Errorf("construct restore cipher: %w", err)
	}
	cipher.SetCounter(counter)

	var window [chaCha20WindowSize]byte
	windowOffset := uint16(0)
	windowLength := uint16(0)
	refillBlocks := uint8(1)
	if offset > 0 {
		cipher.XORKeyStream(window[:chaCha20BlockSize], window[:chaCha20BlockSize])
		windowOffset = uint16(offset)
		windowLength = chaCha20BlockSize
		refillBlocks = chaCha20WindowBlocks
	}

	r.key = key
	r.nonce = nonce
	r.cipher = cipher
	r.blockCounter = counter
	r.offset = offset
	r.window = window
	r.windowOffset = windowOffset
	r.windowLength = windowLength
	r.refillBlocks = refillBlocks
	return nil
}

func (r *ChaCha20) Reseed() error {
	if r.entropy == nil {
		return fmt.Errorf("entropy reader is nil")
	}
	var fresh [chaCha20SeedSize]byte
	if _, err := io.ReadFull(r.entropy, fresh[:]); err != nil {
		return fmt.Errorf("read reseed entropy: %w", err)
	}
	state, err := r.Snapshot()
	if err != nil {
		return fmt.Errorf("snapshot state for reseed: %w", err)
	}
	digest := sha256.Sum256(state)
	material, err := hkdf.Key(sha256.New, fresh[:], digest[:], chaCha20ReseedInfo, chaCha20StateSize)
	if err != nil {
		return fmt.Errorf("derive reseed state: %w", err)
	}
	var key [chacha20.KeySize]byte
	var nonce [chacha20.NonceSize]byte
	copy(key[:], material[:chacha20.KeySize])
	copy(nonce[:], material[chacha20.KeySize:])
	cipher, err := chacha20.NewUnauthenticatedCipher(key[:], nonce[:])
	if err != nil {
		return fmt.Errorf("construct reseed cipher: %w", err)
	}

	r.key = key
	r.nonce = nonce
	r.cipher = cipher
	r.blockCounter = 0
	r.offset = 0
	r.window = [chaCha20WindowSize]byte{}
	r.windowOffset = 0
	r.windowLength = 0
	r.refillBlocks = chaCha20WindowBlocks
	return nil
}

func nonceMax(nonce [chacha20.NonceSize]byte) bool {
	for _, value := range nonce {
		if value != 0xff {
			return false
		}
	}
	return true
}

func incrementNonce(nonce *[chacha20.NonceSize]byte) {
	for i := len(nonce) - 1; i >= 0; i-- {
		nonce[i]++
		if nonce[i] != 0 {
			return
		}
	}
}
