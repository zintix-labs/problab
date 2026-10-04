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

package demo_tags

import (
	"testing"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/tag"
)

func TestDemo0CollectionTags(t *testing.T) {
	r, err := tag.NewRegistry(Catalog[0])
	if err != nil {
		t.Fatal(err)
	}
	bs, err := r.BitSet("bg", "fg")
	if err != nil {
		t.Fatal(err)
	}
	for _, modes := range []int{1, 2} {
		for _, freeWin := range []int{0, 150, 179, 180, 181, 300} {
			sr := &buf.SpinResult{GameModeCount: modes, Bet: 30, TotalWin: freeWin + 90, GameModeList: []*buf.GameModeResult{{TotalWin: 90}}}
			var want uint64
			if modes == 1 {
				want = 1
			} else if freeWin >= 180 {
				want = 2
			}
			if got := bs.Tagging(sr); got != want {
				t.Fatalf("modes=%d freeWin=%d got=%d want=%d", modes, freeWin, got, want)
			}
		}
	}
}
