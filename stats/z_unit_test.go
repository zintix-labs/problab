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

package stats_test

import (
	"math"
	"testing"

	"github.com/zintix-labs/problab/spec"
	"github.com/zintix-labs/problab/stats"
)

// buildStatReport constructs a StatReport from a list of wins with betMult=1.
// All wins are treated as base game wins to simplify assertions.
func buildStatReport(betUnit int, wins []int) *stats.StatReport {
	L := len(stats.Buckets.WinBucketStr())
	bucket := stats.Buckets.GetBucketByBetUnit(betUnit)
	twc := make([]int, L)
	bwCollect := make([]int, L)

	var totalWin, totalWinSq int
	for _, w := range wins {
		idx := bucket.Index(w)
		twc[idx]++
		bwCollect[idx]++
		totalWin += w
		totalWinSq += w * w
	}

	totalBet := betUnit * len(wins)
	report := &stats.StatReport{
		Summary: &stats.SummaryReport{
			GameName:    "TestGame",
			GameId:      spec.GID(0),
			BetUnits:    []int{betUnit},
			BetUnit:     betUnit,
			BetMode:     0,
			BetMult:     1,
			TotalBet:    totalBet,
			TotalWin:    totalWin,
			BaseWin:     totalWin,
			FreeWin:     0,
			Trigger:     0,
			NoWinRounds: twc[0],
			Rounds:      len(wins),
		},
		Mult: &stats.MultReport{
			TotalWinMult:      float64(totalWin) / float64(betUnit),
			BaseWinMult:       float64(totalWin) / float64(betUnit),
			FreeWinMult:       0,
			TotalWinMultSqSum: float64(totalWinSq) / float64(betUnit*betUnit),
			BaseWinMultSqSum:  float64(totalWinSq) / float64(betUnit*betUnit),
		},
		Dist: &stats.DistReport{
			WinBucket:       stats.Buckets.WinBucketStr(),
			TotalWinCollect: twc,
			BaseWinCollect:  bwCollect,
			FreeWinCollect:  make([]int, L),
			TotalWinDist:    make([]float64, L),
			BaseWinDist:     make([]float64, L),
			FreeWinDist:     make([]float64, L),
		},
		Player: &stats.PlayerReport{},
	}
	report.Done()
	return report
}

func TestStatReportCoreMetrics(t *testing.T) {
	bu := 40
	rep := buildStatReport(bu, []int{bu, 2 * bu})

	wantRTP := float64(bu+2*bu) / float64(2*bu)
	if got := rep.Rtp(); math.Abs(got-wantRTP) > 1e-12 {
		t.Fatalf("RTP got %.12f want %.12f", got, wantRTP)
	}

	m0 := float64(bu) / float64(bu)
	m1 := float64(2*bu) / float64(bu)
	variance := ((m0*m0 + m1*m1) - (m0+m1)*(m0+m1)/2) / (2 - 1)
	wantStd := math.Sqrt(max0(variance))
	if got := rep.Std(); math.Abs(got-wantStd) > 1e-12 {
		t.Fatalf("Std got %.12f want %.12f", got, wantStd)
	}

	wantCV := wantStd / wantRTP
	if got := rep.Cv(); math.Abs(got-wantCV) > 1e-12 {
		t.Fatalf("CV got %.12f want %.12f", got, wantCV)
	}

	// Distribution lengths and sums
	if len(rep.Dist.TotalWinCollect) != len(rep.Dist.WinBucket) {
		t.Fatalf("win buckets length mismatch")
	}
	totalRounds := 0
	for _, c := range rep.Dist.TotalWinCollect {
		totalRounds += c
	}
	if totalRounds != rep.Summary.Rounds {
		t.Fatalf("distribution total %d != rounds %d", totalRounds, rep.Summary.Rounds)
	}

	rep.Done() // idempotent
	if rep.Rtp() != wantRTP {
		t.Fatalf("RTP changed after second Done")
	}
}

func TestEstimatorRtpAndSession(t *testing.T) {
	// Build 100 reports with RTP from 0.00 to 0.99
	reports := make([]*stats.StatReport, 0, 100)
	bu := 100
	for i := 0; i < 100; i++ {
		win := i // so RTP = i / 100
		reports = append(reports, buildStatReport(bu, []int{win}))
	}

	est := stats.EstimatorPlayerExp(reports)
	if math.Abs(est.RtpStat.ExpMedian.Hat-0.5) > 0.05 {
		t.Fatalf("median RTP expected ~0.5, got %.3f", est.RtpStat.ExpMedian.Hat)
	}
	if math.Abs(est.RtpStat.ExpPerc.ExpP90.Hat-0.9) > 0.05 {
		t.Fatalf("P90 RTP expected ~0.9, got %.3f", est.RtpStat.ExpPerc.ExpP90.Hat)
	}

	// Session outcome: 3 bust, 2 cashout, 5 alive
	sessionSamples := make([]*stats.StatReport, 10)
	for i := 0; i < 10; i++ {
		r := buildStatReport(bu, []int{0})
		switch {
		case i < 3:
			r.Player.Bust = true
			r.Player.Alive = false
		case i < 5:
			r.Player.Cashout = true
			r.Player.Alive = false
		default:
			r.Player.Alive = true
		}
		sessionSamples[i] = r
	}
	est2 := stats.EstimatorPlayerExp(sessionSamples)
	if est2.SessionStat.Bust.Hat != 0.3 {
		t.Fatalf("Bust rate got %.2f want 0.30", est2.SessionStat.Bust.Hat)
	}
	if est2.SessionStat.Cashout.Hat != 0.2 {
		t.Fatalf("Cashout rate got %.2f want 0.20", est2.SessionStat.Cashout.Hat)
	}
	if est2.SessionStat.Alive.Hat != 0.5 {
		t.Fatalf("Alive rate got %.2f want 0.50", est2.SessionStat.Alive.Hat)
	}
}

// --- helpers ---

func max0(x float64) float64 {
	if x < 0 {
		return 0
	}
	return x
}
