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

package spec

import (
	"fmt"

	"github.com/zintix-labs/problab/errs"
)

// GameSetting 包含啟動一個機台所需的所有高階設定。
type GameSetting struct {
	GameName         string            `yaml:"game_name"           json:"game_name"`
	GameID           GID               `yaml:"game_id"             json:"game_id"`
	LogicKey         LogicKey          `yaml:"logic_key"             json:"logic_key"`
	BetUnits         []int             `yaml:"bet_units"           json:"bet_units"`
	MaxWinLimit      int               `yaml:"max_win_limit"       json:"max_win_limit"`
	GameModeSettings []GameModeSetting `yaml:"game_mode_settings"  json:"game_mode_settings"`
	Fixed            map[string]any    `yaml:"fixed"               json:"fixed"`
}

// init
func (gs *GameSetting) init() error {
	for i := range gs.GameModeSettings {
		mode := &gs.GameModeSettings[i]
		if err := mode.init(); err != nil {
			return err
		}
	}
	return gs.valid()
}

// valid 執行最基本的設定檔檢查，如需更多驗證可在此擴充。
func (gs *GameSetting) valid() error {

	// valid BetUnits
	if len(gs.BetUnits) == 0 {
		return errs.NewFatal(fmt.Sprintf("game_name: %s err:empty bet_units", gs.GameName))
	}

	for _, b := range gs.BetUnits {
		if b < 1 {
			return errs.NewFatal(fmt.Sprintf("game_name: %s err:invalid bet unit", gs.GameName))
		}
		if gs.MaxWinLimit < b {
			return errs.NewFatal(fmt.Sprintf("game_name: %s err:empty game mode settings", gs.GameName))
		}
	}

	// 檢查 GameModeSettings 不能為空
	if len(gs.GameModeSettings) == 0 {
		return errs.NewFatal("empty game_mode_settings")
	}

	// GameModeSetting 檢查
	for i := 0; i < len(gs.GameModeSettings); i++ {
		gms := gs.GameModeSettings[i]
		screenSetting := gms.ScreenSetting
		if screenSetting.Columns <= 0 || screenSetting.Rows <= 0 {
			return errs.NewFatal(fmt.Sprintf("invalid screen dimensions: cols=%d rows=%d", screenSetting.Columns, screenSetting.Rows))
		}
		count := int16(gms.SymbolSetting.SymbolCount)
		rsGp := gms.GenScreenSetting.ReelSetGroup
		if len(rsGp) != 0 {
			for rsid := range rsGp {
				rs := rsGp[rsid].Reels
				for j := range rs {
					for k, s := range rs[j].ReelSymbols {
						if s < 0 || s > count {
							return errs.NewFatal(fmt.Sprintf("symbol out of range: reelset %d reel %d index %d : %d", rsid, j, k, s))
						}
					}
				}
			}
		}
	}

	return nil
}
