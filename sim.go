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
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/recorder"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
	"github.com/zintix-labs/problab/stats"
)

const capPrepare int = 100

// Simulator 用於模擬遊戲行為，可建立多台機台並平行紀錄統計。
type Simulator struct {
	GameName string                   // 遊戲名稱
	GameId   spec.GID                 // 遊戲名稱enum
	initBets int                      // 用戶帶的錢(以轉數設定)
	gs       *spec.GameSetting        // 方便重用建立Statistician
	logic    *slot.LogicRegistry      // 邏輯註冊表
	cf       core.PRNGFactory         // 亂數生成器
	initSeed []byte                   // 敏感 root seed，只供 worker 派生的 private owned clone
	optimal  *optimalrt.Artifact      // Problab instance 共用的不可變優化資料
	mBuf     []*Machine               // 併發執行機台實例
	rBuf     []*recorder.SpinRecorder // 併發遊戲紀錄員
	sBuf     []*stats.StatReport      // 併發統計結果報表(僅Players需要)
}

func newSimulatorWithSeed(gs *spec.GameSetting, reg *slot.LogicRegistry, cf core.PRNGFactory, seed []byte, optimal *optimalrt.Artifact) (*Simulator, error) {
	seed = append([]byte(nil), seed...)
	s := &Simulator{
		GameName: gs.GameName,
		GameId:   gs.GameID,
		initBets: 0,
		gs:       gs,
		logic:    reg,
		cf:       cf,
		initSeed: seed,
		optimal:  optimal,
		mBuf:     make([]*Machine, 1, capPrepare),
		rBuf:     make([]*recorder.SpinRecorder, 0, capPrepare),
		sBuf:     make([]*stats.StatReport, 0, capPrepare),
	}
	m, err := newMachineWithSeed(gs, reg, cf, s.initSeed, true, optimal, rngDeterministic)
	if err != nil {
		return nil, err
	}
	s.mBuf[0] = m
	return s, nil
}

func (s *Simulator) workerSeed(worker int) ([]byte, error) {
	if worker == 0 {
		return append([]byte(nil), s.initSeed...), nil
	}
	seed, err := s.cf.DeriveSeed(s.initSeed, core.StreamID{
		Domain: "sim/worker",
		Index:  uint64(worker - 1),
	})
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), seed...), nil
}

// Sim 單線模擬器：以一台機台連續跑指定 round 並回傳統計結果與用時
func (s *Simulator) Sim(betMode int, round int, showpb bool) (*stats.StatReport, time.Duration, error) {
	defer s.reset()
	if betMode < 0 || betMode >= len(s.gs.BetUnits) {
		return nil, 0, errs.NewWarn("bet mode err: must >= 0 and < len(betunits)")
	}
	if round < 1 {
		return nil, 0, errs.NewWarn("round must > 0")
	}
	if len(s.rBuf) == 0 {
		r, err := recorder.NewSpinRecorder(s.GameName, s.GameId, s.gs.BetUnits, s.initBets, betMode)
		if err != nil {
			return nil, 0, err
		}
		s.rBuf = append(s.rBuf, r)
	}
	r := s.rBuf[0]
	m := s.mBuf[0]

	bar := pb.StartNew(round)
	bar.Set(pb.CleanOnFinish, true)
	if !showpb {
		bar.SetWriter(io.Discard)
	}
	for i := 0; i < round; i++ {
		sr := m.SpinInternal(betMode)
		r.Record(sr)
		bar.Increment()
	}
	used := time.Since(bar.StartTime())
	bar.Finish()
	result := r.Done()
	result.Done()

	return result, used, nil
}

// SimMP 平行執行多個機台，總計 rounds*mp 次 spin，合併統計結果後 回傳統計結果與用時
func (s *Simulator) SimMP(betMode int, rounds int, mp int, showpb bool) (*stats.StatReport, time.Duration, error) {
	defer s.reset()
	if mp <= 0 {
		return nil, 0, errs.NewWarn("workers must > 0")
	}
	if betMode < 0 || betMode >= len(s.gs.BetUnits) {
		return nil, 0, errs.NewWarn("bet mode err: must >= 0 and < len(betunits)")
	}
	if rounds < 1 {
		return nil, 0, errs.NewWarn("round must > 0")
	}
	for len(s.mBuf) < mp {
		seed, err := s.workerSeed(len(s.mBuf))
		if err != nil {
			return nil, 0, errs.Wrap(err, "derive Simulator worker seed")
		}
		m, err := newMachineWithSeed(s.gs, s.logic, s.cf, seed, true, s.optimal, rngDeterministic)
		if err != nil {
			return nil, 0, err
		}
		s.mBuf = append(s.mBuf, m)
	}

	for len(s.rBuf) < mp {
		r, err := recorder.NewSpinRecorder(s.GameName, s.GameId, s.gs.BetUnits, s.initBets, betMode)
		if err != nil {
			return nil, 0, err
		}
		s.rBuf = append(s.rBuf, r)
	}

	wg := new(sync.WaitGroup)
	wg.Add(mp)
	bar := pb.StartNew(rounds * mp)
	bar.Set(pb.CleanOnFinish, true)
	if !showpb {
		bar.SetWriter(io.Discard)
	}
	for i := 0; i < mp; i++ {
		go func(i int) {
			defer wg.Done()
			g := s.mBuf[i]
			st := s.rBuf[i]
			for r := 0; r < rounds; r++ {
				sr := g.SpinInternal(betMode)
				st.Record(sr)
				bar.Increment()
			}
		}(i)
	}
	wg.Wait()
	used := time.Since(bar.StartTime())
	bar.Finish()

	st, _ := recorder.MergeSpinRecorder(s.rBuf)
	result := st.Done()
	result.Done()

	return result, used, nil
}

// SimPlayers 模擬多個玩家各自帶入初始籌碼的遊戲歷程，並產出機台報表與玩家報表。
func (s *Simulator) SimPlayers(mp int, players int, initBets int, betMode int, rounds int, showpb bool) (*stats.StatReport, *stats.EstimatorPlayers, time.Duration, error) {
	defer s.reset()
	if players < 1 || (initBets < 1) || rounds < 1 || mp < 1 || betMode < 0 || betMode >= len(s.gs.BetUnits) {
		return nil, nil, 0, errs.NewWarn("invalid param")
	}
	s.initBets = initBets // 賦值

	// 	準備並行機台
	for len(s.mBuf) < mp {
		seed, err := s.workerSeed(len(s.mBuf))
		if err != nil {
			return nil, nil, 0, errs.Wrap(err, "derive Simulator worker seed")
		}
		m, err := newMachineWithSeed(s.gs, s.logic, s.cf, seed, true, s.optimal, rngDeterministic)
		if err != nil {
			return nil, nil, 0, err
		}
		s.mBuf = append(s.mBuf, m)
	}

	// 準備玩家
	s.sBuf = make([]*stats.StatReport, players)
	for len(s.rBuf) < players {
		r, err := recorder.NewSpinRecorder(s.GameName, s.GameId, s.gs.BetUnits, s.initBets, betMode)
		if err != nil {
			return nil, nil, 0, err
		}
		s.rBuf = append(s.rBuf, r)
	}
	// 作一個2048大小的緩衝channel 使player依序處理
	jobs := make(chan *recorder.SpinRecorder, 2048)

	wg := new(sync.WaitGroup)
	wg.Add(mp) // 併發機台

	bar := pb.StartNew(players)
	bar.Set(pb.CleanOnFinish, true)
	if !showpb {
		bar.SetWriter(io.Discard)
	}
	// 併發執行
	for w := 0; w < mp; w++ {
		go sim(wg, s.mBuf[w], jobs, betMode, rounds, bar)
	}
	// 此時併發已經完成，但由於所有workers都無法從jobs當中取出j(還沒塞進去) 所以不會結束

	// 塞進玩家，開始模擬
	for _, j := range s.rBuf {
		jobs <- j
	}
	close(jobs) // 玩家送完處理完畢關閉通道 通知所有機台不會再有新資料
	wg.Wait()   // 等待機台都執行完任務
	used := time.Since(bar.StartTime())
	bar.Finish()

	// 機台基準報表
	record, err := recorder.MergeSpinRecorder(s.rBuf)
	if err != nil {
		return nil, nil, 0, err
	}
	st := record.Done()
	st.Done()

	// 玩家分析報表
	for i, r := range s.rBuf {
		s.sBuf[i] = r.Done()
		s.sBuf[i].Done()
	}
	est := stats.EstimatorPlayerExp(s.sBuf)
	return st, est, used, nil
}

func sim(wg *sync.WaitGroup, m *Machine, jobs chan *recorder.SpinRecorder, betMode int, rounds int, bar *pb.ProgressBar) {
	defer wg.Done()
	for j := range jobs { // j := <- jobs
		for range rounds {
			sr := m.SpinInternal(betMode)
			if j.RecordWithPlayer(sr) {
				break
			}
		}
		j.Done()
		bar.Increment()
	}
}

func (s *Simulator) reset() {
	s.rBuf = s.rBuf[:0]
	s.sBuf = s.sBuf[:0]
	s.initBets = 0
}

// SeedMaker retains the legacy PCG64 child-seed sequence for source
// compatibility. New code should use PRNGFactory.DeriveSeed with an explicit
// StreamID instead.
type SeedMaker struct {
	parent []byte
	next   atomic.Uint64
}

// NewSeedMaker creates the legacy PCG64 child-seed sequence.
//
// Deprecated: new stream partitioning should use PRNGFactory.DeriveSeed with
// an explicit StreamID domain and ordinal.
func NewSeedMaker(seed int64) *SeedMaker {
	return &SeedMaker{parent: core.EncodeInt64Seed(seed)}
}

// Next atomically reserves the next legacy ordinal and resolves it through the
// PCG64 compatibility Factory. The sequence is identical to the former local
// LCG implementation.
func (s *SeedMaker) Next() int64 {
	index := s.next.Add(1) - 1
	seed, err := core.PCG64().DeriveSeed(s.parent, core.StreamID{
		Domain: "legacy/seedmaker",
		Index:  index,
	})
	if err != nil {
		panic("problab: derive legacy SeedMaker seed: " + err.Error())
	}
	value, err := core.DecodeInt64Seed(seed)
	if err != nil {
		panic("problab: decode legacy SeedMaker seed: " + err.Error())
	}
	return value
}
