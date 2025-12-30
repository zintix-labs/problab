// Package sampler 提供一系列高效能的加權抽樣演算法與工具。
//
// 本檔案 (aliastable.go) 實作了 Vose's Alias Method 加權抽樣演算法 (整數優化版)。
//
// 演算法原理：
//   - 將任意離散分佈轉換為均勻分佈的組合。
//   - 每個槽位 (Bucket) 只存放「自己」和「別名 (Alias)」兩個選項。
//   - 抽樣時先選槽位，再根據機率決定是自己還是別名。
//
// 特性：
//   - 建表時間：O(N)，線性時間。
//   - 抽樣時間：O(1)，穩定且高效。
//   - 空間複雜度：O(N)，與選項數量成正比，**與權重總和無關**。
//
// 適用場景：
//   - 權重總和非常大 (如 > 100,000) 或權重差異懸殊。
//   - 選項數量較多。
//   - 需要穩定的記憶體開銷 (不會因為企劃調整權重數值而暴增記憶體)。
//
// 實作細節：
//   - 採用全整數運算 (Integer Scaling)，避免浮點數精度誤差 (0.999... != 1.0)。
//   - 內建溢位檢查 (Safe Multiply)，確保在大數權重下安全運作。

package sampler

import (
	"math"
	"math/bits"

	"github.com/zintix-labs/problab/sdk/core"
)

// AliasTable 是 Vose Alias Method 的一種 O(1) 加權抽樣結構，適用於從離散分布中快速抽樣。
// 此版本為「整數版本」，使用整數 scaling 來避免浮點數計算中的精度問題與誤差累積。
//
// 結構欄位說明：
// - Prob: 存放每個元素的「調整後機率」，整數形式，經過 scaling。
// - Aliases: 別名索引，用於處理機率不足的元素，指向補足機率的元素。
// - Size: 欄位數量，即元素數量。
// - Total: 權重總和，用於 scaling 與抽樣判斷。
//
// 演算法原理：
//   - 將任意離散分佈轉換為均勻分佈的組合。
//   - 每個槽位 (Bucket) 只存放「自己」和「別名 (Alias)」兩個選項。
//   - 抽樣時先選槽位，再根據機率決定是自己還是別名。
//
// 特性：
//   - 建表時間：O(N)，線性時間。
//   - 抽樣時間：O(1)，穩定且高效。 *固定作2次IntN亂數*
//   - 空間複雜度：O(N)，與選項數量成正比，**與權重總和無關**。
//
// 適用場景：
//   - 權重總和非常大 (如 > 100,000) 或權重差異懸殊。
//   - 選項數量較多。
//   - 需要穩定的記憶體開銷 (不會因為企劃調整權重數值而暴增記憶體)。
//
// 實作細節：
//   - 採用全整數運算 (Integer Scaling)，避免浮點數精度誤差 (0.999... != 1.0)。
//   - 內建溢位檢查 (Safe Multiply)，確保在大數權重下安全運作。
type AliasTable struct {
	Prob    []int
	Aliases []int
	Size    int
	Total   int
}

// BuildAliasTable 根據輸入的權重(weights)建立 AliasTable。
//
// 輸入 weights 說明：
// - weights 為任意非負整數權重陣列，不需事先正規化。
// - 權重可為零，但全部為零會 panic。
//
// 處理流程說明：
//
// - 計算 total 為所有權重之和，若有負權重或 total == 0 則 panic。
// - 檢查 weights 長度與 total 相乘是否會溢位，避免 int64 overflow。
// - 使用兩個 bucket：small 與 large，分別存放 scaled 權重小於 total 及大於等於 total 的索引。
// - 透過 small 與 large 兩桶交互調整 prob 與 aliases，完成 alias table 建立。
//
// 演算法流程條列：
// 1) 將每個權重 w 乘以 n（元素數量）做整數 scaling，得到 prob。
// 2) 分類索引到 small 或 large，依 prob[i] 與 total 比較。
// 3) 從 small 和 large 各取一個元素 s, l，將 l 指派為 s 的 alias，並調整 l 的 prob。
// 4) 重複直到 small 或 large 空。
// 5) 返回建好的 AliasTable 結構。
func BuildAliasTable(weights []int) *AliasTable {
	if len(weights) == 0 {
		return &AliasTable{
			Prob:    []int{},
			Aliases: []int{},
			Size:    0,
			Total:   0,
		}
	}

	n := len(weights)
	total := uint64(0)
	for _, w := range weights {
		if w < 0 {
			panic("AliasTable: negative weight encountered")
		}
		if total > uint64(math.MaxInt)-uint64(w) {
			panic("AliasTable: total weight overflow int range")
		}
		total += uint64(w)
	}

	if total == 0 {
		panic("AliasTable: all weights are zero")
	}

	if !isSafeMultiply(int(total), n) {
		panic("AliasTable: weights are too large, causing overflow")
	}

	prob := make([]int, n)
	aliases := make([]int, n)

	small := make([]int, 0)
	large := make([]int, 0)

	for i, w := range weights {
		prob[i] = w * n           // 整數 scaling: 將權重乘以元素數量 n，方便後續整數比較
		if prob[i] < int(total) { // 以 total 做 partition，分為 small 與 large 兩組
			small = append(small, i)
		} else {
			large = append(large, i)
		}
	}

	for len(small) > 0 && len(large) > 0 {
		s := small[len(small)-1]
		small = small[:len(small)-1]
		l := large[len(large)-1]
		large = large[:len(large)-1]

		aliases[s] = l                           // 把 s 的剩餘機率補到 l，建立別名關係
		prob[l] = prob[l] + prob[s] - int(total) // 調整 l 的機率，維持 sum(prob) = total * n 的不變性

		if prob[l] < int(total) {
			small = append(small, l)
		} else {
			large = append(large, l)
		}
	}

	return &AliasTable{
		Prob:    prob,
		Aliases: aliases,
		Size:    n,
		Total:   int(total),
	}
}

// isSafeMultiply 使用 bits.Mul64 來檢查兩個 int64 乘積是否會超過 math.MaxInt64。
//
// 此檢查用於建表階段，確保 w*n 的乘法不會溢位，避免後續整數計算錯誤。
// 這是防止在建表階段發生溢位，而不是在抽樣階段處理。
func isSafeMultiply(a, b int) bool {
	a1 := uint64(a)
	b1 := uint64(b)
	hi, lo := bits.Mul64(a1, b1)
	return hi == 0 && (lo <= math.MaxInt64)

}

// Pick 從 AliasTable 中抽取一個索引，若表為空則回傳 -1。
//
// 抽樣步驟說明：
//
// 1) 使用 c.IntN(Size) 隨機選擇一個欄位 idx。
//
// 2) 使用 c.IntN(Total) 隨機投票，判斷是否直接選擇 idx，或使用其 alias。
//
// 3) 判斷條件為 IntN(Total) < Prob[idx]，此為整數版的機率比較。
//
// 數學推導簡述：
//   - Prob[idx] = weight[idx] * Size，為整數 scaling 後的機率值。
//   - 浮點版本為 U < p[idx]，U 為 [0,1) 均勻隨機數，p[idx] 為機率。
//   - 將 U 與 p[idx] 放大為整數比較，避免浮點誤差。
//
// 此方法完全用整數運算，不經過 float64 浮點計算，避免原演算法的誤差累積，確保抽樣正確性。
func (at *AliasTable) Pick(c *core.Core) int {
	if at.Size == 0 {
		return -1
	}
	idx := c.IntN(at.Size)
	if c.IntN(at.Total) < at.Prob[idx] {
		return idx
	}
	return at.Aliases[idx]
}
