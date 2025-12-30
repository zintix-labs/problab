package ops

import "github.com/zintix-labs/problab/spec"

// FillScreen 堆疊補盤：配合 Gravity 使用，從 fillIdx 開始往上補
//
//   - screen: 盤面 (原地修改)
//   - reels: 補盤用的輪帶
//   - fillIdxBuf: 每行開始補的位置 (通常由 Gravity 回傳)
//   - reelPosIdx: 每行目前輪帶讀取到的位置 (Stateful，會被修改)
//   - cols: 盤面寬度
func FillScreen(screen []int16, reels *spec.ReelSet, fillIdxBuf []int, reelPosIdx []int, cols int) {
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
			currentReelPos-- // 先--
			// 處理輪帶回捲
			if currentReelPos < 0 {
				currentReelPos = stripLen - 1
			}
			// 填值
			screen[w] = int16(strip[currentReelPos])
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
func FillScreenByHole(screen []int16, reels *spec.ReelSet, reelPosIdx []int, cols int, rows int) {
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
			}
		}
		reelPosIdx[c] = currentReelPos
	}
}
