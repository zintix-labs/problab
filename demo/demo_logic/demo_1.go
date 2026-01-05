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
	"github.com/zintix-labs/problab/sdk/ops"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// ============================================================
// ** 註冊 **
// ============================================================

func init() {
	logic := "demo_cascade"
	builder := buildGame0001
	logics := Logics
	if err := slot.GameRegister(spec.LogicKey(logic), builder, logics); err != nil {
		log.Fatalf("%s register failed: %v", logic, err)
	}
	// register Extend
	if err := dto.RegisterExtendRender[ext0001](spec.LogicKey(logic)); err != nil {
		log.Fatalf("%s register failed: %v", logic, err)
	}
	// register Checkpoint
	// if err := dto.RegisterCheckpoint[check0001](spec.LogicKey(logic)); err != nil {
	// 	log.Fatalf("%s register failed: %v", logic, err)
	// }
}

// ============================================================
// ** 遊戲介面 **
// ============================================================

type game0001 struct {
	fixed *fixed0001
	ext   *ext0001
}

func buildGame0001(g *slot.Game) (slot.GameLogic, error) {
	fix := new(fixed0001)
	if err := spec.DecodeFixed(g.GameSetting, fix); err != nil {
		return nil, err
	}
	fix.fillReelsIdx = make([]int, g.GameSetting.GameModeSettings[0].ScreenSetting.Columns)
	fix.screenFillPos = make([]int, g.GameSetting.GameModeSettings[0].ScreenSetting.Columns)
	fix.symbolTypes = g.GameSetting.GameModeSettings[0].SymbolSetting.SymbolTypes
	g1 := &game0001{fixed: fix}
	g1.ext = g1.newext(g.GameSetting.GameModeSettings[0].ScreenSetting.ScreenSize, g.IsSim)
	return g1, nil
}

// ============================================================
// ** 此遊戲需要的額外結構宣告: Fixed設定宣告 **
// ============================================================

type fixed0001 struct {
	MaxStep       int `yaml:"max_step"`
	FreeRounds    int `yaml:"free_rounds"`
	Trigger       int `yaml:"trigger"`
	ScatterPay    int `yaml:"scatter_pay"`
	fillReelsIdx  []int
	screenFillPos []int
	symbolTypes   []spec.SymbolType
}

// ============================================================
// ** 遊戲需要的額外結構宣告: 需要實作Reset以及SnapShot **
// ============================================================

type ext0001 struct {
	Triggered     bool  `json:"is_trigger"`
	ScatterCount  int   `json:"scatters,omitzero"`
	ScatterHitMap []int `json:"scatter_hits,omitzero"`
	isSim         bool
}

func (g *game0001) newext(screensize int, isSim bool) *ext0001 {
	return &ext0001{
		Triggered:     false,
		ScatterCount:  0,
		ScatterHitMap: make([]int, 0, screensize),
		isSim:         isSim,
	}
}

func (e *ext0001) Reset() {
	e.Triggered = false
	e.ScatterCount = 0
	e.ScatterHitMap = e.ScatterHitMap[:0]
}

func (e *ext0001) Snapshot() any {
	if e.isSim {
		return nil
	}
	hits := make([]int, len(e.ScatterHitMap))
	copy(hits, e.ScatterHitMap)
	ec := &ext0001{
		Triggered:     e.Triggered,
		ScatterCount:  e.ScatterCount,
		ScatterHitMap: hits,
	}
	return ec
}

// ============================================================
// ** 遊戲主邏輯入口 **
// ============================================================

// getResult 主要介面函數 回傳遊戲結果 *res.SpinResult
func (g *game0001) GetResult(r *buf.SpinRequest, gh *slot.Game) *buf.SpinResult {
	sr := gh.StartNewSpin(r)
	base := g.getBaseResult(r, gh)
	sr.AppendModeResult(base)

	if base.Trigger != 0 {
		free := g.getFreeResult(r, gh)
		sr.AppendModeResult(free)
	}
	sr.End()
	return sr
}

// ============================================================
// ** 遊戲中各模式內部邏輯實作 **
// ============================================================

func (g *game0001) getBaseResult(r *buf.SpinRequest, gh *slot.Game) *buf.GameModeResult {
	mode := gh.GameModeHandlerList[0]
	sg := mode.ScreenGenerator
	sc := mode.ScreenCalculator
	gmr := mode.GameModeResult
	maxStep := g.fixed.MaxStep
	fillReelSet := &mode.GameModeSetting.GenScreenSetting.ReelSetGroup[1]
	betMult := r.BetMult
	fix := g.fixed
	g.resetIdx()

	for i := 0; i < 1; i++ {

		// 1. gen first screen
		screen := sg.GenScreen()
		gmr.AddAct(buf.FinishAct, "gen_screen", screen, nil)

		for i := range fix.fillReelsIdx {
			fix.fillReelsIdx[i] = fillReelSet.Reels[i].ReelLUT.Pick(gh.Core)
		}
		for range maxStep {
			// 2. calc win
			sc.CalcScreen(betMult, screen, gmr)
			hit := gmr.HitMapTmp()

			// 3. break loop when win == 0
			if gmr.GetTmpWin() == 0 {
				gmr.FinishStep()
				break
			}
			// 4. record win
			gmr.AddAct(buf.FinishAct, "win", nil, nil)

			// 5. clear hit symbol on screen
			ops.Clear(screen, hit)
			gmr.AddAct(buf.FinishStep, "clear", screen, nil)

			// 6. gravity
			ops.Gravity(screen, sg.Cols, sg.Rows, g.fixed.screenFillPos)
			gmr.AddAct(buf.FinishStep, "gravity", screen, nil)

			// 7. fill screen
			ops.FillScreen(screen, fillReelSet, g.fixed.screenFillPos, g.fixed.fillReelsIdx, sg.Cols)
			gmr.AddAct(buf.FinishStep, "fillscreen", screen, nil)
		}

		// trigger
		gmr.Trigger = g.trigger(screen)
		if gmr.Trigger > 0 {
			gmr.UpdateTmpWin(fix.ScatterPay * betMult)
			gmr.AddAct(buf.FinishAct, "trigger", nil, g.ext)
		}
		gmr.FinishRound()
	}
	return mode.YieldResult()
}

func (g *game0001) getFreeResult(r *buf.SpinRequest, gh *slot.Game) *buf.GameModeResult {
	mode := gh.GameModeHandlerList[1]
	sg := mode.ScreenGenerator
	sc := mode.ScreenCalculator
	gmr := mode.GameModeResult
	maxStep := g.fixed.MaxStep
	fillReelSet := &mode.GameModeSetting.GenScreenSetting.ReelSetGroup[1]
	fix := g.fixed

	betMult := r.BetMult

	for i := 0; i < fix.FreeRounds; i++ {
		// 1. gen first screen
		screen := sg.GenScreen()
		gmr.AddAct(buf.FinishAct, "gen_screen", screen, nil)
		g.resetIdx()

		for i := range fix.fillReelsIdx {
			fix.fillReelsIdx[i] = fillReelSet.Reels[i].ReelLUT.Pick(gh.Core)
		}
		for range maxStep {
			// 2. calc win
			sc.CalcScreen(betMult, screen, gmr)
			hit := gmr.HitMapTmp()

			// 3. break loop when win == 0
			if gmr.GetTmpWin() == 0 {
				gmr.FinishStep()
				break
			}
			// 4. record win
			gmr.AddAct(buf.FinishAct, "win", nil, nil)

			// 5. clear hit symbol on screen
			ops.Clear(screen, hit)
			gmr.AddAct(buf.FinishStep, "clear", screen, nil)

			// 6. gravity
			ops.Gravity(screen, sg.Cols, sg.Rows, g.fixed.screenFillPos)
			gmr.AddAct(buf.FinishStep, "gravity", screen, nil)

			// 7. fill screen
			ops.FillScreen(screen, fillReelSet, g.fixed.screenFillPos, g.fixed.fillReelsIdx, sg.Cols)
			gmr.AddAct(buf.FinishStep, "fillscreen", screen, nil)
		}
		gmr.FinishRound()
	}
	return mode.YieldResult()
}

// ============================================================
// ** 遊戲內部輔助函數實作 **
// ============================================================

// 0 代表不觸發 > 0 各自觸發
func (g *game0001) trigger(screen []int16) int {
	g.ext.Reset()
	ext := g.ext
	symtype := g.fixed.symbolTypes
	for i := 0; i < len(screen); i++ {
		if symtype[screen[i]] == spec.SymbolTypeScatter {
			ext.ScatterCount++
			ext.ScatterHitMap = append(ext.ScatterHitMap, i)
		}
	}
	if ext.ScatterCount >= g.fixed.Trigger {
		ext.Triggered = true
		return 1
	}
	return 0
}

func (g *game0001) resetIdx() {
	for i := 0; i < len(g.fixed.screenFillPos); i++ {
		g.fixed.screenFillPos[i] = 0
		g.fixed.fillReelsIdx[i] = 0
	}
}
