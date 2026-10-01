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

// Regression from independent review: all final-report paths honor nil output.

import (
	"testing"

	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
)

func TestReportNilWriter(t *testing.T) {
	for _, c := range []struct {
		name   string
		result optimizerv2.RunResult
	}{
		{"exported", optimizerv2.RunResult{Status: optimizerv2.StatusExported}},
		{"rgs-exports", optimizerv2.RunResult{Status: optimizerv2.StatusOptimal,
			Report: optimizerv2.RunReport{RGSExports: []optimizerv2.RGSExportReport{{Family: "collected", State: "COMPLETED"}}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("reportV2Outcome(nil, ...) panicked: %v", r)
				}
			}()
			reportV2Outcome(nil, c.result)
		})
	}
}
