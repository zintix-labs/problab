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

package stats

import (
	"fmt"
	"sort"

	"gonum.org/v1/gonum/stat/distuv"
)

// ============================================================
// ** 結構宣告 **
// ============================================================

// 用戶體驗評估
type EstimatorPlayers struct {
	RtpStat     RtpStat
	EventStat   EventStat
	SessionStat SessionStat
}

// Rtp敘事
type RtpStat struct {
	ExpMedian PointStat // 描述體驗的中位數
	ExpPerc   ExpPerc   // 描述玩家的分布(對應RTP)
	RtpPerc   RtpPerc   // 描述Rtp的分布(對應多少比例的玩家)
}

// 用玩家體驗分位數視角看: 最差10％玩家的RTP 最差33%玩家的RTP ...
type ExpPerc struct {
	ExpP10 PointStat
	ExpP33 PointStat
	ExpP67 PointStat
	ExpP90 PointStat
}

// 用Rtp分位數視角看玩家: 有多少玩家體驗到了30%RTP 有多少玩家體驗到了50%RTP ...
type RtpPerc struct {
	Rtp30  PointStat
	Rtp50  PointStat
	Rtp70  PointStat
	Rtp100 PointStat
}

// PointStat 點估計 回傳 估計值 以及信賴區間
type PointStat struct {
	Hat float64
	CI  CI
}

// 事件敘事
type EventStat struct {
	Trigger EventCount
	Bucket  BucketEvent
}

// 事件點估計
type EventCount struct {
	Zero PointStat
	One  PointStat
	Two  PointStat
	More PointStat
}

// 對應分桶的統計
type BucketEvent struct {
	BucketLable []string     // 分桶標籤
	BucketCount []EventCount // 分桶事件點估計
}

// 對應結果敘事
type SessionStat struct {
	Bust    PointStat // 破產
	Cashout PointStat // 贏滿離場
	Alive   PointStat // 活到最後
}

// ============================================================
// ** 對外 : 用戶體驗評估 **
// ============================================================

// EstimatorPlayerExp 用戶體驗評估
//
// 1. RTP 敘事 : 描述用戶大致的RTP分布
//
// 2. Event 敘事 : 描述用戶遇到某些事件(觸發FG、中過幾倍以上獎勵所對應的機率)
//
// 3. Session 敘事 : 描述用戶最終贏到滿足離場、破產離場、打累了離場的機率
func EstimatorPlayerExp(sts []*StatReport) *EstimatorPlayers {
	// 0. 防禦：空輸入
	n := len(sts)
	out := &EstimatorPlayers{}
	if n == 0 {
		return out
	}

	// ------------------------------------------------------------
	// 1) RTP 敘事：收集每位玩家 RTP 並做分位/CI
	// ------------------------------------------------------------
	rtp := make([]float64, n)
	for i, s := range sts {
		rtp[i] = s.Rtp()
	}

	// 中位數 (點估計 + 95% CI)
	medHat := quantilePoint(rtp, 0.5)
	medLo, medHi := quantileCI(rtp, 0.5, 0.95)

	// P10, P33, P67, P90 (點估計 + 95% CI)
	p10Hat := quantilePoint(rtp, 0.10)
	p10Lo, p10Hi := quantileCI(rtp, 0.10, 0.95)

	p33Hat := quantilePoint(rtp, 1.0/3.0)
	p33Lo, p33Hi := quantileCI(rtp, 1.0/3.0, 0.95)

	p67Hat := quantilePoint(rtp, 2.0/3.0)
	p67Lo, p67Hi := quantileCI(rtp, 2.0/3.0, 0.95)

	p90Hat := quantilePoint(rtp, 0.90)
	p90Lo, p90Hi := quantileCI(rtp, 0.90, 0.95)

	// RTP 對標：≤ 30/50/70/100% 的玩家比例（CP 95% CI）
	rtp30Hat, rtp30CI := percentileCIForValue(rtp, 0.30, 0.95)
	rtp50Hat, rtp50CI := percentileCIForValue(rtp, 0.50, 0.95)
	rtp70Hat, rtp70CI := percentileCIForValue(rtp, 0.70, 0.95)
	rtp100Hat, rtp100CI := percentileCIForValue(rtp, 1.00, 0.95)

	out.RtpStat = RtpStat{
		ExpMedian: PointStat{Hat: medHat, CI: CI{Lo: medLo, Hi: medHi}},
		ExpPerc: ExpPerc{
			ExpP10: PointStat{Hat: p10Hat, CI: CI{Lo: p10Lo, Hi: p10Hi}},
			ExpP33: PointStat{Hat: p33Hat, CI: CI{Lo: p33Lo, Hi: p33Hi}},
			ExpP67: PointStat{Hat: p67Hat, CI: CI{Lo: p67Lo, Hi: p67Hi}},
			ExpP90: PointStat{Hat: p90Hat, CI: CI{Lo: p90Lo, Hi: p90Hi}},
		},
		RtpPerc: RtpPerc{
			Rtp30:  PointStat{Hat: rtp30Hat, CI: rtp30CI},
			Rtp50:  PointStat{Hat: rtp50Hat, CI: rtp50CI},
			Rtp70:  PointStat{Hat: rtp70Hat, CI: rtp70CI},
			Rtp100: PointStat{Hat: rtp100Hat, CI: rtp100CI},
		},
	}

	// ------------------------------------------------------------
	// 2) Event 敘事：Trigger 次數分布 + 各桶次數分布（0/1/2/3+）
	// ------------------------------------------------------------
	// 2.1 Trigger（0/1/2/3+）
	var c0, c1, c2, c3p int
	for _, s := range sts {
		t := s.Summary.Trigger
		switch {
		case t == 0:
			c0++
		case t == 1:
			c1++
		case t == 2:
			c2++
		default:
			c3p++
		}
	}
	_, ci0 := proportionCICP(c0, n, 0.95)
	_, ci1 := proportionCICP(c1, n, 0.95)
	_, ci2 := proportionCICP(c2, n, 0.95)
	_, ci3 := proportionCICP(c3p, n, 0.95)

	out.EventStat.Trigger = EventCount{
		Zero: PointStat{Hat: float64(c0) / float64(n), CI: ci0},
		One:  PointStat{Hat: float64(c1) / float64(n), CI: ci1},
		Two:  PointStat{Hat: float64(c2) / float64(n), CI: ci2},
		More: PointStat{Hat: float64(c3p) / float64(n), CI: ci3},
	}

	// 2.2 分桶
	labels := Buckets.WinBucketStr() // 長度 = len(winMultGroup)+1
	L := len(labels)
	out.EventStat.Bucket = BucketEvent{BucketLable: labels, BucketCount: make([]EventCount, L)}

	// 對每個桶，統計玩家中 0/1/2/3+ 次數比例
	for bi := 0; bi < L; bi++ {
		var b0, b1, b2, b3p int
		for _, s := range sts {
			cnt := 0
			if bi < len(s.Dist.TotalWinCollect) {
				cnt = s.Dist.TotalWinCollect[bi]
			}
			switch {
			case cnt == 0:
				b0++
			case cnt == 1:
				b1++
			case cnt == 2:
				b2++
			default:
				b3p++
			}
		}
		_, ciB0 := proportionCICP(b0, n, 0.95)
		_, ciB1 := proportionCICP(b1, n, 0.95)
		_, ciB2 := proportionCICP(b2, n, 0.95)
		_, ciB3 := proportionCICP(b3p, n, 0.95)

		out.EventStat.Bucket.BucketCount[bi] = EventCount{
			Zero: PointStat{Hat: float64(b0) / float64(n), CI: ciB0},
			One:  PointStat{Hat: float64(b1) / float64(n), CI: ciB1},
			Two:  PointStat{Hat: float64(b2) / float64(n), CI: ciB2},
			More: PointStat{Hat: float64(b3p) / float64(n), CI: ciB3},
		}
	}

	// ------------------------------------------------------------
	// 3) Session 敘事：Bust / Cashout / Alive 比例 + CP 95% CI
	// ------------------------------------------------------------
	var bustK, cashK, aliveK int
	for _, s := range sts {
		if s.Player.Bust {
			bustK++
		}
		if s.Player.Cashout {
			cashK++
		}
		if s.Player.Alive {
			aliveK++
		}
	}

	bustHat, bustCI := proportionCICP(bustK, n, 0.95)
	cashHat, cashCI := proportionCICP(cashK, n, 0.95)
	aliveHat, aliveCI := proportionCICP(aliveK, n, 0.95)

	out.SessionStat = SessionStat{
		Bust:    PointStat{Hat: bustHat, CI: bustCI},
		Cashout: PointStat{Hat: cashHat, CI: cashCI},
		Alive:   PointStat{Hat: aliveHat, CI: aliveCI},
	}

	return out
}

// ============================================================
// ** 內部統計函數 **
// ============================================================

// Clopper–Pearson exact CI for binomial proportion (k successes out of n)
func proportionCICP(k int, n int, confidence float64) (pHat float64, ci CI) {
	if n == 0 {
		return 0, CI{0, 1}
	}
	alpha := 1 - confidence
	pHat = float64(k) / float64(n)

	// Beta PPF 映射，處理邊界
	if k == 0 {
		ci.Lo = 0
	} else {
		b := distuv.Beta{Alpha: float64(k), Beta: float64(n - k + 1)}
		ci.Lo = b.Quantile(alpha / 2)
	}
	if k == n {
		ci.Hi = 1
	} else {
		b := distuv.Beta{Alpha: float64(k + 1), Beta: float64(n - k)}
		ci.Hi = b.Quantile(1 - alpha/2)
	}
	return
}

// 問題：給定樣本 data 與門檻 x0，估計 p = P(X ≤ x0) 的點估計與 CI 區間
// 回傳 (pHat, CI)
func percentileCIForValue(data []float64, x0 float64, confidence float64) (pHat float64, ci CI) {
	n := len(data)
	if n == 0 {
		return 0, CI{Lo: 0, Hi: 0}
	}
	// k = 數到 <= x0 的個數
	k := 0
	for _, v := range data {
		if v <= x0 {
			k++
		}
	}
	return proportionCICP(k, n, confidence)
}

// 想估「第 q 分位」的上下界。做法：把 order statistic 的秩視為二項→Beta 反推 p 範圍，再把 p 轉回樣本索引。
// 回傳 (loValue, hiValue)
func quantileCI(data []float64, q, confidence float64) (float64, float64) {
	n := len(data)
	if n == 0 {
		return 0, 0
	}
	cp := make([]float64, n)
	copy(cp, data)
	sort.Float64s(cp)

	alpha := 1 - confidence
	k := int(q * float64(n))
	if k < 1 {
		k = 1
	} else if k > n-1 {
		k = n - 1
	}

	// 以 CP 思想反推 p 範圍
	bLo := distuv.Beta{Alpha: float64(k), Beta: float64(n - k + 1)}
	bHi := distuv.Beta{Alpha: float64(k + 1), Beta: float64(n - k)}
	pLo := bLo.Quantile(alpha / 2)
	pHi := bHi.Quantile(1 - alpha/2)

	li := int(pLo * float64(n))
	ui := int(pHi * float64(n))
	if ui > 0 {
		ui -= 1
	}
	if li < 0 {
		li = 0
	}
	if li > n-1 {
		li = n - 1
	}
	if ui < 0 {
		ui = 0
	}
	if ui > n-1 {
		ui = n - 1
	}
	return cp[li], cp[ui]
}

// quantilePoint returns the empirical quantile point estimate at q.
func quantilePoint(data []float64, q float64) float64 {
	n := len(data)
	if n == 0 {
		return 0
	}
	cp := make([]float64, n)
	copy(cp, data)
	sort.Float64s(cp)
	// 最近秩法
	idx := int(q * float64(n))
	if idx < 0 {
		idx = 0
	}
	if idx > n-1 {
		idx = n - 1
	}
	return cp[idx]
}

// ============================================================
// ** 輸出函數 **
// ============================================================

func (est *EstimatorPlayers) Out() {
	// 1) RTP (Player Experience)
	fmt.Println("=== RTP (Player Experience) ===")
	rtpKeys := []string{
		"Median RTP",
		"P10 RTP",
		"P33 RTP",
		"P67 RTP",
		"P90 RTP",
		"≤30% RTP (players)",
		"≤50% RTP (players)",
		"≤70% RTP (players)",
		"≤100% RTP (players)",
	}
	rtpMsg := map[string]string{
		"Median RTP":          fmtHatCIpct01(est.RtpStat.ExpMedian.Hat, est.RtpStat.ExpMedian.CI),
		"P10 RTP":             fmtHatCIpct01(est.RtpStat.ExpPerc.ExpP10.Hat, est.RtpStat.ExpPerc.ExpP10.CI),
		"P33 RTP":             fmtHatCIpct01(est.RtpStat.ExpPerc.ExpP33.Hat, est.RtpStat.ExpPerc.ExpP33.CI),
		"P67 RTP":             fmtHatCIpct01(est.RtpStat.ExpPerc.ExpP67.Hat, est.RtpStat.ExpPerc.ExpP67.CI),
		"P90 RTP":             fmtHatCIpct01(est.RtpStat.ExpPerc.ExpP90.Hat, est.RtpStat.ExpPerc.ExpP90.CI),
		"≤30% RTP (players)":  fmtHatCIpct01(est.RtpStat.RtpPerc.Rtp30.Hat, est.RtpStat.RtpPerc.Rtp30.CI),
		"≤50% RTP (players)":  fmtHatCIpct01(est.RtpStat.RtpPerc.Rtp50.Hat, est.RtpStat.RtpPerc.Rtp50.CI),
		"≤70% RTP (players)":  fmtHatCIpct01(est.RtpStat.RtpPerc.Rtp70.Hat, est.RtpStat.RtpPerc.Rtp70.CI),
		"≤100% RTP (players)": fmtHatCIpct01(est.RtpStat.RtpPerc.Rtp100.Hat, est.RtpStat.RtpPerc.Rtp100.CI),
	}
	printTable("RTP (Player Experience)", rtpKeys, rtpMsg)

	// 2) Events: Trigger counts per player
	fmt.Println("\n=== Events: Trigger counts per player ===")
	triggerKeys := []string{"0 times", "1 time", "2 times", "3+ times"}
	triggerMsg := map[string]string{
		"0 times":  fmtHatCIpct01(est.EventStat.Trigger.Zero.Hat, est.EventStat.Trigger.Zero.CI),
		"1 time":   fmtHatCIpct01(est.EventStat.Trigger.One.Hat, est.EventStat.Trigger.One.CI),
		"2 times":  fmtHatCIpct01(est.EventStat.Trigger.Two.Hat, est.EventStat.Trigger.Two.CI),
		"3+ times": fmtHatCIpct01(est.EventStat.Trigger.More.Hat, est.EventStat.Trigger.More.CI),
	}
	printTable("Events: Trigger counts per player", triggerKeys, triggerMsg)

	// 3) Events: Buckets (per player hits in bucket)
	fmt.Println("\n=== Events: Buckets (per player hits in bucket) ===")
	for i, label := range est.EventStat.Bucket.BucketLable {
		ec := est.EventStat.Bucket.BucketCount[i]
		fmt.Printf("%-20s : %s\n", label, fmtEventCount(ec))
	}

	// 4) Session Outcome
	fmt.Println("\n=== Session Outcome ===")
	sessionKeys := []string{"Bust", "Cashout", "Alive"}
	sessionMsg := map[string]string{
		"Bust":    fmtHatCIpct01(est.SessionStat.Bust.Hat, est.SessionStat.Bust.CI),
		"Cashout": fmtHatCIpct01(est.SessionStat.Cashout.Hat, est.SessionStat.Cashout.CI),
		"Alive":   fmtHatCIpct01(est.SessionStat.Alive.Hat, est.SessionStat.Alive.CI),
	}
	printTable("Session Outcome", sessionKeys, sessionMsg)
}

func printTable(title string, keys []string, msg map[string]string) {
	fmt.Println(title)
	maxKeyLen := 0
	for _, k := range keys {
		if len(k) > maxKeyLen {
			maxKeyLen = len(k)
		}
	}
	for _, k := range keys {
		fmt.Printf("  %-*s : %s\n", maxKeyLen, k, msg[k])
	}
}

func fmtPct01(x float64) string {
	return fmt.Sprintf("%.2f%%", x*100)
}

func fmtHatCIpct01(hat float64, ci CI) string {
	return fmt.Sprintf("%s [%s, %s]", fmtPct01(hat), fmtPct01(ci.Lo), fmtPct01(ci.Hi))
}

func fmtEventCount(ec EventCount) string {
	return fmt.Sprintf("0x: %s | 1x: %s | 2x: %s | 3+x: %s",
		fmtHatCIpct01(ec.Zero.Hat, ec.Zero.CI),
		fmtHatCIpct01(ec.One.Hat, ec.One.CI),
		fmtHatCIpct01(ec.Two.Hat, ec.Two.CI),
		fmtHatCIpct01(ec.More.Hat, ec.More.CI),
	)
}
