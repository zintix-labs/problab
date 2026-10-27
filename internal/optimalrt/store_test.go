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

package optimalrt

import (
	"testing"

	"github.com/zintix-labs/problab/spec"
)

// 相同的 modes 設定得到相同的鍵；任一路徑、null 記號、入口或 bet unit 改變都會改變鍵。
func TestModesKey(t *testing.T) {
	base := func() spec.OptimalModes {
		return spec.OptimalModes{{
			Start: &spec.GachaSlot{Dist: "a.zst", Bank: "a.bin"},
			After: map[int]*spec.GachaSlot{3: {Dist: "b.zst", Bank: "b.bin"}, 7: {}},
		}}
	}
	key := modesKey(base(), []int{10})
	if modesKey(base(), []int{10}) != key {
		t.Fatal("the key is not stable")
	}
	for name, change := range map[string]func(spec.OptimalModes) []int{
		"start dist": func(m spec.OptimalModes) []int { m[0].Start.Dist = "x.zst"; return []int{10} },
		"entry bank": func(m spec.OptimalModes) []int { m[0].After[3].Bank = "x.bin"; return []int{10} },
		"null slot": func(m spec.OptimalModes) []int {
			m[0].After[7] = &spec.GachaSlot{Dist: "c", Bank: "c"}
			return []int{10}
		},
		"natural": func(m spec.OptimalModes) []int { m[0].Start = &spec.GachaSlot{}; return []int{10} },
		"entry value": func(m spec.OptimalModes) []int {
			m[0].After[8] = m[0].After[7]
			delete(m[0].After, 7)
			return []int{10}
		},
		"bet unit": func(m spec.OptimalModes) []int { return []int{20} },
	} {
		modes := base()
		units := change(modes)
		if modesKey(modes, units) == key {
			t.Errorf("%s did not change the key", name)
		}
	}
}
