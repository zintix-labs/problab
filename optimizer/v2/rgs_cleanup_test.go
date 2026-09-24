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

package v2

// Regression from independent review: failed publication removes only its
// staging and newly created empty run directory, never committed siblings.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/dto"
)

func TestRGSFailureRemovesEmptyRunDirectory(t *testing.T) {
	base := runCompleteProductionPipeline(t, Int64Seed(4127483647), "tmp-litter")
	tuner := rgsTestTuner(t, base, []OutputFormat{OutputRGSCollected},
		WithResultConverter(func(dto.SpinResult) (json.RawMessage, error) { return nil, fmt.Errorf("boom") }))
	if _, err := tuner.Run(context.Background(), RunRequest{PlanID: base.Report.Plan.Plan.ID}); err == nil {
		t.Fatal("expected failure")
	}
	root := tuner.config.Plans[0].Output.Directory
	var leftovers []string
	err := filepath.Walk(root, func(p string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if rel, _ := filepath.Rel(root, p); strings.Contains(rel, "export_") {
			leftovers = append(leftovers, fmt.Sprintf("%s dir=%v", rel, i.IsDir()))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) > 0 {
		t.Errorf("failed export left uncommitted directories behind: %v", leftovers)
	}
}
