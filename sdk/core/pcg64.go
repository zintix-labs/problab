// Package core implements the PCG64 random number generator.
//
// The PCG algorithm is designed by Melissa O'Neill.
// Portions of the bounded random generation logic (UintN/IntN) are
// adapted from the Go standard library (math/rand), which is
// licensed under the BSD 3-Clause License.

package core

import (
	"crypto/rand"
	"math"
	"math/big"
	"math/bits"
	r2 "math/rand/v2"
)

const is32bit = ^uint(0)>>32 == 0

// PCG64 亂數產生器
type PCG64 struct {
	rng *r2.PCG
}

// newPCG64 使用加密隨機來源產生 seed，建立新的 PCG64 實例。
func newPCG64() *PCG64 {
	seed, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	return newPCG64WithSeed(seed.Int64())
}

// newPCG64WithSeed 以指定 seed 建立新的 PCG64 實例。
func newPCG64WithSeed(seed int64) *PCG64 {
	x := uint64(seed) ^ (0x9e3779b97f4a7c15)
	hi := splitmix64(x)
	lo := splitmix64(x ^ 0xDA942042E4DD58B5)
	return &PCG64{rng: r2.NewPCG(hi, lo)}
}

//---------------------------------------
// 回傳方法
//---------------------------------------

// Uint64 回傳非負整數uint64亂數
func (r *PCG64) Uint64() uint64 {
	return r.rng.Uint64()
}

// UintN 產出[0,n) 的uint整數，若 max == 0 回傳 0
func (r *PCG64) UintN(max uint) uint {
	if max == 0 {
		return 0
	}
	return uint(r.uint64n(uint64(max)))
}

// IntN 產出[0,n) 的整數，若 max <= 0 回傳 -1
func (r *PCG64) IntN(max int) int {
	if max <= 0 {
		return -1
	}
	return int(r.uint64n(uint64(max)))
}

// Float64 產出float64(53bits精度)
func (r *PCG64) Float64() float64 {
	return float64(r.Uint64()<<11>>11) / (1 << 53)
}

// Restore 恢復內部狀態
func (r *PCG64) Restore(data []byte) error {
	return r.rng.UnmarshalBinary(data)
}

// Snapshot 取得當下內部狀態
func (r *PCG64) Snapshot() ([]byte, error) {
	return r.rng.MarshalBinary()
}

//---------------------------------------
// 內部方法
//---------------------------------------

// splitmix64 將輸入值混洗成新的 64-bit 狀態，用於種子展開。
func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// uint64n 回傳 [0,n) 的無偏亂數（基於乘法高位與拒絕採樣）。
func (r *PCG64) uint64n(n uint64) uint64 {
	if is32bit && uint64(uint32(n)) == n {
		return uint64(r.uint32n(uint32(n)))
	}
	if n&(n-1) == 0 { // n is power of two, can mask
		return r.Uint64() & (n - 1)
	}
	hi, lo := bits.Mul64(r.Uint64(), n)
	if lo < n {
		thresh := -n % n
		for lo < thresh {
			hi, lo = bits.Mul64(r.Uint64(), n)
		}
	}
	return hi
}

// uint32n 回傳 [0,n) 的無偏亂數（針對 32-bit 目標值）。
func (r *PCG64) uint32n(n uint32) uint32 {
	if n&(n-1) == 0 { // n is power of two, can mask
		return uint32(r.Uint64()) & (n - 1)
	}
	x := r.Uint64()
	lo1a, lo0 := bits.Mul32(uint32(x), n)
	hi, lo1b := bits.Mul32(uint32(x>>32), n)
	lo1, c := bits.Add32(lo1a, lo1b, 0)
	hi += c
	if lo1 == 0 && lo0 < uint32(n) {
		n64 := uint64(n)
		thresh := uint32(-n64 % n64)
		for lo1 == 0 && lo0 < thresh {
			x := r.Uint64()
			lo1a, lo0 = bits.Mul32(uint32(x), n)
			hi, lo1b = bits.Mul32(uint32(x>>32), n)
			lo1, c = bits.Add32(lo1a, lo1b, 0)
			hi += c
		}
	}
	return hi
}
