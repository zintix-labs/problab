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

package dev

import (
	"io/fs"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/spec"
)

// metaResponse 是 GET /dev/meta 的回應。
type metaResponse struct {
	Games       []gameMeta `json:"games"`
	MaxRounds   int        `json:"max_rounds"`   // 一次批次的局數上限
	MaxSegments int        `json:"max_segments"` // 每局的段數上限
	MaxBetMult  int        `json:"max_bet_mult"` // 投注倍數上限
}

// gameMeta 只包含畫面需要的欄位。完整設定含盤面產生、賠率與中獎判定，不得送出；
// 新增欄位時逐一複製，不要整份轉送 spec.GameSetting。
type gameMeta struct {
	GID         spec.GID       `json:"gid"`
	Name        string         `json:"name"`
	Logic       spec.LogicKey  `json:"logic"`
	BetUnits    []int          `json:"bet_units"`
	MaxWinLimit int            `json:"max_win_limit"`
	Fixed       map[string]any `json:"fixed"` // 機台自訂的參數，例如外掛畫進度條用的門檻
	Modes       []modeMeta     `json:"modes"`
	Plugin      bool           `json:"plugin"` // 應用端注入了 <gid>.js；false 時前端只顯示公版，不請求外掛
}

type modeMeta struct {
	Columns int      `json:"columns"`
	Rows    int      `json:"rows"`
	Symbols []string `json:"symbols"` // 圖標名稱，盤面上的圖標 ID 是它的索引
}

func buildMeta(pb *problab.Problab, devGames fs.FS) (metaResponse, error) {
	out := metaResponse{Games: []gameMeta{}, MaxRounds: maxRounds, MaxSegments: problab.DefaultMaxSegments, MaxBetMult: maxBetMult}
	for _, ent := range pb.All() {
		gs, err := pb.GameSettingById(ent.GID)
		if err != nil {
			return metaResponse{}, err
		}
		g := gameMeta{
			GID:         gs.GameID,
			Name:        gs.GameName,
			Logic:       gs.LogicKey,
			BetUnits:    gs.BetUnits,
			MaxWinLimit: gs.MaxWinLimit,
			Fixed:       gs.Fixed,
			Modes:       make([]modeMeta, 0, len(gs.GameModeSettings)),
			Plugin:      hasPlugin(devGames, gs.GameID),
		}
		if g.Fixed == nil {
			g.Fixed = map[string]any{}
		}
		for _, m := range gs.GameModeSettings {
			g.Modes = append(g.Modes, modeMeta{
				Columns: m.ScreenSetting.Columns,
				Rows:    m.ScreenSetting.Rows,
				Symbols: m.SymbolSetting.SymbolUsedStr,
			})
		}
		out.Games = append(out.Games, g)
	}
	return out, nil
}
