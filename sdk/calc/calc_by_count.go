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

// CalcByCount 依 Collect 下注規則計算盤面分數，支援 wild 代任計數。
func CalcByCount(betMult int, screen []int16, gmr *buf.GameModeResult, sc *ScreenCalculator) {
	// 整理圖標位置以及顆數
	calcSymbolCounts(screen, sc)

	// 判斷得分
	calcByCollect(betMult, sc, gmr)

}

func calcSymbolCounts(screen []int16, sc *ScreenCalculator) {
	cols, rows := sc.Cols, sc.Rows
	rc := rows * cols
	symN := len(sc.symbolCounts)

	// 清每局統計（不要重配）
	clear(sc.symbolCounts)
	// hitMapFlat 不用清，覆寫即可

	cnt := sc.symbolCounts     // [symN]
	hits := sc.hitMapFlat      // [symN*rows*cols]
	whits := sc.wildHitMapFlat // [rows*cols]
	isWild := sc.wildMask
	wCnt := 0

	_ = screen[rc-1]          // BCE hint for screen
	for i := 0; i < rc; i++ { // 線性掃描
		s := int(screen[i])
		if uint(s) >= uint(symN) { // 防 -1 / 越界
			continue
		}
		// wild 當作一種獨立計算
		if (isWild & (1 << uint(s))) != 0 {
			whits[wCnt] = int16(i)
			wCnt++
		}

		// 命中位置：緊湊打包（每符號自己的連續區段）
		k := cnt[s] // 第 k+1 次遇到 s
		hits[s*rc+k] = int16(i)
		cnt[s]++
	}
	sc.wildCounts = wCnt
}

func calcByCollect(betMult int, sc *ScreenCalculator, gmr *buf.GameModeResult) *buf.GameModeResult {
	cr := gmr
	cols, rows := sc.Cols, sc.Rows
	rc := cols * rows

	payFlat := sc.PayTableFlat
	payIdx := sc.PayTableIndex
	cnt := sc.symbolCounts
	wCnt := sc.wildCounts
	hits := sc.hitMapFlat
	whits := sc.wildHitMapFlat
	isPaid := sc.paidMask
	isWild := sc.wildMask

	// 縮 cap，避免 append/擴容波及底層 array（雖然目前只讀）
	whitsView := whits[:wCnt:wCnt]

	for s := 0; s < len(cnt); s++ {
		bit := uint64(1) << uint(s)
		// 只評分 paytable 有定義（可計分）的圖標；wild 是否可獨立計分交由 paytable 控制
		if (isPaid & bit) == 0 {
			continue
		}

		base := payIdx[s]

		// A) Wild 自身計分（少見，但若 paytable 有定義就支援）
		if (isWild & bit) != 0 {
			p := payFlat[base+(wCnt-1)] // off-by-one 修正：index = base + (count-1)
			if p > 0 {
				p *= betMult
				// BCE hint：協助編譯器消去 seg 的邊界檢查
				_ = whits[wCnt-1]
				cr.RecordDetail(p, int16(s), 0, wCnt, 0, 0, whitsView)
			}

			continue // wild 自身若可計分，視為獨立一筆；不影響 normal 的加成規則
		}

		// B) Normal + Wild 代任（collect 規則）：normal 的計數可以加上全盤 wild 顆數
		c := cnt[s]
		total := c + wCnt
		if total <= 0 {
			continue
		}

		p := payFlat[base+(total-1)] // off-by-one 修正
		p *= betMult
		if p <= 0 {
			continue
		}

		start := s * rc
		if c > 0 {
			// BCE hint：幫後面的切片消邊界檢查
			_ = hits[start+c-1]
		}
		if wCnt > 0 {
			_ = whits[wCnt-1]
		}

		seg1 := hits[start : start+c] // 自身命中（可能為空）
		seg2 := whitsView             // wild 全段（可能為空）

		cr.RecordDetailSegments(p, int16(s), 0, total, 0, 0, seg1, seg2)
	}

	return gmr
}
