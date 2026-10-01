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

package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"testing"

	v2 "github.com/zintix-labs/problab/optimizer/v2"
)

func TestCLIExtractionBaseline(t *testing.T) {
	var out bytes.Buffer
	for _, status := range []v2.Status{v2.StatusOptimal, v2.StatusExported, v2.StatusInfeasibleConfig} {
		reportV2Outcome(&out, v2.RunResult{Status: status})
	}
	got := fmt.Sprintf("%x", sha256.Sum256(out.Bytes()))
	if got != "7e5ad4e7e4cf60c61faa23b29b9824ff55917fd475d9ecd0dc26a8e9acdf5619" {
		t.Fatal(got)
	}
}
