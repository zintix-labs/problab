// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package playerexp

import (
	"math"
	"testing"
)

func TestConfidenceIntervals(t *testing.T) {
	if p := proportion(0, 0, .95); p.Defined || p.CI.Available {
		t.Fatal(p)
	}
	for _, c := range []float64{.95, .99} {
		p := proportion(0, 1, c)
		want := 1 - (1-c)/2
		if math.Abs(p.CI.Hi-want) > 1e-12 || p.CI.Lo != 0 {
			t.Fatal(p)
		}
		p = proportion(1, 1, c)
		if math.Abs(p.CI.Lo-(1-c)/2) > 1e-12 || p.CI.Hi != 1 {
			t.Fatal(p)
		}
		p = proportion(5, 10, c)
		if !p.CI.Available || p.Value != .5 || p.CI.Lo >= .5 || p.CI.Hi <= .5 {
			t.Fatal(p)
		}
	}
}

// Independent finite binomial enumeration, not the production CDF/search.
func TestQuantileOrderStatisticOracle(t *testing.T) {
	for n := 0; n <= 100; n++ {
		xs := make([]float64, n)
		for i := range xs {
			xs[i] = float64(i + 1)
		}
		for _, q := range []float64{.1, 1.0 / 3, .5, 2.0 / 3, .9} {
			for _, confidence := range []float64{.95, .99} {
				p := quantile(xs, q, confidence)
				if n == 0 {
					if p.Defined || p.CI.Available {
						t.Fatal(p)
					}
					continue
				}
				if p.Value != xs[min(n-1, int(q*float64(n)))] {
					t.Fatal(p)
				}
				pmf := make([]float64, n+1)
				pmf[0] = math.Pow(1-q, float64(n))
				for k := 1; k <= n; k++ {
					pmf[k] = pmf[k-1] * float64(n-k+1) / float64(k) * q / (1 - q)
				}
				tail := (1 - confidence) / 2
				r := 0
				s := n + 1
				for k := 1; k <= n; k++ {
					sum := 0.0
					for j := 0; j < k; j++ {
						sum += pmf[j]
					}
					if sum <= tail {
						r = k
					}
				}
				for k := 1; k <= n; k++ {
					sum := 0.0
					for j := k; j <= n; j++ {
						sum += pmf[j]
					}
					if sum <= tail {
						s = k
						break
					}
				}
				if p.CI.Available != (r >= 1 && s <= n) {
					t.Fatalf("n=%d q=%g r=%d s=%d %+v", n, q, r, s, p)
				}
				if p.CI.Available && (p.CI.Lo != xs[r-1] || p.CI.Hi != xs[s-1]) {
					t.Fatal(n, q, p, r, s)
				}
			}
		}
	}
	xs := make([]float64, 100)
	for i := range xs {
		xs[i] = 1
	}
	if p := quantile(xs, .5, .95); !p.CI.Available || p.CI.Lo != 1 || p.CI.Hi != 1 {
		t.Fatal(p)
	}
}
