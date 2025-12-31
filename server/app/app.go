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

// Package app 提供應用程式生命週期管理（App），負責統一啟動與關閉多個 Component。
package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// App 是一個簡單的生命週期管理器，負責啟動所有註冊的 Component，並在收到 OS 信號或任一 Component 發生錯誤時，協調優雅關閉。
// 它確保所有元件能夠被統一管理其啟動與關閉流程。
type App struct {
	comps []Component
}

// New 建立一個新的 App 實例。
func New() *App { return &App{} }

// NewWith 是 New 的語法糖，允許在建立時直接註冊多個 Component。
func NewWith(copms ...Component) *App {
	app := New()
	for _, c := range copms {
		app.Register(c)
	}
	return app
}

// Register 將一個 Component 註冊到 App 中，該 Component 將在 Run 時被管理。
func (a *App) Register(c Component) {
	a.comps = append(a.comps, c)
}

// Run 啟動所有註冊的 Component，並使用 goroutine 並行執行。
// 本方法會阻塞直到收到 OS 終止信號（SIGINT/SIGTERM）或任一 Component 的 Run 返回。
// - 當收到 OS 終止信號時，觸發優雅關閉並返回 nil，代表正常結束。
// - 當任一 Component Run 返回錯誤時，觸發優雅關閉並返回該錯誤。
// 假設每個 Component.Run 是阻塞調用，代表該元件的生命週期。
func (a *App) Run() error {
	// errCh 用於收集任一 Component 首次返回的錯誤
	errCh := make(chan error, len(a.comps))
	for _, c := range a.comps {
		go func(c Component) {
			errCh <- c.Run()
		}(c)
	}

	// 等待終止信號
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	// select 等待兩種退出路徑：OS 信號或 Component 錯誤
	select {
	case <-quit:
		// 收到終止信號，啟動優雅關閉
		a.gracefulShutdown(5 * time.Second)
		return nil
	case err := <-errCh:
		// Component 發生錯誤，啟動優雅關閉
		a.gracefulShutdown(5 * time.Second)
		return err
	}

}

// gracefulShutdown 在給定的 timeout 內依序呼叫所有 Component.Shutdown。
// 若某些實作無法在期限內關閉，由實作者決定是否強制中止／忽略錯誤。
func (a *App) gracefulShutdown(td time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), td)
	defer cancel()
	for _, c := range a.comps {
		err := c.Shutdown(ctx)
		if err != nil {
			fmt.Fprintf(os.Stdout, "shutdown err: %v\n", err)
		}
	}
}
