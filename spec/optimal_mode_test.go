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

package spec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func decodeModesYAML(raw string) (OptimalModes, error) {
	var s OptimalSetting
	err := yaml.Unmarshal([]byte("modes:\n"+raw), &s)
	return s.Modes, err
}

func decodeModesJSON(raw string) (OptimalModes, error) {
	var s OptimalSetting
	err := json.Unmarshal([]byte(`{"modes":`+raw+`}`), &s)
	return s.Modes, err
}

func natural() *GachaSlot { return &GachaSlot{} }

func seeded(dist, bank string) *GachaSlot { return &GachaSlot{Dist: dist, Bank: bank} }

func TestOptimalModesDecode(t *testing.T) {
	want := OptimalModes{
		{Start: seeded("s.zst", "s.bin"), After: map[int]*GachaSlot{3: seeded("a.zst", "a.bin"), 7: natural()}},
		{Start: natural()},
	}
	yamlCases := map[string]string{
		"flow":        "  - start: {dist: s.zst, bank: s.bin}\n    after:\n      3: {dist: a.zst, bank: a.bin}\n      7: {dist: null, bank: null}\n  - start: {dist: null, bank: null}\n",
		"block nulls": "  - start: {dist: s.zst, bank: s.bin}\n    after:\n      7:\n        dist:\n        bank: ~\n      3: {dist: a.zst, bank: a.bin}\n  - start: {dist: ~, bank: ~}\n    after: {}\n",
	}
	for name, raw := range yamlCases {
		got, err := decodeModesYAML(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "block nulls" && got[1].After != nil && len(got[1].After) == 0 {
			got[1].After = nil // after: {} and an omitted after mean the same
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	got, err := decodeModesJSON(`[{"start":{"dist":"s.zst","bank":"s.bin"},"after":{"3":{"dist":"a.zst","bank":"a.bin"},"7":{"dist":null,"bank":null}}},{"start":{"dist":null,"bank":null}}]`)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("json: %+v %v", got, err)
	}
	if !want[0].Start.Seeded() || want[1].Start.Seeded() {
		t.Fatal("Seeded")
	}
}

func TestOptimalModesRejectInvalidYAML(t *testing.T) {
	for name, c := range map[string]struct{ raw, want string }{
		"null mode":       {"  - ~\n  - start: {dist: ~, bank: ~}\n", "modes[0]: must not be null"},
		"missing start":   {"  - after: {}\n", "start: is required"},
		"null start":      {"  - start: ~\n", "start: must not be null"},
		"empty start":     {"  - start:\n", "start: must not be null"},
		"scalar start":    {"  - start: s.zst\n", "must be a mapping with dist and bank"},
		"empty slot":      {"  - start: {}\n", "is required"},
		"only dist":       {"  - start: {dist: s.zst}\n", "bank: is required"},
		"half null":       {"  - start: {dist: s.zst, bank: null}\n", "both be paths or both be null"},
		"empty strings":   {"  - start: {dist: '', bank: ''}\n", "non-empty path or null"},
		"number path":     {"  - start: {dist: 1, bank: 2}\n", "non-empty path or null"},
		"slot extra key":  {"  - start: {dist: ~, bank: ~, method: x}\n", "unknown key \"method\""},
		"mode extra key":  {"  - start: {dist: ~, bank: ~}\n    aftr: {}\n", "unknown key \"aftr\""},
		"dup mode key":    {"  - start: {dist: ~, bank: ~}\n    start: {dist: ~, bank: ~}\n", "duplicate key"},
		"null entry":      {"  - start: {dist: ~, bank: ~}\n    after: {3: ~}\n", "after[3]: must not be null"},
		"quoted entry":    {"  - start: {dist: ~, bank: ~}\n    after: {'3': {dist: ~, bank: ~}}\n", "must be an integer entry value"},
		"float entry":     {"  - start: {dist: ~, bank: ~}\n    after: {3.5: {dist: ~, bank: ~}}\n", "must be an integer entry value"},
		"duplicate entry": {"  - start: {dist: ~, bank: ~}\n    after: {1: {dist: ~, bank: ~}, 0x1: {dist: ~, bank: ~}}\n", "duplicate entry 1"},
		"scalar after":    {"  - start: {dist: ~, bank: ~}\n    after: 3\n", "must be a mapping from entry values"},
	} {
		modes, err := decodeModesYAML(c.raw)
		if err == nil {
			err = validModes(modes)
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", name, err, c.want)
		}
	}
}

func TestOptimalModesRejectInvalidJSON(t *testing.T) {
	for name, c := range map[string]struct{ raw, want string }{
		"null mode":       {`[null]`, "must not be null"},
		"missing start":   {`[{"after":{}}]`, "start: is required"},
		"null start":      {`[{"start":null}]`, "start: must not be null"},
		"empty slot":      {`[{"start":{}}]`, "is required"},
		"half null":       {`[{"start":{"dist":"s","bank":null}}]`, "both be paths or both be null"},
		"empty strings":   {`[{"start":{"dist":"","bank":""}}]`, "non-empty path or null"},
		"slot extra key":  {`[{"start":{"dist":null,"bank":null,"method":"x"}}]`, "unknown key"},
		"mode extra key":  {`[{"start":{"dist":null,"bank":null},"aftr":{}}]`, "unknown key"},
		"duplicate key":   {`[{"start":{"dist":null,"bank":null,"dist":"x"}}]`, "duplicate key"},
		"null entry":      {`[{"start":{"dist":null,"bank":null},"after":{"3":null}}]`, "after[3]: must not be null"},
		"text entry":      {`[{"start":{"dist":null,"bank":null},"after":{"a":{"dist":null,"bank":null}}}]`, "must be an integer entry value"},
		"duplicate entry": {`[{"start":{"dist":null,"bank":null},"after":{"1":{"dist":null,"bank":null},"01":{"dist":null,"bank":null}}}]`, "duplicate entry 1"},
	} {
		if _, err := decodeModesJSON(c.raw); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", name, err, c.want)
		}
	}
}

// Encoding and decoding again gives the same modes, in YAML and in JSON.
func TestOptimalModesRoundTrip(t *testing.T) {
	want := OptimalSetting{UseOptimal: true, Modes: OptimalModes{
		{Start: seeded("s.zst", "s.bin"), After: map[int]*GachaSlot{3: natural(), 7: seeded("b.zst", "b.bin")}},
		{Start: natural()},
	}}
	raw, err := yaml.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var fromYAML OptimalSetting
	if err := yaml.Unmarshal(raw, &fromYAML); err != nil || !reflect.DeepEqual(fromYAML.Modes, want.Modes) {
		t.Fatalf("yaml %s: %+v %v", raw, fromYAML, err)
	}
	raw, err = json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var fromJSON OptimalSetting
	if err := json.Unmarshal(raw, &fromJSON); err != nil || !reflect.DeepEqual(fromJSON.Modes, want.Modes) {
		t.Fatalf("json %s: %+v %v", raw, fromJSON, err)
	}
	if strings.Contains(string(raw), `"modes":null`) {
		t.Fatal("modes is not omitted")
	}
}

func TestOptimalSettingValidatesModes(t *testing.T) {
	modes := OptimalModes{{Start: natural()}}
	for name, c := range map[string]struct {
		s    OptimalSetting
		want string
	}{
		"exclusive":     {OptimalSetting{Modes: modes, Gachas: []string{"g"}, SeedBank: []string{"b"}}, "cannot be combined"},
		"exclusive off": {OptimalSetting{UseOptimal: false, Modes: modes, Gachas: []string{"g"}}, "cannot be combined"},
		"nil start":     {OptimalSetting{UseOptimal: true, Modes: OptimalModes{{}}}, "start: is required"},
		"nil entry":     {OptimalSetting{UseOptimal: true, Modes: OptimalModes{{Start: natural(), After: map[int]*GachaSlot{3: nil}}}}, "after[3]: must not be nil"},
		"half slot":     {OptimalSetting{UseOptimal: true, Modes: OptimalModes{{Start: &GachaSlot{Dist: "g"}}}}, "both be paths"},
	} {
		if err := c.s.valid(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", name, err, c.want)
		}
	}
	if err := (OptimalSetting{UseOptimal: true, Modes: modes}).valid(); err != nil {
		t.Fatalf("modes without gachas rejected: %v", err)
	}
	gs := &GameSetting{GameName: "test", BetUnits: []int{10, 20}, MaxWinLimit: 100, OptimalSetting: OptimalSetting{UseOptimal: true, Modes: modes}}
	if err := gs.valid(); err == nil || !strings.Contains(err.Error(), "modes must have exactly one entry per bet_unit") {
		t.Fatalf("count error %v", err)
	}
}
