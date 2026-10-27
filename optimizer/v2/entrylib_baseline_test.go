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

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 暫時基準 B2：省略 target.input 的 plan，在收集段的輸入改由共用函式組裝之前與之後，
// ConfigHash、resolved_plan 與優化結果都相同。ConfigHash 以固定輸出目錄的設定計算
// （端到端執行的輸出目錄是暫存目錄，會改變雜湊）。驗收後刪除。

const entrylibBaselinePath = "testdata/entrylib_baseline.json"

func TestEntrylibBaseline(t *testing.T) {
	config, err := ParseConfig([]byte(validConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := config.ResolvePlan("demo-high-win-v2")
	if err != nil {
		t.Fatal(err)
	}
	configHash, err := hashCanonicalJSON(resolved)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := json.Marshal(resolved.Plan)
	if err != nil {
		t.Fatal(err)
	}
	planSum := sha256.Sum256(plan)

	result := runCompleteProductionPipeline(t, Int64Seed(4127483647), "entrylib-baseline")
	report := result.Report
	got := map[string]any{
		"config_hash":   configHash,
		"plan":          hex.EncodeToString(planSum[:]),
		"expected_rtp":  report.ExpectedRTP,
		"model_hash":    report.ModelHash,
		"solution_hash": report.SolutionHash,
		"artifact_hash": report.ArtifactHash,
		"pair":          filepath.Base(report.Publication.Directory),
		"bank_sha256":   report.Collection.Bank.SHA256,
	}
	raw, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PROBLAB_ENTRYLIB_BASELINE") == "update" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(entrylibBaselinePath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(entrylibBaselinePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(want) {
		t.Fatalf("baseline changed:\n got %s\nwant %s", raw, want)
	}
}
