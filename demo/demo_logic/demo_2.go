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

// demo_choice：分段遊戲範例。BG 觸發後，第 0 段停在選擇點，結果的 ChooseNext 列出兩個入口；
// 玩家選擇後，第 1 段執行對應的 FG 並結束這一局。

// ============================================================
// ** 註冊 **
// ============================================================

func init() {
	logic := "demo_choice"
	if err := slot.GameRegister(spec.LogicKey(logic), buildGame0002, Logics); err != nil {
		log.Fatalf("%s register failed: %v", logic, err)
	}
	if err := dto.RegisterExtendRender[ext0002](spec.LogicKey(logic)); err != nil {
		log.Fatalf("%s register failed: %v", logic, err)
	}
	// 續段需要 checkpoint 帶入進入狀態：註冊 codec，讓正式路徑能經 JSON 往返。
	if err := dto.RegisterCheckpoint[check0002](spec.LogicKey(logic)); err != nil {
		log.Fatalf("%s register failed: %v", logic, err)
	}
}

// 入口值（ChooseNext 的內容）。刻意不等於位置索引：續段以 ChooseNext[Choice] 取得入口。
const (
	EntryFGA = 1 // 較多轉
	EntryFGB = 2 // 較少轉、贏分加倍
)

// ============================================================
// ** 遊戲介面 **
// ============================================================

type game0002 struct {
	fixed *fixed0002
	ext   *ext0002
}

func buildGame0002(gh *slot.Game) (slot.GameLogic, error) {
	g := &game0002{fixed: new(fixed0002)}
	if err := spec.DecodeFixed(gh.GameSetting, g.fixed); err != nil {
		return nil, err
	}
	g.fixed.symboltypes = gh.GameSetting.GameModeSettings[0].SymbolSetting.SymbolTypes
	screensize := gh.GameSetting.GameModeSettings[0].ScreenSetting.ScreenSize
	g.ext = &ext0002{ScatterHitMap: make([]int, 0, screensize), isSim: gh.OmitExtend()}
	return g, nil
}

// ============================================================
// ** 此遊戲 Fixed 設定宣告 **
// ============================================================

type fixed0002 struct {
	FreeRoundA  int `yaml:"free_round_a"`
	FreeRoundB  int `yaml:"free_round_b"`
	MultiplierB int `yaml:"multiplier_b"`
	symboltypes []spec.SymbolType
}

// ============================================================
// ** Extend 與 Checkpoint **
// ============================================================

type ext0002 struct {
	Triggered     bool  `json:"is_trigger"`
	ScatterCount  int   `json:"scatters,omitzero"`
	ScatterHitMap []int `json:"scatter_hits,omitzero"`
	isSim         bool
}

func (e *ext0002) Reset() {
	e.Triggered = false
	e.ScatterCount = 0
	e.ScatterHitMap = e.ScatterHitMap[:0]
}

func (e *ext0002) Snapshot() any {
	if e.isSim {
		return nil
	}
	return &ext0002{
		Triggered:     e.Triggered,
		ScatterCount:  e.ScatterCount,
		ScatterHitMap: append([]int(nil), e.ScatterHitMap...),
	}
}

// check0002 是第 0 段交給續段的進入狀態。每次新配置，交出後不再修改。
type check0002 struct {
	Scatters int `json:"scatters"`
}

// ============================================================
// ** 遊戲主邏輯入口 **
// ============================================================

// GetResult 第 0 段執行 BG；觸發時停在選擇點。續段先驗證入口與 checkpoint（不合法時回傳
// nil，不消耗 RNG），再執行所選的 FG。
func (g *game0002) GetResult(r *buf.SpinRequest, gh *slot.Game) *buf.SpinResult {
	if r.Cycle == 0 {
		sr := gh.StartNewSpin(r)
		base, scatters := g.getBaseResult(r.BetMult, gh)
		sr.AppendModeResult(base)
		if base.Trigger == 0 {
			sr.End()
			return sr
		}
		sr.ChooseNext = append(sr.ChooseNext, EntryFGA, EntryFGB)
		sr.State.Checkpoint = &check0002{Scatters: scatters}
		return sr
	}

	cp, ok := r.Checkpoint.(*check0002)
	if !ok || cp.Scatters < 3 {
		return nil
	}
	var mode, rounds, mult int
	switch r.ChooseNext[r.Choice] {
	case EntryFGA:
		mode, rounds, mult = 1, g.fixed.FreeRoundA, 1
	case EntryFGB:
		mode, rounds, mult = 2, g.fixed.FreeRoundB, g.fixed.MultiplierB
	default:
		return nil
	}
	rounds += cp.Scatters - 3 // Scatter 每多一個，多一轉

	sr := gh.StartNewSpin(r)
	sr.AppendModeResult(g.getFreeResult(mode, rounds, r.BetMult*mult, gh))
	sr.End()
	return sr
}

// ============================================================
// ** 遊戲中各模式內部邏輯實作 **
// ============================================================

func (g *game0002) getBaseResult(betMult int, gh *slot.Game) (*buf.GameModeResult, int) {
	mode := gh.GameModeHandlerList[0]
	gmr := mode.GameModeResult
	ext := g.ext
	ext.Reset()

	screen := mode.ScreenGenerator.GenScreen()
	gmr.AddAct(buf.FinishAct, "screen", screen, nil)

	mode.ScreenCalculator.CalcScreen(betMult, screen, gmr)
	if gmr.GetTmpWin() > 0 {
		gmr.AddAct(buf.FinishAct, "win", nil, nil)
	}

	scatters := g.scatters(screen)
	if scatters >= 3 {
		ext.Triggered = true
		gmr.Trigger = 1
		gmr.AddAct(buf.FinishAct, "trigger", nil, ext)
	}
	gmr.FinishRound()
	return mode.YieldResult(), scatters
}

func (g *game0002) getFreeResult(modeIndex, rounds, betMult int, gh *slot.Game) *buf.GameModeResult {
	mode := gh.GameModeHandlerList[modeIndex]
	gmr := mode.GameModeResult
	for range rounds {
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

// scatters 計算盤面上的 Scatter 數量，並記錄在 ext。
func (g *game0002) scatters(screen []int16) int {
	ext := g.ext
	for i, sym := range screen {
		if g.fixed.symboltypes[sym] == spec.SymbolTypeScatter {
			ext.ScatterCount++
			ext.ScatterHitMap = append(ext.ScatterHitMap, i)
		}
	}
	return ext.ScatterCount
}
