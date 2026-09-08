// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v2

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/sdk/core"
	"gopkg.in/yaml.v3"
)

func TestSeedSpecZeroValueIsInt64Zero(t *testing.T) {
	var seed SeedSpec
	want := Int64Seed(0)
	if seed.Kind() != SeedKindInt64 || seed.Len() != 8 || seed.String() != "0" || !bytes.Equal(seed.Bytes(), want.Bytes()) {
		t.Fatalf("zero SeedSpec kind=%q len=%d string=%q bytes=%x", seed.Kind(), seed.Len(), seed.String(), seed.Bytes())
	}
	raw, err := json.Marshal(seed)
	if err != nil || !bytes.Equal(raw, []byte("0")) {
		t.Fatalf("zero SeedSpec JSON=%q err=%v", raw, err)
	}
}

func TestSeedSpecInt64BytesMatchEncodeInt64Seed(t *testing.T) {
	for _, value := range []int64{0, 1, -1, 4127483647, math.MaxInt64, math.MinInt64} {
		seed := Int64Seed(value)
		if !bytes.Equal(seed.Bytes(), core.EncodeInt64Seed(value)) {
			t.Fatalf("Int64Seed(%d) bytes=%x want=%x", value, seed.Bytes(), core.EncodeInt64Seed(value))
		}
	}
}

func TestSeedSpecBytesNeverAliases(t *testing.T) {
	seed, err := UTF8Seed("autumn-build-7")
	if err != nil {
		t.Fatal(err)
	}
	first := seed.Bytes()
	second := seed.Bytes()
	if len(first) == 0 || &first[0] == &second[0] {
		t.Fatal("Bytes returned the same backing array")
	}
	first[0] = 'X'
	if string(second) != "autumn-build-7" || seed.String() != "utf8:autumn-build-7" {
		t.Fatalf("mutating returned bytes changed seed: second=%q seed=%q", second, seed.String())
	}
}

func TestSeedSpecUTF8AndHexMaterial(t *testing.T) {
	utf8Seed, err := UTF8Seed("autumn-build-7")
	if err != nil || utf8Seed.Kind() != SeedKindUTF8 || !bytes.Equal(utf8Seed.Bytes(), []byte("autumn-build-7")) {
		t.Fatalf("UTF8Seed=%q bytes=%x err=%v", utf8Seed.String(), utf8Seed.Bytes(), err)
	}
	lower, err := HexSeed("9f3a")
	if err != nil {
		t.Fatal(err)
	}
	upper, err := HexSeed("9F3A")
	if err != nil {
		t.Fatal(err)
	}
	if lower.Kind() != SeedKindHex || !bytes.Equal(lower.Bytes(), []byte{0x9f, 0x3a}) || !bytes.Equal(lower.Bytes(), upper.Bytes()) {
		t.Fatalf("hex seeds lower=%x upper=%x", lower.Bytes(), upper.Bytes())
	}
	lowerJSON, _ := json.Marshal(lower)
	upperJSON, _ := json.Marshal(upper)
	if !bytes.Equal(lowerJSON, upperJSON) || string(lowerJSON) != `"hex:9f3a"` {
		t.Fatalf("canonical hex JSON lower=%s upper=%s", lowerJSON, upperJSON)
	}
}

func TestSeedSpecRejectsMalformedMaterial(t *testing.T) {
	tests := []string{
		"utf8:", "hex:", "hex:xyz", "hex:abc", "UTF8:x", "Hex:9f", "4127483647",
		"utf8:" + strings.Repeat("x", maxSeedMaterialBytes+1),
		"hex:" + strings.Repeat("aa", maxSeedMaterialBytes+1),
	}
	for _, input := range tests {
		if _, err := ParseSeedSpec(input); err == nil {
			t.Errorf("ParseSeedSpec(%q) accepted malformed input", input)
		}
	}
	if _, err := UTF8Seed(string([]byte{0xff})); err == nil {
		t.Fatal("UTF8Seed accepted invalid UTF-8")
	}
}

func TestSeedSpecUTF8PreservesWhitespaceVerbatim(t *testing.T) {
	seed, err := UTF8Seed("  ")
	if err != nil || !bytes.Equal(seed.Bytes(), []byte{' ', ' '}) || seed.String() != "utf8:  " {
		t.Fatalf("whitespace seed=%q bytes=%x err=%v", seed.String(), seed.Bytes(), err)
	}
}

func TestSeedSpecCloneIsDeep(t *testing.T) {
	seed, err := HexSeed("0102")
	if err != nil {
		t.Fatal(err)
	}
	cloned := seed.clone()
	seed.material[0] = 0xff
	if !bytes.Equal(cloned.Bytes(), []byte{1, 2}) {
		t.Fatalf("clone aliased source material: %x", cloned.Bytes())
	}
}

func TestSeedSpecInt64MarshalsAsBareJSONNumber(t *testing.T) {
	raw, err := json.Marshal(Int64Seed(4127483647))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, []byte("4127483647")) || bytes.ContainsAny(raw, `{"":`) {
		t.Fatalf("int64 SeedSpec JSON=%q", raw)
	}
}

func TestRunReportConfigHashIsStableForInt64Seed(t *testing.T) {
	config, err := ParseConfig([]byte(validConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := config.ResolvePlan("demo-high-win-v2")
	if err != nil {
		t.Fatal(err)
	}
	got, err := hashCanonicalJSON(resolved)
	if err != nil {
		t.Fatal(err)
	}
	// Includes the explicit distribution reporting threshold. Integer seed
	// encoding remains unchanged; adding an engine option changes config identity.
	const golden = "dc5c224a375bca25d930a1ac4983eb681efe64c4ade3c244a76f79829f2d4313"
	if got != golden {
		t.Fatalf("ConfigHash=%s want golden %s", got, golden)
	}
}

func TestSeedSpecJSONRoundTrip(t *testing.T) {
	utf8Seed, _ := UTF8Seed("release-9")
	hexSeed, _ := HexSeed("9F3A")
	for _, source := range []SeedSpec{Int64Seed(-42), utf8Seed, hexSeed} {
		raw, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		var decoded SeedSpec
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if decoded.Kind() != source.Kind() || !bytes.Equal(decoded.Bytes(), source.Bytes()) {
			t.Fatalf("round trip %s => kind=%q bytes=%x", raw, decoded.Kind(), decoded.Bytes())
		}
	}
	for _, raw := range []string{`null`, `true`, `1.5`, `1e3`, `[]`, `{}`, `"42"`} {
		var decoded SeedSpec
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			t.Errorf("JSON %s was accepted", raw)
		}
	}
}

func TestRunOverridesSeedPointerMarshalsLikeValue(t *testing.T) {
	seed := Int64Seed(77)
	raw, err := json.Marshal(RunOverrides{Seed: &seed})
	if err != nil || string(raw) != `{"seed":77}` {
		t.Fatalf("seed override JSON=%s err=%v", raw, err)
	}
	raw, err = json.Marshal(RunOverrides{})
	if err != nil || string(raw) != `{}` {
		t.Fatalf("nil seed override JSON=%s err=%v", raw, err)
	}
}

func TestLoadConfigSeedDecisionTable(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantKind SeedKind
		want     []byte
		valid    bool
	}{
		{name: "positive", yaml: "4127483647", wantKind: SeedKindInt64, want: core.EncodeInt64Seed(4127483647), valid: true},
		{name: "zero", yaml: "0", wantKind: SeedKindInt64, want: core.EncodeInt64Seed(0), valid: true},
		{name: "negative", yaml: "-1", wantKind: SeedKindInt64, want: core.EncodeInt64Seed(-1), valid: true},
		{name: "max", yaml: "9223372036854775807", wantKind: SeedKindInt64, want: core.EncodeInt64Seed(math.MaxInt64), valid: true},
		{name: "min", yaml: "-9223372036854775808", wantKind: SeedKindInt64, want: core.EncodeInt64Seed(math.MinInt64), valid: true},
		{name: "overflow", yaml: "9223372036854775808"},
		{name: "hex integer", yaml: "0x7f", wantKind: SeedKindInt64, want: core.EncodeInt64Seed(127), valid: true},
		{name: "octal integer", yaml: "0o17", wantKind: SeedKindInt64, want: core.EncodeInt64Seed(15), valid: true},
		{name: "underscored integer", yaml: "1_000", wantKind: SeedKindInt64, want: core.EncodeInt64Seed(1000), valid: true},
		{name: "null", yaml: "", wantKind: SeedKindInt64, want: core.EncodeInt64Seed(0), valid: true},
		{name: "utf8", yaml: `"utf8:autumn-build-7"`, wantKind: SeedKindUTF8, want: []byte("autumn-build-7"), valid: true},
		{name: "hex lower", yaml: `"hex:9f3a"`, wantKind: SeedKindHex, want: []byte{0x9f, 0x3a}, valid: true},
		{name: "hex upper", yaml: `"hex:9F3A"`, wantKind: SeedKindHex, want: []byte{0x9f, 0x3a}, valid: true},
		{name: "bare string", yaml: `"4127483647"`},
		{name: "empty utf8", yaml: `"utf8:"`},
		{name: "empty hex", yaml: `"hex:"`},
		{name: "invalid hex", yaml: `"hex:xyz"`},
		{name: "odd hex", yaml: `"hex:abc"`},
		{name: "uppercase utf8 prefix", yaml: `"UTF8:x"`},
		{name: "mixed hex prefix", yaml: `"Hex:9f"`},
		{name: "float", yaml: "1.5"},
		{name: "bool", yaml: "true"},
		{name: "sequence", yaml: "[1]"},
		{name: "mapping", yaml: "{a: 1}"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := strings.Replace(validConfigYAML, "seed: 4127483647", "seed: "+test.yaml, 1)
			config, err := ParseConfig([]byte(raw))
			if !test.valid {
				if err == nil {
					t.Fatalf("seed YAML %q was accepted", test.yaml)
				}
				return
			}
			if err != nil {
				t.Fatalf("seed YAML %q: %v", test.yaml, err)
			}
			got := config.Plans[0].Seed
			if got.Kind() != test.wantKind || !bytes.Equal(got.Bytes(), test.want) {
				t.Fatalf("seed YAML %q => kind=%q bytes=%x", test.yaml, got.Kind(), got.Bytes())
			}
		})
	}
}

func TestLoadConfigMissingAndNullSeedAreInt64Zero(t *testing.T) {
	for _, raw := range []string{
		strings.Replace(validConfigYAML, "    seed: 4127483647\n", "", 1),
		strings.Replace(validConfigYAML, "seed: 4127483647", "seed:", 1),
	} {
		config, err := ParseConfig([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		seed := config.Plans[0].Seed
		if seed.Kind() != SeedKindInt64 || !bytes.Equal(seed.Bytes(), core.EncodeInt64Seed(0)) {
			t.Fatalf("missing/null seed => kind=%q bytes=%x", seed.Kind(), seed.Bytes())
		}
	}
}

func TestLoadConfigSeedErrorNamesLineAndColumn(t *testing.T) {
	raw := strings.Replace(validConfigYAML, "seed: 4127483647", `seed: "bare"`, 1)
	_, err := ParseConfig([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "line ") || !strings.Contains(err.Error(), "column ") {
		t.Fatalf("seed error=%v", err)
	}
}

func TestSeedSpecMarshalYAMLEmitsScalarAndRoundTrips(t *testing.T) {
	utf8Seed, _ := UTF8Seed("autumn-build-7")
	hexSeed, _ := HexSeed("9F3A")
	for _, seed := range []SeedSpec{Int64Seed(-42), utf8Seed, hexSeed} {
		raw, err := yaml.Marshal(struct {
			Seed SeedSpec `yaml:"seed"`
		}{Seed: seed})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("{}")) || bytes.Contains(raw, []byte("seed:\n")) {
			t.Fatalf("seed %q marshaled as non-scalar: %s", seed.String(), raw)
		}
		t.Logf("%s", bytes.TrimSpace(raw))

		config, err := ParseConfig([]byte(validConfigYAML))
		if err != nil {
			t.Fatal(err)
		}
		config.Plans[0].Seed = seed
		configYAML, err := yaml.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := ParseConfig(configYAML)
		if err != nil {
			t.Fatalf("round-trip config for %q: %v\n%s", seed.String(), err, configYAML)
		}
		got := decoded.Plans[0].Seed
		if got.Kind() != seed.Kind() || !bytes.Equal(got.Bytes(), seed.Bytes()) {
			t.Fatalf("YAML round trip %q => kind=%q bytes=%x", seed.String(), got.Kind(), got.Bytes())
		}
	}
}

func TestSeedSpecAcceptsExplicitLongFormYAMLTag(t *testing.T) {
	raw := strings.Replace(validConfigYAML, "seed: 4127483647", "seed: !<tag:yaml.org,2002:int> 42", 1)
	config, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(config.Plans[0].Seed.Bytes(), core.EncodeInt64Seed(42)) {
		t.Fatalf("long-form tagged seed bytes=%x", config.Plans[0].Seed.Bytes())
	}
}

func TestHexSeedCanonicalizationProducesSameConfigHash(t *testing.T) {
	lowerRaw := strings.Replace(validConfigYAML, "seed: 4127483647", `seed: "hex:9f3a"`, 1)
	upperRaw := strings.Replace(validConfigYAML, "seed: 4127483647", `seed: "hex:9F3A"`, 1)
	lowerConfig, err := ParseConfig([]byte(lowerRaw))
	if err != nil {
		t.Fatal(err)
	}
	upperConfig, err := ParseConfig([]byte(upperRaw))
	if err != nil {
		t.Fatal(err)
	}
	lower, _ := lowerConfig.ResolvePlan("demo-high-win-v2")
	upper, _ := upperConfig.ResolvePlan("demo-high-win-v2")
	lowerHash, _ := hashCanonicalJSON(lower)
	upperHash, _ := hashCanonicalJSON(upper)
	if lowerHash != upperHash {
		t.Fatalf("canonical-equivalent hex seeds hashed differently: %s != %s", lowerHash, upperHash)
	}
}
