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

package optimizer

import (
	"fmt"
	"sort"

	"github.com/zintix-labs/problab/sdk/core"
)

type Basis struct {
	Exp float64
	Pos []*Shape
	Neg []*Shape
}

func (c *Class) MakeBasis(core *core.Core) *Basis {
	if len(c.samps) == 0 {
		return nil
	}
	sort.Slice(c.samps, func(i int, j int) bool {
		return c.samps[i].Win < c.samps[j].Win
	})
	wins := make([]float64, len(c.samps))
	for i, s := range c.samps {
		wins[i] = s.Win
	}
	posMax := int(c.cfg.Basis)
	negMax := int(c.cfg.Basis)
	exp := c.cfg.ExpWin

	result := &Basis{
		Exp: exp,
		Pos: make([]*Shape, 0, posMax),
		Neg: make([]*Shape, 0, negMax),
	}

	count := uint64(0)
	for {
		shape := c.gener.Gen(wins, core)
		count++
		if (len(result.Pos) < posMax) && (shape.Mean >= exp) {
			result.Pos = append(result.Pos, shape)
		}
		if (len(result.Neg) < negMax) && (shape.Mean <= exp) {
			result.Neg = append(result.Neg, shape)
		}
		if (len(result.Pos) >= posMax) && (len(result.Neg) >= negMax) {
			fmt.Printf("\r")
			break
		}
		if count%10000 == 0 {
			fmt.Printf("\rpos: %d neg: %d", len(result.Pos), len(result.Neg))
		}
	}
	return result
}
