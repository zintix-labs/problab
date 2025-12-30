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

// Package app 定義應用程式根目錄用以管理長期運行元件的最小生命週期抽象。
package app

import "context"

// Component 抽象任何「可啟動 / 可關閉」的長生命週期元件。
// - Run() 應該是阻塞呼叫，直到元件停止為止（正常或錯誤）。
// - Shutdown(ctx) 用於要求優雅關閉；實作方應該尊重 ctx deadline/cancel。
// 典型實例：HTTP Server、Background Worker、Message Consumer 等。
type Component interface {
	Run() error
	Shutdown(ctx context.Context) error
}
