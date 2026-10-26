// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package buf

import (
	"errors"
	"reflect"
	"testing"
)

// testMode 建立一個手工 GMR：acts 個 Act、screens 格盤面、details 筆有效細項，
// 另外在 Details 尾端留下 stale 筆殘留，模擬 Reset 不截斷 Details。
func testMode(win, acts, screens, details, stale int) *GameModeResult {
	g := &GameModeResult{TotalWin: win, GameModeId: win % 3, IsModeEnd: true, Trigger: win % 2, ScreenSize: 5, TmpAct: &TmpAct{}}
	for i := range acts {
		g.ActResults = append(g.ActResults, ActResult{ActType: "act", ActId: i, ActWin: win, NowTotalWin: win * (i + 1), ExtendResult: &countingExtend{calls: i}})
	}
	for i := range screens {
		g.Screens = append(g.Screens, int16(win+i))
	}
	for i := range details + stale {
		g.Details = append(g.Details, CalcScreenDetail{Win: win*100 + i, HitsFlatStart: len(g.HitsFlat), HitsFlatLen: 1})
		g.HitsFlat = append(g.HitsFlat, int16(i))
	}
	g.HitsFlat = g.HitsFlat[:details]
	g.TmpAct.CurrDetail = details
	g.TmpAct.Acctotalwin = win * acts
	return g
}

// testSegment 建立一段結果；贏分與 GMR 數由 modes 計算。
func testSegment(cycle int, end bool, mode, mult int, modes ...*GameModeResult) *SpinResult {
	sr := &SpinResult{
		GameName: "g", GameID: 7, Logic: "l", BetUnits: []int{10, 20},
		Bet: []int{10, 20}[mode] * mult, BetMode: mode, BetMult: mult,
		Cycle: cycle, IsGameEnd: end, State: &SpinState{},
	}
	for _, g := range modes {
		sr.TotalWin += g.TotalWin
		sr.GameModeCount++
		sr.GameModeList = append(sr.GameModeList, g)
	}
	return sr
}

func mustAdd(t *testing.T, a *SpinAssembler, sr *SpinResult) *SpinResult {
	t.Helper()
	res, err := a.Add(sr)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return res
}

// cloneMode 深複製一個 GMR，作為之後比對的預期值。
func cloneMode(g *GameModeResult) GameModeResult {
	c := *g
	c.ActResults = append([]ActResult(nil), g.ActResults...)
	c.Screens = append([]int16(nil), g.Screens...)
	c.Details = append([]CalcScreenDetail(nil), g.GetDetails()...)
	c.HitsFlat = append([]int16(nil), g.HitsFlat...)
	tmp := *g.TmpAct
	c.TmpAct = &tmp
	return c
}

func TestSpinAssemblerPassThrough(t *testing.T) {
	a := NewSpinAssembler()
	sr := testSegment(0, true, 1, 2, testMode(30, 2, 5, 1, 0))
	before := *sr
	if got := mustAdd(t, a, sr); got != sr {
		t.Fatal("a finished first segment must be returned as is")
	}
	if !reflect.DeepEqual(before, *sr) {
		t.Fatal("pass-through modified the input")
	}
	// 放行之後仍可開始新的一局。
	if got := mustAdd(t, a, testSegment(0, false, 0, 1, testMode(1, 1, 1, 0, 0))); got != nil {
		t.Fatal("an unfinished first segment must not be returned")
	}
	if got := mustAdd(t, a, testSegment(1, true, 0, 1, testMode(2, 1, 1, 0, 0))); got == nil || got.TotalWin != 3 {
		t.Fatalf("game after pass-through: %+v", got)
	}
}

func TestSpinAssemblerRules(t *testing.T) {
	a := NewSpinAssembler()
	s0 := testSegment(0, false, 1, 3, testMode(10, 2, 5, 1, 0), testMode(20, 3, 5, 2, 0))
	s1 := testSegment(1, false, 1, 3, testMode(30, 1, 5, 0, 0))
	s2 := testSegment(2, true, 1, 3, testMode(40, 4, 5, 3, 0))
	s1.GameName, s1.Bet = "other", 999 // 續段的頂層欄位不取用
	if mustAdd(t, a, s0) != nil || mustAdd(t, a, s1) != nil {
		t.Fatal("unfinished segments must return nil")
	}
	res := mustAdd(t, a, s2)
	if res == nil || res == s2 {
		t.Fatal("a multi-segment game must return the assembler's buffer")
	}
	if res.GameName != "g" || res.GameID != 7 || res.Logic != "l" || res.Bet != 60 || res.BetMode != 1 || res.BetMult != 3 {
		t.Fatalf("header must come from cycle 0: %+v", res)
	}
	if &res.BetUnits[0] != &s0.BetUnits[0] {
		t.Fatal("BetUnits is read-only settings and is shared")
	}
	if res.Cycle != 2 || !res.IsGameEnd {
		t.Fatalf("Cycle and IsGameEnd must come from the last segment: %d %v", res.Cycle, res.IsGameEnd)
	}
	if res.TotalWin != 100 || res.GameModeCount != 4 || len(res.GameModeList) != 4 {
		t.Fatalf("wins and mode counts must add up: %d %d %d", res.TotalWin, res.GameModeCount, len(res.GameModeList))
	}
	for i, want := range []int{10, 20, 30, 40} {
		if res.GameModeList[i].TotalWin != want {
			t.Fatalf("mode %d win %d, want %d (order)", i, res.GameModeList[i].TotalWin, want)
		}
	}
}

func TestSpinAssemblerSnapshots(t *testing.T) {
	cases := []struct {
		name string
		snap []byte
	}{{"nil", nil}, {"empty", []byte{}}, {"bytes", []byte{1, 2, 3}}}
	a := NewSpinAssembler()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, after := c.snap, c.snap
			if c.snap != nil {
				start, after = append([]byte{}, c.snap...), append([]byte{}, c.snap...)
				if len(after) > 0 {
					after[0] = 9 // 起始與結束快照的內容不同，才能分辨取自哪一段
				}
			}
			s0 := testSegment(0, false, 0, 1, testMode(1, 1, 1, 0, 0))
			s0.State.StartCoreSnap, s0.State.AfterCoreSnap = start, []byte{7, 7}
			s1 := testSegment(1, true, 0, 1, testMode(2, 1, 1, 0, 0))
			s1.State.StartCoreSnap, s1.State.AfterCoreSnap = []byte{8, 8}, after
			s1.State.Checkpoint = c.name
			mustAdd(t, a, s0)
			res := mustAdd(t, a, s1)
			check := func(what string, got, want []byte) {
				t.Helper()
				if (got == nil) != (want == nil) || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s: got %#v, want %#v", what, got, want)
				}
				if len(want) > 0 && &got[0] == &want[0] {
					t.Fatalf("%s must be copied, not shared", what)
				}
			}
			check("start", res.State.StartCoreSnap, start)
			check("after", res.State.AfterCoreSnap, after)
			if res.State.Checkpoint != c.name {
				t.Fatal("Checkpoint must come from the last segment")
			}
			if len(start) > 0 {
				start[0], after[0] = 99, 99
				if res.State.StartCoreSnap[0] == 99 || res.State.AfterCoreSnap[0] == 99 {
					t.Fatal("modifying the source snapshot changed the result")
				}
			}
		})
	}
	// 沒有 State 的手工結果：快照與 Checkpoint 都是 nil。
	s0 := testSegment(0, false, 0, 1, testMode(1, 1, 1, 0, 0))
	s1 := testSegment(1, true, 0, 1, testMode(2, 1, 1, 0, 0))
	s0.State, s1.State = nil, nil
	mustAdd(t, a, s0)
	res := mustAdd(t, a, s1)
	if res.State == nil || res.State.StartCoreSnap != nil || res.State.AfterCoreSnap != nil || res.State.Checkpoint != nil {
		t.Fatalf("results without State: %+v", res.State)
	}
}

func TestSpinAssemblerModeIndependence(t *testing.T) {
	a := NewSpinAssembler()
	m0, m1 := testMode(10, 3, 6, 2, 4), testMode(20, 2, 4, 1, 0)
	want := []GameModeResult{cloneMode(m0), cloneMode(m1)}
	mustAdd(t, a, testSegment(0, false, 0, 1, m0))
	res := mustAdd(t, a, testSegment(1, true, 0, 1, m1))
	for i, src := range []*GameModeResult{m0, m1} {
		if res.GameModeList[i] == src || res.GameModeList[i].TmpAct == src.TmpAct {
			t.Fatalf("mode %d shares the source GMR or TmpAct", i)
		}
	}
	// 修改並重置來源，模擬 Machine 下一次執行覆寫自己的緩衝。
	for _, src := range []*GameModeResult{m0, m1} {
		for i := range src.ActResults {
			src.ActResults[i].ActWin = -1
		}
		for i := range src.Screens {
			src.Screens[i] = -1
		}
		for i := range src.Details {
			src.Details[i].Win = -1
		}
		for i := range src.HitsFlat {
			src.HitsFlat[i] = -1
		}
		src.TmpAct.CurrDetail = 0
		src.Reset()
	}
	for i := range want {
		if !reflect.DeepEqual(cloneMode(res.GameModeList[i]), want[i]) {
			t.Fatalf("mode %d changed after the source was modified\n got=%+v\nwant=%+v", i, *res.GameModeList[i], want[i])
		}
	}
}

func TestSpinAssemblerDetailsAndTmpAct(t *testing.T) {
	a := NewSpinAssembler()
	stale := testMode(10, 1, 1, 2, 5) // 2 筆有效、5 筆殘留
	manual := &GameModeResult{TotalWin: 5, Details: []CalcScreenDetail{{Win: 1}, {Win: 2}, {Win: 3}}}
	mustAdd(t, a, testSegment(0, false, 0, 1, stale))
	res := mustAdd(t, a, testSegment(1, true, 0, 1, manual))
	if got := res.GameModeList[0].Details; len(got) != 2 || got[1].Win != 1001 {
		t.Fatalf("only the valid Details must be copied: %+v", got)
	}
	m := res.GameModeList[1]
	if m.TmpAct == nil || m.TmpAct.CurrDetail != 3 || len(m.GetDetails()) != 3 {
		t.Fatalf("a GMR without TmpAct gets one covering all Details: %+v", m.TmpAct)
	}
}

func TestSpinAssemblerSharesExtend(t *testing.T) {
	a := NewSpinAssembler()
	m := testMode(10, 2, 1, 0, 0)
	mustAdd(t, a, testSegment(0, false, 0, 1, m))
	res := mustAdd(t, a, testSegment(1, true, 0, 1, testMode(1, 1, 1, 0, 0)))
	if res.GameModeList[0].ActResults[1].ExtendResult != m.ActResults[1].ExtendResult {
		t.Fatal("ExtendResult must be shared by reference")
	}
}

func TestSpinAssemblerReconciliation(t *testing.T) {
	seg := func(cycle int, end bool, mode, mult int) *SpinResult {
		return testSegment(cycle, end, mode, mult, testMode(cycle+1, 1, 1, 0, 0))
	}
	cases := []struct {
		name  string
		setup []*SpinResult
		bad   *SpinResult
		err   error
		text  string
	}{
		{"idle-nonzero", nil, seg(1, true, 0, 1), ErrCycleOutOfOrder, "spin assembler: cycle out of order: got cycle 1, want 0"},
		{"skip", []*SpinResult{seg(0, false, 0, 1)}, seg(2, true, 0, 1), ErrCycleOutOfOrder, "spin assembler: cycle out of order: got cycle 2, want 1"},
		{"restart", []*SpinResult{seg(0, false, 0, 1)}, seg(0, true, 0, 1), ErrCycleOutOfOrder, "spin assembler: cycle out of order: got cycle 0, want 1"},
		{"mode", []*SpinResult{seg(0, false, 0, 1)}, seg(1, true, 1, 1), ErrBetChanged, "spin assembler: bet changed within game: got mode 1 mult 1, want mode 0 mult 1"},
		{"mult", []*SpinResult{seg(0, false, 0, 1)}, seg(1, true, 0, 2), ErrBetChanged, "spin assembler: bet changed within game: got mode 0 mult 2, want mode 0 mult 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := NewSpinAssembler()
			for _, s := range c.setup {
				mustAdd(t, a, s)
			}
			before := *a
			beforeRes := a.res
			res, err := a.Add(c.bad)
			if res != nil || !errors.Is(err, c.err) || err.Error() != c.text {
				t.Fatalf("got %v, %v; want nil, %q", res, err, c.text)
			}
			if a.inGame != before.inGame || a.next != before.next || a.used != before.used || !reflect.DeepEqual(a.res, beforeRes) {
				t.Fatal("a failed Add modified the assembler")
			}
			// 失敗之後補送正確的段，可以完成同一局。
			next := seg(0, true, 0, 1)
			if len(c.setup) > 0 {
				next = seg(1, true, 0, 1)
			}
			if got := mustAdd(t, a, next); got == nil || got.TotalWin != next.TotalWin+len(c.setup) {
				t.Fatalf("resending the right segment: %+v", got)
			}
		})
	}
	// 連續且一致的續段：接上並在結束時交出。
	a := NewSpinAssembler()
	for c := range 3 {
		if got := mustAdd(t, a, seg(c, false, 1, 2)); got != nil {
			t.Fatalf("cycle %d returned a result before the end", c)
		}
	}
	if got := mustAdd(t, a, seg(3, true, 1, 2)); got == nil || got.TotalWin != 1+2+3+4 {
		t.Fatalf("four segments: %+v", got)
	}
}

func TestSpinAssemblerResetAndReuse(t *testing.T) {
	a := NewSpinAssembler()
	mustAdd(t, a, testSegment(0, false, 0, 1, testMode(1, 1, 1, 0, 0), testMode(2, 1, 1, 0, 0)))
	a.Reset()
	if res, err := a.Add(testSegment(0, false, 0, 1, testMode(3, 1, 1, 0, 0))); res != nil || err != nil {
		t.Fatalf("after Reset a new game starts at cycle 0: %v %v", res, err)
	}
	s1 := testSegment(1, true, 0, 1, testMode(4, 1, 1, 0, 0), testMode(5, 1, 1, 0, 0))
	s1.State.AfterCoreSnap = []byte{1, 2, 3}
	res := mustAdd(t, a, s1)
	if res.TotalWin != 12 || len(res.GameModeList) != 3 {
		t.Fatalf("game after Reset: win %d modes %d", res.TotalWin, len(res.GameModeList))
	}
	pool := len(a.modes)
	// 第二局的 GMR 較少，而且沒有快照：清單長度正確，不殘留上一局的快照。
	mustAdd(t, a, testSegment(0, false, 0, 1, testMode(6, 1, 1, 0, 0)))
	res = mustAdd(t, a, testSegment(1, true, 0, 1, testMode(7, 1, 1, 0, 0)))
	if len(res.GameModeList) != 2 || res.GameModeCount != 2 || res.TotalWin != 13 {
		t.Fatalf("reused buffer: modes %d count %d win %d", len(res.GameModeList), res.GameModeCount, res.TotalWin)
	}
	if res.State.AfterCoreSnap != nil || res.State.StartCoreSnap != nil {
		t.Fatal("snapshots leaked from the previous game")
	}
	if len(a.modes) != pool {
		t.Fatal("the GMR pool must be reused")
	}
	// 先組有位元組的快照、再組空 slice：結果是空 slice，不殘留舊位元組。
	s0 := testSegment(0, false, 0, 1, testMode(1, 1, 1, 0, 0))
	s0.State.StartCoreSnap = []byte{}
	mustAdd(t, a, s0)
	s1 = testSegment(1, true, 0, 1, testMode(1, 1, 1, 0, 0))
	s1.State.AfterCoreSnap = []byte{}
	res = mustAdd(t, a, s1)
	if res.State.StartCoreSnap == nil || len(res.State.StartCoreSnap) != 0 || res.State.AfterCoreSnap == nil || len(res.State.AfterCoreSnap) != 0 {
		t.Fatalf("empty snapshots after non-empty ones: %#v %#v", res.State.StartCoreSnap, res.State.AfterCoreSnap)
	}
}

func TestSpinAssemblerAllocations(t *testing.T) {
	a := NewSpinAssembler()
	single := testSegment(0, true, 0, 1, testMode(1, 2, 5, 1, 0))
	if n := testing.AllocsPerRun(100, func() { _, _ = a.Add(single) }); n != 0 {
		t.Fatalf("pass-through allocates %v times", n)
	}
	s0 := testSegment(0, false, 0, 1, testMode(1, 2, 5, 1, 0))
	s1 := testSegment(1, true, 0, 1, testMode(2, 14, 150, 7, 0))
	s0.State.StartCoreSnap, s1.State.AfterCoreSnap = []byte{1, 2}, []byte{3, 4}
	assemble := func() { _, _ = a.Add(s0); _, _ = a.Add(s1) }
	assemble() // 先讓緩衝長到所需容量
	if n := testing.AllocsPerRun(100, assemble); n != 0 {
		t.Fatalf("assembling with enough capacity allocates %v times", n)
	}
}

func TestSpinResultResetClearsCycle(t *testing.T) {
	sr := NewSpinResult(testGameSetting())
	sr.Cycle = 3
	sr.ChooseNext = append(sr.ChooseNext, 1, 2)
	sr.Reset()
	if sr.Cycle != 0 || len(sr.ChooseNext) != 0 || cap(sr.ChooseNext) == 0 {
		t.Fatalf("Reset left Cycle = %d, ChooseNext = %v (cap %d)", sr.Cycle, sr.ChooseNext, cap(sr.ChooseNext))
	}
}

// 組裝的完整局已結束，不帶任何一段的選項。
func TestSpinAssemblerGameHasNoChooseNext(t *testing.T) {
	a := NewSpinAssembler()
	s0 := testSegment(0, false, 0, 1, testMode(1, 2, 15, 1, 0))
	s0.ChooseNext = []int{1, 2}
	mustAdd(t, a, s0)
	res := mustAdd(t, a, testSegment(1, true, 0, 1, testMode(2, 3, 15, 1, 0)))
	if res == nil || len(res.ChooseNext) != 0 {
		t.Fatalf("assembled game ChooseNext = %v", res.ChooseNext)
	}
}

// BenchmarkSpinAssemblerPass 量測單段局放行的成本。
func BenchmarkSpinAssemblerPass(b *testing.B) {
	a := NewSpinAssembler()
	sr := testSegment(0, true, 0, 1, testMode(1, 2, 15, 1, 0))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := a.Add(sr); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSpinAssemblerAssemble 量測兩段組裝一局的成本；規模取自 demo 遊戲進入 FG 的局
// （約 16 個 Act、165 格盤面、8 筆細項）。
func BenchmarkSpinAssemblerAssemble(b *testing.B) {
	a := NewSpinAssembler()
	s0 := testSegment(0, false, 0, 1, testMode(1, 2, 15, 1, 0))
	s1 := testSegment(1, true, 0, 1, testMode(2, 14, 150, 7, 0))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := a.Add(s0); err != nil {
			b.Fatal(err)
		}
		if _, err := a.Add(s1); err != nil {
			b.Fatal(err)
		}
	}
}
