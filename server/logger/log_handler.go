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

package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
)

// enum LogMode
type LogMode uint8

const (
	ModeDev LogMode = iota
	ModeProd
	ModeSilence
)

// =========================================================
// 本包支援兩種 slog 注入/組裝方式：
//
// (A) 直接傳入 *slog.Logger（推薦，最常用）：
//     你可以用 NewDefaultLogger(LogMode) 或自訂組裝 *slog.Logger。
//
// (B) 傳入 slog.Handler（進階用法）：
//     你可以組合 slog.NewJSONHandler / slog.NewTextHandler / ReplaceAttr / LevelVar...，再用 NewLogger(h) 包成 *slog.Logger。
//     這樣可以與外部各種 slog Handler 無縫整合。
//
// 本包也提供 AsyncHandler，讓你可以把任何 slog.Handler 變成非阻塞（async）handler。
// =========================================================

// NewDefaultLogger returns a *slog.Logger built from LogMode defaults.
// 外部最常用的入口：直接注入 *slog.Logger。
func NewDefaultLogger(mode LogMode) *slog.Logger {
	return slog.New(buildHandler(mode))
}

// NewDefaultAsyncLogger returns an async *slog.Logger built from LogMode defaults.
// 外部最常用的非同步Logger入口：直接注入 *slog.Logger。
func NewDefaultAsyncLogger(mode LogMode) *slog.Logger {
	return slog.New(NewAsyncHandler(buildHandler(mode), 8192))
}

// NewLogger wraps a Handler into a *slog.Logger.
// 進階入口：呼叫者自行組裝 Handler（JSON/Text/ReplaceAttr/LevelVar...），再交給 Problab。
func NewLogger(h slog.Handler) *slog.Logger {
	if h == nil {
		h = buildHandler(ModeDev)
	}
	return slog.New(h)
}

// AsyncHandler 是一個 slog.Handler wrapper：
// - 主線程呼叫 Handle 時「盡量不阻塞」：只做 enqueue（channel）
// - 背景 goroutine 逐筆呼叫 next.Handle(...) 寫出
// - channel 滿時採「丟棄（drop）」策略，避免把延遲傳回請求路徑
//
// 設計重點：
// - 以 slog.Handler 形態存在，能與 slog.NewJSONHandler / slog.NewTextHandler / ReplaceAttr / WithAttrs / WithGroup 無縫組合。
// - 這是一個「組裝層（server/runtime）」的工具：你可以選擇不用 async，直接用同步 handler。
//
// 注意：slog.Logger 會忽略 Handler.Handle 回傳的 error。
// 如果你希望處理 I/O error，需在 next handler 內自行包裝（或在更上層改用自家 logger）。
type AsyncHandler struct {
	next slog.Handler
	d    *asyncDispatcher
}

type asyncDispatcher struct {
	ch     chan asyncItem
	closed chan struct{}
	once   sync.Once
	wg     sync.WaitGroup

	// dropCount 記錄因為 buffer 滿而丟棄的筆數（可用於觀測/告警）。
	dropCount atomic.Uint64
}

type asyncItem struct {
	ctx     context.Context
	rec     slog.Record
	handler slog.Handler
}

// NewAsyncHandler wraps next with an async dispatcher.
// buf 控制隊列大小；buf 越大越不容易 drop，但也會增加記憶體占用與 shutdown drain 時間。
func NewAsyncHandler(next slog.Handler, buf int) *AsyncHandler {
	if next == nil {
		next = buildHandler(ModeDev)
	}
	if buf <= 0 {
		buf = 1024
	}

	d := &asyncDispatcher{
		ch:     make(chan asyncItem, buf),
		closed: make(chan struct{}),
	}

	d.wg.Add(1)
	go d.worker()

	return &AsyncHandler{next: next, d: d}
}

func (h *AsyncHandler) Ready() bool {
	return (h != nil && h.d != nil)
}

// Dropped returns number of dropped log records due to a full buffer.
func (h *AsyncHandler) Dropped() uint64 {
	if h == nil || h.d == nil {
		return 0
	}
	return h.d.dropCount.Load()
}

// Close stops the dispatcher and drains buffered logs.
// 這不是 slog.Handler 介面的一部分；只有你拿到 *AsyncHandler 才能呼叫。
func (h *AsyncHandler) Close() {
	if h == nil || h.d == nil {
		return
	}
	h.d.once.Do(func() { close(h.d.closed) })
	h.d.wg.Wait()
}

func (d *asyncDispatcher) worker() {
	defer d.wg.Done()

	// 背景 worker：收到 closed 後會 drain 直到 channel 空。
	for {
		select {
		case it := <-d.ch:
			if it.handler != nil {
				_ = it.handler.Handle(it.ctx, it.rec)
			}
		case <-d.closed:
			for {
				select {
				case it := <-d.ch:
					if it.handler != nil {
						_ = it.handler.Handle(it.ctx, it.rec)
					}
				default:
					return
				}
			}
		}
	}
}

func (h *AsyncHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *AsyncHandler) Handle(ctx context.Context, r slog.Record) error {
	if h == nil || h.d == nil {
		// Not ready; drop silently
		return nil
	}

	// Close() 之後：不再接受新 log，直接 drop
	select {
	case <-h.d.closed:
		h.d.dropCount.Add(1)
		return nil
	default:
	}

	// r.Clone() 會複製 attributes，避免 Record 內部的可變引用在跨 goroutine 時出問題。
	// 這是 slog.Record 的標準用法。
	it := asyncItem{ctx: ctx, rec: r.Clone(), handler: h.next}

	select {
	case h.d.ch <- it:
		return nil
	default:
		h.d.dropCount.Add(1)
		return nil
	}
}

func (h *AsyncHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &AsyncHandler{next: h.next.WithAttrs(attrs), d: h.d}
}

func (h *AsyncHandler) WithGroup(name string) slog.Handler {
	return &AsyncHandler{next: h.next.WithGroup(name), d: h.d}
}

// NewAsync builds a *slog.Logger using LogMode defaults, then wraps its handler with AsyncHandler.
// 這是「我想要預設非阻塞」的便利入口。
func NewAsync(buf int, mode LogMode) (*slog.Logger, *AsyncHandler) {
	base := buildHandler(mode)
	ah := NewAsyncHandler(base, buf)
	return slog.New(ah), ah
}

func buildHandler(logmode LogMode) slog.Handler {
	switch logmode {
	case ModeDev:
		return slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
	case ModeProd:
		// 正式環境：JSON + stdout，給 Loki / Promtail
		return slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
	case ModeSilence:
		// 靜默模式：全部丟掉
		return slog.NewTextHandler(io.Discard, nil)
	default:
		return slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
	}
}
