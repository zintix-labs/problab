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
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/recorder"
	"github.com/zintix-labs/problab/sdk/buf"
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
	cfg      betCfg                   // 執行設定；每次執行開始時取一份副本
}

func newSimulator(gs *spec.GameSetting, reg *slot.LogicRegistry, cf core.PRNGFactory, seed []byte, optimal *optimalrt.Artifact) (*Simulator, error) {
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
		cfg:      defaultBetCfg(),
	}
	m, err := newMachine(gs, reg, cf, s.initSeed, true, optimal, rngDeterministic)
	if err != nil {
		return nil, err
	}
	s.mBuf[0] = m
	return s, nil
}

// BetConfig 是 Simulator 與 DevSimulator 的執行設定。零值即預設：ChoosePlayer（不自動
// 選擇，遇到選擇點時回報 ErrChooseModeRequired）與 DefaultMaxSegments。
type BetConfig struct {
	Choose      ChooseMode // ChooseFirst、ChooseLast 自動選擇；ChoosePlayer 不自動選擇
	MaxSegments int        // 一局的段數上限；0 使用 DefaultMaxSegments，不得為負
}

// betCfg 是解析後的執行設定。
type betCfg struct {
	choose      ChooseMode
	maxSegments int // > 0
}

func defaultBetCfg() betCfg {
	return betCfg{choose: ChoosePlayer, maxSegments: DefaultMaxSegments}
}

// resolve 從預設值開始套用 c，驗證整份設定。
func (c BetConfig) resolve() (betCfg, error) {
	r := defaultBetCfg()
	if !c.Choose.valid() {
		return r, &errs.E{ErrLv: errs.Warn, Message: "invalid bet config", Cause: fmt.Errorf("%w: %d", ErrInvalidChooseMode, uint8(c.Choose))}
	}
	if c.MaxSegments < 0 {
		return r, errs.NewWarn(fmt.Sprintf("invalid bet config: max segments must not be negative, got %d", c.MaxSegments))
	}
	r.choose = c.Choose
	if c.MaxSegments > 0 {
		r.maxSegments = c.MaxSegments
	}
	return r, nil
}

// label 是報表標示的選擇方式；未自動選擇時為空，報表不輸出。
func (c betCfg) label() string {
	if c.choose == ChoosePlayer {
		return ""
	}
	return c.choose.String()
}

func (c betCfg) runner(m *Machine) (*BetRunner, error) {
	br, err := NewBetRunner(m, WithMaxSegments(c.maxSegments))
	if err != nil {
		return nil, errs.Wrap(err, "build bet runner")
	}
	return br, nil
}

// SetCfg 以 c 整份取代目前的執行設定：從預設值開始套用，不合併上次的設定，所以
// SetCfg(BetConfig{}) 回到預設。驗證失敗時回傳 errs.Warn，原設定不變。只改設定，不動
// 機台、RNG 與統計；執行期間不得呼叫。
func (s *Simulator) SetCfg(c BetConfig) error {
	r, err := c.resolve()
	if err != nil {
		return err
	}
	s.cfg = r
	return nil
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
	cfg := s.cfg
	br, err := cfg.runner(m)
	if err != nil {
		return nil, 0, err
	}
	in := SpinInput{BetMode: betMode, BetMult: 1}
	for i := 0; i < round; i++ {
		res, err := playBet(br, &in, cfg.choose)
		if err != nil {
			if isSettingError(err) {
				bar.Finish()
				return nil, 0, simStopped(m.gameId, betMode, i, err)
			}
			panic(simFault(m.gameId, betMode, i, err))
		}
		r.Record(res)
		bar.Increment()
	}
	used := time.Since(bar.StartTime())
	bar.Finish()
	result := r.Done()
	result.Done()
	result.Summary.Choose = cfg.label()

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
		m, err := newMachine(s.gs, s.logic, s.cf, seed, true, s.optimal, rngDeterministic)
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

	cfg := s.cfg
	runners := make([]*BetRunner, mp)
	for i := range runners {
		br, err := cfg.runner(s.mBuf[i])
		if err != nil {
			return nil, 0, err
		}
		runners[i] = br
	}
	run := newSimRun()
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
			br := runners[i]
			in := SpinInput{BetMode: betMode, BetMult: 1}
			for r := 0; r < rounds; r++ {
				if run.stopped() {
					return
				}
				res, err := playBet(br, &in, cfg.choose)
				if err != nil {
					if isSettingError(err) {
						run.fail(simStopped(g.gameId, betMode, r, err))
						return
					}
					panic(simFault(g.gameId, betMode, r, err))
				}
				st.Record(res)
				bar.Increment()
			}
		}(i)
	}
	wg.Wait()
	used := time.Since(bar.StartTime())
	bar.Finish()
	if run.err != nil {
		return nil, 0, run.err
	}

	st, _ := recorder.MergeSpinRecorder(s.rBuf)
	result := st.Done()
	result.Done()
	result.Summary.Choose = cfg.label()

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
		m, err := newMachine(s.gs, s.logic, s.cf, seed, true, s.optimal, rngDeterministic)
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
	cfg := s.cfg
	runners := make([]*BetRunner, mp)
	for i := range runners {
		br, err := cfg.runner(s.mBuf[i])
		if err != nil {
			return nil, nil, 0, err
		}
		runners[i] = br
	}
	// 作一個2048大小的緩衝channel 使player依序處理
	jobs := make(chan *recorder.SpinRecorder, 2048)
	run := newSimRun()

	wg := new(sync.WaitGroup)
	wg.Add(mp) // 併發機台

	bar := pb.StartNew(players)
	bar.Set(pb.CleanOnFinish, true)
	if !showpb {
		bar.SetWriter(io.Discard)
	}
	// 併發執行
	for w := 0; w < mp; w++ {
		go sim(wg, run, runners[w], cfg.choose, jobs, betMode, rounds, bar)
	}
	// 此時併發已經完成，但由於所有workers都無法從jobs當中取出j(還沒塞進去) 所以不會結束

	// 塞進玩家，開始模擬；停止後不再派工，也不會因 worker 不再接收而阻塞
send:
	for _, j := range s.rBuf {
		select {
		case jobs <- j:
		case <-run.done:
			break send
		}
	}
	close(jobs) // 玩家送完處理完畢關閉通道 通知所有機台不會再有新資料
	wg.Wait()   // 等待機台都執行完任務
	used := time.Since(bar.StartTime())
	bar.Finish()
	if run.err != nil {
		return nil, nil, 0, run.err
	}

	// 機台基準報表
	record, err := recorder.MergeSpinRecorder(s.rBuf)
	if err != nil {
		return nil, nil, 0, err
	}
	st := record.Done()
	st.Done()
	st.Summary.Choose = cfg.label()

	// 玩家分析報表
	for i, r := range s.rBuf {
		s.sBuf[i] = r.Done()
		s.sBuf[i].Done()
	}
	est := stats.EstimatorPlayerExp(s.sBuf)
	return st, est, used, nil
}

// ErrChooseModeRequired 表示以 ChoosePlayer（未指定選擇方式）執行時遇到選擇點：不會做
// 選擇的消費端（Simulator、DevSimulator）必須指定 ChooseFirst 或 ChooseLast。
var ErrChooseModeRequired = errors.New("a choice game requires choose mode first or last")

// playBet 以 br 執行一筆下注並回傳完整局，是不會做選擇的消費端共用的包裝：ChoosePlayer
// 停在選擇點時回傳 ErrChooseModeRequired。任何 error 之後都 Reset br，下一局可以直接開始。
// 回傳的結果借用 br 與其 Machine 的緩衝，到下一次 playBet 為止有效。
func playBet(br *BetRunner, in *SpinInput, mode ChooseMode) (*buf.SpinResult, error) {
	res, err := br.Bet(*in, mode)
	if err != nil {
		br.Reset()
		return nil, err
	}
	if !res.IsGameEnd {
		br.Reset()
		return nil, ErrChooseModeRequired
	}
	return res, nil
}

// isSettingError 回報 err 是否來自執行設定（未指定選擇方式、段數上限），而不是內部故障。
func isSettingError(err error) bool {
	return errors.Is(err, ErrChooseModeRequired) || errors.Is(err, ErrSegmentLimit)
}

// simStopped 把設定錯誤轉成 errs.Warn。Cause 是補上局號的 simFault，所以 errors.Is 仍能
// 判斷 ErrChooseModeRequired 與 ErrSegmentLimit。
func simStopped(gid spec.GID, betMode, round int, err error) error {
	msg := "simulation stopped: set choose mode first or last for this game"
	if errors.Is(err, ErrSegmentLimit) {
		msg = "simulation stopped: segment limit reached; it is an execution limit, not a game fault (raise BetConfig.MaxSegments)"
	}
	return &errs.E{ErrLv: errs.Warn, Message: msg, Cause: simFault(gid, betMode, round, err)}
}

// simRun 是一次多 worker 執行的停止狀態：第一個設定錯誤勝出，停止訊號只發出一次。每次
// 執行重新建立，不影響下一次執行。
type simRun struct {
	stop atomic.Bool
	done chan struct{}
	once sync.Once
	err  error
}

func newSimRun() *simRun { return &simRun{done: make(chan struct{})} }

// fail 記錄第一個錯誤並發出停止訊號；之後的錯誤不覆蓋第一個。
func (r *simRun) fail(err error) {
	r.once.Do(func() {
		r.err = err
		r.stop.Store(true)
		close(r.done)
	})
}

func (r *simRun) stopped() bool { return r.stop.Load() }

// simFault 為 Simulator 的錯誤補上遊戲、投注模式與局號。設定錯誤由 simStopped 回傳，其餘
// 屬於內部故障，由呼叫端 panic。round 是該迴圈中從 0 起算的局號。
func simFault(gid spec.GID, betMode, round int, err error) error {
	return fmt.Errorf("simulator: game %d bet mode %d round %d: %w", gid, betMode, round, err)
}

func sim(wg *sync.WaitGroup, run *simRun, br *BetRunner, mode ChooseMode, jobs chan *recorder.SpinRecorder, betMode int, rounds int, bar *pb.ProgressBar) {
	defer wg.Done()
	m := br.m
	in := SpinInput{BetMode: betMode, BetMult: 1}
	for j := range jobs { // j := <- jobs
		for r := range rounds {
			if run.stopped() {
				return
			}
			res, err := playBet(br, &in, mode)
			if err != nil {
				if isSettingError(err) {
					run.fail(simStopped(m.gameId, betMode, r, err))
					return
				}
				panic(simFault(m.gameId, betMode, r, err))
			}
			if j.RecordWithPlayer(res) {
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
