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

package slot

import (
	"fmt"

	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

// Game 負責掌管單一遊戲的生命週期：讀取設定、建立模式處理器、串接邏輯並提供 spin 入口。
type Game struct {
	Core                *core.Core
	GameSetting         *spec.GameSetting
	GameName            string
	GameId              spec.GID
	BetUnits            []int
	MaxWinLimit         int
	GameModeHandlerList []*GameMode
	SpinResult          *buf.SpinResult // Spin結果緩衝
	IsSim               bool
	logic               GameLogic
}

// ============================================================
// ** 創建遊戲實例 **
// ============================================================

// NewGame 建立 Game，使用呼叫端提供的 GameSetting
func NewGame(gs *spec.GameSetting, reg *LogicRegistry, core *core.Core, isSim bool) (*Game, error) {
	g := &Game{
		Core:        core,
		GameName:    gs.GameName,
		GameSetting: gs,
		IsSim:       isSim,
	}
	err := g.init(reg)
	if err != nil {
		return nil, err
	}
	return g, nil
}

// ============================================================
// ** 以下公開方法 **
// ============================================================

// GetResult 依照 betMode / betMult 進行一次遊戲流程並回傳結果緩衝。
func (gh *Game) GetResult(req *buf.SpinRequest) *buf.SpinResult {
	return gh.logic.GetResult(req, gh)
}

// ResetResult 重置共享的 SpinResult 緩衝並同步清空所有 GameModeResult 狀態。
func (gh *Game) ResetResult() {
	// 重置結果
	sr := gh.SpinResult
	sr.Reset()

	// 重置每個Mode內的結果
	for i := 0; i < len(gh.GameModeHandlerList); i++ {
		gm := gh.GameModeHandlerList[i]
		gm.ResetGameModeResult()
	}
}

// StartNewSpin 重置狀態、設定本次投注資訊，並取得可累積結果的 SpinResult 指標。
func (gh *Game) StartNewSpin(r *buf.SpinRequest) *buf.SpinResult {
	gh.ResetResult()
	gh.SpinResult.BetMode = r.BetMode
	gh.SpinResult.BetMult = r.BetMult
	gh.SpinResult.Bet = r.Bet
	return gh.SpinResult
}

// ============================================================
// ** 以下內部方法 **
// ============================================================

func (g *Game) init(reg *LogicRegistry) error {

	g.BetUnits = g.GameSetting.BetUnits
	g.MaxWinLimit = g.GameSetting.MaxWinLimit

	// 建立可重用SpinResult緩衝
	g.SpinResult = buf.NewSpinResult(g.GameSetting)

	// 依據模式建立Handler
	modecount := len(g.GameSetting.GameModeSettings)
	g.GameModeHandlerList = make([]*GameMode, modecount)
	for i := 0; i < modecount; i++ {
		g.GameModeHandlerList[i] = newGameMode(g.Core, &g.GameSetting.GameModeSettings[i], i)
	}

	// 建立介面
	if logic, err := reg.Build(g.GameSetting.LogicKey, g); err != nil {
		return errs.Wrap(err, fmt.Sprintf("build logic failed: game=%q lkey=%q", g.GameName, g.GameSetting.LogicKey))
	} else {
		g.logic = logic
	}
	return nil
}
