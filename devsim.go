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
	"github.com/zintix-labs/problab/recorder"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/stats"
)

// DevSimulator
//
// 只提供給Dev模式使用的模擬器，單線(不併發)，重點在可審計、可重現
type DevSimulator struct {
	sim *Simulator // 只開放Sim功能
	m   *Machine   // 保留 Extend，供 Spins 使用
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

// Spins 從機台目前的位置連續執行 round 局，回傳每局結果與統計。每局經 runBet 執行並擷取
// 快照，統計由 Recorder 計算；Before／After 是機台在整批前後的位置，可交給 RestoreSpins
// 重跑同一批。
func (d *DevSimulator) Spins(betmode int, round int) (DevSpinReport, error) {
	// 限制檢查
	if round < 1 || round > 5000 {
		return DevSpinReport{}, errs.NewWarn("round must be between 1 and 5,000")
	}
	m := d.m
	if betmode < 0 || betmode >= len(m.BetUnits) {
		return DevSpinReport{}, errs.Wrap(errs.NewWarn("bet_mode out of range"), "spin error")
	}
	rec, err := recorder.NewSpinRecorder(m.gameName, m.gameId, m.BetUnits, 0, betmode)
	if err != nil {
		return DevSpinReport{}, err
	}
	// PRNG 可以重用 Snapshot 的緩衝區，整批期間還會擷取快照，所以立即編碼。
	be, err := m.SnapshotCore()
	if err != nil {
		return DevSpinReport{}, err
	}
	before := corefmt.EncodeBase64URL(be)

	// spin
	in := SpinInput{BetMode: betmode, BetMult: 1, CaptureSnapshots: true}
	asm := buf.NewSpinAssembler()
	ds := make([]dto.SpinResult, 0, round)
	for range round {
		res, err := runBet(m, asm, in)
		if err != nil {
			return DevSpinReport{}, errs.Wrap(err, "spin error")
		}
		rec.Record(res)
		out, err := dto.NewSpinResultDTO(res)
		if err != nil {
			return DevSpinReport{}, errs.Wrap(err, "spin error")
		}
		ds = append(ds, out)
	}
	af, err := m.SnapshotCore()
	if err != nil {
		return DevSpinReport{}, err
	}

	// 統計；RTP 維持百分比與原本的運算順序
	b := rec.Basic
	de := DevSpinReport{
		Before:   before,
		After:    corefmt.EncodeBase64URL(af),
		Round:    len(ds),
		Rtp:      100.0 * float64(b.TotalWin) / float64(b.TotalBet),
		TotalBet: b.TotalBet,
		TotalWin: b.TotalWin,
		BaseWin:  b.BaseWin,
		FreeWin:  b.FreeWin,
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

	// restore
	if err := d.sim.mBuf[0].RestoreCore(be); err != nil {
		return DevSimReport{}, errs.Wrap(err, "restore simulator failed")
	}

	return d.Sim(betmode, round)
}
