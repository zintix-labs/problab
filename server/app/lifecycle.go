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
