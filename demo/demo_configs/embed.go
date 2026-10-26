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

package demo_configs

import (
	"embed"
	"io/fs"
)

// FS provides embedded default config YAMLs for external usage.
//
//go:embed *.yaml
var FS embed.FS

//go:embed staged/*.yaml
var staged embed.FS

// StagedFS provides demo configs that are not part of FS yet: games that stop at a
// player choice, which the simulators cannot drive yet. Load it explicitly, for example
// problab.Configs(demo_configs.FS, demo_configs.StagedFS).
var StagedFS fs.FS = func() fs.FS {
	sub, err := fs.Sub(staged, "staged")
	if err != nil {
		panic(err)
	}
	return sub
}()
