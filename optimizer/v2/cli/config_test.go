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

package cli

import (
	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
	"os"
	"strings"
	"testing"
)

// TestParseEmbeddedConfigContentFailuresAreTyped retains the command boundary's
// distinction between strict-schema decoding and semantic validation without
// reintroducing an external-config runtime path.
func TestParseEmbeddedConfigContentFailuresAreTyped(t *testing.T) {
	canonical, err := os.ReadFile("../../../cmd/opt/opt_cfg.yaml")
	if err != nil {
		t.Fatalf("read embedded %s: %v", "opt_cfg.yaml", err)
	}

	tests := []struct {
		name      string
		content   []byte
		want      string
		wantStage string
	}{
		{
			name:      "unknown field",
			content:   append(append([]byte(nil), canonical...), []byte("\nunexpected_embedded_field: true\n")...),
			want:      "unexpected_embedded_field",
			wantStage: "load-config",
		},
		{
			name:      "semantic invalid",
			content:   []byte(strings.Replace(string(canonical), "version: 2", "version: 999", 1)),
			want:      "version",
			wantStage: "static-validation",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseV2ConfigBytes(test.content, `embedded "opt_cfg.yaml"`)
			if err == nil {
				t.Fatal("parseV2ConfigBytes succeeded")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%q, want %q", err, test.want)
			}
			if got := invalidV2ConfigStage(err); got != test.wantStage {
				t.Fatalf("invalid stage=%q, want %q", got, test.wantStage)
			}
			result := configInvalidRunResult(err, test.wantStage, 0)
			if result.Status != optimizerv2.StatusInfeasibleConfig ||
				len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != optimizerv2.DiagnosticConfigInvalid {
				t.Fatalf("typed result=%+v", result)
			}
		})
	}
}
