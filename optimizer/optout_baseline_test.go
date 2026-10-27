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

package optimizer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
	"github.com/zintix-labs/problab/sdk/core"
)

// 暫時基準：V1 是 v1 Save 寫出的檔案位元組（只用於安全寫檔那一步）；V3 是 v1 與 v2 寫出的 gacha
// 解壓後內容與 seed bank（保留到共用寫出器完成）。兩者用同一組固定輸入。驗收後刪除。

const optoutBaselinePath = "testdata/optout_baseline.json"

type optoutBaseline struct {
	V1 map[string]string `json:"v1"`
	V3 map[string]string `json:"v3"`
}

func optoutDigest(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])
}

// optoutMode 以六筆 ChaCha20 快照建立 v2 的 MaterializedMode；v1 使用同一個 picker 與 seed bank。
func optoutMode(t *testing.T) optimizerv2.MaterializedMode {
	t.Helper()
	weights := []float64{1, 2, 3, 4, 5, 6}
	samples := make([]optimizerv2.MaterializedSample, len(weights))
	for i, w := range weights {
		rng, err := core.Default().New(core.EncodeInt64Seed(int64(300 + i)))
		if err != nil {
			t.Fatal(err)
		}
		snap, err := rng.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		samples[i] = optimizerv2.MaterializedSample{ClassID: "c", Win: float64(i), Snapshot: snap, Probability: w / 21}
	}
	mode, err := optimizerv2.MaterializeMode(0, 40, samples)
	if err != nil {
		t.Fatal(err)
	}
	return mode
}

func optoutUnzstd(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func optoutReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func optoutRecord(t *testing.T) optoutBaseline {
	t.Helper()
	mode := optoutMode(t)
	seedLen := len(mode.Samples[0].Snapshot)
	b := optoutBaseline{V1: map[string]string{}, V3: map[string]string{}}

	// v1：Save 寫到 build/optimizer。
	t.Chdir(t.TempDir())
	if err := new(Tuner).Save(7, &Gacha{Picker: mode.Picker, SeedLen: seedLen}, mode.SeedBank); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join("build", "optimizer")
	for _, name := range []string{"gacha_7.json.zst", "seed_bank_7.bin", "prob_7.bin", "aliases_7.bin", "info_7.json"} {
		b.V1[name] = optoutDigest(optoutReadFile(t, filepath.Join(out, name)))
	}
	b.V3["v1_gacha_json"] = optoutDigest(optoutUnzstd(t, filepath.Join(out, "gacha_7.json.zst")))
	b.V3["v1_seed_bank"] = optoutDigest(optoutReadFile(t, filepath.Join(out, "seed_bank_7.bin")))

	// v2：單一 bet mode，現行的寫出器立即發布。
	published, err := optimizerv2.GachaArtifactWriter{Directory: t.TempDir()}.PublishMode(
		context.Background(), 7, core.SnapshotFormatOfPRNG(mustDefaultPRNG(t)), []int{40}, mode)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range published.Paths {
		switch {
		case strings.HasSuffix(path, ".json.zst"):
			b.V3["v2_gacha_json"] = optoutDigest(optoutUnzstd(t, path))
		case strings.HasSuffix(path, ".bin"):
			b.V3["v2_seed_bank"] = optoutDigest(optoutReadFile(t, path))
		}
	}
	return b
}

func mustDefaultPRNG(t *testing.T) core.PRNG {
	t.Helper()
	rng, err := core.Default().New(core.EncodeInt64Seed(1))
	if err != nil {
		t.Fatal(err)
	}
	return rng
}

func TestOptoutBaseline(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wd, optoutBaselinePath)
	got := optoutRecord(t)
	raw, err := json.MarshalIndent(got, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PROBLAB_OPTOUT_BASELINE") == "update" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	var want optoutBaseline
	if err := json.Unmarshal(optoutReadFile(t, path), &want); err != nil {
		t.Fatal(err)
	}
	for _, sec := range []struct {
		name      string
		got, want map[string]string
	}{{"v1", got.V1, want.V1}, {"v3", got.V3, want.V3}} {
		if len(sec.got) != len(sec.want) {
			t.Errorf("%s: %d entries, want %d", sec.name, len(sec.got), len(sec.want))
		}
		for k, w := range sec.want {
			if sec.got[k] != w {
				t.Errorf("%s %s: %s, want %s", sec.name, k, sec.got[k], w)
			}
		}
	}
}
