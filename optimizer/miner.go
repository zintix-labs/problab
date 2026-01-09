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

package optimizer

// import (
// 	"crypto/rand"
// 	"math"
// 	"math/big"
// 	"sync"

// 	"github.com/cheggaaa/pb/v3"
// 	"github.com/zintix-labs/problab"
// 	"github.com/zintix-labs/problab/errs"
// 	"github.com/zintix-labs/problab/sdk/core"
// 	"github.com/zintix-labs/problab/sdk/slot"
// 	"github.com/zintix-labs/problab/spec"
// )

// type Dataminer struct {
// 	gameName  string              // 遊戲名稱
// 	gameId    spec.GID            // 遊戲名稱enum
// 	gs        *spec.GameSetting   // 方便重用建立Machine
// 	logic     *slot.LogicRegistry // 邏輯註冊表
// 	cf        core.PRNGFactory    // 亂數生成器
// 	initSeed  int64               // 初始下的種子
// 	seedmaker *problab.SeedMaker  // 種子生成器
// 	mBuf      []*problab.Machine  // 併發執行機台實例
// }

// func newDataminer(gid spec.GID, lab *problab.Problab) (*Dataminer, error) {
// 	if _, ok := lab.EntryById(gid); !ok {
// 		return nil, errs.Warnf("gid not found: %d", gid)
// 	}
// 	seed, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
// 	if err != nil {
// 		return nil, err
// 	}
// 	return newDataminerWithSeed(gid, lab, seed.Int64(), css...)
// }

// func newDataminerWithSeed(gid spec.GID, lab *Problab, seed int64, css ...optimizer.Class) (*Dataminer, error) {
// 	gs, err := lab.cat.GameSettingById(gid)
// 	if err != nil {
// 		return nil, errs.Warnf("get game setting err %w", err)
// 	}
// 	s := &Dataminer{
// 		gameName:  gs.GameName,
// 		gameId:    gs.GameID,
// 		gs:        gs,
// 		logic:     lab.reg,
// 		cf:        lab.cf,
// 		initSeed:  seed,
// 		seedmaker: newSeedMaker(seed),
// 		css:       css,
// 		mBuf:      make([]*Machine, 1, capPrepare),

// 	}
// 	m, err := newMachineWithSeed(gs, lab.reg, lab.cf, s.initSeed, true)
// 	if err != nil {
// 		return nil, err
// 	}
// 	s.mBuf[0] = m
// 	t, err := optimizer.NewSorter(css...)
// 	if err != nil {
// 		return nil, err
// 	}
// 	s.sBuf[0] = t
// 	return s, nil
// }

// func (d *Dataminer) GameName() string {
// 	return d.gameName
// }

// func (d *Dataminer) GID() spec.GID {
// 	return d.gameId
// }

// func (d *Dataminer) Mine(betMode int, limit int, worker int) error {
// 	if limit < 1 || worker < 1 {
// 		return errs.NewWarn("limit and worker must be at least 1")
// 	}
// 	if betMode < 0 || betMode >= len(d.gs.BetUnits) {
// 		return errs.NewWarn("bet mode err: must >= 0 and < len(betunits)")
// 	}
// 	for len(d.mBuf) < worker {
// 		m, err := newMachineWithSeed(d.gs, d.logic, d.cf, d.seedmaker.next(), true)
// 		if err != nil {
// 			return err
// 		}
// 		d.mBuf = append(d.mBuf, m)
// 		s, err := optimizer.NewSorter(d.css...)
// 		if err != nil {
// 			return err
// 		}
// 		d.sBuf = append(d.sBuf, s)
// 	}

// 	wg := new(sync.WaitGroup)
// 	wg.Add(worker)
// 	bar := pb.StartNew(limit * worker)
// 	bar.Set(pb.CleanOnFinish, true)
// 	for i := 0; i < worker; i++ {
// 		go func(i int) {
// 			defer wg.Done()
// 			g := d.mBuf[i]
// 			st := d.sBuf[i]
// 			for r := 0; r < limit; r++ {
// 				start, _ := g.SnapshotCore()
// 				sr := g.SpinInternal(betMode)
// 				sr.State.StartCoreSnap = start
// 				if st.Classify(sr) {
// 					return
// 				}
// 				bar.Increment()
// 			}
// 		}(i)
// 	}
// 	wg.Wait()
// 	bar.Finish()
// 	return nil
// }

// func (d *Dataminer) Result() (*optimizer.Sorters, error) {
// 	return optimizer.MergeSorters(d.sBuf...)
// }

// // OptimizeAndSave 執行優化並保存 Gacha 到文件。
// //
// // 流程：
// //  1. 合併所有 Sorters 的結果
// //  2. 執行優化（構建 Gacha）
// //  3. 保存 Gacha 和 bin 文件
// //
// // 注意：如果 cfg.UseGMM 為 true（默認），將使用完整的 GMM 優化算法（對應 Rust 版本）。
// func (d *Dataminer) OptimizeAndSave(cfg optimizer.OptimizeConfig, storage optimizer.GachaStorage) error {
// 	// 1. 合併結果
// 	sorters, err := d.Result()
// 	if err != nil {
// 		return err
// 	}

// 	// 2. 執行優化
// 	gacha, entries, err := optimizer.Optimize(sorters, d.css, cfg)
// 	if err != nil {
// 		return err
// 	}

// 	// 3. 保存
// 	return optimizer.SaveGacha(gacha, entries, storage)
// }
