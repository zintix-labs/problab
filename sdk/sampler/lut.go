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
// 本檔案 (lut.go) 實作了查找表 (Look-Up Table) 加權抽樣演算法。
//
// 演算法原理：
//   - 空間換時間：將權重展開為一個長陣列，每個索引出現的次數等於其權重。
//   - 抽樣：直接生成一個隨機索引存取陣列，即為 O(1) 操作。
//
// 特性：
//   - 建表時間：O(sum(weights))
//   - 抽樣時間：O(1)，極快，只需一次 IntN。
//   - 空間複雜度：O(sum(weights))，與權重總和成正比。
//
// 適用場景：
//   - 權重總和較小 (建議 < 100,000)。
//   - 選項數量不多。
//   - 對抽樣效能有極致要求 (比 AliasTable 少一次隨機數生成)。
//   - 用於一般 Slot 遊戲的滾輪權重、簡單 Feature 選項等。
//
// 對比 AliasTable：
//   - 當權重總和很大時 (如百萬級)，LUT 會佔用大量記憶體，此時應選用 AliasTable。

package sampler

import (
	"fmt"
	"math"

	"github.com/zintix-labs/problab/sdk/core"
)

const maxLUTCap uint64 = 10_000_000 // 約 80MB (int slice)

// Look-Up Table (LUT) 加權抽樣
//
// LUT 是「以空間換取時間」的加權抽樣：
// 建表時直接展開所有權重，抽樣時只做一次 IntN
//
// LUT 的時間/空間特性：
//
//   - 建表時間 O(sum(weights))，抽樣 O(1)。
//
//   - 記憶體消耗與權重總和成正比，sum(weights) 很大時不建議使用。
//
// 構建函數: BuildLookUpTable(src []Integers) LUT
//
// 舉例 :
//
// 三個物品，對應權重分別為[3,5,0]
//
// i.e. 權重總和為 8
//
// 抽到第一個物品(idx = 0)的機率為 3/8
// 抽到第二個物品的機率為 5/8 抽到第三個物品的機率為 0/8
//
// LUT 轉換展開 -> [0,0,0,1,1,1,1,1] 這樣只要從Slice當中直接取一個值，就符合抽樣
//
// 建議使用情境：
//   - LUT：權重總和（acc）較小、元素數量不大，且需要非常快速的抽樣時使用。
//   - AliasTable：權重總和可能很大、需要穩定 O(1) 抽樣且避免巨大切片時，優先選用 AliasTable。
//
// 基本判斷原則：
// 總和在 100_000 以下建議使用LUT，超過則建議AliasTable
type LUT []int

// BuildLUT 根據權重列表建立查找表。
//
// src 為任意非負整數權重列表（支援各種 Integers 約束），若遇到負權重會 panic。
//
// 建表流程：
// 1. 先累加 acc 取得權重總和，用來預先配置 lut 容量。
// 2. 對每個元素 i，將其索引重複寫入 lut v 次（v 為權重）。
//
// LUT 的時間/空間特性：
//   - 建表時間 O(sum(weights))，抽樣 O(1)。
//   - 記憶體消耗與權重總和成正比，sum(weights) 很大時不建議使用。
func BuildLUT[T Integers](src []T) LUT {
	if len(src) == 0 {
		return []int{}
	}

	acc := uint64(0)
	// 累加權重總和，用於後續預估 LUT 長度並避免 overflow
	for _, v := range src {
		if v < 0 {
			panic("lut: negative value encountered")
		}
		uv := uint64(v)
		if acc > math.MaxUint64-uv {
			panic("lut: total weight overflow uint64 range")
		}
		acc += uv
	}

	if acc == 0 {
		panic("lut: all weights are zero")
	}

	if acc > maxLUTCap {
		panic(fmt.Sprintf("lut: total weight %d exceeds limit %d, use alias table instead", acc, maxLUTCap))
	}

	lut := make([]int, 0, int(acc))
	for i, v := range src {
		// 將索引 i 重複寫入 v 次，建立展開後的查找表
		for j := T(0); j < v; j++ {
			lut = append(lut, i)
		}
	}
	return lut
}

// Pick 會透過 Core 的 RNG 從 LUT 中隨機位置取一個值
// 若 lut 為空，回傳 -1
// LUT 抽樣與是 O(1)
func (l LUT) Pick(c *core.Core) int {
	return c.Pick(l)
}
