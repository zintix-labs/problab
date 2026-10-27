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
	"bytes"
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
	"github.com/zintix-labs/problab/stats"
)

// 暫時基準：改用 Welford 之前與之後，各模擬入口的報表中 Std、Cv、RtpCI 以外的欄位逐位元組
// 相同；這三個欄位在相對誤差 1e-8 內相同（舊算法的誤差約 1e-9）。平方和欄位與新增的 CV
// 欄位不比對。驗收後刪除。

const welfordBaselinePath = "testdata/welford_baseline.json"

type welfordCase struct {
	Rest  json.RawMessage `json:"rest"` // 報表去掉 Std、Cv、RtpCI、平方和與新 CV 欄位
	Std   float64         `json:"std"`
	Cv    float64         `json:"cv"`
	CILo  float64         `json:"ci_lo"`
	CIHi  float64         `json:"ci_hi"`
	Extra json.RawMessage `json:"extra,omitempty"` // SimPlayers 的 EstimatorPlayers、DevSim 的快照
}

func welfordSplit(t *testing.T, st *stats.StatReport, extra any) welfordCase {
	t.Helper()
	st.Done()
	c := welfordCase{Std: st.Summary.Std, Cv: st.Summary.Cv, CILo: st.Summary.RtpCI.Lo, CIHi: st.Summary.RtpCI.Hi}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"Std", "Cv", "RtpCI", "BaseCv", "FreeCv"} {
		delete(m["Summary"], k)
	}
	for _, k := range []string{"TotalWinMultSqSum", "BaseWinMultSqSum", "FreeWinMultSqSum"} {
		delete(m["Mult"], k)
	}
	if c.Rest, err = json.Marshal(m); err != nil {
		t.Fatal(err)
	}
	if extra != nil {
		if c.Extra, err = json.Marshal(extra); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func welfordLab(t *testing.T) *Problab {
	t.Helper()
	lab, err := NewAuto(core.Default(), Configs(demo_configs.FS), Logics(demo_logic.Logics))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

func welfordSimulator(t *testing.T, lab *Problab, gid int, choose ChooseMode) *Simulator {
	t.Helper()
	s, err := lab.NewSimulator(spec.GID(gid), MachineConfig{Seed: SeedInt64(2026)})
	if err != nil {
		t.Fatal(err)
	}
	if choose != ChoosePlayer {
		if err := s.SetCfg(BetConfig{Choose: choose}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func welfordRecord(t *testing.T) map[string]welfordCase {
	t.Helper()
	lab := welfordLab(t)
	got := map[string]welfordCase{}
	for name, c := range map[string]struct {
		gid    int
		choose ChooseMode
		rounds int
	}{
		"sim-demo0":       {0, ChoosePlayer, 30000},
		"sim-demo1":       {1, ChoosePlayer, 30000},
		"sim-demo2-first": {2, ChooseFirst, 30000},
		"sim-demo2-last":  {2, ChooseLast, 30000},
		"sim-demo3-first": {3, ChooseFirst, 60000},
		"sim-demo3-last":  {3, ChooseLast, 60000},
	} {
		st, _, err := welfordSimulator(t, lab, c.gid, c.choose).Sim(0, c.rounds, false)
		if err != nil {
			t.Fatal(err)
		}
		got[name] = welfordSplit(t, st, nil)
	}
	for name, c := range map[string]struct {
		gid    int
		choose ChooseMode
	}{"simmp-demo0": {0, ChoosePlayer}, "simmp-demo3-first": {3, ChooseFirst}} {
		st, _, err := welfordSimulator(t, lab, c.gid, c.choose).SimMP(0, 40000, 2, false)
		if err != nil {
			t.Fatal(err)
		}
		got[name] = welfordSplit(t, st, nil)
	}
	// 多 worker 的 SimPlayers 依排程分配玩家，結果不可重現，不列入數值基準。
	for name, workers := range map[string]int{"simplayers-1w": 1} {
		st, est, err := func() (*stats.StatReport, *stats.EstimatorPlayers, error) {
			st, est, _, err := welfordSimulator(t, lab, 0, ChoosePlayer).SimPlayers(workers, 12, 100, 0, 300, false)
			return st, est, err
		}()
		if err != nil {
			t.Fatal(err)
		}
		got[name] = welfordSplit(t, st, est)
	}
	d, err := lab.NewDevSimulator(0, 2026)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := d.Sim(0, 20000)
	if err != nil {
		t.Fatal(err)
	}
	got["devsim-demo0"] = welfordSplit(t, rep.Stat, map[string]string{"before": rep.Before, "after": rep.After})
	return got
}

// welfordCompact 去掉縮排：基準檔以縮排寫出，RawMessage 也會被縮排。
func welfordCompact(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	if len(raw) == 0 {
		return ""
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func welfordClose(a, b float64) bool {
	if a == b {
		return true
	}
	return math.Abs(a-b) <= 1e-8*math.Max(math.Abs(a), math.Abs(b))
}

func TestWelfordBaseline(t *testing.T) {
	got := welfordRecord(t)
	if os.Getenv("PROBLAB_WELFORD_BASELINE") == "update" {
		raw, err := json.MarshalIndent(got, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(welfordBaselinePath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(welfordBaselinePath)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]welfordCase
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("cases %d, want %d", len(got), len(want))
	}
	for name, w := range want {
		g := got[name]
		if welfordCompact(t, g.Rest) != welfordCompact(t, w.Rest) || welfordCompact(t, g.Extra) != welfordCompact(t, w.Extra) {
			t.Errorf("%s: report fields other than std changed", name)
		}
		if !welfordClose(g.Std, w.Std) || !welfordClose(g.Cv, w.Cv) || !welfordClose(g.CILo, w.CILo) || !welfordClose(g.CIHi, w.CIHi) {
			t.Errorf("%s: std %v cv %v ci [%v %v], want %v %v [%v %v]", name, g.Std, g.Cv, g.CILo, g.CIHi, w.Std, w.Cv, w.CILo, w.CIHi)
		}
	}
}
