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
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// MachinePool 專門管理「某一款遊戲」的所有機台實例。
// 它透過兩個通道管理機台生命週期：
//  1. pool：健康且可用的機台，供 Spin() 借出 / 歸還。
//  2. broken：在運作過程中發生錯誤或 panic 的壞機台，送往此通道以便後續檢查、維修或丟棄。
//
// 若某台機台於遊戲執行期間發生 panic 或 fatal error，該機台會被送至 broken，並立即補上一台新機以維持容量。
// 整體機制確保整個系統在高併發下仍保持穩定與可用性。
type MachinePool struct {
	gameName      string
	gameId        spec.GID
	gs            *spec.GameSetting
	logic         *slot.LogicRegistry
	cf            core.CoreFactory
	initSeed      int64
	seedMaker     *seedMaker
	pool          chan *Machine // 可用機台的通道，用於取得和歸還機台
	broken        chan *Machine // 壞掉機台的通道，用於送修或丟棄壞掉機台
	done          chan struct{} // 關閉訊號：關閉後不再允許借機/歸還/補機
	closeOnce     sync.Once     // 確保 Close() 只執行一次
	poolsize      int           // 好機台
	rebuild       atomic.Int32  // 重起機台次數
	inflight      atomic.Int32  // 使用中
	panics        atomic.Int32  // panic 次數
	fatals        atomic.Int32  // fatal 次數（機台狀態不可信）
	closeReason   atomic.Value  // string: 關閉原因
	closeInflight atomic.Int32  // 關閉當下 inflight（快照）
	closeAvail    atomic.Int32  // 關閉當下 pool 可用數量（len(pool) 快照）
	closeBroken   atomic.Int32  // 關閉當下 broken backlog（len(broken) 快照）
}

// newMachinePool 建立指定遊戲的機台池。
//   - n: 機台數量（至少為 1）
//   - gn: 遊戲名稱
//
// 初始化內容包含：
//   - 建立 pool（可用機台）與 broken（壞機台）兩個 channel
//   - 預先建立 n 台機台並放入 pool，以便立即提供服務
func newMachinePool(n int, gs *spec.GameSetting, reg *slot.LogicRegistry, cf core.CoreFactory, seed int64) (*MachinePool, error) {
	n = max(1, n) // 確保機台數量至少為1
	p := &MachinePool{
		gameName:  gs.GameName,
		gameId:    gs.GameID,
		gs:        gs,
		logic:     reg,
		cf:        cf,
		initSeed:  seed,
		seedMaker: newSeedMaker(seed),
		pool:      make(chan *Machine, n),   // 建立有緩衝的機台通道，容量為 n
		broken:    make(chan *Machine, 100), // 建立有緩衝的壞掉機台通道，容量固定為100
		done:      make(chan struct{}),
		poolsize:  n,
		inflight:  atomic.Int32{},
		rebuild:   atomic.Int32{},
	}

	p.closeReason.Store("")
	p.closeInflight.Store(-1)
	p.closeAvail.Store(-1)
	p.closeBroken.Store(-1)

	// 上架機台，將 n 台新機台放入池中
	for i := 0; i < n; i++ {
		m, err := newMachineWithSeed(gs, reg, cf, p.seedMaker.next(), false)
		if err != nil {
			return nil, err
		}
		p.pool <- m
	}
	return p, nil
}

// Close 進入關閉狀態：
//   - 通知之後所有 Spin() 應該直接回error
//   - defer 歸還/補機時會觀察 done，避免對已關閉狀態進行 send
func (p *MachinePool) Close() {
	p.closeWithReason("closed")
}

// Closed 回報池是否已進入關閉狀態。
func (p *MachinePool) Closed() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// closeWithReason 進入關閉狀態並記錄原因（thread-safe, reason 只會被寫入一次）。
// reason 建議使用固定字串或小枚舉字串，方便 metrics/telemetry 聚合。
func (p *MachinePool) closeWithReason(reason string) {
	p.closeOnce.Do(func() {
		if reason == "" {
			reason = "closed"
		}
		p.closeReason.Store(reason)
		// 進入關閉狀態的瞬間做一次快照，方便外部觀測與故障排查。
		p.closeInflight.Store(p.inflight.Load())
		p.closeAvail.Store(int32(len(p.pool)))
		p.closeBroken.Store(int32(len(p.broken)))
		close(p.done)
	})
}

// isFatalErr 用於判斷本次錯誤是否代表「機台狀態不可信」需要淘汰/補機。
//
// 原則：
//   - panic 一律視為 broken（由 caller 端的 defer/recover 處理）
//   - 一般的 request/validation 類錯誤不應淘汰機台（例如 BadRequest）
//   - 只有錯誤型別本身明確宣告「fatal」時才視為 broken
func isFatalErr(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*errs.E); ok {
		if e.ErrLv == errs.Fatal {
			return true
		}
	}
	return false
}

func (p *MachinePool) Spin(ctx context.Context, req *buf.SpinRequest) (dto dto.SpinResult, err error) {
	var m *Machine
	borrowed := false
	select {
	case <-p.done:
		// 先觀察是否已關閉：關閉直接回失敗，不阻塞
		return dto, errs.NewFatal("machine pool closed: " + p.ClosedReason())
	case <-ctx.Done():
		// 如果通知取消
		return dto, errs.NewWarn("spin canceled/timeout: " + ctx.Err().Error())
	case m = <-p.pool:
		// 有取出機台
		borrowed = true
		p.inflight.Add(1)
		// ok
	}

	// 理論上不會拿到 nil；若發生代表 pool 有嚴重問題。
	if m == nil {
		return dto, errs.NewFatal("machine pool got nil machine")
	}

	var isPanic bool

	defer func() {
		if borrowed {
			// 有借有還 再借不難
			p.inflight.Add(-1)
		}
		if r := recover(); r != nil {
			// 系統恢復
			isPanic = true
			p.panics.Add(1)
			err = errs.NewFatal(fmt.Sprintf("machine %s panic : %v", m.gameName, r))
		}

		// 若已關閉，直接丟棄機台（不歸還、不補機），避免 send 到已停止的系統。
		if p.Closed() {
			return
		}

		// 若發生 panic 或「致命錯誤」，表示機台狀態不可信，需要送修並補機。
		// 注意：一般的 request/validation error（例如 BadRequest）不應淘汰機台。
		if isPanic || isFatalErr(err) {
			if !isPanic && isFatalErr(err) {
				p.fatals.Add(1)
			}
			// 1) 壞機台送入 broken（避免阻塞）
			select {
			case p.broken <- m:
			default:
				// broken 通道滿代表系統可能正在連續故障：進入關閉狀態讓上層接管維護。
				p.closeWithReason("overwhelmed_by_failures")
				// 若外層尚未有錯誤，補一個更明確的致命訊息
				if err == nil {
					err = errs.NewFatal("machine pool overwhelmed by failures")
				}
				return
			}

			// 2) 補一台新機台（維持容量）
			newMachine, buildErr := newMachineWithSeed(p.gs, p.logic, p.cf, p.seedMaker.next(), false)
			p.rebuild.Add(1)
			if buildErr != nil {
				err = errs.NewFatal(fmt.Sprintf("machine %s can not build", p.gameName))
				p.closeWithReason("rebuild_failed")
				return
			}

			// 補機前再看一次是否已關閉（避免並行 Close 後 send / block）
			select {
			case <-p.done:
				return
			case p.pool <- newMachine:
				// ok
			}

			return
		}

		// 若有錯誤但非致命（多半是 request/validation 類錯誤），機台仍然是健康的：歸還 pool 並把 err 原樣回傳。
		// 注意：此處不改寫 err。
		select {
		case <-p.done:
			return
		case p.pool <- m:
			// ok
		}
	}()

	// 執行機台的 Spin 方法
	result, spinErr := m.Spin(req)
	if spinErr != nil {
		err = spinErr
		return
	}

	dto = result
	return
}

func (mp *MachinePool) PoolSize() int {
	return mp.poolsize
}

func (mp *MachinePool) Inflight() int {
	return int(mp.inflight.Load())
}

func (mp *MachinePool) ReBuild() int {
	return int(mp.rebuild.Load())
}

func (mp *MachinePool) ClosedReason() string {
	if v := mp.closeReason.Load(); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func (mp *MachinePool) Panics() int {
	return int(mp.panics.Load())
}

func (mp *MachinePool) Fatals() int {
	return int(mp.fatals.Load())
}

// MachinePoolMetrics 是一期提供的「拉取式（pull）」觀測快照。
//
// 設計原則：
//   - 不綁任何 metrics/telemetry SDK（Prometheus / OpenTelemetry 等），由上層自己決定如何輸出。
//   - 欄位值以讀取當下為主；其中 Available/brokenBacklog 來自 len(chan)，在高併發下是「近似值」但足夠用於營運觀測。
//   - 關閉瞬間的快照（CloseInflight/CloseAvail/Closebroken）只會在 Close 時寫入一次，用於事後排查。
type MachinePoolMetrics struct {
	GameName string   `json:"game_name"`
	GameID   spec.GID `json:"game_id"`

	PoolSize      int    `json:"pool_size"`      // 目標容量（初始化指定）
	Available     int    `json:"available"`      // 當下可借出的機台數（len(pool)）
	Inflight      int    `json:"inflight"`       // 使用中（借出未歸還）
	BrokenBacklog int    `json:"broken_backlog"` // broken channel 當下 backlog（len(broken)）
	Rebuild       int    `json:"rebuild"`        // 補機次數
	Panics        int    `json:"panics"`         // panic 次數
	Fatals        int    `json:"fatals"`         // fatal 次數
	Closed        bool   `json:"closed"`         // 是否已關閉
	CloseReason   string `json:"close_reason"`   // 關閉原因

	CloseInflight int `json:"close_inflight"` // Close() 當下 inflight（-1 表示尚未關閉）
	CloseAvail    int `json:"close_avail"`    // Close() 當下 available（-1 表示尚未關閉）
	Closebroken   int `json:"close_broken"`   // Close() 當下 broken backlog（-1 表示尚未關閉）
}

// Metrics 回傳一期的觀測快照；上層可用於 log、/metrics、或餵給 Prometheus/OTEL exporter。
func (mp *MachinePool) Metrics() MachinePoolMetrics {
	closed := mp.Closed()
	m := MachinePoolMetrics{
		GameName:      mp.gameName,
		GameID:        mp.gameId,
		PoolSize:      mp.poolsize,
		Available:     len(mp.pool),
		Inflight:      int(mp.inflight.Load()),
		BrokenBacklog: len(mp.broken),
		Rebuild:       int(mp.rebuild.Load()),
		Panics:        int(mp.panics.Load()),
		Fatals:        int(mp.fatals.Load()),
		Closed:        closed,
		CloseReason:   mp.ClosedReason(),
		CloseInflight: int(mp.closeInflight.Load()),
		CloseAvail:    int(mp.closeAvail.Load()),
		Closebroken:   int(mp.closeBroken.Load()),
	}
	return m
}

// Available 回傳當下 pool 可用機台數（len(pool)）。在高併發下為近似值。
func (mp *MachinePool) Available() int {
	return len(mp.pool)
}
