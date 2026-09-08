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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zintix-labs/problab/sdk/core"
	"gopkg.in/yaml.v3"
)

const maxSeedMaterialBytes = 1024

// SeedKind names the root seed's declared representation. It is report and
// diagnostic vocabulary; the optimizer itself consumes only SeedSpec.Bytes.
type SeedKind string

const (
	SeedKindInt64 SeedKind = "int64"
	SeedKindUTF8  SeedKind = "utf8"
	SeedKindHex   SeedKind = "hex"
)

// seedKind is the internal discriminator. int64 is deliberately the zero
// value so a zero SeedSpec preserves the old int64(0) seed exactly.
type seedKind uint8

const (
	seedKindInt64 seedKind = iota
	seedKindUTF8
	seedKindHex
)

// SeedSpec is the optimizer's polymorphic root seed. Its fields are private so
// non-zero values can only be produced by validating constructors or decoders.
// The zero value is the legacy int64 seed 0.
type SeedSpec struct {
	kind     seedKind
	value    int64
	material []byte
}

// Int64Seed constructs the legacy-compatible signed integer seed kind.
func Int64Seed(value int64) SeedSpec {
	return SeedSpec{kind: seedKindInt64, value: value}
}

// UTF8Seed constructs a seed from exact, non-empty UTF-8 material. material is
// the part after the "utf8:" prefix and is never trimmed or normalized.
func UTF8Seed(material string) (SeedSpec, error) {
	if material == "" {
		return SeedSpec{}, fmt.Errorf("utf8 seed material must not be empty")
	}
	if !utf8.ValidString(material) {
		return SeedSpec{}, fmt.Errorf("utf8 seed material is not valid UTF-8")
	}
	if len(material) > maxSeedMaterialBytes {
		return SeedSpec{}, fmt.Errorf("utf8 seed material is %d bytes; maximum is %d", len(material), maxSeedMaterialBytes)
	}
	return SeedSpec{kind: seedKindUTF8, material: []byte(material)}, nil
}

// HexSeed constructs a seed from the hex characters after the "hex:" prefix.
// Uppercase input is accepted; String and serialization normalize it to lower.
func HexSeed(material string) (SeedSpec, error) {
	if material == "" {
		return SeedSpec{}, fmt.Errorf("hex seed material must not be empty")
	}
	if len(material)%2 != 0 {
		return SeedSpec{}, fmt.Errorf("hex seed material must contain an even number of characters")
	}
	if len(material)/2 > maxSeedMaterialBytes {
		return SeedSpec{}, fmt.Errorf("hex seed material decodes to %d bytes; maximum is %d", len(material)/2, maxSeedMaterialBytes)
	}
	decoded, err := hex.DecodeString(material)
	if err != nil {
		return SeedSpec{}, fmt.Errorf("decode hex seed material: %w", err)
	}
	return SeedSpec{kind: seedKindHex, material: decoded}, nil
}

// ParseSeedSpec parses one explicitly prefixed string form. Bare strings are
// rejected rather than guessed as decimal, UTF-8, or hexadecimal input.
func ParseSeedSpec(text string) (SeedSpec, error) {
	switch {
	case strings.HasPrefix(text, "utf8:"):
		return UTF8Seed(strings.TrimPrefix(text, "utf8:"))
	case strings.HasPrefix(text, "hex:"):
		return HexSeed(strings.TrimPrefix(text, "hex:"))
	default:
		return SeedSpec{}, fmt.Errorf("string seed must start with %q or %q; bare strings are not interpreted", "utf8:", "hex:")
	}
}

// Kind reports the declared seed representation.
func (s SeedSpec) Kind() SeedKind {
	switch s.kind {
	case seedKindInt64:
		return SeedKindInt64
	case seedKindUTF8:
		return SeedKindUTF8
	case seedKindHex:
		return SeedKindHex
	default:
		return SeedKind("")
	}
}

// Bytes returns a fresh copy of the root seed material handed to the PRNG
// factory. Callers may retain or mutate the returned slice.
func (s SeedSpec) Bytes() []byte {
	switch s.kind {
	case seedKindInt64:
		return core.EncodeInt64Seed(s.value)
	case seedKindUTF8, seedKindHex:
		return append([]byte(nil), s.material...)
	default:
		return nil
	}
}

// Len reports the root seed material length without allocating.
func (s SeedSpec) Len() int {
	if s.kind == seedKindInt64 {
		return 8
	}
	return len(s.material)
}

// String renders the canonical YAML scalar: decimal for int64 and an explicit
// lowercase prefix for byte material.
func (s SeedSpec) String() string {
	switch s.kind {
	case seedKindInt64:
		return strconv.FormatInt(s.value, 10)
	case seedKindUTF8:
		return "utf8:" + string(s.material)
	case seedKindHex:
		return "hex:" + hex.EncodeToString(s.material)
	default:
		return ""
	}
}

// clone returns an equivalent SeedSpec that shares no material backing array.
func (s SeedSpec) clone() SeedSpec {
	cloned := s
	cloned.material = append([]byte(nil), s.material...)
	return cloned
}

// MarshalJSON preserves a bare JSON number for legacy int64 seeds and emits a
// canonical prefixed string for UTF-8 and hexadecimal material.
func (s SeedSpec) MarshalJSON() ([]byte, error) {
	switch s.kind {
	case seedKindInt64:
		return strconv.AppendInt(nil, s.value, 10), nil
	case seedKindUTF8, seedKindHex:
		return json.Marshal(s.String())
	default:
		return nil, fmt.Errorf("optimizer v2 seed has invalid internal kind %d", s.kind)
	}
}

// UnmarshalJSON accepts only an int64 JSON number or an explicitly prefixed
// string. It never guesses the meaning of a bare string.
func (s *SeedSpec) UnmarshalJSON(raw []byte) error {
	if s == nil {
		return fmt.Errorf("optimizer v2 seed JSON destination is nil")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode optimizer v2 seed JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode optimizer v2 seed JSON: multiple values are not allowed")
		}
		return fmt.Errorf("decode optimizer v2 seed JSON: %w", err)
	}

	var parsed SeedSpec
	var err error
	switch typed := value.(type) {
	case json.Number:
		var number int64
		number, err = strconv.ParseInt(typed.String(), 10, 64)
		if err == nil {
			parsed = Int64Seed(number)
		}
	case string:
		parsed, err = ParseSeedSpec(typed)
	default:
		err = fmt.Errorf("seed must be an int64 JSON number or a prefixed string")
	}
	if err != nil {
		return fmt.Errorf("decode optimizer v2 seed JSON: %w", err)
	}
	*s = parsed
	return nil
}

// MarshalYAML emits a scalar rather than exposing SeedSpec's private fields as
// an empty mapping.
func (s SeedSpec) MarshalYAML() (any, error) {
	switch s.kind {
	case seedKindInt64:
		return s.value, nil
	case seedKindUTF8, seedKindHex:
		return s.String(), nil
	default:
		return nil, fmt.Errorf("optimizer v2 seed has invalid internal kind %d", s.kind)
	}
}

// UnmarshalYAML dispatches by the normalized YAML tag. Errors retain source
// line and column so the strict config loader can report the precise scalar.
func (s *SeedSpec) UnmarshalYAML(node *yaml.Node) error {
	if s == nil {
		return fmt.Errorf("optimizer v2 seed YAML destination is nil")
	}
	if node == nil {
		return fmt.Errorf("optimizer v2 seed YAML node is nil")
	}
	if node.Kind != yaml.ScalarNode {
		return seedYAMLError(node, "seed must be a scalar integer or prefixed string")
	}

	var parsed SeedSpec
	switch node.ShortTag() {
	case "!!null":
		parsed = Int64Seed(0)
	case "!!int":
		var value int64
		if err := node.Decode(&value); err != nil {
			return seedYAMLError(node, "decode int64 seed: %v", err)
		}
		parsed = Int64Seed(value)
	case "!!str":
		var err error
		parsed, err = ParseSeedSpec(node.Value)
		if err != nil {
			return seedYAMLError(node, "%v", err)
		}
	default:
		return seedYAMLError(node, "seed must be an int64 integer or a string prefixed with %q or %q", "utf8:", "hex:")
	}
	*s = parsed
	return nil
}

func seedYAMLError(node *yaml.Node, format string, args ...any) error {
	return fmt.Errorf("optimizer v2 seed at line %d column %d: %s", node.Line, node.Column, fmt.Sprintf(format, args...))
}
