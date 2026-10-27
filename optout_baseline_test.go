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

package problab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"regexp"
	"testing"
	"testing/fstest"

	"github.com/klauspost/compress/zstd"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/sampler"
)

// 暫時基準 V2：以 gacha 設定開啟 optimal 的 demo_0，FS 與 Dir 各錄一份。夾具與設定都在本檔自建，
// 不依賴之後會改名或改寫的測試輔助。驗收後刪除。

const optoutBaselinePath = "testdata/optout_baseline.json"

var optoutSettingBlock = regexp.MustCompile(`(?s)optimal_setting:.*?\n\n`)

func optoutGachaFS(t *testing.T) fstest.MapFS {
	t.Helper()
	weights := []float64{1, 2, 3, 4, 5, 6, 7, 8}
	var bank []byte
	seedLen := 0
	for i := range weights {
		rng, err := core.Default().New(core.EncodeInt64Seed(int64(100 + i)))
		if err != nil {
			t.Fatal(err)
		}
		snap, err := rng.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		seedLen = len(snap)
		bank = append(bank, snap...)
	}
	raw, err := json.Marshal(struct {
		Picker  *sampler.AliasTableF64 `json:"picker"`
		SeedLen int                    `json:"seed_len"`
	}{sampler.BuildAliasTableF64(weights), seedLen})
	if err != nil {
		t.Fatal(err)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return fstest.MapFS{
		"opt/gacha.json.zst": &fstest.MapFile{Data: enc.EncodeAll(raw, nil)},
		"opt/seed_bank.bin":  &fstest.MapFile{Data: bank},
	}
}

func optoutConfigFS(t *testing.T) fs.FS {
	t.Helper()
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	block := "optimal_setting:\n  use_optimal: true\n  gachas: [opt/gacha.json.zst]\n  seed_bank: [opt/seed_bank.bin]\n\n"
	if !optoutSettingBlock.Match(raw) {
		t.Fatal("demo config has no optimal_setting block")
	}
	updated := optoutSettingBlock.ReplaceAllLiteral(raw, []byte(block))
	return fstest.MapFS{"game.yaml": &fstest.MapFile{Data: updated}}
}

func optoutHash(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:12])
}

func optoutRecord(t *testing.T, opt ProblabOption) map[string]any {
	t.Helper()
	lab, err := NewAuto(core.Default(), Configs(optoutConfigFS(t)), Logics(demo_logic.Logics), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	m, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(2026)})
	if err != nil {
		t.Fatal(err)
	}
	var results []byte
	cores := make([]string, 0, 300)
	for range 300 {
		raw, err := json.Marshal(m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}))
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, raw...)
		cores = append(cores, optoutHash(coreState(t, m)))
	}
	s, err := lab.NewSimulator(0, MachineConfig{Seed: SeedInt64(2026)})
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := s.Sim(0, 20000, false)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"spins": optoutHash(results), "spin_cores": optoutHash([]byte(mustJSONString(t, cores))),
		"sim": st, "sim_core": optoutHash(coreState(t, s.mBuf[0])),
	}
}

func TestOptoutBaseline(t *testing.T) {
	source := optoutGachaFS(t)
	got := map[string]any{
		"fs":  optoutRecord(t, WithOptimalFS(source)),
		"dir": optoutRecord(t, WithOptimalDir(materializeTestFS(t, source))),
	}
	raw := []byte(mustJSONString(t, got))
	if os.Getenv("PROBLAB_OPTOUT_BASELINE") == "update" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(optoutBaselinePath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(optoutBaselinePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(want) {
		t.Fatalf("optimal baseline differs:\n got %s\nwant %s", raw, want)
	}
}
