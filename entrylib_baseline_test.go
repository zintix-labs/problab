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

// 暫時基準 B1：以 gachas 設定開啟 optimal 的 demo_0（逐局結果、RNG 位置與一次模擬），以及
// optimal 關閉時 demo_2 以 first／last 執行的完整局。夾具與設定都在本檔自建，不依賴之後
// 會改寫的測試輔助。驗收後刪除。

const entrylibBaselinePath = "testdata/entrylib_baseline.json"

var entrylibSettingBlock = regexp.MustCompile(`(?s)optimal_setting:.*?\n\n`)

func entrylibGachaFS(t *testing.T) fstest.MapFS {
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

func entrylibConfigFS(t *testing.T) fs.FS {
	t.Helper()
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	block := "optimal_setting:\n  use_optimal: true\n  gachas: [opt/gacha.json.zst]\n  seed_bank: [opt/seed_bank.bin]\n\n"
	if !entrylibSettingBlock.Match(raw) {
		t.Fatal("demo config has no optimal_setting block")
	}
	updated := entrylibSettingBlock.ReplaceAllLiteral(raw, []byte(block))
	return fstest.MapFS{"game.yaml": &fstest.MapFile{Data: updated}}
}

func entrylibHash(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:12])
}

func entrylibOptimal(t *testing.T) map[string]any {
	t.Helper()
	lab, err := NewAuto(core.Default(), Configs(entrylibConfigFS(t)), Logics(demo_logic.Logics), WithOptimalFS(entrylibGachaFS(t)))
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
		cores = append(cores, entrylibHash(coreState(t, m)))
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
		"spins": entrylibHash(results), "spin_cores": entrylibHash([]byte(mustJSONString(t, cores))),
		"sim": st, "sim_core": entrylibHash(coreState(t, s.mBuf[0])),
	}
}

func entrylibChoice(t *testing.T, mode ChooseMode) map[string]any {
	t.Helper()
	lab, err := NewAuto(core.Default(), Configs(demo_configs.FS), Logics(demo_logic.Logics))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	m, err := lab.NewMachine(2, MachineConfig{Seed: SeedInt64(2026)})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewBetRunner(m)
	if err != nil {
		t.Fatal(err)
	}
	var results []byte
	cores := make([]string, 0, 2000)
	for range 2000 {
		res, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1}, mode)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, raw...)
		cores = append(cores, entrylibHash(coreState(t, m)))
	}
	return map[string]any{"games": entrylibHash(results), "cores": entrylibHash([]byte(mustJSONString(t, cores)))}
}

func TestEntrylibBaseline(t *testing.T) {
	got := map[string]any{
		"demo0_optimal": entrylibOptimal(t),
		"demo2_first":   entrylibChoice(t, ChooseFirst),
		"demo2_last":    entrylibChoice(t, ChooseLast),
	}
	raw := []byte(mustJSONString(t, got))
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
