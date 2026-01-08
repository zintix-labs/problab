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

import "github.com/zintix-labs/problab/sdk/core"

func (c *Class) fitOneOnOne(bs *Basis, core *core.Core) *Shape {
	for range maxTry {
		pos := bs.Pos[core.IntN(len(bs.Pos))]
		neg := bs.Neg[core.IntN(len(bs.Neg))]
		diff := pos.Mean - neg.Mean
		if diff == 0 {
			return pos
		}
		if diff < 0 {
			pos, neg = neg, pos
			diff = -diff
		}
		p := (bs.Exp - neg.Mean) / (pos.Mean - neg.Mean)
		if p < 0 || p > 1 {
			continue
		}
		q := 1.0 - p
		weights := make([]float64, len(pos.Weights))
		for i := range pos.Weights {
			weights[i] = p*pos.Weights[i] + q*neg.Weights[i]
		}
		return &Shape{
			Weights: weights,
			Mean:    meanOf(c.wins, weights),
			Median:  medianOf(c.wins, weights),
		}
	}
	return nil
}
