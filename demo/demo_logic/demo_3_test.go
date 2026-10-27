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
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
)

// demo_entry（game 3）：入口互相觸發、續段只取決於入口值與 seed 的分段遊戲。

const demoEntryID = 3

const (
	entryA = demo_logic.Demo0003EntryFGA
	entryB = demo_logic.Demo0003EntryFGB
)

func demoEntryMachine(t *testing.T, seed int64) *problab.Machine {
	t.Helper()
	lab, err := problab.NewAuto(core.Default(), problab.Configs(demo_configs.FS), problab.Logics(demo_logic.Logics))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	m, err := lab.NewMachine(demoEntryID, problab.MachineConfig{Seed: problab.SeedInt64(seed)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func snapshot(t *testing.T, m *problab.Machine) []byte {
	t.Helper()
	snap, err := m.SnapshotCore()
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), snap...)
}

// segmentAt 從 start 快照執行一段續段，回傳結果的 JSON 與執行後的 RNG。
func segmentAt(t *testing.T, m *problab.Machine, start []byte, cycle, choice int, next []int, cp any) ([]byte, []byte) {
	t.Helper()
	if err := m.RestoreCore(start); err != nil {
		t.Fatal(err)
	}
	sr := m.SpinUnsafe(problab.SpinInput{BetMode: 0, BetMult: 1, Cycle: cycle, Choice: choice, ChooseNext: next, Checkpoint: cp})
	if sr == nil {
		t.Fatalf("entry %d was rejected", next[choice])
	}
	sr.Cycle = 0 // 由引擎填入，不屬於遊戲的結果
	raw, err := json.Marshal(sr)
	if err != nil {
		t.Fatal(err)
	}
	return raw, snapshot(t, m)
}

func modeIDs(sr *buf.SpinResult) []int {
	ids := make([]int, len(sr.GameModeList))
	for i, gmr := range sr.GameModeList {
		ids[i] = gmr.GameModeId
	}
	return ids
}

// first／last 都能跑完整局；選項只有初始段的兩個入口，或 FG 再觸發後的單一入口。
func TestDemoEntryFullGames(t *testing.T) {
	for _, mode := range []problab.ChooseMode{problab.ChooseFirst, problab.ChooseLast} {
		r, err := problab.NewBetRunner(demoEntryMachine(t, 7))
		if err != nil {
			t.Fatal(err)
		}
		triggered, retriggered := 0, 0
		for range 20000 {
			res, err := r.Bet(problab.SpinInput{BetMode: 0, BetMult: 1}, mode)
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsGameEnd || len(res.ChooseNext) != 0 {
				t.Fatalf("game end %v next %v", res.IsGameEnd, res.ChooseNext)
			}
			ids := modeIDs(res)
			if len(ids) > 1 {
				triggered++
			}
			if len(ids) > 2 {
				retriggered++
			}
			// FG 依序交替：first 先進 FG-A（mode 1），last 先進 FG-B（mode 2）。
			for i := 1; i < len(ids); i++ {
				want := 1 + (i-1)%2
				if mode == problab.ChooseLast {
					want = 2 - (i-1)%2
				}
				if ids[i] != want {
					t.Fatalf("%v: game modes %v", mode, ids)
				}
			}
		}
		if triggered == 0 || retriggered == 0 {
			t.Fatalf("%v: triggered %d, retriggered %d", mode, triggered, retriggered)
		}
	}
}

// player：第 0 段提供兩個入口；FG-A 再觸發時只提供 FG-B，反之亦然。
func TestDemoEntryChoices(t *testing.T) {
	r, err := problab.NewBetRunner(demoEntryMachine(t, 7))
	if err != nil {
		t.Fatal(err)
	}
	for range 50000 {
		sr, err := r.Bet(problab.SpinInput{BetMode: 0, BetMult: 1}, problab.ChoosePlayer)
		if err != nil {
			t.Fatal(err)
		}
		if sr.IsGameEnd {
			continue
		}
		if !reflect.DeepEqual(sr.ChooseNext, []int{entryA, entryB}) {
			t.Fatalf("first choice %v", sr.ChooseNext)
		}
		for choice := 0; ; choice = 0 {
			entered := sr.ChooseNext[choice]
			if sr, err = r.Next(choice); err != nil {
				t.Fatal(err)
			}
			if sr.IsGameEnd {
				break
			}
			want := []int{entryB}
			if entered == entryB {
				want = []int{entryA}
			}
			if !reflect.DeepEqual(sr.ChooseNext, want) {
				t.Fatalf("after entry %d: choose next %v", entered, sr.ChooseNext)
			}
			return // 看到一次再觸發後的單一選項即可
		}
	}
	t.Fatal("no retrigger in 50000 games")
}

// 不認得的入口回傳 nil，不消耗 RNG。
func TestDemoEntryRejectsUnknownEntries(t *testing.T) {
	m := demoEntryMachine(t, 7)
	for _, entry := range []int{0, 1, 2, 5} {
		before := snapshot(t, m)
		if m.SpinUnsafe(problab.SpinInput{BetMode: 0, BetMult: 1, Cycle: 1, ChooseNext: []int{entry}}) != nil {
			t.Errorf("entry %d was accepted", entry)
		}
		if !bytes.Equal(before, snapshot(t, m)) {
			t.Errorf("entry %d consumed RNG", entry)
		}
	}
}

// 契約：同一個起始快照，續段的結果與執行後的 RNG 只取決於入口值；換 Cycle、只留單一選項、
// 帶 checkpoint 都相同。
func TestDemoEntryContinuationDependsOnlyOnEntry(t *testing.T) {
	m := demoEntryMachine(t, 7)
	for i := range 200 {
		start := snapshot(t, m)
		for choice, entry := range []int{entryA, entryB} {
			base, baseCore := segmentAt(t, m, start, 1, choice, []int{entryA, entryB}, nil)
			for name, in := range map[string]struct {
				cycle, choice int
				next          []int
				cp            any
			}{
				"cycle 2":       {2, choice, []int{entryA, entryB}, nil},
				"cycle 9":       {9, choice, []int{entryA, entryB}, nil},
				"single option": {1, 0, []int{entry}, nil},
				"checkpoint":    {1, choice, []int{entryA, entryB}, "ignored"},
			} {
				got, gotCore := segmentAt(t, m, start, in.cycle, in.choice, in.next, in.cp)
				if !bytes.Equal(base, got) || !bytes.Equal(baseCore, gotCore) {
					t.Fatalf("start %d entry %d: %s differs", i, entry, name)
				}
			}
		}
		if err := m.RestoreCore(start); err != nil {
			t.Fatal(err)
		}
		m.SpinUnsafe(problab.SpinInput{BetMode: 0, BetMult: 1}) // 換下一個起點
	}
}
