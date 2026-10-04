// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package playerexp

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const validYAML = "game_id: 0\nanalysis:\n  - name: a\n    player: {players: 2, init_bet: 10, strategy: s}\n    win_buckets: [0, 0.5, 1, 2]\n    events:\n      - event: zero\n        win_range: [0, 0]\n"

func TestStrictConfig(t *testing.T) {
	if _, e := ParseConfig([]byte(validYAML)); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{"", strings.Replace(validYAML, "game_id: 0\n", "", 1), validYAML + "---\n", validYAML + "bad: 1\n", validYAML + "game_id: 1\n", strings.Replace(validYAML, "players: 2", "players: 2.0", 1), strings.Replace(validYAML, "[0, 0]", "[0]", 1), strings.Replace(validYAML, "[0, 0]", "[null, 2]", 1), strings.Replace(validYAML, "[0, 0]", "[0, 3]", 1), strings.Replace(validYAML, "win_range: [0, 0]", "unknown: 3", 1), validYAML + "max_spin: 1\n", validYAML + "checkpoint_policy: none\n", strings.Replace(validYAML, "init_bet: 10", "init_bet: 0", 1)} {
		if _, e := ParseConfig([]byte(raw)); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestSeedSerializationFixtures(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		kind        SeedKind
		length      int
	}{
		{"77", "77", SeedKindInt64, 8}, {"-1", "-1", SeedKindInt64, 8}, {`"hex:AB00"`, `"hex:ab00"`, SeedKindHex, 2}, {`"utf8:hello"`, `"utf8:hello"`, SeedKindUTF8, 5},
	} {
		var s SeedSpec
		if e := json.Unmarshal([]byte(tc.input), &s); e != nil {
			t.Fatal(e)
		}
		raw, e := json.Marshal(s)
		if e != nil || string(raw) != tc.want || s.Kind() != tc.kind || s.Len() != tc.length {
			t.Fatal(string(raw), s, e)
		}
		y, e := yaml.Marshal(s)
		if e != nil {
			t.Fatal(e)
		}
		var round SeedSpec
		if e = yaml.Unmarshal(y, &round); e != nil || !bytes.Equal(round.Bytes(), s.Bytes()) || round.String() != s.String() {
			t.Fatal(round, e)
		}
	}
	for _, raw := range []string{`true`, `null`, `1.0`, `"77"`, `9223372036854775808`} {
		var s SeedSpec
		if e := json.Unmarshal([]byte(raw), &s); e == nil {
			t.Fatal(raw)
		}
	}
	if _, e := UTF8Seed(strings.Repeat("x", 1024)); e != nil {
		t.Fatal(e)
	}
	if _, e := HexSeed(strings.Repeat("ab", 1024)); e != nil {
		t.Fatal(e)
	}
	if _, e := HexSeed(strings.Repeat("ab", 1025)); e == nil {
		t.Fatal("accepted oversized hex")
	}
}

func TestRequiredRangeAndNumericalConfiguration(t *testing.T) {
	for _, raw := range []string{
		strings.Replace(validYAML, "        win_range: [0, 0]\n", "", 1),
		strings.Replace(validYAML, "[0, 0]", "['0', 0]", 1),
		strings.Replace(validYAML, "[0, 0]", "[0, .inf]", 1),
		strings.Replace(validYAML, "[0, 0.5, 1, 2]", "[0, 1, 1]", 1),
		strings.Replace(validYAML, "[0, 0.5, 1, 2]", "[0, .nan]", 1),
		strings.Replace(validYAML, "name: a", "name: a\n    ci: confidence_90", 1),
		strings.Replace(validYAML, "game_id: 0", "game_id: -1", 1),
	} {
		if _, e := ParseConfig([]byte(raw)); e == nil {
			t.Fatal("accepted invalid config", raw)
		}
	}
}
func TestSeedContractFixtures(t *testing.T) {
	for _, tt := range []struct{ raw, hex string }{{"", "0000000000000000"}, {"seed: null\n", "0000000000000000"}, {"seed: 77\n", "000000000000004d"}, {"seed: 'utf8:你好'\n", "e4bda0e5a5bd"}, {"seed: 'hex:AB00'\n", "ab00"}} {
		c, e := ParseConfig([]byte(tt.raw + validYAML))
		if e != nil {
			t.Fatal(e)
		}
		want, _ := hex.DecodeString(tt.hex)
		if !bytes.Equal(c.Seed.Bytes(), want) {
			t.Fatalf("%s: %x", tt.raw, c.Seed.Bytes())
		}
		b := c.Seed.Bytes()
		b[0] ^= 255
		if bytes.Equal(b, c.Seed.Bytes()) {
			t.Fatal("seed aliases bytes")
		}
	}
	for _, seed := range []string{"'77'", "true", "1.1", "9223372036854775808", "'hex:0'", "'utf8:'", "'hex:xx'", "'utf8:" + strings.Repeat("x", 1025) + "'"} {
		if _, e := ParseConfig([]byte("seed: " + seed + "\n" + validYAML)); e == nil {
			t.Fatal(seed)
		}
	}
}
func TestBucketsAndRanges(t *testing.T) {
	bs := []float64{0, .5, 1, 2, 5, 10}
	for _, tt := range []struct {
		x float64
		i int
	}{{0, 0}, {math.SmallestNonzeroFloat64, 1}, {.5, 2}, {1, 3}, {2, 4}, {5, 5}, {10, 6}, {100, 6}} {
		if got := bucketIndex(bs, tt.x); got != tt.i {
			t.Fatal(tt, got)
		}
	}
	ptr := func(v float64) *float64 { return &v }
	for _, tt := range []struct {
		r      WinRange
		lo, hi int
	}{{WinRange{0, ptr(0)}, 0, 1}, {WinRange{0, ptr(.5)}, 1, 2}, {WinRange{0, ptr(2)}, 0, 4}, {WinRange{.5, ptr(2)}, 2, 4}, {WinRange{10, nil}, 6, 7}, {WinRange{0, nil}, 0, 7}} {
		lo, hi, e := rangeBuckets(bs, tt.r)
		if e != nil || lo != tt.lo || hi != tt.hi {
			t.Fatal(tt, lo, hi, e)
		}
	}
	for _, r := range []WinRange{{1, ptr(1)}, {2, ptr(1)}, {math.Inf(1), nil}, {0, ptr(math.NaN())}} {
		if _, _, e := rangeBuckets(bs, r); e == nil {
			t.Fatal(r)
		}
	}
	if bucketIndex([]float64{0}, 1) != 1 {
		t.Fatal("single boundary")
	}
	if bucketIndex([]float64{0, 1.0 / 3}, float64(1)/3) != 2 {
		t.Fatal("fraction boundary")
	}
}
