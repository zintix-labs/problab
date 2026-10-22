// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package playerexp_test

// T0 playerexp tag baseline, recorded before tag resolution moves to
// GameTagCatalog without changing production code. It compares reports and
// event hits for tagged events and the pre-check error texts. Predicate call
// order and counts are intentionally not recorded: batch evaluation changes
// them by design. This is a temporary acceptance tool and is removed after
// the migration is accepted.
//
// Record: PROBLAB_RECORD_TAG_BASELINE=t0 go test -run TestTagBaselinePlayerexp .

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/demo/demo_tags"
	"github.com/zintix-labs/problab/playerexp"
	"github.com/zintix-labs/problab/playerexp/strategies"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/tag"
	"github.com/zintix-labs/problab/spec"
)

// tagT0PlayerexpSHA256 pins testdata/tag_baseline/t0/playerexp.json.
const tagT0PlayerexpSHA256 = "912043a764ec98fa564cae70ba6922d39e7b63477ec5de2870e014f442e91d45"

const tagT0PlayerexpDir = "testdata/tag_baseline"

func tagT0Tags() map[spec.GID]map[string]tag.IsTag {
	defs := map[string]tag.IsTag{
		"win": func(sr *buf.SpinResult) bool { return sr.TotalWin > 0 },
		"big": func(sr *buf.SpinResult) bool { return sr.TotalWin >= 3*sr.Bet },
	}
	maps.Copy(defs, demo_tags.Demo_0_Tags)
	return map[spec.GID]map[string]tag.IsTag{0: defs}
}

// The groups exercise win-range exclusion, AND conditions, event priority
// (several events can match one round), a duplicated tag and an untagged event.
const tagT0Config = `game_id: 0
seed: 99
analysis:
  - name: flat
    player: {players: 12, init_bet: 300, strategy: flat}
    win_buckets: [0, 1, 2, 5]
    events:
      - {event: fg_big, win_range: [2, null], tags: [fg, big]}
      - {event: win_dup, win_range: [0, null], tags: [win, win]}
      - {event: fg_any, win_range: [0, null], tags: [fg]}
      - {event: zero, win_range: [0, 0]}
  - name: double
    player: {players: 8, init_bet: 300, strategy: double}
    win_buckets: [0, 1, 2, 5]
    events:
      - {event: bg_small, win_range: [0, 1], tags: [bg]}
      - {event: big_or_tagged, win_range: [0, null], tags: [demo_0_tag_1, big]}
      - {event: rest, win_range: [0, null]}
`

func tagT0Registry(t *testing.T) *playerexp.StrategyRegistry {
	t.Helper()
	rg := playerexp.NewStrategyRegistry()
	for name, f := range map[string]playerexp.StrategyFactory{
		"flat":   strategies.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 150, SatisfiedRatio: 3},
		"double": strategies.Monotone{BetMode: 0, BetMult: 2, MaxSpins: 150, SatisfiedRatio: 3},
	} {
		if err := rg.Register(0, name, f); err != nil {
			t.Fatal(err)
		}
	}
	return rg
}

func tagT0KeepInts(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if kept := tagT0KeepInts(val); kept != nil {
				out[k] = kept
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, val := range x {
			out = append(out, tagT0KeepInts(val))
		}
		return out
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return x
		}
		return nil
	default:
		return x
	}
}

func tagT0RunConfig(t *testing.T, raw string, tags map[spec.GID]map[string]tag.IsTag) any {
	t.Helper()
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	c, err := playerexp.ParseConfig([]byte(raw))
	if err != nil {
		return map[string]any{"parse_error": err.Error()}
	}
	r, err := playerexp.Run(context.Background(), c, playerexp.Options{Lab: lab, Strategies: tagT0Registry(t), Tags: tags})
	out := map[string]any{"status": string(r.Status)}
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		t.Fatal(err)
	}
	out["report"] = tagT0KeepInts(tree)
	return out
}

func tagT0PlayerexpOutput(t *testing.T) []byte {
	t.Helper()
	nilTag := tagT0Tags()
	nilTag[0]["broken"] = nil
	cases := map[string]any{
		"report": tagT0RunConfig(t, tagT0Config, tagT0Tags()),
		// The first event references an unknown tag; a later event is fine.
		"unknown-first": tagT0RunConfig(t, strings.Replace(tagT0Config, "tags: [fg, big]", "tags: [fg, nope]", 1), tagT0Tags()),
		// A later event references an unknown tag after valid events.
		"unknown-later": tagT0RunConfig(t, strings.Replace(tagT0Config, "tags: [demo_0_tag_1, big]", "tags: [demo_0_tag_1, nope]", 1), tagT0Tags()),
		// A defined name whose predicate is nil.
		"nil-predicate": tagT0RunConfig(t, strings.Replace(tagT0Config, "tags: [fg]", "tags: [broken]", 1), nilTag),
		// No definitions for the game at all.
		"no-game": tagT0RunConfig(t, tagT0Config, map[spec.GID]map[string]tag.IsTag{1: {"fg": func(*buf.SpinResult) bool { return true }}}),
		// An invalid win range in a later event than an unknown tag.
		"range-after-unknown": tagT0RunConfig(t, strings.Replace(strings.Replace(tagT0Config, "tags: [fg, big]", "tags: [fg, nope]", 1), "win_range: [0, 1], tags: [bg]", "win_range: [0, 3], tags: [bg]", 1), tagT0Tags()),
	}
	raw, err := json.MarshalIndent(cases, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func TestTagBaselinePlayerexp(t *testing.T) {
	mode := os.Getenv("PROBLAB_RECORD_TAG_BASELINE")
	if mode == "" && testing.Short() {
		t.Skip("tag baseline comparison is skipped under -short")
	}
	path := filepath.Join(tagT0PlayerexpDir, "t0", "playerexp.json")
	manifestPath := filepath.Join(tagT0PlayerexpDir, "t0-playerexp.sha256")
	switch mode {
	case "t0":
		first, second := tagT0PlayerexpOutput(t), tagT0PlayerexpOutput(t)
		if !bytes.Equal(first, second) {
			t.Fatal("two recordings on the same commit differ; the baseline is not reproducible")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, first, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(first)
		if err := os.WriteFile(manifestPath, fmt.Appendf(nil, "%x  playerexp.json\n", sum), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded T0 playerexp: SHA-256=%x", sum)
	case "":
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(want))
		manifest, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(manifest), sum+" ") {
			t.Fatal("playerexp.json does not match t0-playerexp.sha256; the baseline was modified")
		}
		if sum != tagT0PlayerexpSHA256 {
			t.Fatalf("playerexp.json SHA-256=%s does not match the pinned constant %q", sum, tagT0PlayerexpSHA256)
		}
		if got := tagT0PlayerexpOutput(t); !bytes.Equal(got, want) {
			t.Fatalf("playerexp output differs from T0\n got=%s", got)
		}
	default:
		t.Fatalf("unknown PROBLAB_RECORD_TAG_BASELINE=%q", mode)
	}
}
