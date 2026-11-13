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
	"strings"
	"testing"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"gopkg.in/yaml.v3"
)

func initHitNeighbor(t *testing.T, raw string, trim []int) error {
	t.Helper()
	var gms GameModeSetting
	if err := yaml.Unmarshal([]byte(raw), &gms.HitSetting); err != nil {
		t.Fatal(err)
	}
	// 3x2 盤面：
	//   0 1 2
	//   3 4 5
	gms.ScreenSetting = ScreenSetting{Columns: 3, Rows: 2, Trim: trim}
	if err := gms.ScreenSetting.Init(); err != nil {
		t.Fatal(err)
	}
	if err := gms.HitSetting.Init(); err != nil {
		return err
	}
	return hitNeighborValid(&gms.HitSetting, &gms.ScreenSetting)
}

func TestHitSettingNeighbor(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		trim []int
		want string
	}{
		{"valid", "bet_type: cluster_by_neighbor\nneighbor:\n  1: [2, 3]\n  2: [1]\n  3: [1]\n", []int{1, 0, 0}, ""},
		{"missing", "bet_type: cluster_by_neighbor\n", nil, "neighbor is empty"},
		{"other bet type", "bet_type: cluster\nneighbor:\n  0: [1]\n  1: [0]\n", nil, "only allowed with bet_type cluster_by_neighbor"},
		{"other bet type empty", "bet_type: line_ltr\nline_table: [[0, 0, 0]]\nneighbor: {}\n", nil, ""},
		{"key out of range", "bet_type: cluster_by_neighbor\nneighbor:\n  6: [5]\n  5: [6]\n", nil, "neighbor cell 6 must be between 0 to 5"},
		{"value out of range", "bet_type: cluster_by_neighbor\nneighbor:\n  0: [-1]\n", nil, "neighbor cell -1 must be between 0 to 5"},
		{"trimmed", "bet_type: cluster_by_neighbor\nneighbor:\n  0: [1]\n  1: [0]\n", []int{1, 0, 0}, "neighbor cell 0 is trimmed"},
		{"self", "bet_type: cluster_by_neighbor\nneighbor:\n  0: [0]\n", nil, "neighbor cell 0 lists itself"},
		{"duplicate", "bet_type: cluster_by_neighbor\nneighbor:\n  0: [1, 1]\n  1: [0]\n", nil, "neighbor cell 0 lists 1 more than once"},
		{"one way", "bet_type: cluster_by_neighbor\nneighbor:\n  0: [1, 4]\n  1: [0]\n", nil, "neighbor cell 0 lists 4 but 4 does not list 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := initHitNeighbor(t, tc.raw, tc.trim)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

// 既有設定載入後寫出 YAML／JSON，必須能再次載入；未設定的 neighbor 不可寫出。
func TestGameSettingRoundTrip(t *testing.T) {
	files, err := demo_configs.FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".yaml") {
			continue
		}
		t.Run(f.Name(), func(t *testing.T) {
			raw, err := demo_configs.FS.ReadFile(f.Name())
			if err != nil {
				t.Fatal(err)
			}
			gs, err := GetGameSettingByYAML(raw)
			if err != nil {
				t.Fatal(err)
			}
			y, err := yaml.Marshal(gs)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := GetGameSettingByYAML(y); err != nil {
				t.Fatalf("reload yaml: %v", err)
			}
			j, err := json.Marshal(gs)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := GetGameSettingByJSON(j); err != nil {
				t.Fatalf("reload json: %v", err)
			}
			if strings.Contains(string(y), "neighbor") || strings.Contains(string(j), "neighbor") {
				t.Fatal("unset neighbor was written out")
			}
		})
	}
}
