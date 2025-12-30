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

package recorder

import (
	"fmt"

	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
	"github.com/zintix-labs/problab/stats"
)

// SpinRecorder 遊戲紀錄員
//
// SpinRecorder 負責紀錄遊戲結果，並透過Done輸出統計報表
type SpinRecorder struct {
	GameName string
	GameId   spec.GID
	BetUnits []int
	BetUnit  int
	BetMode  int
	InitBets int
	Basic    *BasicRecord
	Dist     *DistRecord
	Player   *PlayerRecord
}

// BasicRecord 基本遊戲資料紀錄
type BasicRecord struct {
	TotalBet      int
	TotalWin      int
	BaseWin       int
	FreeWin       int
	TotalWinSqSum int // 平方和
	BaseWinSqSum  int // 平方和
	FreeWinSqSum  int // 平方和
	Trigger       int
	Rounds        int
}

// DistRecord 分數區間落點統計
//
// 紀錄時紀錄int資訊
type DistRecord struct {
	Bucket          *stats.WinBucket
	TotalWinCollect []int
	BaseWinCollect  []int
	FreeWinCollect  []int
}

// PlayerRecord 玩家統計
type PlayerRecord struct {
	leaveLine   int
	InitBalance int
	Balance     int
	MaxBalance  int
	MinBalance  int
	Bust        bool
	Cashout     bool
	Alive       bool
}

func NewSpinRecorder(name string, id spec.GID, betUnits []int, initBets int, betMode int) (*SpinRecorder, error) {
	s := new(SpinRecorder)

	if len(betUnits) == 0 {
		return s, errs.NewFatal(fmt.Sprintf("betunits err %v", betUnits))
	}

	for _, v := range betUnits {
		if v <= 0 {
			return s, errs.NewFatal(fmt.Sprintf("betunits err %v", betUnits))
		}
	}

	if betMode < 0 || betMode >= len(betUnits) {
		return s, errs.NewFatal(fmt.Sprintf("betMode err %d", betMode))
	}

	if initBets < 0 {
		return s, errs.NewFatal(fmt.Sprintf("init bets must not negative integer, got: %d", initBets))
	}
	// 通過valid
	s.GameName = name
	s.GameId = id
	s.BetUnits = betUnits
	s.BetUnit = betUnits[betMode]
	s.BetMode = betMode
	s.InitBets = initBets
	s.Basic = new(BasicRecord)
	s.Dist = newDistRecord(s.BetUnit)
	s.Player = newPlayerRecord(s.BetUnit, s.InitBets)

	return s, nil
}

func MergeSpinRecorder(r []*SpinRecorder) (*SpinRecorder, error) {
	r0 := r[0]
	s, err := NewSpinRecorder(r0.GameName, r0.GameId, r0.BetUnits, r0.InitBets, r0.BetMode)
	if err != nil {
		return s, err
	}
	for _, v := range r {
		if v.GameName != r0.GameName {
			return s, errs.NewFatal("merge spin record err : different game name")
		}
		for i, b := range v.BetUnits {
			if b != r0.BetUnits[i] {
				return s, errs.NewFatal("merge spin record err : different betunits")
			}
		}
		if v.InitBets != r0.InitBets {
			return s, errs.NewFatal("merge spin record err : different init bets")
		}
		if v.BetMode != r0.BetMode {
			return s, errs.NewFatal("merge spin record err : different betmode")
		}
		s.Basic.TotalBet += v.Basic.TotalBet
		s.Basic.TotalWin += v.Basic.TotalWin
		s.Basic.BaseWin += v.Basic.BaseWin
		s.Basic.FreeWin += v.Basic.FreeWin
		s.Basic.TotalWinSqSum += v.Basic.TotalWinSqSum
		s.Basic.BaseWinSqSum += v.Basic.BaseWinSqSum
		s.Basic.FreeWinSqSum += v.Basic.FreeWinSqSum
		s.Basic.Rounds += v.Basic.Rounds
		s.Basic.Trigger += v.Basic.Trigger

		// 整合Dist
		for i := range len(v.Dist.TotalWinCollect) {
			s.Dist.TotalWinCollect[i] += v.Dist.TotalWinCollect[i]
			s.Dist.BaseWinCollect[i] += v.Dist.BaseWinCollect[i]
			s.Dist.FreeWinCollect[i] += v.Dist.FreeWinCollect[i]
		}
	}
	return s, nil
}

// Record 以單次 SpinResult 更新基本統計（不含玩家與倍數）
func (s *SpinRecorder) Record(sr *buf.SpinResult) {
	s.recordBasic(sr) // Basic
	s.recordDist(sr)  // Dist
}

// RecordWithPlayer 在 Record 的基礎上，進一步更新玩家餘額／離場狀態，並回傳玩家是否停止遊戲。
func (s *SpinRecorder) RecordWithPlayer(sr *buf.SpinResult) bool {
	if s.Player.Balance < s.BetUnit {
		return true
	}
	s.recordBasic(sr)
	s.recordDist(sr)
	r := s.recordPlayer(sr)
	return r
}

func (s *SpinRecorder) Done() *stats.StatReport {
	bufloat := float64(s.BetUnit)
	bb := bufloat * bufloat

	report := &stats.StatReport{
		Summary: &stats.SummaryReport{
			GameName:    s.GameName,
			GameId:      s.GameId,
			BetUnits:    s.BetUnits,
			BetUnit:     s.BetUnit,
			BetMode:     s.BetMode,
			BetMult:     s.BetUnit / (s.BetUnits[s.BetMode]),
			TotalBet:    s.Basic.TotalBet,
			TotalWin:    s.Basic.TotalWin,
			BaseWin:     s.Basic.BaseWin,
			FreeWin:     s.Basic.FreeWin,
			RTP:         s.rtp(),
			Trigger:     s.Basic.Trigger,
			TriggerRate: float64(s.Basic.Trigger) / float64(s.Basic.Rounds),
			NoWinRounds: s.Dist.TotalWinCollect[0],
			HitRate:     1.0 - (float64(s.Dist.TotalWinCollect[0]) / float64(s.Basic.Rounds)),
			Rounds:      s.Basic.Rounds,
		},
		Mult: &stats.MultReport{
			TotalWinMult:      float64(s.Basic.TotalWin) / bufloat,
			BaseWinMult:       float64(s.Basic.BaseWin) / bufloat,
			FreeWinMult:       float64(s.Basic.FreeWin) / bufloat,
			TotalWinMultSqSum: float64(s.Basic.TotalWinSqSum) / bb,
			BaseWinMultSqSum:  float64(s.Basic.BaseWinSqSum) / bb,
			FreeWinMultSqSum:  float64(s.Basic.FreeWinSqSum) / bb,
		},
		Dist: &stats.DistReport{
			WinBucket:       stats.Buckets.WinBucketStr(),
			TotalWinCollect: s.Dist.TotalWinCollect,
			BaseWinCollect:  s.Dist.BaseWinCollect,
			FreeWinCollect:  s.Dist.FreeWinCollect,
			TotalWinDist:    nil,
			BaseWinDist:     nil,
			FreeWinDist:     nil,
		},
		Player: &stats.PlayerReport{
			InitBalance: s.Player.InitBalance,
			Balance:     s.Player.Balance,
			MaxBalance:  s.Player.MaxBalance,
			MinBalance:  s.Player.MinBalance,
			Bust:        s.Player.Bust,
			Cashout:     s.Player.Cashout,
			Alive:       s.Player.Alive,
		},
	}

	length := len(report.Dist.WinBucket)

	totalWinF := make([]float64, length)
	baseWinF := make([]float64, length)
	freeWinF := make([]float64, length)
	rf := float64(report.Summary.Rounds)
	for i := range length {
		totalWinF[i] = float64(report.Dist.TotalWinCollect[i]) / rf
		baseWinF[i] = float64(report.Dist.BaseWinCollect[i]) / rf
		freeWinF[i] = float64(report.Dist.FreeWinCollect[i]) / rf
	}

	report.Dist.TotalWinDist = totalWinF
	report.Dist.BaseWinDist = baseWinF
	report.Dist.FreeWinDist = freeWinF

	return report
}

func (s *SpinRecorder) rtp() float64 {
	if s.Basic.Rounds == 0 || s.Basic.TotalBet == 0 {
		return 0
	}
	return (float64(s.Basic.TotalWin) / float64(s.Basic.TotalBet))
}

func (s *SpinRecorder) recordBasic(res *buf.SpinResult) {
	w := res.TotalWin
	bw := res.GameModeList[0].TotalWin
	fw := w - bw

	// Basic
	s.Basic.TotalBet += res.Bet
	s.Basic.TotalWin += w
	s.Basic.BaseWin += bw
	s.Basic.FreeWin += fw
	s.Basic.TotalWinSqSum += w * w
	s.Basic.BaseWinSqSum += bw * bw
	s.Basic.FreeWinSqSum += fw * fw

	if res.GameModeCount > 1 {
		s.Basic.Trigger++
	}
	s.Basic.Rounds++
}

func (s *SpinRecorder) recordDist(res *buf.SpinResult) {
	d := s.Dist
	b := d.Bucket
	tw := res.TotalWin
	bw := res.GameModeList[0].TotalWin
	fw := tw - bw

	d.TotalWinCollect[b.Index(tw)]++
	d.BaseWinCollect[b.Index(bw)]++
	d.FreeWinCollect[b.Index(fw)]++
}

func (s *SpinRecorder) recordPlayer(sr *buf.SpinResult) bool {
	p := s.Player
	w := sr.TotalWin
	b := s.BetUnit

	// 更新資金
	p.Balance -= b
	p.Balance += w

	// 更新歷史最高資產
	if p.Balance > p.MaxBalance {
		p.MaxBalance = p.Balance
	}
	// 更新歷史最低資產
	if p.Balance < p.MinBalance {
		p.MinBalance = p.Balance
	}

	// 更新結局
	leave := false
	if p.Balance < b {
		p.Bust = true
		leave = true
	}
	if p.Balance >= p.leaveLine {
		p.Cashout = true
		leave = true
	}
	return leave
}

func newDistRecord(bu int) *DistRecord {
	d := new(DistRecord)
	d.Bucket = stats.Buckets.GetBucketByBetUnit(bu)
	d.TotalWinCollect = make([]int, len(stats.Buckets.WinBucketStr()))
	d.BaseWinCollect = make([]int, len(stats.Buckets.WinBucketStr()))
	d.FreeWinCollect = make([]int, len(stats.Buckets.WinBucketStr()))
	return d
}

func newPlayerRecord(bu int, initBets int) *PlayerRecord {

	p := new(PlayerRecord)

	b := bu * initBets // 初始帶入總金額(依最低押注額看)

	p.InitBalance = b
	p.Balance = b
	p.MaxBalance = b
	p.MinBalance = b
	p.Cashout = false
	p.Bust = false
	p.Alive = false
	p.leaveLine = 3 * b // 設定離場條件(3倍本金)

	return p
}
