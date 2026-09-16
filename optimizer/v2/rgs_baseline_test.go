// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Captured before production changes for the RGS export implementation.
func TestRGSNativePreChangeBaseline(t *testing.T) {
	r := runCompleteProductionPipeline(t, Int64Seed(4127483647), "rgs-baseline")
	if r.Report.ModelHash != "6581871339c863041655a023a1670ed20d31850c6e84b8246eb885dd8110fe1f" || r.Report.SolutionHash != "1b5e48254dd3fef67ea4eab2b33b1392e24c481bbea233f12d8752c5dd00fa07" || r.Report.Collection.Bank.SHA256 != "f12afcc525d4d4b5c29970d3710e62f5674175a8b80e49bbb3ade8244d3b3ade" {
		t.Fatal("native baseline changed")
	}
	want := map[string]string{
		"manifest.json":    "85ffaedeae4ee122965970c5c3660b85db51dbd77978930eedef97a9f32b27eb",
		"gacha_0.json.zst": "927a461737e819f1d9bd7860a4b8b70758129877ce6d08488b8c7b403279ba58",
		"seed_bank_0.bin":  "f12afcc525d4d4b5c29970d3710e62f5674175a8b80e49bbb3ade8244d3b3ade",
		"mode_0.json":      "74ca31cb3b1dfeabc212043f8c4d239ed7ac30983267ee825e36eb87e3879397",
		"prob_0.bin":       "1a46cf28730fdbcf9d8af2dd2a2794ae19021b8e9a93eab950e526f8c65e10c6",
		"aliases_0.bin":    "f9a6d985846761cfe2444158273b90f0862238f8545c9785892212b3266a5067",
	}
	for _, p := range r.ArtifactPaths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != want[filepath.Base(p)] {
			t.Fatalf("%s=%s want=%s", p, got, want[filepath.Base(p)])
		}
	}
}
