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

type GenScreenWithDampFn func(*ScreenGenerator, []spec.Reel) ([]int16, []int16)

// genScreenMap 將 GenReelType 與實際生成函式綁定，初始化時決定後便不再修改。
var genScreenMap = map[spec.GenReelType]GenScreenFn{
	spec.GenReelByReelIdx:      genScreenByReelIdx,
	spec.GenReelBySymbolWeight: genScreenBySymbolWeight,
}

var genScreenWithDampMap = map[spec.GenReelType]GenScreenWithDampFn{
	spec.GenReelByReelIdx:      genScreenWithDampByReelIdx,
	spec.GenReelBySymbolWeight: genScreenWithDampBySymbolWeight,
}

// ScreenGenerator 保存生成盤面所需的所有狀態。
// 會快取列數、行數、查表資料與輸出緩衝，以避免重複配置與計算。
type ScreenGenerator struct {
	core             *core.Core
	ScreenSetting    *spec.ScreenSetting
	GenScreenSetting *spec.GenScreenSetting
	// ScreenSetting 內容建立
	Cols int
	Rows int

	// GenScreenSetting 內容建立
	ReelSetGroup []spec.ReelSet
	// 生成函數以及盤面Buffer(避免重複判斷以及重複new盤面)
	genScreenFn         GenScreenFn
	genScreenWithDampFn GenScreenWithDampFn
	Screen              []int16 // 只有實際運算用的盤面
	ScreenWithDamp      []int16 // 包含Damp
}

// NewScreenGenerator 根據設定與核心亂數器建立生成器，並立即完成初始化，
// 讓之後的生成流程可以免配置快速執行。
func NewScreenGenerator(core *core.Core, screenSetting *spec.ScreenSetting, genScreenSetting *spec.GenScreenSetting) *ScreenGenerator {
	result := &ScreenGenerator{
		core:             core,
		ScreenSetting:    screenSetting,
		GenScreenSetting: genScreenSetting,
	}
	_ = result.init()
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

	// GenScreenSetting 內容建立
	sg.ReelSetGroup = sg.GenScreenSetting.ReelSetGroup

	// 生成函數以及盤面Buffer(避免重複判斷以及重複new盤面)
	if val, ok := genScreenMap[sg.GenScreenSetting.GenReelType]; ok {
		sg.genScreenFn = val
		sg.genScreenWithDampFn = genScreenWithDampMap[sg.GenScreenSetting.GenReelType]
	} else {
		log.Fatal("GenReelType wrong")
	}
	sg.Screen = make([]int16, sg.ScreenSetting.ScreenSize)
	sg.ScreenWithDamp = make([]int16, sg.ScreenSetting.FullScreenSize)
	return nil
}

// GenScreen 生成盤面熱路徑函數。回傳重用 buffer，需要保留時由外部複製。
// 不更新 ScreenWithDamp；圖標權重模式仍消耗展示圖標的抽樣。
func (sg *ScreenGenerator) GenScreen() []int16 {
	idx := sg.GenScreenSetting.ReelSetLUT.Pick(sg.core)
	reels := sg.ReelSetGroup[idx].Reels
	return sg.genScreenFn(sg, reels)
}

// GenScreenByReelSetIdx 使用ReelSetGroup中指定輪帶組生成盤面
func (sg *ScreenGenerator) GenScreenByReelSetIdx(i int) []int16 {
	return sg.genScreenFn(sg, sg.ReelSetGroup[i].Reels)
}

// GenScreenWithDamp 回傳展示盤面與正常盤面，兩者都是獨立的重用 buffer。
// 生成後修改 Screen 不會同步修改 ScreenWithDamp。生成器不可並行使用。
func (sg *ScreenGenerator) GenScreenWithDamp() ([]int16, []int16) {
	idx := sg.GenScreenSetting.ReelSetLUT.Pick(sg.core)
	return sg.genScreenWithDampFn(sg, sg.ReelSetGroup[idx].Reels)
}

// GenScreenWithDampByReelSetIdx 使用指定輪帶組生成展示盤面與正常盤面。
func (sg *ScreenGenerator) GenScreenWithDampByReelSetIdx(i int) ([]int16, []int16) {
	return sg.genScreenWithDampFn(sg, sg.ReelSetGroup[i].Reels)
}

func genScreenWithDampByReelIdx(sg *ScreenGenerator, reels []spec.Reel) ([]int16, []int16) {
	cols := sg.Cols
	rows := sg.Rows
	damp := sg.ScreenSetting.Damp
	fullRows := rows + damp*2
	trim := sg.ScreenSetting.Trim
	s := sg.Screen
	full := sg.ScreenWithDamp

	for col := range cols {
		reel := &reels[col]
		idx := reel.ReelLUT.Pick(sg.core)
		length := reel.ReelLength
		top := trim[col]
		for row := 0; row < top; row++ {
			s[row*cols+col] = -1
			full[row*cols+col] = -1
		}
		pos := (idx + top%length - damp%length) % length
		if pos < 0 {
			pos += length
		}
		for fullRow := top; fullRow < fullRows; fullRow++ {
			symbol := reel.ReelSymbols[pos]
			full[fullRow*cols+col] = symbol
			row := fullRow - damp
			if row >= top && row < rows {
				s[row*cols+col] = symbol
			}
			pos++
			if pos == length {
				pos = 0
			}
		}
	}
	return full, s
}

// 依照輪軸權重生成盤面
// idx 仍代表未裁切盤面的 row 0，Trim 不改變輪帶位置語意。
func genScreenByReelIdx(sg *ScreenGenerator, reels []spec.Reel) []int16 {
	cols := sg.Cols
	rows := sg.Rows
	trim := sg.ScreenSetting.Trim
	s := sg.Screen
	_ = s[(rows-1)*cols+(cols-1)] // BCE hint

	for col := range cols {
		reel := &reels[col]
		idx := reel.ReelLUT.Pick(sg.core)
		length := reel.ReelLength
		top := trim[col]

		// 每次重新寫入trim，避免上一輪遊戲修改過 buffer。
		for row := 0; row < top; row++ {
			s[row*cols+col] = -1 // -1 代表不可達區域
		}

		pos := (idx + top%length) % length
		for row := top; row < rows; row++ {
			s[row*cols+col] = reel.ReelSymbols[pos]

			pos++
			if pos == length {
				pos = 0
			}
		}
	}
	return s
}

// 依照圖標權重生成盤面
func genScreenBySymbolWeight(sg *ScreenGenerator, reels []spec.Reel) []int16 {
	genScreenSymbolWeight(sg, reels, false)
	return sg.Screen
}

func genScreenWithDampBySymbolWeight(sg *ScreenGenerator, reels []spec.Reel) ([]int16, []int16) {
	genScreenSymbolWeight(sg, reels, true)
	return sg.ScreenWithDamp, sg.Screen
}

// 先抽完整正常盤面（含 Trim），再逐軸抽上 Damp、下 Damp。
// 兩個入口共用抽樣順序，只有是否保存展示盤面的差別。
func genScreenSymbolWeight(sg *ScreenGenerator, reels []spec.Reel, withDamp bool) {
	cols := sg.Cols
	rows := sg.Rows
	damp := sg.ScreenSetting.Damp
	trim := sg.ScreenSetting.Trim

	s := sg.Screen
	_ = s[(rows-1)*cols+(cols-1)] // BCE hint

	for col := range cols {
		reel := &reels[col]
		for row := range rows {
			id := reel.ReelLUT.Pick(sg.core)
			if row < trim[col] {
				s[row*cols+col] = -1
			} else {
				s[row*cols+col] = reel.ReelSymbols[id]
			}
		}
	}
	full := sg.ScreenWithDamp
	if withDamp {
		for col := range cols {
			for row := 0; row < trim[col]; row++ {
				full[row*cols+col] = -1
			}
			for row := trim[col]; row < rows; row++ {
				full[(row+damp)*cols+col] = s[row*cols+col]
			}
		}
	}
	for col := range cols {
		reel := &reels[col]
		for i := range damp {
			id := reel.ReelLUT.Pick(sg.core)
			if withDamp {
				full[(trim[col]+i)*cols+col] = reel.ReelSymbols[id]
			}
		}
		for i := range damp {
			id := reel.ReelLUT.Pick(sg.core)
			if withDamp {
				full[(rows+damp+i)*cols+col] = reel.ReelSymbols[id]
			}
		}
	}
}
