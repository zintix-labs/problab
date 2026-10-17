// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// arm64 was captured before the RGS changes. amd64 was independently captured
// from pre-RGS commit fd4c5eea47b2db266cf594de327e86a42ba5b665 and checked
// against the new code on Linux and Darwin (Go 1.25.2, default compiler flags).
// Floating-point compiler/backend paths need not be byte-identical across
// architectures. These are exact per-architecture regression baselines, not a
// promise of cross-architecture LP bytes. Never regenerate from current code.
func TestRGSNativePreChangeBaseline(t *testing.T) {
	r := runCompleteProductionPipeline(t, Int64Seed(4127483647), "rgs-baseline")
	const bankHash = "f12afcc525d4d4b5c29970d3710e62f5674175a8b80e49bbb3ade8244d3b3ade"
	if r.Report.Collection.Bank.SHA256 != bankHash {
		t.Errorf("collection hash=%s want=%s", r.Report.Collection.Bank.SHA256, bankHash)
	}
	modelHash := "6581871339c863041655a023a1670ed20d31850c6e84b8246eb885dd8110fe1f"
	solutionHash := "18bace80e28ca49b1fe303595bf307b5657ceee4ddf9410521f8e2141ec5364c"
	want := map[string]string{
		"manifest.json":    "96e873359f245083e0c1fbf4d4627b57f56f5583e2553a10ac5682e3b35ca6b7",
		"gacha_0.json.zst": "d9aa63522fc4dd3ee68d160a1ec49531f64392190170033a888beceea7abdba3",
		"seed_bank_0.bin":  "f12afcc525d4d4b5c29970d3710e62f5674175a8b80e49bbb3ade8244d3b3ade",
		"mode_0.json":      "9acfe0b9bb1b4a17ae032d0b89dad2ec5d385ee30ba865f650e59d6b99561826",
		"prob_0.bin":       "2272eb5f466e5e500903203e3950e2e04309b526a12946df65909a358dc3c326",
		"aliases_0.bin":    "f9a6d985846761cfe2444158273b90f0862238f8545c9785892212b3266a5067",
	}
	switch runtime.GOARCH {
	case "arm64":
		// PRD 0010 (intent-lp-v2.10.0) changed the selected point: solution,
		// manifest, gacha, mode and prob bytes re-captured on darwin/arm64;
		// model, collection, seed bank and alias bytes are unchanged.
	case "amd64":
		t.Skip("amd64 golden predates PRD 0010 (intent-lp-v2.10.0) and needs re-capture on amd64")
		modelHash = "31bb020f3e3475099658e7f66b26c0d1381e5be910900115c0dacce32bffd0f1"
		solutionHash = "98534e5980bc99338e03472a87bc2fd4848f7eb58855de23f3c1e6b02e009727"
		want["manifest.json"] = "c19d8d2ba45bb55797ebb96816435d76e63cec8e053d7b3c4465aaffebd44342"
		want["gacha_0.json.zst"] = "752aa04ee146373df3c65d5f42cb0c82302af41925a64321d9255df6ab63861a"
		want["mode_0.json"] = "06259b1f76879da15691d77ce68689ef00ee2b3fb35383e455b4143cc064b79b"
		want["prob_0.bin"] = "9ad73b09e8d7c4fa02bd0c78421dbabdd2c10d1fbd06a57bbd2b95922eed6a93"
	default:
		t.Skipf("no pre-change floating-point golden for %s; portable mixed-output parity is tested separately", runtime.GOARCH)
	}
	t.Logf("baseline environment: %s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	for _, check := range []struct{ name, got, want string }{
		{"model", r.Report.ModelHash, modelHash},
		{"solution", r.Report.SolutionHash, solutionHash},
	} {
		if check.got != check.want {
			t.Errorf("%s hash=%s want=%s", check.name, check.got, check.want)
		}
	}
	seen := make(map[string]bool)
	for _, p := range r.ArtifactPaths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != want[filepath.Base(p)] {
			t.Errorf("%s=%s want=%s", p, got, want[filepath.Base(p)])
		}
		seen[filepath.Base(p)] = true
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("missing baseline artifact %s", name)
		}
	}
}
