//go:build !poc

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

package main

import "embed"

// embeddedV2Config contains only the production v2 RunPlan. The historical
// nine-case formulation POC remains available with `-tags poc` and therefore
// cannot be mistaken for the command's runtime optimizer configuration.
//
//go:embed opt_cfg.yaml
var embeddedV2Config embed.FS
