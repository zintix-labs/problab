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
)

func TestDemo0CollectionTags(t *testing.T) {
	tagger, err := Catalog.CompileBy(0, "bg", "fg")
	if err != nil {
		t.Fatal(err)
	}
	bg, err := tagger.Want("bg")
	if err != nil {
		t.Fatal(err)
	}
	fg, err := tagger.Want("fg")
	if err != nil {
		t.Fatal(err)
	}
	none, err := tagger.Want()
	if err != nil {
		t.Fatal(err)
	}
	tags := tagger.NewMasks()
	for _, modes := range []int{1, 2} {
		for _, freeWin := range []int{0, 150, 179, 180, 181, 300} {
			sr := &buf.SpinResult{GameModeCount: modes, Bet: 30, TotalWin: freeWin + 90, GameModeList: []*buf.GameModeResult{{TotalWin: 90}}}
			want := none
			if modes == 1 {
				want = bg
			} else if freeWin >= 180 {
				want = fg
			}
			tagger.Tag(tags, sr)
			if !tagger.Equal(tags, want) {
				t.Fatalf("modes=%d freeWin=%d got=%v want=%v", modes, freeWin, tagger.ToNames(tags), tagger.ToNames(want))
			}
		}
	}
}
