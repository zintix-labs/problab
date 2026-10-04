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
	"strings"
	"testing"
)

func TestStrategyCatalogBuildRegistry(t *testing.T) {
	f := fixedFactory(1)
	c := StrategyCatalog{1: {{Name: "same", Factory: f}}, 0: {{Name: "same", Factory: f}}}
	a, err := c.BuildRegistry()
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.BuildRegistry()
	if err != nil || a == b {
		t.Fatal("registry not fresh", err)
	}
	if _, ok := a.Get(0, "same"); !ok {
		t.Fatal("missing game 0")
	}
	if _, ok := a.Get(1, "same"); !ok {
		t.Fatal("missing game 1")
	}
	if _, ok := a.Get(2, "same"); ok {
		t.Fatal("cross-game fallback")
	}
	c[0][0].Name = "changed"
	delete(c, 1)
	if _, ok := a.Get(0, "same"); !ok {
		t.Fatal("catalog slice aliases registry")
	}
	if _, ok := a.Get(1, "same"); !ok {
		t.Fatal("catalog map aliases registry")
	}
	if err := a.Register(0, "extra", f); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Get(0, "extra"); ok {
		t.Fatal("registry maps shared")
	}
	var empty StrategyCatalog
	if r, err := empty.BuildRegistry(); err != nil || r == nil {
		t.Fatal(r, err)
	}
}

func TestStrategyCatalogValidationOrder(t *testing.T) {
	var typedNil factoryFunc
	for _, entries := range [][]StrategyRegistration{
		{{Name: " ", Factory: fixedFactory(1)}},
		{{Name: "nil"}},
		{{Name: "typed", Factory: typedNil}},
		{{Name: "repeat", Factory: fixedFactory(1)}, {Name: "repeat", Factory: fixedFactory(1)}},
	} {
		c := StrategyCatalog{9: {{Name: "later"}}, 2: entries}
		for i := 0; i < 50; i++ {
			r, err := c.BuildRegistry()
			if r != nil || err == nil || !strings.Contains(err.Error(), "game 2 strategy[") {
				t.Fatal(r, err)
			}
		}
	}
}
