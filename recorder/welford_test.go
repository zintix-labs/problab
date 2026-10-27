// Copyright 2026 Zintix Labs
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

package recorder

import (
	"math"
	"math/big"
	"math/rand/v2"
	"testing"
)

// exactStd 以有理數算出整數贏分 ÷ unit 的精確樣本標準差與平均值。
func exactStd(wins []int64, unit int64) (std, mean float64) {
	if len(wins) == 0 {
		return 0, 0
	}
	n := big.NewInt(int64(len(wins)))
	sum, sq := new(big.Int), new(big.Int)
	for _, w := range wins {
		sum.Add(sum, big.NewInt(w))
		sq.Add(sq, new(big.Int).Mul(big.NewInt(w), big.NewInt(w)))
	}
	mean, _ = new(big.Rat).SetFrac(sum, new(big.Int).Mul(n, big.NewInt(unit))).Float64()
	if len(wins) < 2 {
		return 0, mean
	}
	num := new(big.Int).Sub(new(big.Int).Mul(n, sq), new(big.Int).Mul(sum, sum))
	den := new(big.Int).Mul(n, new(big.Int).Sub(n, big.NewInt(1)))
	den.Mul(den, big.NewInt(unit*unit))
	v, _ := new(big.Rat).SetFrac(num, den).Float64()
	return math.Sqrt(v), mean
}

func near(got, want float64) bool {
	return math.Abs(got-want) <= 1e-15+1e-11*math.Abs(want)
}

func accumulate(wins []int64, unit int64) welford {
	var w welford
	for _, x := range wins {
		w.add(float64(x) / float64(unit))
	}
	return w
}

func welfordCases() map[string][]int64 {
	r := rand.New(rand.NewPCG(7, 11))
	cases := map[string][]int64{"empty": nil, "one": {120}, "zeros": make([]int64, 1000)}
	decimal := make([]int64, 20000)
	jackpot := make([]int64, 200000)
	flat := make([]int64, 1000000)
	for i := range decimal {
		decimal[i] = int64(r.IntN(173))
	}
	for i := range jackpot {
		if r.IntN(50000) == 0 {
			jackpot[i] = 2000000
		} else if r.IntN(3) == 0 {
			jackpot[i] = int64(r.IntN(400))
		}
	}
	for i := range flat {
		flat[i] = 4
	}
	flat[123] = 5
	cases["decimal"], cases["jackpot"], cases["low variance"] = decimal, jackpot, flat
	return cases
}

func TestWelfordMatchesExact(t *testing.T) {
	for name, wins := range welfordCases() {
		w := accumulate(wins, 40)
		std, mean := exactStd(wins, 40)
		if !near(w.std(), std) {
			t.Errorf("%s: std %v, want %v", name, w.std(), std)
		}
		cv := 0.0
		if len(wins) >= 2 && mean > 0 {
			cv = std / mean
		}
		if !near(w.cv(), cv) {
			t.Errorf("%s: cv %v, want %v", name, w.cv(), cv)
		}
	}
}

// 依順序合併的結果與單一串流在容差內相同；同一順序重算逐位元相同；空的一方不影響結果。
func TestWelfordMerge(t *testing.T) {
	wins := welfordCases()["jackpot"]
	single := accumulate(wins, 40)
	merge := func(parts int) welford {
		chunks := make([]welford, parts)
		for i, x := range wins {
			chunks[i%parts].add(float64(x) / 40)
		}
		var m welford
		for _, c := range chunks {
			m.merge(c)
		}
		return m
	}
	for _, parts := range []int{2, 7, 64} {
		m := merge(parts)
		if m.n != single.n || !near(m.std(), single.std()) || !near(m.cv(), single.cv()) {
			t.Errorf("%d parts: std %v cv %v, want %v %v", parts, m.std(), m.cv(), single.std(), single.cv())
		}
		if again := merge(parts); again != m {
			t.Errorf("%d parts: the same merge order gave a different result", parts)
		}
	}
	m := single
	m.merge(welford{})
	var empty welford
	empty.merge(single)
	if m != single || empty != single {
		t.Fatal("merging an empty side changed the result")
	}
}

// 現行的 int 平方和在這個規模會溢位；Welford 仍與精確值相同。
func TestWelfordBeyondIntSquareSums(t *testing.T) {
	const unit = 1000
	wins := make([]int64, 200)
	for i := range wins {
		if i%2 == 0 {
			wins[i] = 4_000_000_000 // 每局的平方 1.6e19 已超過 int64
		}
	}
	w := accumulate(wins, unit)
	std, mean := exactStd(wins, unit)
	if !near(w.std(), std) || !near(w.cv(), std/mean) {
		t.Fatalf("std %v cv %v, want %v %v", w.std(), w.cv(), std, std/mean)
	}
}
