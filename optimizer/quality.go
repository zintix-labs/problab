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

func (c *Class) qualityEval(shape *Shape) bool {
	median := shape.Median
	if shape.Median <= 0 {
		if shape.Mean <= 0 {
			return (1 <= c.skew[1]) && (1 >= c.skew[0])
		}
		median = 1e-6
	}
	ratio := shape.Mean / median
	if ratio > c.skew[0] && ratio < c.skew[1] {
		c.fail = 0
		return true
	}
	c.fail++
	if c.fail >= mercy {
		c.skew[0] -= 0.2
		c.skew[1] += 0.2
	}
	return false
}
