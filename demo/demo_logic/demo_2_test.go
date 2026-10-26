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

package demo_logic_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

// demo_choice（game 2）：BG 觸發後二選一的分段遊戲。

const demoChoiceID spec.GID = 2

func demoChoiceMachine(t *testing.T, seed int64) *problab.Machine {
	t.Helper()
	lab, err := problab.NewAuto(core.Default(), problab.Configs(demo_configs.FS), problab.Logics(demo_logic.Logics))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	m, err := lab.NewMachine(demoChoiceID, problab.MachineConfig{Seed: problab.SeedInt64(seed)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// startTriggered 以 BetRunner（player）反覆開局，直到第 0 段停在選擇點，回傳該段。
func startTriggered(t *testing.T, r *problab.BetRunner) *buf.SpinResult {
	t.Helper()
	for range 5000 {
		sr, err := r.Bet(problab.SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: true}, problab.ChoosePlayer)
		if err != nil {
			t.Fatal(err)
		}
		if !sr.IsGameEnd {
			return sr
		}
	}
	t.Fatal("the choice game never triggered")
	return nil
}

func screenActs(gmr *buf.GameModeResult) int {
	n := 0
	for _, a := range gmr.ActResults {
		if a.ActType == "screen" {
			n++
		}
	}
	return n
}

// 兩個入口：FG-A 轉數為 8、FG-B 為 4，Scatter 每多一個多一轉；完整局的贏分等於兩段相加。
func TestDemoChoiceEntries(t *testing.T) {
	for choice, base := range []int{8, 4} {
		r, err := problab.NewBetRunner(demoChoiceMachine(t, 11), problab.WithMaxSegments(2))
		if err != nil {
			t.Fatal(err)
		}
		seg0 := startTriggered(t, r)
		if !reflect.DeepEqual(seg0.ChooseNext, []int{demo_logic.EntryFGA, demo_logic.EntryFGB}) {
			t.Fatalf("choose next %v", seg0.ChooseNext)
		}
		raw, err := dto.EncodeCheckpoint(seg0.Logic, seg0.State.Checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		var cp struct{ Scatters int }
		if err := json.Unmarshal(raw, &cp); err != nil || cp.Scatters < 3 {
			t.Fatalf("checkpoint %s: %v", raw, err)
		}
		win0, modes0 := seg0.TotalWin, seg0.GameModeCount
		// 最後一段結束本局，回傳的是完整局：第 1 段的 GMR 接在第 0 段之後。
		res, err := r.Next(choice)
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsGameEnd || res.Cycle != 1 || len(res.ChooseNext) != 0 || res.GameModeCount != modes0+1 {
			t.Fatalf("game: end %v cycle %d next %v modes %d", res.IsGameEnd, res.Cycle, res.ChooseNext, res.GameModeCount)
		}
		fg := res.GameModeList[modes0]
		if got, want := screenActs(fg), base+cp.Scatters-3; got != want {
			t.Fatalf("choice %d: %d free rounds, want %d", choice, got, want)
		}
		if fg.GameModeId != choice+1 {
			t.Fatalf("choice %d entered mode %d", choice, fg.GameModeId)
		}
		if res.TotalWin != win0+fg.TotalWin {
			t.Fatalf("game win %d, want %d + %d", res.TotalWin, win0, fg.TotalWin)
		}
	}
}

// 遊戲以 nil 拒絕不接受的入口與 checkpoint。
func TestDemoChoiceRejects(t *testing.T) {
	m := demoChoiceMachine(t, 11)
	r, err := problab.NewBetRunner(m, problab.WithMaxSegments(2))
	if err != nil {
		t.Fatal(err)
	}
	cp := startTriggered(t, r).State.Checkpoint
	for name, in := range map[string]problab.SpinInput{
		"unknown entry":    {Cycle: 1, ChooseNext: []int{9}, Checkpoint: cp},
		"wrong checkpoint": {Cycle: 1, ChooseNext: []int{demo_logic.EntryFGA}, Checkpoint: "x"},
		"no checkpoint":    {Cycle: 1, ChooseNext: []int{demo_logic.EntryFGA}},
	} {
		in.BetMode, in.BetMult = 0, 1
		if m.SpinUnsafe(in) != nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
