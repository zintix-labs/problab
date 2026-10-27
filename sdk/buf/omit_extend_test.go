// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package buf

import "testing"

type countingExtend struct{ calls int }

func (e *countingExtend) Reset() {}

func (e *countingExtend) Snapshot() any {
	e.calls++
	return &countingExtend{calls: e.calls}
}

// U3（techspec §6.5）：NewGameModeResult 的 omitExtend 決定 AddAct 是否呼叫
// Snapshot()，且 Reset 不會改變這個政策。
func TestNewGameModeResultOmitExtendPolicy(t *testing.T) {
	gms := testGameModeSetting()
	if err := gms.ScreenSetting.Init(); err != nil {
		t.Fatal(err)
	}
	if err := gms.HitSetting.Init(); err != nil {
		t.Fatal(err)
	}
	for _, omit := range []bool{false, true} {
		gmr := NewGameModeResult(0, gms, 4, 4, omit)
		ext := &countingExtend{}
		for round := range 3 {
			gmr.AddAct(FinishAct, "screen", nil, ext)
			gmr.AddAct(FinishRound, "win", nil, nil)
			if round == 1 {
				gmr.Reset()
			}
		}
		wantCalls := 3
		if omit {
			wantCalls = 0
		}
		if ext.calls != wantCalls {
			t.Fatalf("omitExtend=%v: Snapshot calls=%d, want %d", omit, ext.calls, wantCalls)
		}
		for _, act := range gmr.ActResults {
			if act.ActType == "screen" && (act.ExtendResult == nil) != omit {
				t.Fatalf("omitExtend=%v: ExtendResult=%v", omit, act.ExtendResult)
			}
			if act.ActType == "win" && act.ExtendResult != nil {
				t.Fatal("act without ext produced an ExtendResult")
			}
		}
	}
}
