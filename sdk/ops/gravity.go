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

package ops

import "github.com/zintix-labs/problab/sdk/layer"

// Gravity 執行標準的單格圖標下落邏輯 (Column-wise compact)
// 負值格保持原位與原值；圖標可跨過負值格，0 代表可補圖的空格。
//
//   - screen: 盤面數據 (將被原地修改)
//   - cols, rows: 盤面維度
//   - fillIdxBuf: (選用) 用於回傳每列需要補圖的位置，若為 nil 則內部不紀錄
//
// 可選 layers 隨圖標複製到目的格，最後重置有效空格；未傳入時只操作 screen。
// 負值格的狀態保持原位原值；固定格子狀態不要傳入。
// 呼叫前須符合 layer.Layer 的無別名契約；所有 Layer 長度在寫入前檢查，不符或 nil 會 panic。
func Gravity(screen []int16, cols int, rows int, fillIdxBuf []int, layers ...layer.SyncOps) {
	validateLayers(screen, layers)
	// 掉落 (原地壓縮演算法)
	for c := 0; c < cols; c++ {
		wp := (rows-1)*cols + c // Write Pointer (寫入位置，從底開始)
		for wp >= 0 && screen[wp] < 0 {
			wp -= cols
		}

		// 自底向上掃描
		for r := rows - 1; r >= 0; r-- {
			rp := r*cols + c // Read Pointer
			if screen[rp] > 0 {
				if rp != wp {
					screen[wp] = screen[rp]
					if len(layers) != 0 {
						for _, lr := range layers {
							lr.Copy(wp, rp)
						}
					}
				}
				wp -= cols
				for wp >= 0 && screen[wp] < 0 {
					wp -= cols
				}
			}
		}

		// 3. 紀錄補圖起始點 (如果調用者需要)
		if fillIdxBuf != nil && c < len(fillIdxBuf) {
			fillIdxBuf[c] = wp
		}

		// 4. 上方剩餘空間補 0
		for w := wp; w >= 0; w -= cols {
			if screen[w] >= 0 {
				screen[w] = 0
				if len(layers) != 0 {
					for _, lr := range layers {
						lr.ResetIdx(w)
					}
				}
			}
		}
	}
}
