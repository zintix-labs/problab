// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab_test

// §6.6 遷移相容檢查（techspec）：只使用公開 API，重現既有機台套用唯一一項機械式
// 遷移（gh.IsSim → gh.OmitExtend()）後的寫法，證明它不需其他修改即可編譯，且結果
// 與 demo_0 完全相同。demo_0 本身與 B0 的一致性由 TestConstructionBaseline 證明。

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// 以下是既有遊戲常見寫法的縮影（與 demo_0 邏輯相同），只把 build 時讀取的
// gh.IsSim 換成 gh.OmitExtend()；ext 內原本的 isSim 判斷保留不動。

type compatExt struct {
	Triggered     bool  `json:"is_trigger"`
	ScatterCount  int   `json:"scatters,omitzero"`
	ScatterHitMap []int `json:"scatter_hits,omitzero"`
	isSim         bool
}

func (e *compatExt) Reset() {
	e.Triggered = false
	e.ScatterCount = 0
	e.ScatterHitMap = e.ScatterHitMap[:0]
}

func (e *compatExt) Snapshot() any {
	if e.isSim {
		return nil
	}
	hits := make([]int, len(e.ScatterHitMap))
	copy(hits, e.ScatterHitMap)
	return &compatExt{Triggered: e.Triggered, ScatterCount: e.ScatterCount, ScatterHitMap: hits}
}

type compatFixed struct {
	FreeRound int    `yaml:"free_round"`
	DemoB     []int  `yaml:"demo_b"`
	DemoC     string `yaml:"demo_c"`
}

type compatGame struct {
	fixed       compatFixed
	symbolTypes []spec.SymbolType
	ext         *compatExt
}

func buildCompatGame(gh *slot.Game) (slot.GameLogic, error) {
	g := &compatGame{}
	if err := spec.DecodeFixed(gh.GameSetting, &g.fixed); err != nil {
		return nil, err
	}
	g.symbolTypes = gh.GameSetting.GameModeSettings[0].SymbolSetting.SymbolTypes
	size := gh.GameSetting.GameModeSettings[0].ScreenSetting.ScreenSize
	// 唯一的遷移：原本是 gh.IsSim。
	g.ext = &compatExt{ScatterHitMap: make([]int, 0, size), isSim: gh.OmitExtend()}
	return g, nil
}

func (g *compatGame) GetResult(r *buf.SpinRequest, gh *slot.Game) *buf.SpinResult {
	sr := gh.StartNewSpin(r)
	base := g.base(r.BetMult, gh)
	sr.AppendModeResult(base)
	if base.Trigger != 0 {
		sr.AppendModeResult(g.free(r.BetMult, gh))
	}
	sr.End()
	return sr
}

func (g *compatGame) base(betMult int, gh *slot.Game) *buf.GameModeResult {
	mode := gh.GameModeHandlerList[0]
	gmr := mode.GameModeResult
	g.ext.Reset()
	screen := mode.ScreenGenerator.GenScreen()
	gmr.AddAct(buf.FinishAct, "screen", screen, nil)
	mode.ScreenCalculator.CalcScreen(betMult, screen, gmr)
	if gmr.GetTmpWin() > 0 {
		gmr.AddAct(buf.FinishAct, "win", nil, nil)
	}
	g.ext.Reset()
	for i, s := range screen {
		if g.symbolTypes[s] == spec.SymbolTypeScatter {
			g.ext.ScatterCount++
			g.ext.ScatterHitMap = append(g.ext.ScatterHitMap, i)
		}
	}
	if g.ext.ScatterCount > 2 {
		g.ext.Triggered = true
		gmr.Trigger = 1
		gmr.AddAct(buf.FinishAct, "trigger", nil, g.ext)
	}
	gmr.FinishRound()
	return mode.YieldResult()
}

func (g *compatGame) free(betMult int, gh *slot.Game) *buf.GameModeResult {
	mode := gh.GameModeHandlerList[1]
	gmr := mode.GameModeResult
	g.ext.Reset()
	for range g.fixed.FreeRound {
		screen := mode.ScreenGenerator.GenScreen()
		gmr.AddAct(buf.FinishAct, "screen", screen, nil)
		mode.ScreenCalculator.CalcScreen(betMult, screen, gmr)
		if gmr.GetTmpWin() > 0 {
			gmr.AddAct(buf.FinishAct, "win", nil, nil)
		}
		gmr.FinishRound()
	}
	return mode.YieldResult()
}

func compatLab(t *testing.T) *problab.Problab {
	t.Helper()
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	compat := string(raw)
	for _, pair := range [][2]string{{"game_id: 0", "game_id: 950"}, {"game_name: demo_normal", "game_name: compat_demo"}, {"logic_key: demo_normal", "logic_key: compat_demo"}} {
		next := strings.Replace(compat, pair[0], pair[1], 1)
		if next == compat {
			t.Fatalf("replace %q failed", pair[0])
		}
		compat = next
	}
	reg := slot.NewLogicRegistry()
	if err := slot.GameRegister("compat_demo", buildCompatGame, reg); err != nil {
		t.Fatal(err)
	}
	if err := dto.RegisterExtendRender[compatExt]("compat_demo"); err != nil {
		t.Fatal(err)
	}
	cfgs := fstest.MapFS{
		"demo.yaml":   &fstest.MapFile{Data: raw},
		"compat.yaml": &fstest.MapFile{Data: []byte(compat)},
	}
	lab, err := problab.NewAuto(core.Default(), problab.Configs(cfgs), problab.Logics(demo_logic.Logics, reg))
	if err != nil {
		t.Fatal(err)
	}
	return lab
}

// normalize 去掉兩個遊戲只因身分不同而不同的欄位（遊戲名稱與編號），其餘逐位元比對。
func normalize(t *testing.T, r dto.SpinResult) []byte {
	t.Helper()
	r.GameName, r.GameID = "", 0
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMigratedGamePatternMatchesDemo(t *testing.T) {
	lab := compatLab(t)
	defer func() { _ = lab.Close() }()

	// 原本的 NewMachine(id, true)／NewMachineWithSeed(...) 改為新入口。
	demo, err := lab.NewMachine(0, problab.MachineConfig{Seed: problab.SeedInt64(42)})
	if err != nil {
		t.Fatal(err)
	}
	compat, err := lab.NewMachine(950, problab.MachineConfig{Seed: problab.SeedInt64(42)})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 500 {
		want, err := demo.Spin(&dto.SpinRequest{UID: "c", GameName: "demo_normal", GameId: 0, Bet: 40, BetMode: 0, BetMult: 1})
		if err != nil {
			t.Fatal(err)
		}
		got, err := compat.Spin(&dto.SpinRequest{UID: "c", GameName: "compat_demo", GameId: 950, Bet: 40, BetMode: 0, BetMult: 1})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(normalize(t, got), normalize(t, want)) {
			t.Fatalf("spin %d: migrated pattern differs from demo_0", i)
		}
	}

	// 原本的 NewSimulatorWithSeed(id, seed) 改為新入口；統計結果應與 demo_0 相同。
	ds, err := lab.NewSimulator(0, problab.MachineConfig{Seed: problab.SeedInt64(7)})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := lab.NewSimulator(950, problab.MachineConfig{Seed: problab.SeedInt64(7)})
	if err != nil {
		t.Fatal(err)
	}
	dr, _, err := ds.Sim(0, 5000, false)
	if err != nil {
		t.Fatal(err)
	}
	cr, _, err := cs.Sim(0, 5000, false)
	if err != nil {
		t.Fatal(err)
	}
	if dr.Summary.TotalWin != cr.Summary.TotalWin || dr.Summary.Trigger != cr.Summary.Trigger || dr.Summary.Rounds != cr.Summary.Rounds {
		t.Fatal("migrated pattern changed Simulator statistics")
	}
}

// 遊戲單元測試直接呼叫 slot.NewGame（位置參數 bool 的型別與極性不變），並以帶欄位名的
// literal 建立 buf.SpinRequest 後直接呼叫 GetResult。
func TestDirectSlotNewGameStillCompiles(t *testing.T) {
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	gs, err := spec.GetGameSettingByYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, isSim := range []bool{true, false} {
		var outputs [2][]byte
		for k := range outputs {
			prng, err := core.Default().New(core.EncodeInt64Seed(11))
			if err != nil {
				t.Fatal(err)
			}
			gh, err := slot.NewGame(gs, demo_logic.Logics, core.New(prng), isSim)
			if err != nil {
				t.Fatal(err)
			}
			if gh.OmitExtend() != isSim {
				t.Fatal("slot.NewGame bool polarity changed")
			}
			req := &buf.SpinRequest{BetMode: 0, BetMult: 1, Bet: 40}
			out, err := dto.NewSpinResultDTO(gh.GetResult(req))
			if err != nil {
				t.Fatal(err)
			}
			outputs[k], _ = json.Marshal(out)
		}
		if !bytes.Equal(outputs[0], outputs[1]) {
			t.Fatal("direct slot.NewGame usage is not deterministic")
		}
	}
}
