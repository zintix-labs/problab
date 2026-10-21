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
	"testing"

	v2 "github.com/zintix-labs/problab/optimizer/v2"
)

// Without report details, each status prints exactly one result line.
func TestReportV2OutcomeWithoutReportDetails(t *testing.T) {
	for _, c := range []struct {
		status v2.Status
		want   string
	}{
		{v2.StatusOptimal, "[Result] Generation succeeded\n"},
		{v2.StatusExported, "[Result] Exported; LP and distribution verification were not requested.\n"},
		{v2.StatusInfeasibleConfig, "[Result] Failed (INFEASIBLE_CONFIG): no diagnostics available\n"},
	} {
		var out bytes.Buffer
		reportV2Outcome(&out, v2.RunResult{Status: c.status})
		if got := out.String(); got != c.want {
			t.Errorf("%s: got %q want %q", c.status, got, c.want)
		}
	}
}
