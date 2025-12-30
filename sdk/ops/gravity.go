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

// Gravity 執行標準的單格圖標下落邏輯 (Column-wise compact)
//
//   - screen: 盤面數據 (將被原地修改)
//   - cols, rows: 盤面維度
//   - fillIdxBuf: (選用) 用於回傳每列需要補圖的位置，若為 nil 則內部不紀錄
func Gravity(screen []int16, cols int, rows int, fillIdxBuf []int) {
	// 掉落 (原地壓縮演算法)
	for c := 0; c < cols; c++ {
		wp := (rows-1)*cols + c // Write Pointer (寫入位置，從底開始)

		// 自底向上掃描
		for r := rows - 1; r >= 0; r-- {
			rp := r*cols + c // Read Pointer
			if screen[rp] != 0 {
				if rp != wp {
					screen[wp] = screen[rp]
				}
				wp -= cols
			}
		}

		// 3. 紀錄補圖起始點 (如果調用者需要)
		if fillIdxBuf != nil && c < len(fillIdxBuf) {
			fillIdxBuf[c] = wp
		}

		// 4. 上方剩餘空間補 0
		for w := wp; w >= 0; w -= cols {
			screen[w] = 0
		}
	}
}
