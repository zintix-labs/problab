// Copyright 2026 Zintix Labs
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

package seedspec

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/sdk/core"
	"gopkg.in/yaml.v3"
)

func TestZeroValueIsInt64Zero(t *testing.T) {
	var seed Spec
	want := Int64(0)
	if seed.Kind() != KindInt64 || seed.Len() != 8 || seed.String() != "0" || !bytes.Equal(seed.Bytes(), want.Bytes()) {
		t.Fatalf("zero Spec kind=%q len=%d string=%q bytes=%x", seed.Kind(), seed.Len(), seed.String(), seed.Bytes())
	}
	raw, err := json.Marshal(seed)
	if err != nil || !bytes.Equal(raw, []byte("0")) {
		t.Fatalf("zero Spec JSON=%q err=%v", raw, err)
	}
}

func TestInt64BytesMatchEncodeInt64Seed(t *testing.T) {
	for _, value := range []int64{0, 1, -1, 4127483647, math.MaxInt64, math.MinInt64} {
		seed := Int64(value)
		if !bytes.Equal(seed.Bytes(), core.EncodeInt64Seed(value)) {
			t.Fatalf("Int64(%d) bytes=%x want=%x", value, seed.Bytes(), core.EncodeInt64Seed(value))
		}
	}
}

func TestBytesNeverAliases(t *testing.T) {
	seed, err := UTF8("autumn-build-7")
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

// Decoding into a Spec replaces its value; a copy taken earlier keeps its own material.
func TestDecodeDoesNotChangeCopies(t *testing.T) {
	seed, err := UTF8("before")
	if err != nil {
		t.Fatal(err)
	}
	copied := seed
	if err := json.Unmarshal([]byte(`"utf8:after!"`), &seed); err != nil {
		t.Fatal(err)
	}
	if copied.String() != "utf8:before" || seed.String() != "utf8:after!" {
		t.Fatalf("copied=%q seed=%q", copied.String(), seed.String())
	}
}

func TestUTF8AndHexMaterial(t *testing.T) {
	utf8Seed, err := UTF8("autumn-build-7")
	if err != nil || utf8Seed.Kind() != KindUTF8 || !bytes.Equal(utf8Seed.Bytes(), []byte("autumn-build-7")) {
		t.Fatalf("UTF8=%q bytes=%x err=%v", utf8Seed.String(), utf8Seed.Bytes(), err)
	}
	lower, err := Hex("9f3a")
	if err != nil {
		t.Fatal(err)
	}
	upper, err := Hex("9F3A")
	if err != nil {
		t.Fatal(err)
	}
	if lower.Kind() != KindHex || !bytes.Equal(lower.Bytes(), []byte{0x9f, 0x3a}) || !bytes.Equal(lower.Bytes(), upper.Bytes()) {
		t.Fatalf("hex seeds lower=%x upper=%x", lower.Bytes(), upper.Bytes())
	}
	lowerJSON, _ := json.Marshal(lower)
	upperJSON, _ := json.Marshal(upper)
	if !bytes.Equal(lowerJSON, upperJSON) || string(lowerJSON) != `"hex:9f3a"` {
		t.Fatalf("canonical hex JSON lower=%s upper=%s", lowerJSON, upperJSON)
	}
}

func TestParseRejectsMalformedMaterial(t *testing.T) {
	tests := []string{
		"utf8:", "hex:", "hex:xyz", "hex:abc", "UTF8:x", "Hex:9f", "4127483647",
		"utf8:" + strings.Repeat("x", MaxMaterialBytes+1),
		"hex:" + strings.Repeat("aa", MaxMaterialBytes+1),
	}
	for _, input := range tests {
		if _, err := Parse(input); err == nil {
			t.Errorf("Parse(%q) accepted malformed input", input)
		}
	}
	if _, err := UTF8(string([]byte{0xff})); err == nil {
		t.Fatal("UTF8 accepted invalid UTF-8")
	}
	if _, err := UTF8(strings.Repeat("x", MaxMaterialBytes)); err != nil {
		t.Fatalf("UTF8 rejected material at the limit: %v", err)
	}
	if _, err := Hex(strings.Repeat("ab", MaxMaterialBytes)); err != nil {
		t.Fatalf("Hex rejected material at the limit: %v", err)
	}
}

func TestUTF8PreservesWhitespaceVerbatim(t *testing.T) {
	seed, err := UTF8("  ")
	if err != nil || !bytes.Equal(seed.Bytes(), []byte{' ', ' '}) || seed.String() != "utf8:  " {
		t.Fatalf("whitespace seed=%q bytes=%x err=%v", seed.String(), seed.Bytes(), err)
	}
}

func TestParseText(t *testing.T) {
	valid := []struct {
		text string
		kind Kind
		want []byte
	}{
		{"0", KindInt64, core.EncodeInt64Seed(0)},
		{"-1", KindInt64, core.EncodeInt64Seed(-1)},
		{"007", KindInt64, core.EncodeInt64Seed(7)},
		{"9223372036854775807", KindInt64, core.EncodeInt64Seed(math.MaxInt64)},
		{"-9223372036854775808", KindInt64, core.EncodeInt64Seed(math.MinInt64)},
		{"hex:0A", KindHex, []byte{0x0a}},
		{"utf8:abc", KindUTF8, []byte("abc")},
		{"utf8: 12 ", KindUTF8, []byte(" 12 ")},
	}
	for _, test := range valid {
		got, err := ParseText(test.text)
		if err != nil || got.Kind() != test.kind || !bytes.Equal(got.Bytes(), test.want) {
			t.Errorf("ParseText(%q) => kind=%q bytes=%x err=%v", test.text, got.Kind(), got.Bytes(), err)
		}
	}
	for _, text := range []string{"", "-", "abc", "0x10", "+1", " 1", "1 ", "1.5", "9223372036854775808", "-9223372036854775809"} {
		if got, err := ParseText(text); err == nil {
			t.Errorf("ParseText(%q) accepted as %q", text, got.String())
		}
	}
	// String's output always parses back to the same seed.
	utf8Seed, _ := UTF8("x y")
	hexSeed, _ := Hex("FF00")
	for _, seed := range []Spec{Int64(-42), utf8Seed, hexSeed} {
		got, err := ParseText(seed.String())
		if err != nil || got.Kind() != seed.Kind() || !bytes.Equal(got.Bytes(), seed.Bytes()) {
			t.Errorf("ParseText(%q) => %q err=%v", seed.String(), got.String(), err)
		}
	}
}

func TestInt64MarshalsAsBareJSONNumber(t *testing.T) {
	raw, err := json.Marshal(Int64(4127483647))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, []byte("4127483647")) || bytes.ContainsAny(raw, `{"":`) {
		t.Fatalf("int64 Spec JSON=%q", raw)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	utf8Seed, _ := UTF8("release-9")
	hexSeed, _ := Hex("9F3A")
	for _, source := range []Spec{Int64(-42), utf8Seed, hexSeed} {
		raw, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Spec
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if decoded.Kind() != source.Kind() || !bytes.Equal(decoded.Bytes(), source.Bytes()) {
			t.Fatalf("round trip %s => kind=%q bytes=%x", raw, decoded.Kind(), decoded.Bytes())
		}
	}
	for _, raw := range []string{`null`, `true`, `1.5`, `1e3`, `[]`, `{}`, `"42"`, `1 2`} {
		var decoded Spec
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			t.Errorf("JSON %s was accepted", raw)
		}
	}
}

func TestYAMLScalarRoundTrip(t *testing.T) {
	utf8Seed, _ := UTF8("autumn-build-7")
	hexSeed, _ := Hex("9F3A")
	type doc struct {
		Seed Spec `yaml:"seed"`
	}
	for _, seed := range []Spec{Int64(-42), utf8Seed, hexSeed} {
		raw, err := yaml.Marshal(doc{Seed: seed})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("{}")) || bytes.Contains(raw, []byte("seed:\n")) {
			t.Fatalf("seed %q marshaled as non-scalar: %s", seed.String(), raw)
		}
		var decoded doc
		if err := yaml.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if decoded.Seed.Kind() != seed.Kind() || !bytes.Equal(decoded.Seed.Bytes(), seed.Bytes()) {
			t.Fatalf("YAML round trip %q => kind=%q bytes=%x", seed.String(), decoded.Seed.Kind(), decoded.Seed.Bytes())
		}
	}
}

func TestYAMLErrorNamesLineAndColumn(t *testing.T) {
	var decoded struct {
		Seed Spec `yaml:"seed"`
	}
	err := yaml.Unmarshal([]byte("a: 1\nseed: \"bare\"\n"), &decoded)
	if err == nil || !strings.Contains(err.Error(), "seed at line 2 column 7") {
		t.Fatalf("seed error=%v", err)
	}
}
