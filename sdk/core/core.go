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

package core

import "github.com/zintix-labs/problab/sdk/core/internal"

// PRNG 定義 Core 所需的亂數來源，需同時支援取樣與狀態保存/還原。
type PRNG interface {
	RAND
	Restorable
}

// Restorable 定義可快照與還原的狀態介面。
type Restorable interface {
	// Snapshot 回傳可用於還原的序列化狀態。
	Snapshot() ([]byte, error)
	// Restore 依序列化狀態還原 PRNG 內部狀態。
	Restore([]byte) error
}

// RAND 定義核心亂數取樣能力。
//
// 為什麼要求同時提供 4 個方法（Uint64 / Float64 / UintN / IntN），而不是只要求 Uint64？
//
// 1) 允許實作針對 32-bit / 64-bit 平台做最佳化
//   - 有些 PRNG 的「原生輸出寬度」是 32-bit（例如以 uint32 為核心），在 32-bit 平台或某些 CPU 上
//     直接產生 uint32/uint 可能更快、更少指令。
//   - 反之也有 64-bit PRNG（例如輸出 uint64），在 64-bit 平台上直接提供 Uint64/UintN 會更自然。
//   - 若合約只要求 Uint64，所有實作都被迫走「先產生 uint64 再轉換/裁切」的路徑，
//     會把 32-bit 友善的 PRNG 退化成比較慢的寫法。
//   - 不同 PRNG 對 bounded 生成可能有更快/更正確的實作（例如使用 32-bit 或 64-bit 的 fast path）。
//     把 IntN/UintN 交由 PRNG 自己實作，能讓每個 PRNG 用最合適的 bounded 策略。
//
// 2) Float64 的精度與生成方式應由 PRNG 決定
//   - Float64 通常希望使用 53-bit mantissa 來生成 [0,1)；但有些實作只提供 32-bit 精度或有更快的路徑。
//   - 讓 PRNG 自己提供 Float64，可以明確表達「精度（32-bit vs 53-bit）」與「效能」取捨。
type RAND interface {
	// Uint64 回傳非負 uint64 亂數。
	Uint64() uint64
	// Float64 回傳 [0,1) 的浮點亂數。
	Float64() float64
	// UintN 回傳 [0,max) 的 uint 亂數，若 max == 0 回傳 0。
	UintN(uint) uint
	// IntN 回傳 [0,max) 的 int 亂數，若 max <= 0 回傳 -1。
	IntN(int) int
}

type PRNGFactory interface {
	// New 以指定 seed 建立新的 PRNG。
	//
	// 合約（很重要）：在同一個實作與同一個版本下，New(seed) 必須是「決定性」的——
	// 也就是相同的 seed 必須產生相同的初始內部狀態與輸出序列。
	//
	// 為什麼只保留 New？
	//   - Problab 需要可重現（審計/回放/併發模擬的多機台派生）。
	//   - seed 的生命週期由 Problab 統一管理：外部未提供時由 Problab 產生並保存 baseSeed，
	//     後續所有 Machine/Sim 皆由 baseSeed 以固定算法派生子 seed。
	//   - 因此 Problab 內部永遠不需要呼叫「不帶 seed 的 New()」，避免行為不一致與難以重現。
	New(int64) PRNG
}

// DefaultPRNG 實作預設的 CoreFactory
type DefaultPRNG struct{}

// New 滿足合約
func (d *DefaultPRNG) New(seed int64) PRNG {
	return internal.NewPCG64WithSeed(seed)
}

func Default() *DefaultPRNG {
	return &DefaultPRNG{}
}

// Core 封裝 PRNG，並提供常用取樣與工具方法。
type Core struct {
	PRNG
}

// New 允許使用外部自實現的 PRNG 建立 Core。
func New(rng PRNG) *Core {
	return &Core{rng}
}

// Pick 從列表中隨機選取一個元素，若列表為空回傳 -1
// 熱路徑中只使用哨兵值回傳
func (c *Core) Pick(src []int) int {
	if len(src) == 0 {
		return -1
	}
	idx := c.IntN(len(src))
	return src[idx]
}

// ShuffleInts 使用 Fisher-Yates (亦稱 Knuth Shuffle) 演算法
// 對[]int進行「就地 (In-place)」隨機重排。
//
// 演算法特性：
//
//  1. 公平性 (Unbiased)：
//     此算法保證所有可能的 N! 種排列組合出現的機率是嚴格相等的 (1/N!)。
//     這解決了傳統 "Naive Shuffle" (每個位置都隨機跟任意位置交換) 導致的機率偏差問題。
//
//  2. 效能 (High Performance)：
//     - 時間複雜度：O(N)，只需要對陣列進行一次線性掃描。
//     - 空間複雜度：O(1)，直接在原記憶體位置交換，實現零配置 (Zero Allocation)。
func (c *Core) ShuffleInts(src []int) {
	if len(src) <= 1 {
		return
	}

	for i := len(src) - 1; i > 0; i-- {
		j := c.IntN(i + 1)
		src[i], src[j] = src[j], src[i]
	}
}
