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

import "github.com/zintix-labs/problab/seedspec"

// SeedSpec is the polymorphic root seed; see package seedspec for the rules.
type SeedSpec = seedspec.Spec

// SeedKind names the root seed's declared representation.
type SeedKind = seedspec.Kind

const (
	SeedKindInt64 = seedspec.KindInt64
	SeedKindUTF8  = seedspec.KindUTF8
	SeedKindHex   = seedspec.KindHex
)

// Int64Seed constructs the signed integer seed kind.
func Int64Seed(value int64) SeedSpec { return seedspec.Int64(value) }

// UTF8Seed constructs a seed from exact, non-empty UTF-8 material.
func UTF8Seed(material string) (SeedSpec, error) { return seedspec.UTF8(material) }

// HexSeed constructs a seed from the hex characters after the "hex:" prefix.
func HexSeed(material string) (SeedSpec, error) { return seedspec.Hex(material) }

// ParseSeedSpec parses one explicitly prefixed string form ("utf8:" or "hex:").
func ParseSeedSpec(text string) (SeedSpec, error) { return seedspec.Parse(text) }
