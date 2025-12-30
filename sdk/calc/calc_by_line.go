package calc

import (
	"github.com/zintix-labs/problab/sdk/buf"
)

// CalcByLine 依線下注規則計算盤面分數，會同時處理 LTR/RTL 兩種方向。
func CalcByLine(betMult int, screen []int16, gmr *buf.GameModeResult, sc *ScreenCalculator) {

	// 取得計算結果
	paidMask := sc.paidMask

	// 沒有可計分圖標，直接回傳空結果
	if paidMask == 0 {
		return
	}
	// LTR
	if sc.LTR {
		calcOneDirection(betMult, screen, sc, 0, gmr)
	}
	// RTL
	if sc.RTL {
		calcOneDirection(betMult, screen, sc, 1, gmr)
	}
}

// calcOneDirection 這個函式做「方向專屬的準備工作」與「每條線的外層流程」，熱路徑仍呼叫 calcLineLTR
func calcOneDirection(betMult int, screen []int16, sc *ScreenCalculator, direction uint8, cr *buf.GameModeResult) {
	cols := sc.Cols
	starts := sc.LineTableIndex

	// 方向專屬的平坦線表
	var flat []int16
	if direction == 1 {
		flat = sc.LineTableFlatRTL
	} else {
		flat = sc.LineTableFlat
	}

	// 局部快取
	wildMask := sc.wildMask
	paidMask := sc.paidMask
	payFlat := sc.PayTableFlat
	payIdx := sc.PayTableIndex

	// 逐線
	for lineIdx := 0; lineIdx < sc.LineCount; lineIdx++ {
		start := starts[lineIdx]
		line := flat[start : start+cols] // 固定長度，BCE 友善

		// 熱路徑：計算單線分數
		sym, hitLen, win := int16(0), 0, 0

		// ── 首格初始化（迴圈外處理，避免每圈 pos==0 分支） ──
		firstSym := screen[line[0]] // wild-only 分支用的基準符號
		wildRun := 0                // 連續 wild 前綴長度
		normSym := int16(-1)        // 首個非 wild 且可計分的符號
		normRun := 0                // normal 串長（包含前綴 wild）

		if wildMask&(1<<uint(firstSym)) != 0 {
			wildRun = 1
		} else {
			// 首格非 wild：若不可計分則此線 0 分
			if paidMask&(1<<uint(firstSym)) == 0 {
				continue // 下一線
			}
			normSym = firstSym
			normRun = 1
		}

		// ── 主迴圈：從第二格開始 ──
		for pos := 1; pos < cols; pos++ {
			s := screen[line[pos]]
			isWild := (wildMask&(1<<uint(s)) != 0)

			// A) 純 wild 前綴：只有「前面全 wild」且本格仍 wild 或等於 firstSym 才延長
			if (normSym < 0) && (wildRun == pos) && isWild {
				wildRun++
				continue
			}

			// B) 尚未起手 normal，且本格是首個非 wild
			if normSym < 0 && !isWild {
				// 只有在這裡才需要檢查是否可計分；其餘回合不做 isPaid 減少分支
				if paidMask&(1<<uint(s)) == 0 {
					// 首個非 wild 也不可計分 → 只剩 wild-only 分支可比，直接結束本線
					break
				}
				normSym = s
				normRun = wildRun + 1 // 把前綴 wild 併入 normal 串
				continue
			}

			// C) normal 已起手：允許同符號或 wild 代任延長
			if normSym >= 0 {
				if s == normSym || isWild {
					normRun++
					continue
				}
				break
			}
			// 尚未遇到首個非 wild 且不屬於純前綴（例如中間又遇到非 firstSym 的非 wild），直接跳出或下一格
			// 依現行語意：此處什麼都不做，繼續下一格
		}

		// ── 查表並取較大者（CSR：base + (len-1)）──
		wildWin := 0
		if wildRun > 0 {
			wildWin = betMult * payFlat[payIdx[firstSym]+(wildRun-1)]
		}

		normWin := 0
		if normRun > 0 {
			normWin = betMult * payFlat[payIdx[normSym]+(normRun-1)]
		}

		if wildWin > normWin {
			sym = firstSym
			hitLen = wildRun
			win = wildWin
		} else {
			sym = normSym
			hitLen = normRun
			win = normWin
		}

		// 計分
		if win > 0 {
			cr.RecordDetail(win, sym, lineIdx, hitLen, 0, direction, line[:hitLen])
		}
	}
}
