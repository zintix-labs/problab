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
	"flag"
	"fmt"
	"log"
	"strconv"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/optimizer"
	"github.com/zintix-labs/problab/spec"
)

var optgid spec.GID
var mode int

func main() {
	flag.Var(gidFlag{&optgid}, "game", "target game id")
	flag.IntVar(&mode, "mode", 0, "bet mode index")
	flag.Parse()
	lab, err := demo.NewProbLab()
	if err != nil {
		log.Fatal(err)
	}
	tuner, err := optimizer.New(OptCfg, "opt_cfg.yaml")
	if err != nil {
		log.Fatal(err)
	}
	if err := tuner.Run(optgid, mode, lab, 4127483647); err != nil {
		log.Fatal(err)
	}
}

type gidFlag struct{ p *spec.GID }

func (f gidFlag) String() string { return fmt.Sprint(uint(*f.p)) }
func (f gidFlag) Set(s string) error {
	u, err := strconv.ParseUint(s, 10, 0)
	if err != nil {
		return err
	}
	*f.p = spec.GID(uint(u))
	return nil
}
