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

package dto

import (
	"encoding/json"

	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

type SpinResult struct {
	GameName  string              `json:"game"`                // 遊戲名稱
	GameID    spec.GID            `json:"gameid"`              // 遊戲編號
	TotalWin  int                 `json:"win"`                 // 總贏分
	Bet       int                 `json:"bet"`                 // 本次押注
	BetMode   int                 `json:"betmode"`             // 押注類型
	BetMult   int                 `json:"betmult"`             // 押注倍數
	GameModes []GameModeResultDTO `json:"gamemodes,omitempty"` // 每個遊戲模式的完整結構
	IsGameEnd bool                `json:"isend"`               // 遊戲結束旗標
	State     SpinState           `json:"spin_state"`          // 遊戲狀態
}

// GameModeResultDTO 為對外輸出的 GameModeResult 序列化結構。
type GameModeResultDTO struct {
	TotalWin   int            `json:"win"`     // 整個GameMode贏分
	GameModeId int            `json:"modeid"`  // 設定檔中的第幾個Mode狀態
	IsModeEnd  bool           `json:"isend"`   // 模式是否完整結束
	Trigger    int            `json:"trigger"` // 觸發遊戲
	ActResults []ActResultDTO `json:"acts,omitempty"`
}

type ActResultDTO struct {
	ActType string `json:"acttype"`
	Id      int    `json:"id"`

	RoundId int `json:"round"`
	StepId  int `json:"step"`
	ActId   int `json:"act"`

	IsRoundEnd bool `json:"is_round_end,omitempty"`
	IsStepEnd  bool `json:"is_step_end,omitempty"`

	NowTotalWin int `json:"nowtotalwin"`
	RoundAccWin int `json:"roundaccwin"`
	StepAccWin  int `json:"stepaccwin"`
	ActWin      int `json:"actwin"`

	Screen  []int16               `json:"screen,omitempty"`
	Details []CalcScreenDetailDTO `json:"details,omitempty"`

	ExtendResult any `json:"ext,omitempty"` // 未輸出模式下存struct指標 轉到DTO時轉 map[string]any(Json) 或 []byte
}

// CalcScreenDetail 盤面算分細項
type CalcScreenDetailDTO struct {
	Win          int     `json:"win"`       // 本DetailCalcResult輸贏
	SymbolID     int16   `json:"symbol"`    // 圖標ID
	LineID       int     `json:"line"`      // Line專用 : 線表ID
	Count        int     `json:"count"`     // 計算數量 直接對應PayTable (Line: 連線長度, Cluster: 集群圖標數量, Collect: 收集數量)
	Combinations int     `json:"comb"`      // Way專用 : 組合數量
	Direction    uint8   `json:"direction"` // 方向，0: 左到右，1: 右到左 (Way Line用)
	HitMap       []int16 `json:"hits"`
}

func NewSpinResultDTO(sr *buf.SpinResult) (SpinResult, error) {
	if sr == nil {
		return SpinResult{}, errs.NewWarn("spin result is nil")
	}
	state := SpinState{
		StartCoreSnapB64U: corefmt.EncodeBase64URL(sr.State.StartCoreSnap),
		AfterCoreSnapB64U: corefmt.EncodeBase64URL(sr.State.AfterCoreSnap),
	}
	if sr.State.Checkpoint != nil {
		cp, err := EncodeCheckpoint(sr.Logic, sr.State.Checkpoint)
		if err != nil {
			return SpinResult{}, err
		}
		state.Checkpoint = cp
	}

	dto := SpinResult{
		GameName:  sr.GameName,
		GameID:    sr.GameID,
		TotalWin:  sr.TotalWin,
		Bet:       sr.Bet,
		BetMode:   sr.BetMode,
		BetMult:   sr.BetMult,
		IsGameEnd: sr.IsGameEnd,
		State:     state,
	}

	if len(sr.GameModeList) > 0 {
		dto.GameModes = make([]GameModeResultDTO, len(sr.GameModeList))
		for i, gm := range sr.GameModeList {
			dto.GameModes[i] = newGameModeResultDTO(sr.Logic, gm)
		}
	}

	return dto, nil
}

func newGameModeResultDTO(lkey spec.LogicKey, gmr *buf.GameModeResult) GameModeResultDTO {
	if gmr == nil {
		return GameModeResultDTO{}
	}

	dto := GameModeResultDTO{
		TotalWin:   gmr.TotalWin,
		GameModeId: gmr.GameModeId,
		IsModeEnd:  gmr.IsModeEnd,
		Trigger:    gmr.Trigger,
	}
	snap := snapshotGameMode(gmr)
	if len(gmr.ActResults) > 0 {
		dto.ActResults = make([]ActResultDTO, len(gmr.ActResults))
		for i, a := range gmr.ActResults {
			dto.ActResults[i] = newActResultDTO(i, a, gmr, snap)
			dto.ActResults[i].ExtendResult = renderExtendResult(lkey, a.ExtendResult)
		}
	}

	return dto
}

func newActResultDTO(id int, act buf.ActResult, gmr *buf.GameModeResult, snap *gameModeSnapshot) ActResultDTO {
	dto := ActResultDTO{
		ActType: act.ActType,
		Id:      id,

		RoundId: act.RoundId,
		StepId:  act.StepId,
		ActId:   act.ActId,

		IsRoundEnd: act.IsRoundEnd,
		IsStepEnd:  act.IsStepEnd,

		NowTotalWin: act.NowTotalWin,
		RoundAccWin: act.RoundAccWin,
		StepAccWin:  act.StepAccWin,
		ActWin:      act.ActWin,

		Screen:  getScreenDtoFromSnap(act.ScreenStart, snap),
		Details: newDetailDto(act, gmr, snap),
	}
	return dto
}

func newDetailDto(a buf.ActResult, gmr *buf.GameModeResult, snap *gameModeSnapshot) []CalcScreenDetailDTO {
	startIdx := a.DetailsStart
	endIdx := a.DetailsEnd
	length := endIdx - startIdx
	if length <= 0 {
		return nil
	}
	dto := make([]CalcScreenDetailDTO, length)
	for i := 0; i < length; i++ {
		dto[i] = CalcScreenDetailDTO{
			Win:          gmr.Details[startIdx+i].Win,
			SymbolID:     gmr.Details[startIdx+i].SymbolID,
			LineID:       gmr.Details[startIdx+i].LineID,
			Count:        gmr.Details[startIdx+i].Count,
			Combinations: gmr.Details[startIdx+i].Combinations,
			Direction:    gmr.Details[startIdx+i].Direction,
			HitMap:       hitMapFromSnap(gmr.Details[startIdx+i], snap),
		}
	}
	return dto
}

// gameModeSnapshot
//
// 對於要深拷貝且零碎的物件作一次集中深拷貝快照
// 讓後續Dto時候都只對快照作切片，避免了多次make/拷貝的GC波動
type gameModeSnapshot struct {
	Screens    []int16
	Details    []buf.CalcScreenDetail // 依你實際型別
	HitsFlat   []int16
	ScreenSize int
}

func snapshotGameMode(gmr *buf.GameModeResult) *gameModeSnapshot {
	s := gameModeSnapshot{
		ScreenSize: gmr.ScreenSize,
	}
	// 一次性深拷貝
	s.Screens = append([]int16(nil), gmr.Screens...)
	s.HitsFlat = append([]int16(nil), gmr.HitsFlat...)
	s.Details = append([]buf.CalcScreenDetail(nil), gmr.Details...) // 如果 Details 內沒有指標/切片，這樣就夠
	return &s
}

func getScreenDtoFromSnap(start int, snap *gameModeSnapshot) []int16 {
	if start < 0 {
		return nil
	}
	end := start + snap.ScreenSize
	if end > len(snap.Screens) {
		return nil
	}
	return snap.Screens[start:end] // 不拷貝
}

func hitMapFromSnap(d buf.CalcScreenDetail, snap *gameModeSnapshot) []int16 {
	hs := d.HitsFlatStart
	he := hs + d.HitsFlatLen
	if hs < 0 || he > len(snap.HitsFlat) || he < hs {
		return nil
	}
	return snap.HitsFlat[hs:he] // 不拷貝
}

type SpinState struct {
	StartCoreSnapB64U string          `json:"start_b64u"`   // 必回
	AfterCoreSnapB64U string          `json:"after_b64u"`   // 必回
	Checkpoint        json.RawMessage `json:"cp,omitempty"` // 視你是否要每局都回；若審計要強制，也可以去掉 omitempty
	// JP delta/hit (optional)
}
