package ops

// Clear 消除標記位置的圖標(改為0)
//
//   - screen: 盤面數據 (將被原地修改)
//   - hitmap: 消除位置 (這些位置會被標記為 0)
func Clear(screen []int16, hitmap []int16) {
	for _, v := range hitmap {
		if v < int16(len(screen)) { // 簡單防禦
			screen[v] = 0
		}
	}
}
