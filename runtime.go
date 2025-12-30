package problab

import (
	"context"
	"sync"
	"sync/atomic"

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
}

func (rt *SlotRuntime) Spin(ctx context.Context, req *buf.SpinRequest) (dto.SpinResult, error) {
	select {
	case <-ctx.Done():
		// 如果通知取消
		return dto.SpinResult{}, errs.NewWarn("spin canceled/timeout: " + ctx.Err().Error())
	case <-rt.done:
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

func (rt *SlotRuntime) ClosedReason() string {
	r, _ := rt.reason.Load().(string)
	return r
}
