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

// Package seedspec is the shared root seed representation: an int64, exact
// UTF-8 material, or hexadecimal bytes. Configuration files, reports and text
// inputs use the same rules; bare strings are never guessed.
package seedspec

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

// MaxMaterialBytes is the largest UTF-8 or decoded hexadecimal seed material.
const MaxMaterialBytes = 1024

// Kind names the root seed's declared representation. It is report and
// diagnostic vocabulary; consumers use only Spec.Bytes.
type Kind string

const (
	KindInt64 Kind = "int64"
	KindUTF8  Kind = "utf8"
	KindHex   Kind = "hex"
)

// kind is the internal discriminator. int64 is deliberately the zero value so
// a zero Spec preserves the old int64(0) seed exactly.
type kind uint8

const (
	kindInt64 kind = iota
	kindUTF8
	kindHex
)

// Spec is a polymorphic root seed. Its fields are private so non-zero values
// can only be produced by validating constructors or decoders. The zero value
// is the int64 seed 0.
//
// A Spec is immutable: no method changes its material in place (decoders
// replace the whole value), so copies may share storage safely.
type Spec struct {
	kind     kind
	value    int64
	material []byte
}

// Int64 constructs the signed integer seed kind.
func Int64(value int64) Spec {
	return Spec{kind: kindInt64, value: value}
}

// UTF8 constructs a seed from exact, non-empty UTF-8 material. material is the
// part after the "utf8:" prefix and is never trimmed or normalized.
func UTF8(material string) (Spec, error) {
	if material == "" {
		return Spec{}, fmt.Errorf("utf8 seed material must not be empty")
	}
	if !utf8.ValidString(material) {
		return Spec{}, fmt.Errorf("utf8 seed material is not valid UTF-8")
	}
	if len(material) > MaxMaterialBytes {
		return Spec{}, fmt.Errorf("utf8 seed material is %d bytes; maximum is %d", len(material), MaxMaterialBytes)
	}
	return Spec{kind: kindUTF8, material: []byte(material)}, nil
}

// Hex constructs a seed from the hex characters after the "hex:" prefix.
// Uppercase input is accepted; String and serialization normalize it to lower.
func Hex(material string) (Spec, error) {
	if material == "" {
		return Spec{}, fmt.Errorf("hex seed material must not be empty")
	}
	if len(material)%2 != 0 {
		return Spec{}, fmt.Errorf("hex seed material must contain an even number of characters")
	}
	if len(material)/2 > MaxMaterialBytes {
		return Spec{}, fmt.Errorf("hex seed material decodes to %d bytes; maximum is %d", len(material)/2, MaxMaterialBytes)
	}
	decoded, err := hex.DecodeString(material)
	if err != nil {
		return Spec{}, fmt.Errorf("decode hex seed material: %w", err)
	}
	return Spec{kind: kindHex, material: decoded}, nil
}

// Parse parses one explicitly prefixed string form. Bare strings are rejected
// rather than guessed as decimal, UTF-8, or hexadecimal input.
func Parse(text string) (Spec, error) {
	switch {
	case strings.HasPrefix(text, "utf8:"):
		return UTF8(strings.TrimPrefix(text, "utf8:"))
	case strings.HasPrefix(text, "hex:"):
		return Hex(strings.TrimPrefix(text, "hex:"))
	default:
		return Spec{}, fmt.Errorf("string seed must start with %q or %q; bare strings are not interpreted", "utf8:", "hex:")
	}
}

// ParseText parses a seed typed as plain text, such as a form field: an
// optionally negative decimal integer is an int64 seed, and anything else must
// carry a "utf8:" or "hex:" prefix (Parse). The text is not trimmed. A decimal
// integer outside the int64 range is an error, never reinterpreted as a string.
func ParseText(text string) (Spec, error) {
	if text == "" {
		return Spec{}, fmt.Errorf("seed must not be empty")
	}
	if isDecimal(text) {
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return Spec{}, fmt.Errorf("integer seed %q is outside the int64 range", text)
		}
		return Int64(value), nil
	}
	return Parse(text)
}

// isDecimal reports whether text is an optional "-" followed by one or more
// ASCII digits.
func isDecimal(text string) bool {
	digits := strings.TrimPrefix(text, "-")
	if digits == "" {
		return false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}
	return true
}

// Kind reports the declared seed representation.
func (s Spec) Kind() Kind {
	switch s.kind {
	case kindInt64:
		return KindInt64
	case kindUTF8:
		return KindUTF8
	case kindHex:
		return KindHex
	default:
		return Kind("")
	}
}

// Bytes returns a fresh copy of the root seed material handed to the PRNG
// factory. Callers may retain or mutate the returned slice.
func (s Spec) Bytes() []byte {
	switch s.kind {
	case kindInt64:
		return core.EncodeInt64Seed(s.value)
	case kindUTF8, kindHex:
		return append([]byte(nil), s.material...)
	default:
		return nil
	}
}

// Len reports the root seed material length without allocating.
func (s Spec) Len() int {
	if s.kind == kindInt64 {
		return 8
	}
	return len(s.material)
}

// String renders the canonical scalar: decimal for int64 and an explicit
// lowercase prefix for byte material. ParseText accepts every result.
func (s Spec) String() string {
	switch s.kind {
	case kindInt64:
		return strconv.FormatInt(s.value, 10)
	case kindUTF8:
		return "utf8:" + string(s.material)
	case kindHex:
		return "hex:" + hex.EncodeToString(s.material)
	default:
		return ""
	}
}

// MarshalJSON preserves a bare JSON number for int64 seeds and emits a
// canonical prefixed string for UTF-8 and hexadecimal material.
func (s Spec) MarshalJSON() ([]byte, error) {
	switch s.kind {
	case kindInt64:
		return strconv.AppendInt(nil, s.value, 10), nil
	case kindUTF8, kindHex:
		return json.Marshal(s.String())
	default:
		return nil, fmt.Errorf("seed has invalid internal kind %d", s.kind)
	}
}

// UnmarshalJSON accepts only an int64 JSON number or an explicitly prefixed
// string. It never guesses the meaning of a bare string.
func (s *Spec) UnmarshalJSON(raw []byte) error {
	if s == nil {
		return fmt.Errorf("seed JSON destination is nil")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode seed JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode seed JSON: multiple values are not allowed")
		}
		return fmt.Errorf("decode seed JSON: %w", err)
	}

	var parsed Spec
	var err error
	switch typed := value.(type) {
	case json.Number:
		var number int64
		number, err = strconv.ParseInt(typed.String(), 10, 64)
		if err == nil {
			parsed = Int64(number)
		}
	case string:
		parsed, err = Parse(typed)
	default:
		err = fmt.Errorf("seed must be an int64 JSON number or a prefixed string")
	}
	if err != nil {
		return fmt.Errorf("decode seed JSON: %w", err)
	}
	*s = parsed
	return nil
}

// MarshalYAML emits a scalar rather than exposing Spec's private fields as an
// empty mapping.
func (s Spec) MarshalYAML() (any, error) {
	switch s.kind {
	case kindInt64:
		return s.value, nil
	case kindUTF8, kindHex:
		return s.String(), nil
	default:
		return nil, fmt.Errorf("seed has invalid internal kind %d", s.kind)
	}
}

// UnmarshalYAML dispatches by the normalized YAML tag. Errors retain source
// line and column so strict config loaders can report the precise scalar.
func (s *Spec) UnmarshalYAML(node *yaml.Node) error {
	if s == nil {
		return fmt.Errorf("seed YAML destination is nil")
	}
	if node == nil {
		return fmt.Errorf("seed YAML node is nil")
	}
	if node.Kind != yaml.ScalarNode {
		return yamlError(node, "seed must be a scalar integer or prefixed string")
	}

	var parsed Spec
	switch node.ShortTag() {
	case "!!null":
		parsed = Int64(0)
	case "!!int":
		var value int64
		if err := node.Decode(&value); err != nil {
			return yamlError(node, "decode int64 seed: %v", err)
		}
		parsed = Int64(value)
	case "!!str":
		var err error
		parsed, err = Parse(node.Value)
		if err != nil {
			return yamlError(node, "%v", err)
		}
	default:
		return yamlError(node, "seed must be an int64 integer or a string prefixed with %q or %q", "utf8:", "hex:")
	}
	*s = parsed
	return nil
}

func yamlError(node *yaml.Node, format string, args ...any) error {
	return fmt.Errorf("seed at line %d column %d: %s", node.Line, node.Column, fmt.Sprintf(format, args...))
}
