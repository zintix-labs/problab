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

package sampler

import (
	"crypto/rand"
	"math"
	"math/big"
	"slices"
	"testing"

	"github.com/zintix-labs/problab/sdk/core"
)

// -----------------------------------------------------------------------------
// Helper Functions
// -----------------------------------------------------------------------------

// assertPanic 驗證函數是否如預期觸發 panic
func assertPanic(t *testing.T, f func(), msg string) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic for %s, but got none", msg)
		}
	}()
	f()
}

// checkDistribution 驗證抽樣結果的分佈是否符合預期權重
func checkDistribution(t *testing.T, name string, weights []int, samples []int, tolerance float64) {
	t.Helper()
	totalW := 0
	for _, w := range weights {
		totalW += w
	}
	if totalW == 0 {
		return
	}

	counts := make(map[int]int)
	for _, idx := range samples {
		counts[idx]++
	}

	totalSamples := len(samples)
	for i, w := range weights {
		if w == 0 {
			if counts[i] > 0 {
				t.Errorf("[%s] expected 0 samples for index %d (weight 0), got %d", name, i, counts[i])
			}
			continue
		}
		expectedProb := float64(w) / float64(totalW)
		actualProb := float64(counts[i]) / float64(totalSamples)
		diff := math.Abs(expectedProb - actualProb)

		if diff > tolerance {
			t.Errorf("[%s] index %d: expected prob %.3f, got %.3f (diff %.3f > tol %.3f)",
				name, i, expectedProb, actualProb, diff, tolerance)
		}
	}
}

// setEqual 檢查兩個 slice 是否包含相同的元素（不考慮順序）
func setEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if !slices.Contains(b, v) {
			return false
		}
	}
	return true
}

// -----------------------------------------------------------------------------
// Tests for WeightedShuffle
// -----------------------------------------------------------------------------

// TestWeightedShuffle_Basic 驗證基本的加權洗牌機率分佈
// 檢查項目: 高權重項目排在前面的機率較高
func TestWeightedShuffle_Basic(t *testing.T) {
	c := core.New(core.Default().New(1))
	weights := []int{10, 90} // Index 1 (權重90) 應該有較高機率排在前面
	trials := 10000
	firstIdxCount := 0

	for i := 0; i < trials; i++ {
		res := WeightedShuffle(c, weights)
		if len(res) != 2 {
			t.Fatalf("expected length 2, got %d", len(res))
		}
		if res[0] == 1 {
			firstIdxCount++
		}
	}

	rate := float64(firstIdxCount) / float64(trials)
	// 期望機率約為 0.90
	if rate < 0.85 || rate > 0.95 {
		t.Errorf("WeightedShuffle prob mismatch: expected ~0.90, got %.4f", rate)
	}
}

// TestWeightedShuffleZerosAtEnd 驗證權重為 0 的項目是否被排在最後
// 檢查項目: 權重 0 的項目應出現在非 0 權重項目之後
func TestWeightedShuffleZerosAtEnd(t *testing.T) {
	c := core.New(core.Default().New(1))
	weights := []int{0, 3, 0, 2}

	got := WeightedShuffle(c, weights)
	if len(got) != len(weights) {
		t.Fatalf("length mismatch, got %d want %d", len(got), len(weights))
	}

	seen := map[int]bool{}
	for _, idx := range got {
		if idx < 0 || idx >= len(weights) {
			t.Fatalf("index out of range: %d", idx)
		}
		if seen[idx] {
			t.Fatalf("duplicate index: %d", idx)
		}
		seen[idx] = true
	}

	// 前 2 個元素應該是正權重項目 (index 1 和 3)
	prefixLen := 2
	prefix := got[:prefixLen]
	for _, idx := range prefix {
		if idx == 0 || idx == 2 {
			t.Fatalf("zero-weight index appeared before positives: %v", got)
		}
	}
	// 後 2 個元素應該是零權重項目 (index 0 和 2)
	suffix := got[prefixLen:]
	for _, idx := range suffix {
		if idx != 0 && idx != 2 {
			t.Fatalf("positive index appeared after zeros: %v", got)
		}
	}
}

// TestWeightedShuffle_NegativePanic 驗證負權重是否觸發 panic
// 檢查項目: 輸入負權重應導致 panic
func TestWeightedShuffle_NegativePanic(t *testing.T) {
	rd, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	c := core.New(core.Default().New(rd.Int64()))
	assertPanic(t, func() {
		WeightedShuffle(c, []int{10, -1})
	}, "Negative Weight")
}

// -----------------------------------------------------------------------------
// Tests for WeightedShuffleWithFilter
// -----------------------------------------------------------------------------

// TestWeightedShuffleWithFilterSkipsZeros 驗證過濾零權重的加權洗牌
// 檢查項目: 結果中不應包含權重為 0 的項目
func TestWeightedShuffleWithFilterSkipsZeros(t *testing.T) {
	c := core.New(core.Default().New(2))
	weights := []int{0, 1, 0, 2}

	got := WeightedShuffleWithFilter(c, weights)
	if len(got) != 2 {
		t.Fatalf("expected 2 items, got %d", len(got))
	}
	if !setEqual(got, []int{1, 3}) {
		t.Fatalf("unexpected indices: %v", got)
	}
}

// TestWeightedShuffleWithFilter_NegativePanic 驗證負權重是否觸發 panic
// 檢查項目: 輸入負權重應導致 panic
func TestWeightedShuffleWithFilter_NegativePanic(t *testing.T) {
	rd, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	c := core.New(core.Default().New(rd.Int64()))
	assertPanic(t, func() {
		WeightedShuffleWithFilter(c, []int{10, -1})
	}, "Negative Weight")
}

// -----------------------------------------------------------------------------
// Tests for WeightedSample
// -----------------------------------------------------------------------------

// TestWeightedSample_Basic 驗證加權 K 抽樣的分佈
// 檢查項目: 抽樣結果應符合權重比例
func TestWeightedSample_Basic(t *testing.T) {
	rd, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	c := core.New(core.Default().New(rd.Int64()))
	weights := []int{10, 10, 80}
	trials := 100000
	samples := make([]int, 0, trials)

	// 每次取 Top-1
	for i := 0; i < trials; i++ {
		res := WeightedSample(c, weights, 1)
		if len(res) > 0 {
			samples = append(samples, res[0])
		}
	}
	checkDistribution(t, "WeightedSample K=1", weights, samples, 0.01)
}

// TestWeightedSampleMatchesFilteredShuffle 驗證 WeightedSample 與 FilteredShuffle 的一致性
// 檢查項目: 在相同 Seed 下，WeightedSample 取出的前 K 個應與 WeightedShuffleWithFilter 的前 K 個相同
func TestWeightedSampleMatchesFilteredShuffle(t *testing.T) {
	weights := []int{5, 0, 1, 4}
	const seed = 7

	// 使用相同的 seed 建立兩個 core，確保隨機數序列一致
	order := WeightedShuffleWithFilter(core.New(core.Default().New(seed)), weights)
	got := WeightedSample(core.New(core.Default().New(seed)), weights, 2)

	expected := order[:2]
	if !slices.Equal(expected, got) {
		t.Fatalf("expected %v, got %v (WeightedSample should pick top-k of shuffle order)", expected, got)
	}
}

// TestWeightedSampleKExceedsPositives 驗證 K 大於有效權重數量的處理
// 檢查項目: 當有效項目少於 K 時，應只回傳所有有效項目，不應 panic
func TestWeightedSampleKExceedsPositives(t *testing.T) {
	weights := []int{0, 2, 0}
	// 請求 5 個項目，但只有 1 個權重 > 0
	got := WeightedSample(core.New(core.Default().New(11)), weights, 5)

	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("expected only index 1, got %v", got)
	}
}

// TestWeightedSampleAllZero 驗證所有權重為 0 的情況
// 檢查項目: 應回傳空切片
func TestWeightedSampleAllZero(t *testing.T) {
	weights := []int{0, 0, 0}
	got := WeightedSample(core.New(core.Default().New(13)), weights, 3)
	if len(got) != 0 {
		t.Fatalf("expected empty result, got %v", got)
	}
}

// TestWeightedSampleNegativePanics 驗證負權重是否觸發 panic
// 檢查項目: 輸入負權重應導致 panic
func TestWeightedSampleNegativePanics(t *testing.T) {
	seed, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	c := core.New(core.Default().New(seed.Int64()))
	assertPanic(t, func() {
		WeightedSample(c, []int{1, -1, 2}, 2)
	}, "Negative Weight")
}

// -----------------------------------------------------------------------------
// Tests for AliasTable
// -----------------------------------------------------------------------------

// TestAliasTable_Distribution 驗證 Alias Table 的抽樣分佈
// 檢查項目: 大量抽樣結果應符合權重比例
func TestAliasTable_Distribution(t *testing.T) {
	seed, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	c := core.New(core.Default().New(seed.Int64()))
	weights := []int{10, 20, 70}
	at := BuildAliasTable(weights)

	trials := 100000
	samples := make([]int, trials)
	for i := 0; i < trials; i++ {
		samples[i] = at.Pick(c)
	}
	checkDistribution(t, "AliasTable", weights, samples, 0.01)
}

// TestAliasTable_Panics 驗證 Alias Table 的各種錯誤情境
// 檢查項目: 全零權重、負權重、總權重溢位應觸發 panic
func TestAliasTable_Panics(t *testing.T) {
	// All zero
	assertPanic(t, func() {
		BuildAliasTable([]int{0, 0, 0})
	}, "All zero weights")

	// Negative
	assertPanic(t, func() {
		BuildAliasTable([]int{10, -1})
	}, "Negative weight")

	// Total overflow check
	assertPanic(t, func() {
		BuildAliasTable([]int{math.MaxInt, 1})
	}, "Total overflow")
}

// -----------------------------------------------------------------------------
// Tests for Look-Up Table (LUT)
// -----------------------------------------------------------------------------

// TestLUT_Distribution 驗證 LUT 的抽樣分佈
// 檢查項目: 大量抽樣結果應符合權重比例
func TestLUT_Distribution(t *testing.T) {
	seed, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	c := core.New(core.Default().New(seed.Int64()))
	weights := []int{1, 2, 7} // 適合 LUT 的小權重
	lut := BuildLUT(weights)

	trials := 10000
	samples := make([]int, trials)
	for i := 0; i < trials; i++ {
		samples[i] = lut.Pick(c)
	}
	checkDistribution(t, "LUT", weights, samples, 0.015)
}

// TestLUT_Panics 驗證 LUT 的各種錯誤情境
// 檢查項目: 超過容量上限、負權重、全零權重應觸發 panic
func TestLUT_Panics(t *testing.T) {
	// Capacity Limit
	assertPanic(t, func() {
		// 模擬超過 MaxLUTCapacity
		weights := []int{int(maxLUTCap) + 1}
		BuildLUT(weights)
	}, "Exceed MaxLUTCapacity")

	// Negative
	assertPanic(t, func() {
		BuildLUT([]int{10, -10})
	}, "Negative weight")

	// All zero
	assertPanic(t, func() {
		BuildLUT([]int{0, 0})
	}, "All zero weights")
}

// -----------------------------------------------------------------------------
// Tests for Shuffle
// -----------------------------------------------------------------------------

// TestShuffle_Basic 驗證基本洗牌功能
// 檢查項目: 洗牌後元素集合不變 (總和不變)，長度不變
func TestShuffle_Basic(t *testing.T) {
	seed, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	c := core.New(core.Default().New(seed.Int64()))
	src := []int{1, 2, 3, 4, 5}
	original := make([]int, len(src))
	copy(original, src)

	// Run shuffle
	Shuffle(c, src)

	// Check elements are preserved (sum check)
	sum1, sum2 := 0, 0
	for _, v := range original {
		sum1 += v
	}
	for _, v := range src {
		sum2 += v
	}
	if sum1 != sum2 {
		t.Fatal("Shuffle altered elements values")
	}

	if len(src) != len(original) {
		t.Fatal("Length mismatch")
	}
}
