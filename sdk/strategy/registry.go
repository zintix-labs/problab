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

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/zintix-labs/problab/spec"
)

// Registry is game-scoped. Its zero value works; registration and Run
// must not overlap. Factories themselves are borrowed, not cloned.
type Registry struct {
	factories map[spec.GID]map[string]Factory
}

// NewRegistry creates an empty game-scoped registry.
func NewRegistry() *Registry { return &Registry{} }

// Register binds an exact name; duplicates and nil/typed-nil factories fail.
func (r *Registry) Register(gid spec.GID, name string, f Factory) error {
	if r == nil || strings.TrimSpace(name) == "" || nilLike(f) {
		return fmt.Errorf("invalid strategy registration %q", name)
	}
	if _, ok := r.Get(gid, name); ok {
		return fmt.Errorf("duplicate strategy %q for game %d", name, gid)
	}
	if r.factories == nil {
		r.factories = make(map[spec.GID]map[string]Factory)
	}
	if r.factories[gid] == nil {
		r.factories[gid] = make(map[string]Factory)
	}
	r.factories[gid][name] = f
	return nil
}

// Get resolves only the requested game's namespace; nil registries are empty.
func (r *Registry) Get(gid spec.GID, name string) (Factory, bool) {
	if r == nil {
		return nil, false
	}
	f, ok := r.factories[gid][name]
	return f, ok
}
func nilLike(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	}
	return false
}
