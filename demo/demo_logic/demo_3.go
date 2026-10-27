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

package demo_logic

import (
	"log"

	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// demo_entry：入口庫範例。BG 觸發後二選一；FG-A 再觸發時只能進 FG-B，FG-B 再觸發時只能
// 進 FG-A。續段的結果只取決於入口值與 seed：不讀 checkpoint、Cycle 或其他選項，也不保留
// 跨段的狀態，所以每個入口都可以配置自己的 optimal 庫。

// ============================================================
// ** 註冊 **
// ============================================================

func init() {
	logic := "demo_entry"
	if err := slot.GameRegister(spec.LogicKey(logic), buildGame0003, Logics); err != nil {
		log.Fatalf("%s register failed: %v", logic, err)
	}
	if err := dto.RegisterExtendRender[ext0003](spec.LogicKey(logic)); err != nil {
		log.Fatalf("%s register failed: %v", logic, err)
	}
}

// 入口值（ChooseNext 的內容）。刻意不從 0 開始、也不連續：optimal 的 after 以入口值為鍵，
// 不依賴位置。
const (
	Demo0003EntryFGA = 3 // 較多轉
	Demo0003EntryFGB = 7 // 較少轉、贏分加倍
)

// ============================================================
// ** 遊戲介面 **
// ============================================================

type game0003 struct {
	fixed *fixed0003
	ext   *ext0003
}

func buildGame0003(gh *slot.Game) (slot.GameLogic, error) {
	g := &game0003{fixed: new(fixed0003)}
	if err := spec.DecodeFixed(gh.GameSetting, g.fixed); err != nil {
		return nil, err
	}
	g.fixed.symboltypes = gh.GameSetting.GameModeSettings[0].SymbolSetting.SymbolTypes
	screensize := gh.GameSetting.GameModeSettings[0].ScreenSetting.ScreenSize
	g.ext = &ext0003{ScatterHitMap: make([]int, 0, screensize), isSim: gh.OmitExtend()}
	return g, nil
}

// ============================================================
// ** 此遊戲 Fixed 設定宣告 **
// ============================================================

type fixed0003 struct {
	FreeRoundA  int `yaml:"free_round_a"`
	FreeRoundB  int `yaml:"free_round_b"`
	MultiplierB int `yaml:"multiplier_b"`
	symboltypes []spec.SymbolType
}

// ============================================================
// ** Extend **
// ============================================================

type ext0003 struct {
	Triggered     bool  `json:"is_trigger"`
	ScatterCount  int   `json:"scatters,omitzero"`
	ScatterHitMap []int `json:"scatter_hits,omitzero"`
	isSim         bool
}

func (e *ext0003) Reset() {
	e.Triggered = false
	e.ScatterCount = 0
	e.ScatterHitMap = e.ScatterHitMap[:0]
}

func (e *ext0003) Snapshot() any {
	if e.isSim {
		return nil
	}
	return &ext0003{
		Triggered:     e.Triggered,
		ScatterCount:  e.ScatterCount,
		ScatterHitMap: append([]int(nil), e.ScatterHitMap...),
	}
}

// ============================================================
// ** 遊戲主邏輯入口 **
// ============================================================

// GetResult 第 0 段執行 BG，觸發時停在選擇點；續段依入口執行 FG，FG 中再觸發時停在下一個
// 選擇點。不認得的入口回傳 nil，不消耗 RNG。
func (g *game0003) GetResult(r *buf.SpinRequest, gh *slot.Game) *buf.SpinResult {
	sr := gh.StartNewSpin(r)
	if r.Cycle == 0 {
		base := g.getBaseResult(r, gh)
		sr.AppendModeResult(base)
		if base.Trigger > 0 {
			sr.ChooseNext = append(sr.ChooseNext, Demo0003EntryFGA, Demo0003EntryFGB)
		} else {
			sr.End()
		}
		return sr
	}

	var fg *buf.GameModeResult
	var next int
	switch r.ChooseNext[r.Choice] {
	case Demo0003EntryFGA:
		fg, next = g.getFreeAResult(r, gh), Demo0003EntryFGB
	case Demo0003EntryFGB:
		fg, next = g.getFreeBResult(r, gh), Demo0003EntryFGA
	default:
		return nil
	}

	sr.AppendModeResult(fg)
	if fg.Trigger > 0 {
		sr.ChooseNext = append(sr.ChooseNext, next)
	} else {
		sr.End()
	}
	return sr
}

// ============================================================
// ** 遊戲中各模式內部邏輯實作 **
// ============================================================

func (g *game0003) getBaseResult(r *buf.SpinRequest, gh *slot.Game) *buf.GameModeResult {
	mode := gh.GameModeHandlerList[0]
	gmr := mode.GameModeResult
	ext := g.ext
	ext.Reset()

	screen := mode.ScreenGenerator.GenScreen()
	gmr.AddAct(buf.FinishAct, "screen", screen, nil)

	mode.ScreenCalculator.CalcScreen(r.BetMult, screen, gmr)
	if gmr.GetTmpWin() > 0 {
		gmr.AddAct(buf.FinishAct, "win", nil, nil)
	}

	if g.scatters(screen) >= 3 {
		ext.Triggered = true
		gmr.Trigger = 1
		gmr.AddAct(buf.FinishAct, "trigger", nil, ext)
	}
	gmr.FinishRound()
	return mode.YieldResult()
}

func (g *game0003) getFreeAResult(r *buf.SpinRequest, gh *slot.Game) *buf.GameModeResult {
	return g.getFreeResult(1, g.fixed.FreeRoundA, r.BetMult, gh)
}

func (g *game0003) getFreeBResult(r *buf.SpinRequest, gh *slot.Game) *buf.GameModeResult {
	return g.getFreeResult(2, g.fixed.FreeRoundB, r.BetMult*g.fixed.MultiplierB, gh)
}

// getFreeResult 執行固定轉數的 FG；任一轉出現 3 個以上 Scatter 即標記再觸發。
func (g *game0003) getFreeResult(modeIndex, rounds, betMult int, gh *slot.Game) *buf.GameModeResult {
	mode := gh.GameModeHandlerList[modeIndex]
	gmr := mode.GameModeResult
	ext := g.ext
	for range rounds {
		ext.Reset()
		screen := mode.ScreenGenerator.GenScreen()
		gmr.AddAct(buf.FinishAct, "screen", screen, nil)
		mode.ScreenCalculator.CalcScreen(betMult, screen, gmr)
		if gmr.GetTmpWin() > 0 {
			gmr.AddAct(buf.FinishAct, "win", nil, nil)
		}
		if g.scatters(screen) >= 3 && gmr.Trigger == 0 {
			ext.Triggered = true
			gmr.Trigger = 1
			gmr.AddAct(buf.FinishAct, "trigger", nil, ext)
		}
		gmr.FinishRound()
	}
	return mode.YieldResult()
}

// scatters 計算盤面上的 Scatter 數量，並記錄在 ext。
func (g *game0003) scatters(screen []int16) int {
	ext := g.ext
	for i, sym := range screen {
		if g.fixed.symboltypes[sym] == spec.SymbolTypeScatter {
			ext.ScatterCount++
			ext.ScatterHitMap = append(ext.ScatterHitMap, i)
		}
	}
	return ext.ScatterCount
}
