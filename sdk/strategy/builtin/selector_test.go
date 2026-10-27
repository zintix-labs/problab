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

package builtin_test

import (
	"reflect"
	"testing"

	"github.com/zintix-labs/problab/sdk/strategy/builtin"
)

// picks returns the positions a fresh Selector takes for choice points with the
// given option counts.
func picks(mode builtin.Choose, counts ...int) []int {
	s := builtin.Selector{Mode: mode}
	out := make([]int, len(counts))
	for i, n := range counts {
		out[i], _ = s.Pick(make([]int, n))
	}
	return out
}

func TestSelectorIter(t *testing.T) {
	for _, c := range []struct{ counts, want []int }{
		{[]int{2, 2, 2}, []int{0, 1, 0}},
		{[]int{2, 1, 2, 1, 2}, []int{0, 0, 1, 0, 0}}, // a single option leaves the counter alone
		{[]int{3, 3, 3, 2}, []int{0, 1, 2, 0}},       // past the options of the next point: back to 0
		{[]int{2, 2, 3}, []int{0, 1, 2}},             // more options at the next point: carries on
		{[]int{1, 1, 3, 3}, []int{0, 0, 0, 1}},
	} {
		if got := picks(builtin.ChooseIter, c.counts...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("counts %v: positions %v, want %v", c.counts, got, c.want)
		}
	}
}

func TestSelectorFirstLastUnset(t *testing.T) {
	if got := picks(builtin.ChooseFirst, 3, 1, 2); !reflect.DeepEqual(got, []int{0, 0, 0}) {
		t.Errorf("first %v", got)
	}
	if got := picks(builtin.ChooseLast, 3, 1, 2); !reflect.DeepEqual(got, []int{2, 0, 1}) {
		t.Errorf("last %v", got)
	}
	var s builtin.Selector
	if _, ok := s.Pick([]int{3, 7}); ok {
		t.Error("an unset selector chose")
	}
}
