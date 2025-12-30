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

// Package sampler 提供一系列高效能的加權抽樣演算法與工具。
//
// 本檔案 (weightitem.go) 定義了加權排序與抽樣所需的內部輔助結構。
//
// 設計目的：
//   - 提供一個輕量的容器，封裝原始索引 (Index) 與計算後的隨機分數 (Score)。
//   - 支援 WeightedShuffle 與 WeightedReservoirSample 中的排序與堆積操作。
//
// 結構說明：
//   - weightItem: 基本單元。
//   - weightHeap: 實作 heap.Interface 的 Max-Heap，用於K抽樣的動態維護。
//
// 注意：如果某個weights中某一個weight = 0 ，則在WeightedShuffle當中會被排到最後，但K抽樣則永不入選
package sampler

import (
	"cmp"
	"container/heap"
	"math"
	"slices"

	"github.com/zintix-labs/problab/sdk/core"
)

// weightItem 是加權排序中的基本單元。
// 它封裝了原始數據的索引 (Index) 與計算出的隨機權重分數 (Score)。
type weightItem struct {
	idx   int     // 原始數據的 Index
	score float64 // 根據權重與隨機數計算出的排序分數
}

// weightHeap 實作了 heap.Interface，用於維護一個 Max-Heap (最大堆)。
//
// 用途：在 WeightedReservoirSample 中，我們需要保留分數「最小」的前 K 個元素。
// 為此，我們維護一個容量為 K 的 Max-Heap。
// 堆頂 (heap[0]) 存儲的是這 K 個元素中「分數最大」(最爛) 的那個。
// 當新元素的分數比堆頂還小時，代表新元素比堆頂更優秀，我們就將堆頂替換掉。
type weightHeap []weightItem

func (h weightHeap) Len() int { return len(h) }

// Less 實作 Max-Heap 的關鍵：
// 我們希望 Pop() 拿出來的是「最大值」，或者 h[0] 是最大值。
// 在 Go 的 heap 實作中，h[0] 是最小值（Min-Heap）。
// 為了反轉這個行為，當 i 的分數大於 j 時，我們回傳 true。
// 這樣「分數大」的元素會被視為「更小(更優先)」，進而浮到堆頂。
func (h weightHeap) Less(i, j int) bool { return h[i].score > h[j].score }

func (h weightHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *weightHeap) Push(x any) {
	*h = append(*h, x.(weightItem))
}

func (h *weightHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

// -----------------------------------------------------------------------------
// 公開 API (Public APIs)
// -----------------------------------------------------------------------------

// WeightedShuffle 加權不放回抽樣 - 全排列 (Weighted Shuffle without Replacement)
//
// 演算法：Efraimidis-Spirakis Algorithm A-ExpJ
// 參考文獻：2006, "Weighted random sampling with a reservoir"
//
// 核心邏輯：
//  1. 為每個元素 i 生成一個特徵分數 $k_i = U_i^(1/w_i)$。
//     為了數值穩定與效能，實作上使用 Log 轉換： $Score_i = -ln(U_i) / w_i$。
//     其中 $-ln(U_i)$ 即為標準指數分佈 (ExpFloat64)。
//  2. 權重 $w_i$ 越大，分母越大，分數 $Score_i$ 越小。
//  3. 將所有元素按 Score 由小到大排序。
//  4. 排序後的順序即為加權隨機排列的結果。
//
// 特殊處理：
//   - 權重 < 0：Panic (視為錯誤)。
//   - 權重 == 0：分數設為 +Inf，這保證它們會被排在列表的最後面。
//
// 適用場景：
//   - 需要取得完整的隨機排列（如：洗牌、滾輪條生成）。
//   - 抽取的數量 K 接近總數 N。
//
// 複雜度：
//   - 時間：O(N log N) (瓶頸在排序)
//   - 空間：O(N) (需要存儲所有元素的分數)
func WeightedShuffle(c *core.Core, weights []int) []int {
	n := len(weights)
	if n == 0 {
		return []int{}
	}

	// 1. 分數生成 (Score Generation)
	// 直接分配 n 大小的 slice，避免多次 append 的擴容開銷
	items := make([]weightItem, n)

	for i, w := range weights {
		if w < 0 {
			panic("WeightedShuffle: negative weight")
		}
		if w == 0 {
			// 權重為 0 的處理：給予正無窮大分數 (排到最後)
			items[i] = weightItem{idx: i, score: math.Inf(1)}
			continue
		}

		// 核心公式： Score = ExpFloat64 / Weight
		// ExpFloat64 是隨機的「路程」，Weight 是「速度」。
		// Score 代表「跑完所需時間」。時間越短 (Score 越小)，排名越靠前。
		score := c.ExpFloat64() / float64(w)
		items[i] = weightItem{idx: i, score: score}
	}

	// 2. 排序 (Sorting)
	// 依照 Score 由小到大 (Ascending) 排序
	slices.SortFunc(items, func(a, b weightItem) int {
		return cmp.Compare(a.score, b.score)
	})

	// 3. 提取結果 (Extract Indices)
	result := make([]int, n)
	for i, item := range items {
		result[i] = item.idx
	}

	return result
}

// WeightedShuffleWithFilter 加權不放回抽樣 - 全排列但過濾零權重
//
// 這是 WeightedShuffle 的變體，專門用於需要「排除無法選中項目」的場景。
//
// 行為差異：
//   - WeightedShuffle: 回傳長度 N，權重為 0 者排在最後。
//   - WeightedShuffleWithFilter: 回傳長度 M (M <= N)，僅包含權重 > 0 的項目。
//
// 適用場景：
//   - 抽樣結果不應包含無效項目（例如：只列出有中獎的獎項順序）。
func WeightedShuffleWithFilter(c *core.Core, weights []int) []int {
	n := len(weights)
	if n == 0 {
		return []int{}
	}

	// 1. 分數生成 (Score Generation)
	// 預分配容量但長度為 0，動態 append 有效項目
	items := make([]weightItem, 0, n)

	for i, w := range weights {
		if w < 0 {
			panic("WeightedShuffleWithFilter: negative weight")
		}
		// 權重為 0 的元素直接忽略，不加入列表
		if w == 0 {
			continue
		}

		score := c.ExpFloat64() / float64(w)
		items = append(items, weightItem{idx: i, score: score})
	}

	// 2. 排序 (Sorting)
	slices.SortFunc(items, func(a, b weightItem) int {
		return cmp.Compare(a.score, b.score)
	})

	// 3. 提取結果 (Extract Indices)
	result := make([]int, len(items))
	for i, item := range items {
		result[i] = item.idx
	}

	return result
}

// WeightedSample 加權不放回抽樣 - 只取前 K 個 (Weighted Reservoir Sampling)
//
// 演算法：Efraimidis-Spirakis Algorithm A-Res
//
// 核心邏輯：
//
//	維護一個容量為 K 的「領獎台」(Reservoir)，裡面存放著目前分數最小的 K 個元素。
//	使用 Max-Heap 來實作這個領獎台，讓我們能以 O(1) 找到這 K 個人裡面「分數最大」(最該被淘汰) 的人。
//
// 處理流程：
//  1. 遍歷所有元素，權重 < 0 Panic，權重 == 0 跳過。
//  2. 維護一個大小不超過 K 的 Heap。
//  3. 最終從 Heap 彈出結果。
//
// 相比 WeightedShuffle 的優勢：
//  1. 空間複雜度僅為 O(K)：不需要分配 N 大小的記憶體，對 GC 極度友善 (當 N=10000, K=3 時差異巨大)。
//  2. 時間複雜度為 O(N log K)：當 K << N 時，比全排序快得多。
//
// 適用場景：選取少量項目，且容易抽不到。
func WeightedSample(c *core.Core, weights []int, k int) []int {
	n := len(weights)
	// 邊界檢查：若 k <= 0 或無資料，回傳空
	if k <= 0 || n == 0 {
		return []int{}
	}
	// 若要取的數量超過總數，邏輯上等同於全取 (但排序依據權重)
	if k > n {
		k = n
	}

	// 建立一個 Max-Heap (容量為 K)
	// 這裡預分配容量為 k，避免 append 擴容
	h := make(weightHeap, 0, k)

	for i, w := range weights {
		if w < 0 {
			panic("WeightedSample: negative weight")
		}
		// 權重為 0 的元素無法被選中，直接忽略
		if w == 0 {
			continue
		}

		// 計算分數
		score := c.ExpFloat64() / float64(w)

		if h.Len() < k {
			// 1. 如果堆還沒滿，直接放入
			heap.Push(&h, weightItem{idx: i, score: score})
		} else {
			// 2. 如果堆滿了，檢查當前分數是否比「堆裡面最爛(最大)的分數」還小
			// h[0] 是 Max-Heap 的最大值 (目前入選者中分數最高的)
			if score < h[0].score {
				// 踢掉最大的，放入新的小的
				// 優化技巧：直接修改 root 並呼叫 Fix，比 Pop() + Push() 少一次 log K 操作
				h[0] = weightItem{idx: i, score: score}
				heap.Fix(&h, 0)
			}
		}
	}

	// 3. 取出結果 (Extract Results)
	// 注意：如果有效(>0)權重數量 < k，heap 的長度會小於 k。
	// 我們必須使用 h.Len() 作為實際結果長度。
	actualCount := h.Len()
	if actualCount == 0 {
		return []int{}
	}

	result := make([]int, actualCount)
	// 為了讓回傳結果符合「由小到大」(排名先後) 的直覺，我們需要依序 Pop 出來。
	// 由於這是 Max-Heap，Pop 出來的是「最大」的(最後一名)，所以我們倒序填入 result。
	for i := actualCount - 1; i >= 0; i-- {
		item := heap.Pop(&h).(weightItem)
		result[i] = item.idx
	}

	return result
}
