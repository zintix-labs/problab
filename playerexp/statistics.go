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

package playerexp

import (
	"sort"

	"gonum.org/v1/gonum/stat/distuv"
)

func proportion(k, n uint64, confidence float64) PointStat {
	p := PointStat{CI: Interval{Reason: "no_samples", Method: "clopper_pearson"}}
	if n == 0 {
		return p
	}
	p.Estimate = Estimate{float64(k) / float64(n), true}
	p.CI = Interval{Available: true, Method: "clopper_pearson"}
	a := (1 - confidence) / 2
	if k > 0 {
		p.CI.Lo = distuv.Beta{Alpha: float64(k), Beta: float64(n-k) + 1}.Quantile(a)
	}
	p.CI.Hi = 1
	if k < n {
		p.CI.Hi = distuv.Beta{Alpha: float64(k) + 1, Beta: float64(n - k)}.Quantile(1 - a)
	}
	return p
}
func quantile(sorted []float64, q, confidence float64) PointStat {
	n := len(sorted)
	p := PointStat{CI: Interval{Reason: "no_samples", Method: "binomial_order_statistic"}}
	if n == 0 {
		return p
	}
	p.Estimate = Estimate{sorted[min(n-1, int(q*float64(n)))], true}
	d := distuv.Binomial{N: float64(n), P: q}
	tail := (1 - confidence) / 2
	// CDF(-1)=0, Survival(n)=0. Search includes unbounded ranks.
	r := sort.Search(n+1, func(i int) bool { return i > 0 && d.CDF(float64(i-1)) > tail }) - 1
	if r < 0 {
		r = n
	}
	s := 1 + sort.Search(n+1, func(i int) bool { return d.Survival(float64(i)) <= tail })
	if r < 1 || s > n {
		p.CI.Reason = "insufficient_samples_for_finite_interval"
		return p
	}
	p.CI = Interval{Lo: sorted[r-1], Hi: sorted[s-1], Available: true, Method: "binomial_order_statistic"}
	return p
}
func summarize(g *GroupReport) {
	c := .95
	if g.Config.CI == "confidence_99" {
		c = .99
	}
	if g.TotalBet > 0 {
		g.PooledRTP = Estimate{float64(g.TotalWin) / float64(g.TotalBet), true}
	}
	for i := range g.Events {
		for j, k := range g.Events[i].Counts {
			g.Events[i].Stats[j] = proportion(k, uint64(g.Completed), c)
		}
	}
	for i, k := range g.Exits {
		g.ExitStats[i] = proportion(k, uint64(g.Completed), c)
	}
	g.MaxSpinStat = proportion(g.MaxSpinReached, uint64(g.Completed), c)
	sort.Float64s(g.rtps)
	g.RTP = nil
	for _, q := range []float64{.1, 1.0 / 3, .5, 2.0 / 3, .9} {
		g.RTP = append(g.RTP, RTPStat{Metric: "quantile", Parameter: q, N: uint64(len(g.rtps)), PointStat: quantile(g.rtps, q, c)})
	}
	for _, t := range []float64{.3, .5, .7, 1} {
		k := sort.Search(len(g.rtps), func(i int) bool { return g.rtps[i] > t })
		g.RTP = append(g.RTP, RTPStat{Metric: "threshold", Parameter: t, N: uint64(len(g.rtps)), Count: uint64(k), PointStat: proportion(uint64(k), uint64(len(g.rtps)), c)})
	}
	g.rtps = nil
}
