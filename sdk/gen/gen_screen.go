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

package gen

import (
	"log"

	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

// GenScreenFn 描述熱路徑生成函式，會填滿 ScreenGenerator 重用的盤面緩衝並回傳。
type GenScreenFn func(*ScreenGenerator, []spec.Reel) []int16

// genScreenMap 將 GenReelType 與實際生成函式綁定，初始化時決定後便不再修改。
var genScreenMap = map[spec.GenReelType]GenScreenFn{
	spec.GenReelByReelIdx:      genScreenByReelIdx,
	spec.GenReelBySymbolWeight: genScreenBySymbolWeight,
}

// ScreenGenerator 保存生成盤面所需的所有狀態。
// 會快取列數、行數、查表資料與輸出緩衝，以避免重複配置與計算。
type ScreenGenerator struct {
	core             *core.Core
	ScreenSetting    *spec.ScreenSetting
	GenScreenSetting *spec.GenScreenSetting
	// ScreenSetting 內容建立
	Cols         int
	Rows         int
	WriteableIdx []int // 可寫入格子idx（從Mask轉化）
	// GenScreenSetting 內容建立
	ReelSetGroup []spec.ReelSet
	// 生成函數以及盤面Buffer(避免重複判斷以及重複new盤面)
	genScreenFn GenScreenFn
	Screen      []int16
}

// NewScreenGenerator 根據設定與核心亂數器建立生成器，並立即完成初始化，
// 讓之後的生成流程可以免配置快速執行。
func NewScreenGenerator(core *core.Core, screenSetting *spec.ScreenSetting, genScreenSetting *spec.GenScreenSetting) *ScreenGenerator {
	result := &ScreenGenerator{
		core:             core,
		ScreenSetting:    screenSetting,
		GenScreenSetting: genScreenSetting,
	}
	result.init()
	return result
}

// init 對於已經資料賦值的 ScreenGenerator 作初始化
func (sg *ScreenGenerator) init() error {
	// 防止錯誤
	if err := sg.GenScreenSetting.Init(); err != nil {
		return err
	}
	if err := sg.ScreenSetting.Init(); err != nil {
		return err
	}

	// screenSetting 內容建立
	sg.Cols = sg.ScreenSetting.Columns
	sg.Rows = sg.ScreenSetting.Rows
	sg.WriteableIdx = make([]int, 0)
	for i := 0; i < len(sg.ScreenSetting.Mask); i++ {
		if sg.ScreenSetting.Mask[i] == 1 {
			sg.WriteableIdx = append(sg.WriteableIdx, i)
		}
	}

	// GenScreenSetting 內容建立
	sg.ReelSetGroup = sg.GenScreenSetting.ReelSetGroup

	// 生成函數以及盤面Buffer(避免重複判斷以及重複new盤面)
	if val, ok := genScreenMap[sg.GenScreenSetting.GenReelType]; ok {
		sg.genScreenFn = val
	} else {
		log.Fatal("GenReelType wrong")
	}
	sg.Screen = make([]int16, sg.Cols*sg.Rows)
	return nil
}

// GenScreen 生成盤面熱路徑函數
func (sg *ScreenGenerator) GenScreen() []int16 {
	idx := sg.GenScreenSetting.ReelSetLUT.Pick(sg.core)
	reels := sg.ReelSetGroup[idx].Reels
	return sg.genScreenFn(sg, reels)
}

// GenScreenByReelSetIdx 使用ReelSetGroup中指定輪帶組生成盤面
func (sg *ScreenGenerator) GenScreenByReelSetIdx(i int) []int16 {
	return sg.genScreenFn(sg, sg.ReelSetGroup[i].Reels)
}

// 依照輪軸權重生成盤面(無視mask)
func genScreenByReelIdx(sg *ScreenGenerator, reels []spec.Reel) []int16 {
	cols := sg.Cols
	rows := sg.Rows

	s := sg.Screen
	_ = s[(rows-1)*cols+(cols-1)] // BCE hint

	for col := range cols {
		reel := &reels[col]
		id := reel.ReelLUT.Pick(sg.core)
		length := reel.ReelLength
		for row := range rows {
			s[(row*cols)+col] = reel.ReelSymbols[(id+row)%length]
		}
	}
	return sg.Screen
}

// 依照圖標權重生成盤面
func genScreenBySymbolWeight(sg *ScreenGenerator, reels []spec.Reel) []int16 {
	cols := sg.Cols
	rows := sg.Rows

	s := sg.Screen
	_ = s[(rows-1)*cols+(cols-1)] // BCE hint

	for col := range cols {
		reel := &reels[col]
		for row := range rows {
			id := reel.ReelLUT.Pick(sg.core)
			s[(row*cols)+col] = reel.ReelSymbols[id]
		}
	}
	return sg.Screen
}
