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
	"sync"
	"sync/atomic"
	"time"

	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

type SlotRuntime struct {
	// build-time 來源（只讀引用）
	pb *Problab // 方便取 catalog/registry/corefactory 與共用一些 helper

	// data-plane：關鍵主池（每個遊戲一個 pool）
	pools map[spec.GID]*MachinePool
	ids   []spec.GID // 固定順序，用於觀測/列舉（來自 cat.IDs()）

	// lifecycle
	done      chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
	reason    atomic.Value // string

	// runtime 行為設定（一期先簡單，之後可擴展）
	poolSize int // 每個遊戲的池大小（Run(n) 的 n）

	// health
	healthSnap        atomic.Value // RuntimeHealth
	healthNextRefresh atomic.Int64 // 下次允許 refresh 的時間點（UnixNano）
	hmu               sync.Mutex
	ttl               time.Duration // health TTL
}

func (rt *SlotRuntime) Spin(ctx context.Context, req *buf.SpinRequest) (dto.SpinResult, error) {
	select {
	case <-ctx.Done():
		// 如果通知取消
		return dto.SpinResult{}, errs.NewWarn("spin canceled/timeout: " + ctx.Err().Error())
	case <-rt.done:
		// done is the source of truth; keep a fast boolean for cheap reads/telemetry.
		return dto.SpinResult{}, errs.NewFatal("slot runtime closed: " + rt.ClosedReason())
	default:
	}

	mp, ok := rt.pools[req.GameId]
	if !ok {
		return dto.SpinResult{}, errs.NewWarn("game id not found")
	}

	// pool 自己會處理 done / close / rebuild / metrics
	return mp.Spin(ctx, req)
}

// Close transitions the runtime into a closed state. It is safe to call multiple times.
func (rt *SlotRuntime) Close() {
	rt.closeWithReason("closed")
}

// closeWithReason closes the runtime and records the reason (written once).
func (rt *SlotRuntime) closeWithReason(reason string) {
	rt.closeOnce.Do(func() {
		if reason == "" {
			reason = "closed"
		}
		rt.reason.Store(reason)
		rt.closed.Store(true)
		close(rt.done)
	})
}

// Closed reports whether the runtime has been closed.
func (rt *SlotRuntime) Closed() bool {
	return rt.closed.Load()
}

func (rt *SlotRuntime) ClosedReason() string {
	if v := rt.reason.Load(); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

type RuntimeHealth struct {
	AtUnixNano  int64             `json:"at_unix_nano"`
	RuntimeOK   bool              `json:"runtime_ok"`
	Reason      string            `json:"reason,omitempty"`
	Overall     string            `json:"overall"`
	PoolsClosed map[spec.GID]bool `json:"pools_closed"`
}

func (rt *SlotRuntime) Health() RuntimeHealth {
	now := time.Now().UnixNano()
	if v := rt.healthSnap.Load(); v != nil {
		next := rt.healthNextRefresh.Load()
		if now < next {
			return v.(RuntimeHealth)
		}
	}

	// slow path: TTL 到了，嘗試 refresh（只有一個 goroutine 做）
	rt.hmu.Lock()
	defer rt.hmu.Unlock()

	// double check
	now = time.Now().UnixNano()
	if v := rt.healthSnap.Load(); v != nil {
		next := rt.healthNextRefresh.Load()
		if now < next {
			return v.(RuntimeHealth)
		}
	}

	snap := rt.buildHealthSnapshot(now)
	rt.healthSnap.Store(snap)
	rt.healthNextRefresh.Store(now + rt.ttl.Nanoseconds())
	return snap
}

func (rt *SlotRuntime) buildHealthSnapshot(now int64) RuntimeHealth {
	runtimeOK := !rt.Closed()
	overall := "ok"
	if !runtimeOK {
		overall = "down"
	}

	poolsClosed := make(map[spec.GID]bool, len(rt.ids))
	degraded := false

	if runtimeOK {
		for _, id := range rt.ids {
			mp := rt.pools[id]
			closed := mp.Closed()
			poolsClosed[id] = closed
			if closed {
				degraded = true
			}
		}
		if degraded {
			overall = "degraded"
		}
	}

	snap := RuntimeHealth{
		AtUnixNano:  now,
		RuntimeOK:   runtimeOK,
		Reason:      rt.ClosedReason(),
		Overall:     overall,
		PoolsClosed: poolsClosed,
	}
	return snap
}

func (rt *SlotRuntime) PoolMetrics(gid spec.GID) (MachinePoolMetrics, bool) {
	if p, ok := rt.pools[gid]; ok {
		return p.Metrics(), ok
	}
	return MachinePoolMetrics{}, false
}
