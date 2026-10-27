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

import "math"

// welford 以 Welford 演算法逐筆累加一條贏倍的平均值與 M2（離均差平方和），不會溢位，低變異時
// 也不會有相減抵銷的誤差。零值即為空的累加器。
type welford struct {
	n    int
	mean float64
	m2   float64
}

func (w *welford) add(x float64) {
	w.n++
	d := x - w.mean
	w.mean += d / float64(w.n)
	w.m2 += d * (x - w.mean)
}

// merge 以 Chan 的公式把 o 併入 w。合併順序會影響最後幾位數，呼叫端必須以固定的順序合併。
func (w *welford) merge(o welford) {
	if o.n == 0 {
		return
	}
	if w.n == 0 {
		*w = o
		return
	}
	n := float64(w.n + o.n)
	d := o.mean - w.mean
	w.mean += d * float64(o.n) / n
	w.m2 += o.m2 + d*d*float64(w.n)*float64(o.n)/n
	w.n += o.n
}

// std 回傳樣本標準差；少於兩筆時為 0。
func (w welford) std() float64 {
	if w.n < 2 {
		return 0
	}
	return math.Sqrt(max(w.m2, 0) / float64(w.n-1))
}

// cv 回傳變異係數 std ÷ 平均值；少於兩筆或平均值不大於 0 時為 0，表示沒有定義。
func (w welford) cv() float64 {
	if w.n < 2 || w.mean <= 0 {
		return 0
	}
	return w.std() / w.mean
}
