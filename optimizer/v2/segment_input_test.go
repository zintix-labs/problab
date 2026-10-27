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

import (
	"reflect"
	"testing"

	"github.com/zintix-labs/problab"
)

func TestSegmentInput(t *testing.T) {
	if got := segmentInput(Target{}, 2); !reflect.DeepEqual(got, problab.SpinInput{BetMode: 2, BetMult: 1}) {
		t.Fatalf("initial segment %+v", got)
	}
	choice := 1
	got := segmentInput(Target{Input: &TargetInput{ChooseNext: []int{3, 7}, Choice: &choice}}, 0)
	want := problab.SpinInput{BetMode: 0, BetMult: 1, Cycle: 1, ChooseNext: []int{3, 7}, Choice: 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entry segment %+v", got)
	}
	if entry := (&TargetInput{ChooseNext: []int{3, 7}, Choice: &choice}).Entry(); entry != 7 {
		t.Fatalf("entry %d", entry)
	}
}
