// Copyright 2025-2026 Zintix Labs
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

package ops

import (
	"github.com/zintix-labs/problab/sdk/layer"
	"github.com/zintix-labs/problab/spec"
)

// FillScreen 堆疊補盤：配合 Gravity 使用，從 fillIdx 開始往上補
// 負值格不補圖，也不消耗輪帶位置。
//
//   - screen: 盤面 (原地修改)
//   - reels: 補盤用的輪帶
//   - fillIdxBuf: 每行開始補的位置 (通常由 Gravity 回傳)
//   - reelPosIdx: 每行目前輪帶讀取到的位置 (Stateful，會被修改)
//   - cols: 盤面寬度
//
// 可選 layers 將補入位置重置為各自的預設值；未傳入時只操作 screen。
// 跳過負值格；新圖標的遊戲狀態由呼叫者之後設定。
// Layer 須無別名；長度不符或 nil 在任何寫入前 panic，其餘參數沿用 FillScreen 契約。
func FillScreen(screen []int16, reels *spec.ReelSet, fillIdxBuf []int, reelPosIdx []int, cols int, layers ...layer.SyncOps) {
	validateLayers(screen, layers)
	for c, startRowPtr := range fillIdxBuf {
		// 如果該行滿了 (startRowPtr < 0)，就跳過
		if startRowPtr < 0 {
			continue
		}

		currentReelPos := reelPosIdx[c]
		strip := reels.Reels[c].ReelSymbols
		stripLen := len(strip)

		// 從起始點往上補到頂 (0)
		for w := startRowPtr; w >= 0; w -= cols {
			if screen[w] < 0 {
				continue
			}
			currentReelPos-- // 先--
			// 處理輪帶回捲
			if currentReelPos < 0 {
				currentReelPos = stripLen - 1
			}
			// 填值
			screen[w] = int16(strip[currentReelPos])
			if len(layers) != 0 {
				for _, lr := range layers {
					lr.ResetIdx(w)
				}
			}
		}
		// 更新狀態回 caller
		reelPosIdx[c] = currentReelPos
	}
}

// FillScreenByHole 穿透補盤：掃描全盤，見縫插針
//
// 相較FillScreen 少了fillStartIdx，直接掃描全盤補足，性能差一點，但更為萬用
//   - screen: 盤面 (原地修改)
//   - reels: 補盤用的輪帶
//   - reelPosIdx: 每行目前輪帶讀取到的位置 (Stateful，會被修改)
//   - cols: 盤面寬度
//   - rows: 盤面高度
//
// 可選 layers 只重置實際補入的零值格，保留既有圖標與負值格的狀態。
// Layer 與初始化責任同 FillScreen；輪帶消耗與原入口一致。
func FillScreenByHole(screen []int16, reels *spec.ReelSet, reelPosIdx []int, cols int, rows int, layers ...layer.SyncOps) {
	validateLayers(screen, layers)
	for c := 0; c < cols; c++ {
		currentReelPos := reelPosIdx[c]
		strip := reels.Reels[c].ReelSymbols
		stripLen := len(strip)

		// 自底向上掃描
		for r := rows - 1; r >= 0; r-- {
			idx := r*cols + c
			// 只有是空位 (0) 才補
			if screen[idx] == 0 {
				currentReelPos-- // 先--
				if currentReelPos < 0 {
					currentReelPos = stripLen - 1
				}
				screen[idx] = int16(strip[currentReelPos])
				if len(layers) != 0 {
					for _, lr := range layers {
						lr.ResetIdx(idx)
					}
				}
			}
		}
		reelPosIdx[c] = currentReelPos
	}
}
