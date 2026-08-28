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

package v2

import "math"

// compensatedSum implements deterministic Neumaier summation for semantic
// aggregates such as Expected RTP, probability totals, total variation, and
// collision concentration. These sums can mix very different magnitudes or
// millions of replay atoms; ordinary left-to-right addition can otherwise lose
// several trillionths and manufacture a failed numerical boundary check.
type compensatedSum struct {
	sum        float64
	correction float64
}

func (s *compensatedSum) Add(value float64) {
	next := s.sum + value
	if math.Abs(s.sum) >= math.Abs(value) {
		s.correction += (s.sum - next) + value
	} else {
		s.correction += (value - next) + s.sum
	}
	s.sum = next
}

func (s compensatedSum) Value() float64 { return s.sum + s.correction }
