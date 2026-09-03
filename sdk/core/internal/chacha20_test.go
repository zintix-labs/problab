// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package internal

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math"
	"testing"

	"golang.org/x/crypto/chacha20"
)

func TestChaCha20RFC8439BlockVector(t *testing.T) {
	key := make([]byte, chacha20.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	nonce, _ := hex.DecodeString("000000090000004a00000000")
	want, _ := hex.DecodeString(
		"10f1e7e4d13b5915500fdd1fa32071c4" +
			"c7d1f4c733c068030422aa9ac3d46c4e" +
			"d2826446079faa0914c2d705d98b02a2" +
			"b5129cd1de164eb9cbd083e8a2503c4e",
	)
	rng, err := NewChaCha20(nil, bytes.NewReader(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if err := rng.Restore(makeChaChaSnapshot(key, nonce, 1, 0)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 0, 64)
	var word [8]byte
	for range 8 {
		binary.LittleEndian.PutUint64(word[:], rng.Uint64())
		got = append(got, word[:]...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("RFC 8439 block mismatch:\n got %x\nwant %x", got, want)
	}
	snapshot, _ := rng.Snapshot()
	if counter := binary.BigEndian.Uint32(snapshot[48:52]); counter != 2 || snapshot[52] != 0 {
		t.Fatalf("post-block state counter=%d offset=%d, want 2/0", counter, snapshot[52])
	}
}

func TestChaCha20SnapshotRestoreAtCanonicalOffsets(t *testing.T) {
	for _, draws := range []int{0, 1, 7, 8, 9, 31} {
		rng, err := NewChaCha20([]byte("snapshot-seed"), bytes.NewReader(make([]byte, 32)))
		if err != nil {
			t.Fatal(err)
		}
		for range draws {
			rng.Uint64()
		}
		snapshot, err := rng.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		want := make([]uint64, 24)
		for i := range want {
			want[i] = rng.Uint64()
		}
		if err := rng.Restore(snapshot); err != nil {
			t.Fatalf("draws=%d Restore: %v", draws, err)
		}
		for i, expected := range want {
			if got := rng.Uint64(); got != expected {
				t.Fatalf("draws=%d output[%d]=%x, want %x", draws, i, got, expected)
			}
		}
	}
}

func TestChaCha20RestoreRejectsMalformedStateAtomically(t *testing.T) {
	rng, err := NewChaCha20([]byte("atomic-restore"), bytes.NewReader(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	rng.Uint64()
	before, _ := rng.Snapshot()
	cases := [][]byte{
		before[:52],
		append(append([]byte(nil), before...), 0),
		func() []byte { value := append([]byte(nil), before...); value[0] ^= 0xff; return value }(),
		func() []byte { value := append([]byte(nil), before...); value[52] = 64; return value }(),
		func() []byte { value := append([]byte(nil), before...); value[52] = 1; return value }(),
	}
	for i, malformed := range cases {
		inputBefore := append([]byte(nil), malformed...)
		if err := rng.Restore(malformed); err == nil {
			t.Fatalf("case %d was accepted", i)
		}
		if !bytes.Equal(malformed, inputBefore) {
			t.Fatalf("case %d Restore modified its input", i)
		}
		after, _ := rng.Snapshot()
		if !bytes.Equal(before, after) {
			t.Fatalf("case %d partially changed state", i)
		}
	}
}

func TestChaCha20CounterEpochTransition(t *testing.T) {
	key := make([]byte, chacha20.KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	nonce := make([]byte, chacha20.NonceSize)
	nonce[len(nonce)-1] = 9

	reference, err := chacha20.NewUnauthenticatedCipher(key, nonce)
	if err != nil {
		t.Fatal(err)
	}
	reference.SetCounter(math.MaxUint32)
	lastBlock := make([]byte, 64)
	reference.XORKeyStream(lastBlock, lastBlock)

	rng, err := NewChaCha20(nil, bytes.NewReader(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if err := rng.Restore(makeChaChaSnapshot(key, nonce, math.MaxUint32, 56)); err != nil {
		t.Fatal(err)
	}
	if got, want := rng.Uint64(), binary.LittleEndian.Uint64(lastBlock[56:]); got != want {
		t.Fatalf("last epoch word=%x, want %x", got, want)
	}

	nextNonce := append([]byte(nil), nonce...)
	nextNonce[len(nextNonce)-1]++
	snapshot, _ := rng.Snapshot()
	if !bytes.Equal(snapshot[36:48], nextNonce) || binary.BigEndian.Uint32(snapshot[48:52]) != 0 || snapshot[52] != 0 {
		t.Fatalf("epoch transition state=%x", snapshot)
	}
	nextCipher, _ := chacha20.NewUnauthenticatedCipher(key, nextNonce)
	nextBlock := make([]byte, 64)
	nextCipher.XORKeyStream(nextBlock, nextBlock)
	if got, want := rng.Uint64(), binary.LittleEndian.Uint64(nextBlock); got != want {
		t.Fatalf("new epoch first word=%x, want %x", got, want)
	}
}

func TestChaCha20TerminalNoncePanicsBeforeStateChange(t *testing.T) {
	key := make([]byte, chacha20.KeySize)
	nonce := bytes.Repeat([]byte{0xff}, chacha20.NonceSize)
	rng, err := NewChaCha20(nil, bytes.NewReader(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if err := rng.Restore(makeChaChaSnapshot(key, nonce, math.MaxUint32, 56)); err != nil {
		t.Fatal(err)
	}
	before, _ := rng.Snapshot()
	defer func() {
		if recover() == nil {
			t.Fatal("terminal nonce did not panic")
		}
		after, _ := rng.Snapshot()
		if !bytes.Equal(before, after) {
			t.Fatal("terminal panic changed state")
		}
	}()
	rng.Uint64()
}

func TestChaCha20WindowSplitsAtCounterEpochBoundary(t *testing.T) {
	key := make([]byte, chacha20.KeySize)
	for i := range key {
		key[i] = byte(0x80 + i)
	}
	nonce := make([]byte, chacha20.NonceSize)
	nonce[len(nonce)-1] = 0x2a
	const initialCounter = math.MaxUint32 - 6

	oldCipher, err := chacha20.NewUnauthenticatedCipher(key, nonce)
	if err != nil {
		t.Fatal(err)
	}
	oldCipher.SetCounter(initialCounter)
	want := make([]byte, 7*chaCha20BlockSize)
	oldCipher.XORKeyStream(want, want)
	nextNonce := append([]byte(nil), nonce...)
	nextNonce[len(nextNonce)-1]++
	newCipher, _ := chacha20.NewUnauthenticatedCipher(key, nextNonce)
	next := make([]byte, chaCha20BlockSize)
	newCipher.XORKeyStream(next, next)
	want = append(want, next...)

	rng, err := NewChaCha20(nil, bytes.NewReader(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if err := rng.Restore(makeChaChaSnapshot(key, nonce, initialCounter, 0)); err != nil {
		t.Fatal(err)
	}
	for i := range len(want) / 8 {
		if got, expected := rng.Uint64(), binary.LittleEndian.Uint64(want[i*8:]); got != expected {
			t.Fatalf("word[%d]=%x, want %x", i, got, expected)
		}
	}
	snapshot, _ := rng.Snapshot()
	if !bytes.Equal(snapshot[36:48], nextNonce) || binary.BigEndian.Uint32(snapshot[48:52]) != 1 || snapshot[52] != 0 {
		t.Fatalf("post-split canonical state=%x", snapshot)
	}
}

func makeChaChaSnapshot(key, nonce []byte, counter uint32, offset byte) []byte {
	state := make([]byte, chaCha20SnapshotSize)
	copy(state[:4], chaCha20SnapshotMagic[:])
	copy(state[4:36], key)
	copy(state[36:48], nonce)
	binary.BigEndian.PutUint32(state[48:52], counter)
	state[52] = offset
	return state
}
