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
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/sampler"
)

// GenReelType 表示盤面生成時使用的輪帶/符號選擇策略。
type GenReelType int

const (
	GenReelTypeNone GenReelType = iota
	GenReelByReelIdx
	GenReelBySymbolWeight
)

var GenReelTypeMap = map[string]GenReelType{
	"GenReelTypeNone":       GenReelTypeNone,
	"GenReelByReelIdx":      GenReelByReelIdx,
	"GenReelBySymbolWeight": GenReelBySymbolWeight,
}

// Reel 一條輪帶設定，可以產出一軸結果的最小單位
type Reel struct {
	ReelSymbols []int16     `yaml:"symbols" json:"symbols"`
	ReelWeights []int       `yaml:"weights" json:"weights"`
	ReelLength  int         `yaml:"-"       json:"-"`
	ReelLUT     sampler.LUT `yaml:"-"       json:"-"`
}

// ReelSet 一組輪帶設定，可以產出一個盤面的最小單位
type ReelSet struct {
	Weight int    `yaml:"weight" json:"weight"`
	Reels  []Reel `yaml:"reels"  json:"reels"`
}

// GenScreenSetting 生成盤面的設定
type GenScreenSetting struct {
	GenReelTypeStr string      `yaml:"gen_reel_type"   json:"gen_reel_type"`
	GenReelType    GenReelType `yaml:"-"               json:"-"`
	ReelSetGroup   []ReelSet   `yaml:"reel_set_group"  json:"reel_set_group"`
	ReelSetLUT     sampler.LUT `yaml:"-"               json:"-"`
	initFlag       bool
}

// Init 建立生成盤面所需的查表資料。
// 會將輪帶累積權重轉換成 O(1) 查表陣列，並預先算好輪帶組選擇表。
func (gs *GenScreenSetting) Init() error {
	if gs.initFlag {
		return nil
	}

	// 1. 解析 GenReelType
	if gs.GenReelType == GenReelTypeNone {
		grt, ok := GenReelTypeMap[gs.GenReelTypeStr]
		if !ok {
			return errs.NewFatal("invalid gen reel type")
		}
		gs.GenReelType = grt
	}

	// 2. 建立 ReelSet 選擇用的 LUT
	weights := make([]int, len(gs.ReelSetGroup))
	for i := range gs.ReelSetGroup {
		rs := &gs.ReelSetGroup[i]
		weights[i] = rs.Weight

		// 轉Reel內部資料
		for j := range rs.Reels {
			reel := &rs.Reels[j]
			if len(reel.ReelSymbols) == 0 {
				return errs.NewFatal("len(ReelSymbols) == 0")
			}
			if len(reel.ReelWeights) == 0 {
				// 如果長度為0 / nil 默認等權重
				reel.ReelWeights = make([]int, len(reel.ReelSymbols))
				for i := range len(reel.ReelSymbols) {
					reel.ReelWeights[i] = 1
				}
			}
			if len(reel.ReelSymbols) != len(reel.ReelWeights) {
				return errs.NewFatal("len(ReelSymbols) != len(ReelWeights)")
			}
			reel.ReelLength = len(reel.ReelSymbols)
			reel.ReelLUT = sampler.BuildLUT(reel.ReelWeights)
		}
	}

	// 建立最外層 ReelSet 權重表
	gs.ReelSetLUT = sampler.BuildLUT(weights)
	gs.initFlag = true
	return nil
}
