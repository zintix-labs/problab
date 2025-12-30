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

// clusterBuf 優化版：移除了 union/adj 相關的複雜結構，只保留 BFS 必要的緩衝
type clusterBuf struct {
	rows, cols int
	n          int

	// BFS 佇列
	q []int
	// 記錄當前 cluster 找到的所有 cell (包含 wild)
	hits []int16

	// visited 記錄「普通符號」是否已被任何 cluster 使用過 (本次 Spin 全局有效)
	// 使用 bool 或 int (0/1) 均可，這裡為了重用性使用 int 作為標記
	visited []bool

	// wildMark 記錄「Wild 符號」在「當前 cluster」是否已被訪問 (只在當前 cluster 有效)
	// 配合 wildEpoch 使用，避免每次清零
	wildMark  []int
	wildEpoch int
}

// resetSizes 只調整容量，不清內容
func (b *clusterBuf) resetSizes(rows int, cols int) {
	b.rows, b.cols = rows, cols
	b.n = rows * cols
	needN := b.n

	if cap(b.visited) < needN {
		b.visited = make([]bool, needN)
	} else {
		b.visited = b.visited[:needN]
	}

	if cap(b.wildMark) < needN {
		b.wildMark = make([]int, needN)
	} else {
		b.wildMark = b.wildMark[:needN]
	}

	if cap(b.q) < needN {
		b.q = make([]int, 0, needN)
	}
	// q 和 hits 每次使用前會歸零，這裡不需要 reset cap，只需確保存在
	if cap(b.hits) < needN {
		b.hits = make([]int16, 0, needN)
	}
}

// CalcByClusterOptimized 直接在 Grid 上進行 BFS，移除了 buildUnions 階段。
// 效能優勢：減少了 50% 以上的記憶體分配與訪問，Cache Miss 大幅降低。
func CalcByCluster(betMult int, screen []int16, gmr *buf.GameModeResult, sc *ScreenCalculator) {
	rows, cols := sc.Rows, sc.Cols
	b := sc.clusterBuf
	if b == nil {
		sc.clusterBuf = &clusterBuf{}
		b = sc.clusterBuf
	}
	// 初始化 Buffer 大小
	b.resetSizes(rows, cols)

	// 重置 Global Visited (普通符號每個 Spin 清一次)
	// Go 編譯器對 range loop clear 有優化，速度極快 (memclr)
	for i := range b.visited {
		b.visited[i] = false
	}
	// wildMark 不用清，依靠 epoch 區分
	// 如果 epoch 溢位才清一次 (極少發生)
	b.wildEpoch++
	if b.wildEpoch < 0 { // handle overflow
		b.wildEpoch = 1
		for i := range b.wildMark {
			b.wildMark[i] = 0
		}
	}

	n := b.n
	wildMask := sc.wildMask
	paidMask := sc.paidMask
	payFlat := sc.PayTableFlat
	payIdx := sc.PayTableIndex
	minPay := sc.minPayCount

	isWild := func(s int16) bool { return ((wildMask >> uint(s)) & 1) != 0 }
	isPaid := func(s int16) bool { return ((paidMask >> uint(s)) & 1) != 0 }

	// 遍歷每一個格子作為潛在的 Cluster 起點
	for i := 0; i < n; i++ {
		sym := screen[i]

		// 1. 如果是 Wild，跳過 (Wild 不能作為 Cluster 的 "主體" 起始，它只能依附)
		if isWild(sym) {
			continue
		}
		// 2. 如果該符號不派彩，跳過
		if !isPaid(sym) {
			continue
		}
		// 3. 如果該格已經屬於某個已計算過的 Cluster，跳過
		if b.visited[i] {
			continue
		}

		// --- 開始一個新的 Cluster ---

		// 每次新的 Cluster 計算，Wild 的訪問狀態要重置 (使用 Epoch 技巧)
		b.wildEpoch++
		currentWildEpoch := b.wildEpoch

		// 初始化 BFS
		b.q = b.q[:0]
		b.hits = b.hits[:0] // 重用 result buffer

		// 加入起點
		b.q = append(b.q, i)
		b.visited[i] = true
		b.hits = append(b.hits, int16(i))

		clusterSize := 0 // 實際長度 (普通 + Wild)
		head := 0

		// BFS Loop
		for head < len(b.q) {
			// Pop
			curr := b.q[head]
			head++
			clusterSize++

			r := curr / cols
			c := curr % cols

			// 定義鄰居檢查邏輯
			checkNeighbor := func(next int) {
				ns := screen[next]

				// 情況 A: 鄰居是 Wild
				if isWild(ns) {
					// 檢查這個 Wild 在「當前 Cluster」是否已經被訪問過
					if b.wildMark[next] != currentWildEpoch {
						b.wildMark[next] = currentWildEpoch // 標記為本次已訪問
						b.q = append(b.q, next)
						b.hits = append(b.hits, int16(next))
					}
					return
				}

				// 情況 B: 鄰居是同種符號
				if ns == sym {
					// 檢查這個符號在「全盤」是否已被訪問過
					if !b.visited[next] {
						b.visited[next] = true // 標記為全局已訪問，未來不會再作為起點
						b.q = append(b.q, next)
						b.hits = append(b.hits, int16(next))
					}
				}
			}

			// 展開四個方向
			if c > 0 {
				checkNeighbor(curr - 1)
			}
			if c+1 < cols {
				checkNeighbor(curr + 1)
			}
			if r > 0 {
				checkNeighbor(curr - cols)
			}
			if r+1 < rows {
				checkNeighbor(curr + cols)
			}
		}

		// --- 算分 ---

		// 1. 檢查最小連線數
		symInt := int(sym)
		if clusterSize < minPay[symInt] {
			continue
		}

		// 2. 查表
		base := payIdx[symInt]
		end := len(payFlat)
		if symInt+1 < len(payIdx) {
			if payIdx[symInt+1] < end {
				end = payIdx[symInt+1]
			}
		}

		// 夾斷 (Clamp) 到最大賠率長度
		idx := base + (clusterSize - 1)
		if idx >= end {
			idx = end - 1
		}

		pay := payFlat[idx]
		if pay > 0 {
			win := pay * betMult
			// 3. 寫入結果
			gmr.RecordDetail(win, sym, 0, clusterSize, 0, 0, b.hits)
		}
	}
}
