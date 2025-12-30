package core

import (
	"crypto/rand"
	"math"
	"math/big"
	"math/bits"
)

const (
	pcg32Multiplier = 6364136223846793005
	pcg32FloatUnit  = 1.0 / (1 << 32)
)

// SeedStatePCG 紀錄初始化時的狀態，用於追蹤或除錯。
type SeedStatePCG struct {
	Seed1 uint64
	Seed2 uint64
}

// PCG32 為 64-bit 狀態、32-bit 輸出的 PCG (XSH RR) 產生器。
// 介面設計對齊 PCG64 版本，便於在 core.Core 中互換。
type PCG32 struct {
	state uint64
	inc   uint64
}

// --------------------------------------
// 提供兩種New方式
// --------------------------------------

func newPCG32() *PCG32 {
	seed, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	r := &PCG32{}
	r.initWithSeed(seed.Int64(), 1)
	return r
}

func newPCG32WithSeed(seed int64) *PCG32 {
	r := &PCG32{}
	r.initWithSeed(seed, 1)
	return r
}

//---------------------------------------
// 回傳介面方法
//---------------------------------------

// Uint32 回傳非負整數uint32亂數。
func (r *PCG32) Uint32() uint32 {
	return r.nextUint32()
}

// Uint64 回傳非負整數uint64亂數
func (r *PCG32) Uint64() uint64 {
	return (uint64(r.nextUint32()) << 32) | uint64(r.nextUint32())
}

// UintN 產出[0,n) 的uint整數，若 max == 0 回傳 0
func (r *PCG32) UintN(max uint) uint {
	if max == 0 {
		return 0
	}
	return uint(r.randBelowUint64(uint64(max)))
}

// Int32 返回一個"非負"的int32亂數(31 bits)
func (r *PCG32) Int32() int32 {
	return int32(r.nextUint32() &^ (1 << 31))
}

// Int64 返回一個"非負"的int64亂數(63 bits)
func (r *PCG32) Int64() int64 {
	return int64(r.Uint64() &^ (1 << 63))
}

// IntN 回傳 [0,n) 的亂數；若 n <= 0 回傳 -1。
func (r *PCG32) IntN(max int) int {

	if max <= 0 {
		return -1
	}
	if max <= math.MaxUint32 {
		return int(r.randBelowUint32(uint32(max)))
	}
	return int(r.randBelowUint64(uint64(max)))
}

// Float64 回傳 [0,1) 的浮點亂數（32-bit 精度）。
func (r *PCG32) Float64() float64 {
	return float64(r.nextUint32()) * pcg32FloatUnit
}

// SetSeed 直接設定seed
func (r *PCG32) Restore(data []byte) error {
	return nil
}

// Snapshot 取得當下內部狀態(seed)
func (r *PCG32) Snapshot() ([]byte, error) {
	b := make([]byte, 0, 16)
	b = AppendUint64(b, r.state)
	b = AppendUint64(b, r.inc)
	return b, nil
}

//---------------------------------------
// 內部方法
//---------------------------------------

func (r *PCG32) initWithSeed(baseSeed int64, seq uint64) {
	seedState := deriveSeedStatePCG32(baseSeed, seq)
	r.state = seedState.Seed1
	r.inc = seedState.Seed2
}

func deriveSeedStatePCG32(baseSeed int64, seq uint64) SeedStatePCG {
	inc := (seq << 1) | 1
	// PCG 建議的初始化流程：先用 stream 初始化一次，再加 seed，最後再 step。
	g := pcg32Core{state: 0, inc: inc}
	g.next()
	g.state += uint64(baseSeed)
	g.next()

	return SeedStatePCG{
		Seed1: g.state,
		Seed2: inc,
	}
}

// pcg32Core 供初始化階段使用，避免污染外部 PCG32。
type pcg32Core struct {
	state uint64
	inc   uint64
}

func (p *pcg32Core) next() uint32 {
	oldstate := p.state
	p.state = oldstate*pcg32Multiplier + p.inc
	xorshifted := uint32(((oldstate >> 18) ^ oldstate) >> 27)
	rot := uint32(oldstate >> 59)
	return bits.RotateLeft32(xorshifted, -int(rot))
}

func (r *PCG32) nextUint32() uint32 {
	oldstate := r.state
	r.state = oldstate*pcg32Multiplier + r.inc
	xorshifted := uint32(((oldstate >> 18) ^ oldstate) >> 27)
	rot := uint32(oldstate >> 59)
	return bits.RotateLeft32(xorshifted, -int(rot))
}

func (r *PCG32) randBelowUint32(bound uint32) uint32 {
	if bound == 0 {
		return 0
	}
	threshold := uint32((^uint32(0) - bound + 1) % bound)
	for {
		v := r.nextUint32()
		if v >= threshold {
			return v % bound
		}
	}
}

func (r *PCG32) randBelowUint64(bound uint64) uint64 {
	if bound == 0 {
		return 0
	}
	threshold := (^uint64(0) - bound + 1) % bound
	for {
		hi := uint64(r.nextUint32())
		lo := uint64(r.nextUint32())
		v := (hi << 32) | lo
		if v >= threshold {
			return v % bound
		}
	}
}

func AppendUint64(b []byte, v uint64) []byte {
	return append(b,
		byte(v>>56),
		byte(v>>48),
		byte(v>>40),
		byte(v>>32),
		byte(v>>24),
		byte(v>>16),
		byte(v>>8),
		byte(v),
	)
}
