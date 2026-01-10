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

import (
	"log"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/optimizer"
)

func main() {
	lab, err := demo.NewProbLab()
	if err != nil {
		log.Fatal(err)
	}
	tuner, err := optimizer.New(OptCfg, "opt_cfg.yaml")
	if err != nil {
		log.Fatal(err)
	}
	if err := tuner.Run(1, 0, lab, 4127483647); err != nil {
		log.Fatal(err)
	}
}
