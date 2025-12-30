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

package calc

import "github.com/zintix-labs/problab/sdk/buf"

// CalcByWay 依 Ways 下注規則計算盤面分數，支援 LTR/RTL 雙向。
func CalcByWay(betMult int, screen []int16, gmr *buf.GameModeResult, sc *ScreenCalculator) {
	// 計算核心表
	calcSymbolInCols(screen, sc)

	// 評分
	if sc.LTR {
		calcByWayLTR(betMult, screen, gmr, sc)
	}
	if sc.RTL {
		calcByWayRTL(betMult, screen, gmr, sc)
	}
}

func calcSymbolInCols(screen []int16, sc *ScreenCalculator) {
	cols, rows := sc.Cols, sc.Rows
	rc := rows * cols
	symN := len(sc.symbolCounts)

	// 清每局統計（不要重配）
	clear(sc.symbolInCols)
	clear(sc.symbolCounts)
	clear(sc.wildInCols)
	// hitMapFlat 不用清，覆寫即可

	wArr := sc.wildInCols      // [1 * cols] 所有wild當作一種
	arr := sc.symbolInCols     // [symN*cols]
	cnt := sc.symbolCounts     // [symN]
	hits := sc.hitMapFlat      // [symN*rows*cols]
	wHits := sc.wildHitMapFlat // [rows*cols]
	isWild := sc.wildMask
	wCnt := 0

	_ = screen[rc-1] // BCE hint for screen
	for c := 0; c < cols; c++ {
		colBase := c // 用局部變數避免在索引裡反覆做乘法
		for r := 0; r < rows; r++ {
			s := int(screen[r*cols+colBase])
			if uint(s) >= uint(symN) { // 防 -1 / 越界
				continue
			}
			// 每欄計數：用 base + c 的型式讓編譯器較易做 BCE
			baseSymCols := s * cols
			arr[baseSymCols+c]++
			if (isWild & (1 << uint(s))) != 0 {
				wHits[wCnt] = int16(r*cols + c)
				wCnt++
				wArr[c]++
			}

			// 命中位置：緊湊打包（每符號自己的連續區段）
			k := cnt[s] // 第 k+1 次遇到 s
			hits[s*rc+k] = int16(r*cols + c)
			cnt[s]++
		}
	}
}

func calcByWayLTR(betMult int, screen []int16, gmr *buf.GameModeResult, sc *ScreenCalculator) {
	cols, rows := sc.Cols, sc.Rows
	// rc := rows * cols // 若要追加整段 hitmap 可解開並使用

	// 快速別名
	arr := sc.symbolInCols     // [symN*cols]：每符號×欄計數
	hits := sc.hitMapFlat      // [symN*rc]：若要快速整段追加可使用
	wHits := sc.wildHitMapFlat // [rc]
	payIdx := sc.PayTableIndex
	payFlat := sc.PayTableFlat
	wildMask := sc.wildMask
	paidMask := sc.paidMask
	keepMask := wildMask | paidMask
	isWildPaid := (wildMask & paidMask) != 0 // 規則1：wild 若要帶頭必須有自己的算分
	wc := sc.wildInCols                      // [cols]：每欄 wild 聚合總數（已預算）
	symN := len(sc.symbolCounts)

	// 首欄候選去重（≤64 符號 → bitset）
	var seen uint64 = 0

	for r := 0; r < rows; r++ {
		s := screen[r*cols]
		if uint(s) >= uint(symN) {
			continue
		}
		bit := uint64(1) << uint(s)

		// 非 keep 或已處理過 → 略過
		if (keepMask&bit) == 0 || (seen&bit) != 0 {
			continue
		}
		seen |= bit // 紀錄處理過了

		bestLen := 0
		bestWin := 0
		bestComb := 0 // 預留：組合數；目前紀錄為 int
		selfCnt := 0
		wildCnt := 0

		// ── A) wild 帶頭（規則1：必須有自己的算分，且「只算 wild」）──
		if ((wildMask & bit) != 0) && isWildPaid {

			// 起手即計入第 0 欄：run=1，comb=wc[0]
			bestLen = 1
			bestComb = wc[0]
			selfCnt = bestComb
			for c := 1; c < cols; c++ {
				if wc[c] == 0 {
					break
				}
				bestComb *= wc[c]
				bestLen++

				wildCnt += wc[c] // 後續只計入wild當中 都會算到
			}

			base := payIdx[s]
			bestWin = int(payFlat[base+(bestLen-1)])

			// wild 起手情形已完成
			if bestWin > 0 {
				bestWin *= betMult
				start := int(s) * sc.ScreenSize
				seg1 := hits[start : start+selfCnt]
				seg2 := wHits[wc[0] : wc[0]+wildCnt]
				gmr.RecordDetailSegments(bestWin, int16(s), 0, bestLen, bestComb, 0, seg1, seg2)
			}
			continue
		}

		// ── B) normal 帶頭（規則2：第一軸的 wild 不可併入；第二軸起可代任）──
		if (paidMask & bit) != 0 {
			base := int(s) * cols

			// 起手：第 0 欄必定有目標（能進來代表首欄看到此符號），bestLen=1，bestComb=arr[s*cols+0]
			bestLen = 1
			bestComb = arr[base]
			selfCnt = bestComb

			for c := 1; c < cols; c++ { // 從第 2 欄開始可借 wild
				sum := arr[base+c] + wc[c]
				if sum == 0 {
					break
				}
				selfCnt += arr[base+c] // 全計入
				wildCnt += wc[c]       // 全計入
				bestLen++
				bestComb *= sum
			}

			pbase := payIdx[s]
			bestWin = int(payFlat[pbase+(bestLen-1)])
			if bestWin > 0 {
				bestWin *= betMult
				start := int(s) * sc.ScreenSize
				seg1 := hits[start : start+selfCnt]
				seg2 := wHits[wc[0] : wc[0]+wildCnt]
				gmr.RecordDetailSegments(bestWin, int16(s), 0, bestLen, bestComb, 0, seg1, seg2)
			}
		}
	}
}

func calcByWayRTL(betMult int, screen []int16, gmr *buf.GameModeResult, sc *ScreenCalculator) {
	cols, rows := sc.Cols, sc.Rows
	rc := rows * cols

	// 快速別名
	arr := sc.symbolInCols     // [symN*cols]：每符號×欄計數
	hits := sc.hitMapFlat      // [symN*rc]
	wHits := sc.wildHitMapFlat // [rc]
	payIdx := sc.PayTableIndex
	payFlat := sc.PayTableFlat
	wildMask := sc.wildMask
	paidMask := sc.paidMask
	keepMask := wildMask | paidMask
	isWildPaid := (wildMask & paidMask) != 0 // 規則1：wild 若要帶頭必須有自己的算分
	wc := sc.wildInCols                      // [cols]：每欄 wild 聚合總數
	symN := len(sc.symbolCounts)

	// wild 總顆數（不建 off，僅作總量，供 RTL 右端切片回推）
	wildTotal := 0
	for c := 0; c < cols; c++ {
		wildTotal += wc[c]
	}

	// 候選去重（≤64 符號 → bitset）。RTL 從最後一欄挑起手候選
	var seen uint64 = 0

	for r := 0; r < rows; r++ {
		s := int(screen[r*cols+(cols-1)])
		if uint(s) >= uint(symN) {
			continue
		}
		bit := uint64(1) << uint(s)
		if (keepMask&bit) == 0 || (seen&bit) != 0 {
			continue
		}
		seen |= bit

		bestLen := 0
		bestWin := 0
		bestComb := 0 // 組合數
		selfCntR := 0 // 自身在「最右 bestLen 欄」的累積顆數
		wildCntR := 0 // wild 在「最右 bestLen 欄，但不含最後一欄」的累積顆數（normal 起手時）

		// ── A) wild 帶頭（規則1：wild 必須有自己的賠付，且只算 wild 本身）──
		if ((wildMask & bit) != 0) && isWildPaid {
			// 起手即包含最後一欄
			bestLen = 1
			bestComb = wc[cols-1]
			wildCntR = wc[cols-1] // wild-only：累計含最後一欄

			for c := cols - 2; c >= 0; c-- {
				if wc[c] == 0 {
					break
				}
				bestComb *= wc[c]
				bestLen++
				wildCntR += wc[c]
			}

			base := payIdx[s]
			bestWin = int(payFlat[base+(bestLen-1)])
			if bestWin > 0 {
				bestWin *= betMult
				// 取最右 bestLen 欄的 wild 命中（含最後一欄）
				seg := wHits[wildTotal-wildCntR : wildTotal]
				gmr.RecordDetail(bestWin, int16(s), 0, bestLen, bestComb, 1, seg)
			}
			continue
		}

		// ── B) normal 帶頭（規則2：最後一欄的 wild 不可併入；往左可代任）──
		if (paidMask & bit) != 0 {
			base := s * cols

			// 起手：最後一欄必定有目標（能進來代表該欄看到此符號）
			bestLen = 1
			bestComb = arr[base+(cols-1)]
			selfCntR = bestComb
			wildCntR = 0 // 最後一欄不可借 wild

			for c := cols - 2; c >= 0; c-- { // 從倒數第 2 欄開始可借 wild
				sum := arr[base+c] + wc[c]
				if sum == 0 {
					break
				}
				bestLen++
				bestComb *= sum
				selfCntR += arr[base+c]
				wildCntR += wc[c]
			}

			pbase := payIdx[s]
			bestWin = int(payFlat[pbase+(bestLen-1)])
			if bestWin > 0 {
				bestWin *= betMult
				// 自身：最右 bestLen 欄的自身命中
				symTotal := sc.symbolCounts[s]
				segSelf := hits[s*rc+(symTotal-selfCntR) : s*rc+symTotal]

				// wild：最右 bestLen 欄，但不含最後一欄（normal 起手的規則）
				wildExLast := wildTotal - wc[cols-1]
				segWild := wHits[wildExLast-wildCntR : wildExLast]

				gmr.RecordDetailSegments(bestWin, int16(s), 0, bestLen, bestComb, 1, segSelf, segWild)
			}
		}
	}
}
