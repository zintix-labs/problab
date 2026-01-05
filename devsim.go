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

package problab

import (
	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/stats"
)

// DevSimulator
//
// 只提供給Dev模式使用的模擬器，單線(不併發)，重點在可審計、可重現
type DevSimulator struct {
	sim      *Simulator // 只開放Sim功能
	m        *Machine   // 同步seed
	before   []byte
	after    []byte
	before64 string
	after64  string
}

type DevSpinReport struct {
	Before   string           `json:"start_b64u"`
	After    string           `json:"after_b64u"`
	Round    int              `json:"round"`
	Rtp      float64          `json:"rtp"`
	TotalBet int              `json:"total_bet"`
	TotalWin int              `json:"total_win"`
	BaseWin  int              `json:"base_win"`
	FreeWin  int              `json:"free_win"`
	Results  []dto.SpinResult `json:"results"`
}

func (d *DevSimulator) spinOne(betmode int) (dto.SpinResult, error) {
	bu := d.m.gh.GameSetting.BetUnits
	if betmode < 0 || betmode >= len(bu) {
		return dto.SpinResult{}, errs.NewWarn("bet_mode out of range")
	}
	req := &dto.SpinRequest{
		GameName: d.m.gameName,
		GameId:   d.m.gameId,
		BetMode:  betmode,
		BetMult:  1,
		Bet:      bu[betmode],
	}
	return d.m.Spin(req)
}

func (d *DevSimulator) Spins(betmode int, round int) (DevSpinReport, error) {
	// 限制檢查
	if round < 1 || round > 5000 {
		return DevSpinReport{}, errs.NewWarn("round must be between 1 and 5,000")
	}

	// spin
	ds := make([]dto.SpinResult, 0, round)
	for range round {
		result, err := d.spinOne(betmode)
		if err != nil {
			return DevSpinReport{}, errs.Wrap(err, "spin error")
		}
		ds = append(ds, result)
	}
	// 統計
	bet, win, base, free := 0, 0, 0, 0
	for _, r := range ds {
		bet += r.Bet
		win += r.TotalWin
		base += r.GameModes[0].TotalWin
		free += (win - base)
	}

	de := DevSpinReport{
		Before:   ds[0].State.StartCoreSnapB64U,
		After:    ds[len(ds)-1].State.AfterCoreSnapB64U,
		Round:    len(ds),
		Rtp:      100.0 * float64(win) / float64(bet),
		TotalBet: bet,
		TotalWin: win,
		BaseWin:  base,
		FreeWin:  free,
		Results:  ds,
	}
	return de, nil
}

func (d *DevSimulator) RestoreSpins(be64 string, betmode int, round int) (DevSpinReport, error) {
	// 限制檢查
	if round < 1 || round > 5000 {
		return DevSpinReport{}, errs.NewWarn("round must be between 1 and 5,000")
	}
	// 解析seed
	be, err := corefmt.DecodeBase64URL(be64)
	if err != nil {
		return DevSpinReport{}, errs.NewWarn("decode seed failed" + err.Error())
	}
	// restore
	if err := d.m.RestoreCore(be); err != nil {
		return DevSpinReport{}, errs.NewWarn("machine restore failed")
	}
	return d.Spins(betmode, round)
}

type DevSimReport struct {
	Before string            `json:"before"`
	After  string            `json:"after"`
	Stat   *stats.StatReport `json:"statistic"`
}

func (d *DevSimulator) Sim(betmode int, round int) (DevSimReport, error) {
	// 先存 before 快照
	m := d.sim.mBuf[0]
	be, err := m.SnapshotCore()
	if err != nil {
		return DevSimReport{}, err
	}
	be64 := corefmt.EncodeBase64URL(be)
	d.before = be
	d.before64 = be64

	// Spin
	bu := d.m.gh.GameSetting.BetUnits
	if betmode < 0 || betmode >= len(bu) {
		return DevSimReport{}, errs.NewWarn("bet_mode out of range")
	}
	if round < 1 || round > 3_000_000 {
		return DevSimReport{}, errs.NewWarn("round must be between 1 and 3,000,000")
	}
	stat, _, err := d.sim.Sim(betmode, round, false)
	if err != nil {
		return DevSimReport{}, errs.Wrap(err, "sim failed")
	}

	// 再存 after 快照
	af, err := m.SnapshotCore()
	if err != nil {
		return DevSimReport{}, err
	}
	af64 := corefmt.EncodeBase64URL(af)
	d.after = af
	d.after64 = af64

	return DevSimReport{
		Before: be64,
		After:  af64,
		Stat:   stat,
	}, nil
}

func (d *DevSimulator) RestoreSim(be64 string, betmode int, round int) (DevSimReport, error) {
	// 反解析 string -> []byte
	be, err := corefmt.DecodeBase64URL(be64)
	if err != nil {
		return DevSimReport{}, errs.Wrap(err, "decode seed failed")
	}
	d.before = be
	d.before64 = be64

	// restore
	if err := d.sim.mBuf[0].RestoreCore(be); err != nil {
		return DevSimReport{}, errs.Wrap(err, "restore simulator failed")
	}

	return d.Sim(betmode, round)
}
