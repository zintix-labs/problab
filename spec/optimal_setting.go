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
	"github.com/zintix-labs/problab/errs"
)

type OptimalSetting struct {
	UseOptimal bool     `yaml:"use_optimal" json:"use_optimal"`
	Gachas     []string `yaml:"gachas"      json:"gachas"`
	SeedBank   []string `yaml:"seed_bank"   json:"seed_bank"`
}

// valid validates the OptimalSetting configuration.
// Rules:
// 1) If UseOptimal is true, both gachas and seed_bank must be non-empty.
// 2) gachas and seed_bank must have the same length (1:1 mapping).
func (s OptimalSetting) valid() error {
	if !s.UseOptimal {
		return nil
	}

	if len(s.Gachas) == 0 {
		return errs.NewFatal("optimal_setting: gachas must not be empty when use_optimal=true")
	}
	if len(s.SeedBank) == 0 {
		return errs.NewFatal("optimal_setting: seed_bank must not be empty when use_optimal=true")
	}
	if len(s.Gachas) != len(s.SeedBank) {
		return errs.Fatalf(
			"optimal_setting: gachas and seed_bank length mismatch (gachas=%d seed_bank=%d)",
			len(s.Gachas),
			len(s.SeedBank),
		)
	}
	return nil
}
