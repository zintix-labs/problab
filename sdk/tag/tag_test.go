// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package tag

import (
	"fmt"
	"sync"
	"testing"

	"github.com/zintix-labs/problab/sdk/buf"
)

func yes(*buf.SpinResult) bool { return true }
func no(*buf.SpinResult) bool  { return false }

func TestRegistryOwnsDefinitionsAndBitSetOwnsNames(t *testing.T) {
	input := map[string]IsTag{"a": yes, "b": no}
	r, err := NewRegistry(input)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewRegistry(input)
	if err != nil {
		t.Fatal(err)
	}
	input["a"] = no
	delete(input, "b")
	names := []string{"b", "a"}
	bs, err := r.BitSet(names...)
	if err != nil {
		t.Fatal(err)
	}
	names[0], names[1] = "a", "b"
	if bs.Tagging(nil) != 2 {
		t.Fatal("predicate ownership lost")
	}
	if mask, err := bs.Mask("a"); err != nil || mask != 2 {
		t.Fatalf("mask=%d err=%v", mask, err)
	}
	added := map[string]IsTag{"c": yes}
	if err := r.RegisterTag(added); err != nil {
		t.Fatal(err)
	}
	added["c"] = no
	c, err := r.BitSet("c")
	if err != nil || c.Tagging(nil) != 1 {
		t.Fatal("registration retained input map")
	}
	if _, exists := input["c"]; exists {
		t.Fatal("modified caller map")
	}
	if _, err := other.BitSet("c"); err == nil {
		t.Fatal("registries share definitions")
	}
	if _, err := bs.Mask("c"); err == nil {
		t.Fatal("compiled bitset changed after registration")
	}
}

func TestRegistryValidationIsAtomicAndZeroValueUsable(t *testing.T) {
	for _, invalid := range []map[string]IsTag{{"": yes}, {"nil": nil}} {
		if _, err := NewRegistry(invalid); err == nil {
			t.Fatal("accepted invalid definition")
		}
	}
	for _, r := range []*Registry{{}, mustRegistry(t, nil)} {
		if err := r.RegisterTag(map[string]IsTag{"a": yes}); err != nil {
			t.Fatal(err)
		}
		for _, invalid := range []map[string]IsTag{{"a": no, "new": yes}, {"": yes, "new": yes}, {"bad": nil, "new": yes}} {
			if err := r.RegisterTag(invalid); err == nil {
				t.Fatal("accepted invalid registration")
			}
			if _, err := r.BitSet("new"); err == nil {
				t.Fatal("partial registration")
			}
		}
		bs, err := r.BitSet("a")
		if err != nil || bs.Tagging(nil) != 1 {
			t.Fatal("original definition changed")
		}
	}
}

func mustRegistry(t *testing.T, definitions map[string]IsTag) *Registry {
	t.Helper()
	r, err := NewRegistry(definitions)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBitSetValidationAnd64BitBoundary(t *testing.T) {
	names := make([]string, 65)
	defs := make(map[string]IsTag)
	for i := range names {
		names[i] = fmt.Sprint(i)
		defs[names[i]] = yes
	}
	r := mustRegistry(t, defs)
	for _, names := range [][]string{nil, {"missing"}, {""}, {"0", "0"}, names} {
		if _, err := r.BitSet(names...); err == nil {
			t.Fatalf("accepted %v", names)
		}
	}
	bs, err := r.BitSet(names[:64]...)
	if err != nil {
		t.Fatal(err)
	}
	if bs.Tagging(nil) != ^uint64(0) {
		t.Fatal("lost bit 63")
	}
	if got, err := bs.Mask("63", "63"); err != nil || got != uint64(1)<<63 {
		t.Fatalf("mask=%x err=%v", got, err)
	}
	if got, err := bs.Mask(); err != nil || got != 0 {
		t.Fatal("empty mask")
	}
	if got, err := bs.Mask("0", "unknown"); err == nil || got != 0 {
		t.Fatal("unknown mask must fail atomically")
	}
}

func TestFrozenBitSetConcurrentReadsAndIndependentRegistrations(t *testing.T) {
	r := mustRegistry(t, map[string]IsTag{"a": yes, "b": no})
	bs, err := r.BitSet("a", "b")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				if bs.Tagging(nil) != 1 {
					t.Error("unstable tag bits")
					return
				}
				if mask, err := bs.Mask("b"); err != nil || mask != 2 {
					t.Error("unstable mask")
					return
				}
			}
		}()
	}
	// Registration is not concurrent with Registry compilation, only with
	// already-frozen BitSet reads, which must not access the registry map.
	for i := range 100 {
		if err := r.RegisterTag(map[string]IsTag{fmt.Sprint(i): yes}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
