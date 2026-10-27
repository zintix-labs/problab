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

package strategy

import "testing"

// Register rejects blank names, nil and typed-nil factories and duplicates
// within a game; the same name in another game is a separate entry.
func TestRegistryRegister(t *testing.T) {
	r := NewRegistry()
	var typedNil factoryFunc
	for name, f := range map[string]Factory{" ": fixedFactory(1), "nil": nil, "typed": typedNil} {
		if err := r.Register(0, name, f); err == nil {
			t.Errorf("%q was registered", name)
		}
	}
	if err := r.Register(0, "s", fixedFactory(1)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(0, "s", fixedFactory(1)); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := r.Register(1, "s", fixedFactory(1)); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get(2, "s"); ok {
		t.Fatal("cross-game fallback")
	}
	var empty *Registry
	if _, ok := empty.Get(0, "s"); ok {
		t.Fatal("nil registry is not empty")
	}
	if err := empty.Register(0, "s", fixedFactory(1)); err == nil {
		t.Fatal("nil registry accepted a registration")
	}
}
