// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package optimizer

import (
	"strings"
	"testing"

	"github.com/zintix-labs/problab/sdk/buf"
)

func TestNewRegisterTagsResetsToBaseline(t *testing.T) {
	t.Cleanup(func() {
		if err := NewRegisterTags(nil); err != nil {
			t.Errorf("restore baseline tags: %v", err)
		}
	})

	alwaysTrue := func(*buf.SpinResult) bool { return true }
	if err := NewRegisterTags(map[string]IsTag{"custom_a": alwaysTrue}); err != nil {
		t.Fatalf("register first game tags: %v", err)
	}
	if _, err := GetTagger("bg", "fg", "custom_a"); err != nil {
		t.Fatalf("resolve first game tags: %v", err)
	}

	if err := NewRegisterTags(map[string]IsTag{"custom_b": alwaysTrue}); err != nil {
		t.Fatalf("register second game tags: %v", err)
	}
	if _, err := GetTagger("custom_a"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("stale custom_a lookup error = %v, want not found", err)
	}
	if _, err := GetTagger("bg", "fg", "custom_b"); err != nil {
		t.Fatalf("resolve reset baseline and second game tags: %v", err)
	}
}

func TestNewRegisterTagsRejectsBuiltinNameCollision(t *testing.T) {
	t.Cleanup(func() {
		if err := NewRegisterTags(nil); err != nil {
			t.Errorf("restore baseline tags: %v", err)
		}
	})

	alwaysTrue := func(*buf.SpinResult) bool { return true }
	if err := NewRegisterTags(map[string]IsTag{"bg": alwaysTrue}); err == nil {
		t.Fatal("NewRegisterTags accepted a custom bg predicate")
	}
	tagger, err := GetTagger("bg")
	if err != nil {
		t.Fatalf("resolve built-in bg after rejected collision: %v", err)
	}
	if got := tagger.Tagging(&buf.SpinResult{GameModeCount: 1}); got != 1 {
		t.Fatalf("built-in bg did not match one-mode result: mask=%d", got)
	}
	if got := tagger.Tagging(&buf.SpinResult{GameModeCount: 2}); got != 0 {
		t.Fatalf("rejected custom bg replaced the built-in predicate: mask=%d", got)
	}
}

func TestNewRegisterTagsAllowsEmptyOrNilInput(t *testing.T) {
	t.Cleanup(func() {
		if err := NewRegisterTags(nil); err != nil {
			t.Errorf("restore baseline tags: %v", err)
		}
	})

	for _, tags := range []map[string]IsTag{nil, {}} {
		if err := NewRegisterTags(tags); err != nil {
			t.Fatalf("NewRegisterTags(%v): %v", tags, err)
		}
		if _, err := GetTagger("bg", "fg"); err != nil {
			t.Fatalf("resolve baseline after NewRegisterTags(%v): %v", tags, err)
		}
	}
}
