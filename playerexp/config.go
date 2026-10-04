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

package playerexp

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/zintix-labs/problab/spec"
	"gopkg.in/yaml.v3"
)

// Config fixes one game's seed and runs ordered, independent statistical groups
// on its continuous RNG stream. Omitted Seed is int64 zero.
type Config struct {
	GameID   spec.GID      `yaml:"game_id"`
	Seed     SeedSpec      `yaml:"seed"`
	Analysis []GroupConfig `yaml:"analysis"`
}

// GroupConfig controls observation only, not the game's distribution source.
type GroupConfig struct {
	Name       string        `yaml:"name"`
	CI         string        `yaml:"ci"`
	Player     PlayerConfig  `yaml:"player"`
	WinBuckets []float64     `yaml:"win_buckets"`
	Events     []EventConfig `yaml:"events"`
}

// PlayerConfig specifies count, initial capital in BetUnits[0], and the exact
// registered strategy name. It cannot override the system spin limit.
type PlayerConfig struct {
	Players  int    `yaml:"players"`
	InitBet  int    `yaml:"init_bet"`
	Strategy string `yaml:"strategy"`
}

// EventConfig selects a contiguous bucket range and AND-ed tag predicates.
// Events have first-match precedence in their configured order.
type EventConfig struct {
	Name     string   `yaml:"event"`
	WinRange WinRange `yaml:"win_range"`
	Tags     []string `yaml:"tags"`
}

// WinRange uses nil Max for an unbounded tail. Zero-valued Go configuration
// means [0,+Inf); YAML requires an explicit two-element range.
type WinRange struct {
	Min float64
	Max *float64
}

func (r *WinRange) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.SequenceNode || len(n.Content) != 2 {
		return fmt.Errorf("win_range requires exactly [min,max]")
	}
	if n.Content[0].Tag == "!!null" {
		return fmt.Errorf("win_range min cannot be null")
	}
	for i, value := range n.Content {
		if value.Tag != "!!int" && value.Tag != "!!float" && !(i == 1 && value.Tag == "!!null") {
			return fmt.Errorf("win_range endpoints must be numbers (only max may be null)")
		}
	}
	if err := n.Content[0].Decode(&r.Min); err != nil {
		return err
	}
	r.Max = nil
	if n.Content[1].Tag != "!!null" {
		var v float64
		if err := n.Content[1].Decode(&v); err != nil {
			return err
		}
		r.Max = &v
	}
	return nil
}

// ParseConfig rejects unknown fields and multiple documents before any game
// is constructed. Game 0 is valid; a missing game_id is not.
func ParseConfig(raw []byte) (Config, error) {
	var node yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(raw))
	if err := d.Decode(&node); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return Config{}, fmt.Errorf("config must contain exactly one document")
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return Config{}, fmt.Errorf("config must be a mapping")
	}
	if err := validateNodes(node.Content[0], ""); err != nil {
		return Config{}, err
	}
	var wire struct {
		GameID   *spec.GID     `yaml:"game_id"`
		Seed     SeedSpec      `yaml:"seed"`
		Analysis []GroupConfig `yaml:"analysis"`
	}
	d = yaml.NewDecoder(bytes.NewReader(raw))
	d.KnownFields(true)
	if err := d.Decode(&wire); err != nil {
		return Config{}, err
	}
	if wire.GameID == nil {
		return Config{}, fmt.Errorf("game_id is required")
	}
	c := Config{GameID: *wire.GameID, Seed: wire.Seed, Analysis: wire.Analysis}
	return normalizeConfig(c)
}

func validateNodes(n *yaml.Node, key string) error {
	if n.Kind == yaml.AliasNode {
		return fmt.Errorf("YAML aliases are not supported")
	}
	if key == "players" || key == "init_bet" || key == "game_id" {
		if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
			return fmt.Errorf("%s must be an integer", key)
		}
	}
	if key == "win_range" && (n.Kind != yaml.SequenceNode || len(n.Content) != 2) {
		return fmt.Errorf("win_range requires [min,max]")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if seen[k] {
				return fmt.Errorf("duplicate YAML key %q", k)
			}
			seen[k] = true
			if err := validateNodes(n.Content[i+1], k); err != nil {
				return err
			}
		}
		if seen["event"] && !seen["win_range"] {
			return fmt.Errorf("event requires win_range")
		}
	} else {
		for _, child := range n.Content {
			if err := validateNodes(child, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeConfig(c Config) (Config, error) {
	c.Seed = c.Seed.clone()
	c.Analysis = append([]GroupConfig(nil), c.Analysis...)
	if len(c.Analysis) == 0 {
		return c, fmt.Errorf("analysis must not be empty")
	}
	seen := map[string]bool{}
	for i := range c.Analysis {
		g := &c.Analysis[i]
		if strings.TrimSpace(g.Name) == "" || seen[g.Name] {
			return c, fmt.Errorf("analysis[%d]: empty or duplicate name", i)
		}
		seen[g.Name] = true
		if g.CI == "" {
			g.CI = "confidence_95"
		}
		if g.CI != "confidence_95" && g.CI != "confidence_99" {
			return c, fmt.Errorf("group %q: unknown ci %q", g.Name, g.CI)
		}
		if g.Player.Players <= 0 || g.Player.InitBet <= 0 || strings.TrimSpace(g.Player.Strategy) == "" {
			return c, fmt.Errorf("group %q: invalid player settings", g.Name)
		}
		g.WinBuckets = append([]float64(nil), g.WinBuckets...)
		if len(g.WinBuckets) == 0 || g.WinBuckets[0] != 0 {
			return c, fmt.Errorf("group %q: buckets must begin at zero", g.Name)
		}
		for j, b := range g.WinBuckets {
			if math.IsNaN(b) || math.IsInf(b, 0) || b < 0 || (j > 0 && b <= g.WinBuckets[j-1]) {
				return c, fmt.Errorf("group %q: invalid bucket boundary", g.Name)
			}
		}
		g.Events = append([]EventConfig(nil), g.Events...)
		names := map[string]bool{}
		for j := range g.Events {
			e := &g.Events[j]
			e.Tags = append([]string(nil), e.Tags...)
			if e.WinRange.Max != nil {
				v := *e.WinRange.Max
				e.WinRange.Max = &v
			}
			if strings.TrimSpace(e.Name) == "" || names[e.Name] {
				return c, fmt.Errorf("group %q: empty or duplicate event", g.Name)
			}
			names[e.Name] = true
			if _, _, err := rangeBuckets(g.WinBuckets, e.WinRange); err != nil {
				return c, fmt.Errorf("group %q event %q: %w", g.Name, e.Name, err)
			}
		}
	}
	return c, nil
}

func boundaryIndex(bs []float64, x float64) int {
	for i, b := range bs {
		if x == b {
			return i
		}
	}
	return -1
}
func rangeBuckets(bs []float64, r WinRange) (int, int, error) {
	a := boundaryIndex(bs, r.Min)
	if a < 0 {
		return 0, 0, fmt.Errorf("min must be a bucket boundary")
	}
	if r.Max == nil {
		if a == 0 {
			return 0, len(bs) + 1, nil
		}
		return a + 1, len(bs) + 1, nil
	}
	b := boundaryIndex(bs, *r.Max)
	if b < 0 || *r.Max < r.Min || (*r.Max == r.Min && r.Min != 0) {
		return 0, 0, fmt.Errorf("invalid max boundary")
	}
	if a == 0 && b == 0 {
		return 0, 1, nil
	}
	if a == 0 && b == 1 {
		return 1, 2, nil
	}
	if a == 0 {
		return 0, b + 1, nil
	}
	return a + 1, b + 1, nil
}
func bucketIndex(bs []float64, x float64) int {
	if x == 0 {
		return 0
	}
	for i := 1; i < len(bs); i++ {
		if x < bs[i] {
			return i
		}
	}
	return len(bs)
}
func bucketStats(bs []float64) []BucketStat {
	out := make([]BucketStat, len(bs)+1)
	zero := 0.0
	out[0] = BucketStat{Max: &zero, IncludeMin: true, IncludeMax: true}
	for i := 1; i < len(out); i++ {
		out[i].Min = bs[i-1]
		out[i].IncludeMin = i > 1
		if i < len(bs) {
			v := bs[i]
			out[i].Max = &v
		}
	}
	return out
}
