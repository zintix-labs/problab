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

package dev

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/seedspec"
	"github.com/zintix-labs/problab/spec"
)

// chooseMode 是一局遇到選擇點時的處理方式。
type chooseMode string

const (
	chooseFirst  chooseMode = "first"  // 自動選位置 0
	chooseLast   chooseMode = "last"   // 自動選最後一個位置
	choosePlayer chooseMode = "player" // 停在選擇點，由使用者以 /dev/next 繼續
)

func parseChoose(s string) (chooseMode, error) {
	switch m := chooseMode(s); m {
	case chooseFirst, chooseLast, choosePlayer:
		return m, nil
	case "":
		return "", errs.NewWarn("choose is required: first, last or player")
	default:
		return "", errs.NewWarn(fmt.Sprintf("choose must be first, last or player, got %q", s))
	}
}

// bet 是一局：投注只計第 0 段一次，贏分是各段贏分的加總。
type bet struct {
	Index    int              `json:"index"` // 這批的第幾局，從 1 起算
	Bet      int              `json:"bet"`
	Win      int              `json:"win"`
	IsEnd    bool             `json:"isend"`
	Segments []dto.SpinResult `json:"segments"`
}

func newBet(index, wager int, segments []dto.SpinResult) bet {
	b := bet{Index: index, Bet: wager, Segments: segments}
	for _, s := range segments {
		b.Win += s.TotalWin
	}
	b.IsEnd = segments[len(segments)-1].IsGameEnd
	return b
}

// runner 以一台 deterministic 的機台逐段呼叫 Machine.Spin，與正式後端同一條路徑：
// 新局不帶快照時依機台的 RNG 政策取得起點（optimal 照常抽庫），續段帶回上一段的
// choose_next 與 cp。不使用 BetRunner，因為它組出的完整局不保留每一段的起始快照。
type runner struct {
	m       *problab.Machine
	gid     spec.GID
	name    string
	betMode int
	wager   int
}

func newRunner(pb *problab.Problab, gid spec.GID, betMode int, seed seedspec.Spec) (*runner, error) {
	ent, ok := pb.EntryById(gid)
	if !ok {
		return nil, errs.NewWarn(fmt.Sprintf("gid %d not found", gid))
	}
	m, err := pb.NewMachine(gid, problab.MachineConfig{Seed: problab.SeedBytes(seed.Bytes())})
	if err != nil {
		return nil, err
	}
	if betMode < 0 || betMode >= len(m.BetUnits) {
		return nil, errs.NewWarn(fmt.Sprintf("betmode %d out of range [0, %d)", betMode, len(m.BetUnits)))
	}
	return &runner{m: m, gid: gid, name: ent.Name, betMode: betMode, wager: m.BetUnits[betMode]}, nil
}

// segment 執行一段。cycle 為 0 時 choice、chooseNext 必須為空；snap 非空時從該快照開始。
func (r *runner) segment(cycle int, choice *int, chooseNext []int, snap string, cp json.RawMessage) (dto.SpinResult, error) {
	req := &dto.SpinRequest{
		GameName: r.name, GameId: r.gid,
		Bet: r.wager, BetMode: r.betMode, BetMult: 1,
		Cycle: cycle, Choice: choice, ChooseNext: chooseNext,
	}
	if snap != "" || len(cp) != 0 {
		req.StartState = &dto.StartState{StartCoreSnapB64U: snap, Checkpoint: cp}
	}
	return r.m.Spin(req)
}

// play 執行一局：第 0 段從 snap 開始（空字串表示依機台的 RNG 政策），之後依 mode
// 自動選擇到局結束；player 停在第一個選擇點。
func (r *runner) play(index int, mode chooseMode, snap string) (bet, error) {
	seg, err := r.segment(0, nil, nil, snap, nil)
	if err != nil {
		return bet{}, segmentErr(index, 0, err)
	}
	segments := []dto.SpinResult{seg}
	for !seg.IsGameEnd && mode != choosePlayer {
		cycle := len(segments)
		if cycle >= problab.DefaultMaxSegments {
			return bet{}, errs.NewWarn(fmt.Sprintf("bet %d exceeds %d segments", index, problab.DefaultMaxSegments))
		}
		choice := 0
		if mode == chooseLast {
			choice = len(seg.ChooseNext) - 1
		}
		seg, err = r.segment(cycle, &choice, seg.ChooseNext, "", seg.State.Checkpoint)
		if err != nil {
			return bet{}, segmentErr(index, cycle, err)
		}
		segments = append(segments, seg)
	}
	return newBet(index, r.wager, segments), nil
}

func segmentErr(index, cycle int, err error) error {
	return errs.Wrap(err, fmt.Sprintf("bet %d segment %d", index, cycle))
}

// randomSeed 以 crypto/rand 產生一個 int64 seed，回應中回傳，之後可以原樣重現。
func randomSeed() (seedspec.Spec, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return seedspec.Spec{}, errs.NewFatal("generate seed: " + err.Error())
	}
	return seedspec.Int64(int64(binary.BigEndian.Uint64(b[:]) >> 1)), nil
}
