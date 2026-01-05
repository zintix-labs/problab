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

package demo_logic

import (
	"log"

	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// ============================================================
// ** 註冊 **
// ============================================================

func init() {
	logic := "demo_normal"
	builder := buildGame0000
	logics := Logics
	if err := slot.GameRegister(spec.LogicKey(logic), builder, logics); err != nil {
		log.Fatalf("%s register failed: %v", logic, err)
	}
	// register Extend
	if err := dto.RegisterExtendRender[ext0000](spec.LogicKey(logic)); err != nil {
		log.Fatalf("%s register failed: %v", logic, err)
	}
	// register Checkpoint
	// if err := dto.RegisterCheckpoint[check0000](spec.LogicKey(logic)); err != nil {
	// 	log.Fatalf("%s register failed: %v", logic, err)
	// }
}

// ============================================================
// ** 遊戲介面 **
// ============================================================

type game0000 struct {
	fixed *fixed0000
	ext   *ext0000
}

func buildGame0000(gh *slot.Game) (slot.GameLogic, error) {
	g := &game0000{
		fixed: new(fixed0000),
		ext:   nil,
	}
	if err := spec.DecodeFixed(gh.GameSetting, g.fixed); err != nil {
		return nil, err
	}
	g.fixed.symboltypes = gh.GameSetting.GameModeSettings[0].SymbolSetting.SymbolTypes
	g.ext = g.newext(gh.GameSetting.GameModeSettings[0].ScreenSetting.ScreenSize, gh.IsSim)
	return g, nil
}

// ============================================================
// ** 此遊戲 Fixed 設定宣告 **
// ============================================================

// fixed
type fixed0000 struct {
	FreeRound   int    `yaml:"free_round"`
	DemoB       []int  `yaml:"demo_b"`
	DemoC       string `yaml:"demo_c"`
	symboltypes []spec.SymbolType
}

// ============================================================
// ** 遊戲需要的額外結構宣告: 需要實作 Reset 以及 SnapShot **
// ============================================================

type ext0000 struct {
	Triggered     bool  `json:"is_trigger"`
	ScatterCount  int   `json:"scatters,omitzero"`
	ScatterHitMap []int `json:"scatter_hits,omitzero"`
	isSim         bool
}

func (g *game0000) newext(screensize int, isSim bool) *ext0000 {
	return &ext0000{
		Triggered:     false,
		ScatterCount:  0,
		ScatterHitMap: make([]int, 0, screensize),
		isSim:         isSim,
	}
}

func (e *ext0000) Reset() {
	e.Triggered = false
	e.ScatterCount = 0
	e.ScatterHitMap = e.ScatterHitMap[:0]
}

func (e *ext0000) Snapshot() any {
	if e.isSim {
		return nil
	}
	hits := make([]int, len(e.ScatterHitMap))
	copy(hits, e.ScatterHitMap)
	ec := &ext0000{
		Triggered:     e.Triggered,
		ScatterCount:  e.ScatterCount,
		ScatterHitMap: hits,
	}
	return ec
}

// ============================================================
// ** 遊戲主邏輯入口 **
// ============================================================

// GetResult 主要介面函數 回傳遊戲結果 *res.SpinResult
func (g *game0000) GetResult(r *buf.SpinRequest, gh *slot.Game) *buf.SpinResult {
	sr := gh.StartNewSpin(r)

	base := g.getBaseResult(r.BetMult, gh)
	sr.AppendModeResult(base)

	if base.Trigger != 0 {
		free := g.getFreeResult(r.BetMult, gh)
		sr.AppendModeResult(free)
	}
	sr.End()
	return sr
}

// ============================================================
// ** 遊戲中各模式內部邏輯實作 **
// ============================================================

func (g *game0000) getBaseResult(betMult int, gh *slot.Game) *buf.GameModeResult {
	mode := gh.GameModeHandlerList[0]
	sg := mode.ScreenGenerator
	sc := mode.ScreenCalculator
	gmr := mode.GameModeResult
	ext := g.ext
	ext.Reset()

	// 1. 生成盤面
	screen := sg.GenScreen()
	gmr.AddAct(buf.FinishAct, "screen", screen, nil)

	// 2. 算分
	sc.CalcScreen(betMult, screen, gmr)
	if gmr.GetTmpWin() > 0 {
		gmr.AddAct(buf.FinishAct, "win", nil, nil)
	}

	// 3. 判斷觸發
	gmr.Trigger = g.trigger(screen)
	if gmr.Trigger > 0 {
		gmr.AddAct(buf.FinishAct, "trigger", nil, ext)
	}

	// 4. Round提交
	gmr.FinishRound()

	return mode.YieldResult()
}

func (g *game0000) getFreeResult(betMult int, gh *slot.Game) *buf.GameModeResult {
	mode := gh.GameModeHandlerList[1]
	sg := mode.ScreenGenerator
	sc := mode.ScreenCalculator
	gmr := mode.GameModeResult
	round := g.fixed.FreeRound
	ext := g.ext
	ext.Reset()

	for i := 0; i < round; i++ {
		// 1. 生成盤面
		screen := sg.GenScreen()
		gmr.AddAct(buf.FinishAct, "screen", screen, nil)

		// 2. 算分
		sc.CalcScreen(betMult, screen, gmr)
		if gmr.GetTmpWin() > 0 {
			gmr.AddAct(buf.FinishAct, "win", nil, nil)
		}

		// 3. Round Act完成
		gmr.FinishRound()
	}

	return mode.YieldResult()
}

// ============================================================
// ** 遊戲內部輔助函數實作 **
// ============================================================

// 0 代表不觸發 > 0 各自觸發
func (g *game0000) trigger(screen []int16) int {
	g.ext.Reset()
	ext := g.ext
	symtype := g.fixed.symboltypes
	for i := 0; i < len(screen); i++ {
		if symtype[screen[i]] == spec.SymbolTypeScatter {
			ext.ScatterCount++
			ext.ScatterHitMap = append(ext.ScatterHitMap, i)
		}
	}
	if ext.ScatterCount > 2 {
		ext.Triggered = true
		return 1
	}
	return 0
}
